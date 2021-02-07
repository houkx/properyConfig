package properyConfig

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/magiconair/properties"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type HMapI interface {
	Get(key string) (string, bool)
	MustGetString(key string) string
	Keys() []string
	Map() map[string]string
}

type ValueSetter func(v string, vMap map[string]string)
type PropertyConfig struct {
	logger        *log.Logger
	configUrl     string
	observers     map[string][]ValueSetter
	lastConfigs   map[string]string
	lastVersion   string
	isRestConfig  bool
	scheduleTimer *time.Timer
	running       bool
	isJson        bool
}

const CallInitMethod = "Init"

func NewPropertyConfig(configUrl string, observers ...interface{}) (p *PropertyConfig, err error) {

	logger := log.New(os.Stdout, "INFO ", log.Lshortfile|log.Ldate|log.Ltime)
	observersMap := make(map[string][]ValueSetter, 64)
	for _, observer := range observers {
		initValueSetter(logger, observer, observersMap)
	}
	p = &PropertyConfig{configUrl: configUrl,
		observers:    observersMap,
		logger:       logger,
		isRestConfig: strings.HasPrefix(configUrl, "http"),
		isJson:       false,
	}
	logger.Println("configUrl =", configUrl)

	// 初始通知
	err = p.notifyObservers()
	if err == nil {
		for _, ob := range observers {
			observer := reflect.ValueOf(ob)
			// 首次注入完配置, 调用每个观察者的 Init()方法
			if mv := observer.MethodByName(CallInitMethod); mv.IsValid() && mv.Type().NumIn() == 0 {
				var tp = observer.Type().Elem()
				p.logger.Printf("invoke: %s/%s:: %s()\n", tp.PkgPath(), tp.Name(), CallInitMethod)
				mv.Call([]reflect.Value{})
			}
		}
	}
	return p, err
}
func (p *PropertyConfig) ScheduleCheckUpdate(ctx context.Context) {
	// 定时拉取； 如果是http请求，则带上版本号参数,如果有的话
	if p.scheduleTimer != nil {
		p.scheduleTimer.Stop()
	}
	if os.Getenv("conf.checkUpdate") == "false" {
		p.logger.Println("config Not checkUpdate!")
		return
	}
	p.scheduleTimer = time.NewTimer(time.Minute)
	p.running = true
	go func() {
		for ; p.running; {
			select {
			case <-ctx.Done():
				p.running = false
				break
			default:
				runtime.Gosched()
				<-p.scheduleTimer.C
				p.notifyObservers()
				p.scheduleTimer.Reset(time.Minute)
			}
		}
		p.logger.Println("Stopped.")
		// clear all
		p.scheduleTimer = nil
		p.observers = nil
		p.lastConfigs = nil
		p.logger = nil
	}()
}
func (p *PropertyConfig) Close() error {
	p.running = false
	if p.scheduleTimer != nil {
		p.scheduleTimer.Stop()
	}
	return nil
}

type MapStruct struct {
	m map[string]string
}

func (m *MapStruct) Get(key string) (string, bool) {
	if v, ok := m.m[key]; ok {
		return v, ok
	}
	return "", false
}
func (m *MapStruct) MustGetString(key string) string {
	if v, ok := m.m[key]; ok {
		return v
	}
	panic("noKey:" + key)
}
func (m *MapStruct) Keys() []string {
	ks := make([]string, 0)
	for k := range m.m {
		ks = append(ks, k)
	}
	return ks
}
func (m *MapStruct) Map() map[string]string {
	return m.m
}
func (p *PropertyConfig) notifyObservers() error {
	var cfgUrl = p.configUrl
	if p.lastVersion != "" {
		cfgUrl += "&v=" + p.lastVersion
	}
	var e error
	var hashMap HMapI
	if p.isJson {
		if resp, ep := http.Get(cfgUrl); ep == nil {
			mapObj := make(map[string]string)
			jsonBytes, _ := ioutil.ReadAll(resp.Body)
			if ejson := json.Unmarshal(jsonBytes, &mapObj); ejson == nil {
				hashMap = &MapStruct{m: mapObj}
			} else {
				e = ejson
			}
		} else {
			e = ep
		}
		if e != nil {
			p.logger.Println("Err load config:", cfgUrl, e.Error())
			return e
		}
	} else {
		hashMap, e = properties.LoadURL(cfgUrl)
		if e != nil {
			var jsonOk = false
			if strings.Contains(e.Error(), "json") {
				if resp, ep := http.Get(cfgUrl); ep == nil {
					mapObj := make(map[string]string)
					jsonBytes, _ := ioutil.ReadAll(resp.Body)
					if ejson := json.Unmarshal(jsonBytes, &mapObj); ejson == nil {
						hashMap = &MapStruct{m: mapObj}
						jsonOk = true
						p.isJson = true
					}
				}
			}
			if !jsonOk {
				p.logger.Println("Fail to load config:", cfgUrl)
				return e
			}
		}
	}
	if p.isRestConfig {
		if version, exists := hashMap.Get("version"); exists {
			p.lastVersion = url.PathEscape(version)
		}
	}
	curmap := hashMap.Map()
	p.notifyChange(p.lastConfigs, curmap)
	p.lastConfigs = curmap
	return e
}

/**
  尝试通过Set方法调用通知观察者

  @param conf    - 最新的配置对象
  @param oldConf - 旧的配置
  @param newConf - 新的配置
 */
func (p *PropertyConfig) notifyChange(oldConf, newConf map[string]string) (countChangedConf int) {
	//if conf.Kind() == reflect.Ptr {
	//	conf = conf.Elem()
	//}
	// 用反射对比新旧两个对象的字段有哪些不同，然后归到变更的集合，最后通知所有的观察者
	var changedConf map[string]string
	if oldConf == nil || len(oldConf) == 0 {
		// 旧的配置为空，为初始化的情况，每个setter方法都应该被调用
		changedConf = newConf
	} else if newConf != nil && len(newConf) > 0 {
		changedConf = make(map[string]string, len(newConf))
		// 以新配置为基准，处理修改和新增的情况
		for k, v := range newConf {
			if v2, existsOld := oldConf[k]; !existsOld || v != v2 {
				changedConf[k] = v
			}
		}
		// 以旧配置为基准，处理删除的情况
		for k, v := range oldConf {
			if _, exists := newConf[k]; !exists {
				changedConf[k] = v
			}
		}
	} else {
		return 0
	}
	setConfigValues(changedConf, p.observers)
	return len(changedConf)
}
func filterStripPrefix(p map[string]string, prefix string) *properties.Properties {
	pp := properties.NewProperties()
	n := len(prefix)
	for k, v := range p {
		if len(k) > len(prefix) && strings.HasPrefix(k, prefix) {
			pp.Set(k[n+1:], v)
		}
	}
	return pp
}
func setConfigValues(conf map[string]string, vSet map[string][]ValueSetter) {
	for k, vs := range vSet {
		var hasK = false
		vStr := os.Getenv(k)
		if vStr != "" {
			hasK = true
		} else {
			vStr, hasK = conf[k]

		}
		if hasK {
			for _, v := range vs {
				v(vStr, nil)
			}
		} else if ps := filterStripPrefix(conf, k); ps.Len() > 0 {
			for _, v := range vs {
				v("", ps.Map())
			}
		} else {
			for _, v := range vs {
				v("", nil)
			}
		}
	}
}

func initValueSetter(logger *log.Logger, observer interface{}, vSet map[string][]ValueSetter) {
	var t = reflect.TypeOf(observer).Elem()
	if t.Kind() != reflect.Struct {
		return
	}
	var objV = reflect.ValueOf(observer)
	for i, max := 0, t.NumField(); i < max; i++ {
		var f = t.Field(i)
		propK, def, opts := keyDefaultValue(f)
		if propK == "" {
			continue // 忽略没有 properties tag 的字段
		}
		prefix := f.Name
		if upper0 := strings.ToUpper(prefix[0:1]); upper0 != prefix[0:1] {
			prefix = upper0 + prefix[1:]
		}
		var setterMName = "Set" + prefix
		var vSetter func(value reflect.Value)
		var argType reflect.Type
		if mv := objV.MethodByName(setterMName); mv.IsValid() && mv.Type().NumIn() == 1 {
			// 方法参数类型
			argType = mv.Type().In(0)
			vSetter = func(value reflect.Value) {
				logger.Printf("invoke: %s/%s.%s(%+v)\n", t.PkgPath(), t.Name(), setterMName, value.Interface())
				mv.Call([]reflect.Value{value})
			}
		} else if fv := objV.Elem().Field(i); fv.CanSet() {
			argType = fv.Type()
			vSetter = func(value reflect.Value) {
				logger.Printf("SetField: %s/%s.%s: %+v\n", t.PkgPath(), t.Name(), f.Name, value.Interface())
				fv.Set(value)
			}
		} else {
			continue
		}
		var setter = func(str string, mapv map[string]string) {
			if str == "" {
				str = def
			}
			var value reflect.Value
			var convertEr error
			if argType.Kind() != reflect.Map {
				value, convertEr = convertValue(str, argType, opts)
			} else {
				var m = reflect.MakeMap(argType)
				var vt = argType.Elem()
				if mapv == nil {
					tmpV := make(map[string]interface{}, 0)
					mapv = make(map[string]string, 0)
					convertEr = json.Unmarshal([]byte(str), &tmpV)
					for k, v := range tmpV {
						mapv[k] = fmt.Sprint(v)
					}
				}
				if convertEr == nil {
					for mk, v := range mapv {
						ev, cer := convertValue(v, vt, opts)
						if cer != nil {
							convertEr = cer
						} else {
							m.SetMapIndex(reflect.ValueOf(mk), ev)
						}
					}
				}
				if convertEr == nil {
					value = m
				}
			}
			if convertEr == nil {
				vSetter(value)
			} else {
				logger.Printf("WARN: %s/%s::%s \t%s\n", t.PkgPath(), t.Name(), f.Name, convertEr.Error())
			}
		}
		if vs, hasK := vSet[propK]; hasK {
			vSet[propK] = append(vs, ValueSetter(setter))
		} else {
			vs = make([]ValueSetter, 0, 3)
			vSet[propK] = append(vs, ValueSetter(setter))
		}
	}
}
func keyDefaultValue(f reflect.StructField) (string, string, map[string]string) {
	tag := f.Tag.Get("properties")
	if tag == "" {
		return "", "", nil
	}
	_key, _opts := parseTag(tag)

	var _def = ""
	if d, ok := _opts["default"]; ok {
		_def = d
	}
	if _key == "" {
		_key = f.Name
	}
	return _key, _def, _opts
}

// parseTag parses a "key,k=v,k=v,..."
func parseTag(tag string) (key string, opts map[string]string) {
	opts = map[string]string{}
	for i, s := range strings.Split(tag, ",") {
		if i == 0 {
			key = s
			continue
		}

		pp := strings.SplitN(s, "=", 2)
		if len(pp) == 1 {
			opts[pp[0]] = ""
		} else {
			opts[pp[0]] = pp[1]
		}
	}
	return key, opts
}

func boolVal(v string) bool {
	v = strings.ToLower(v)
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

//func isArray(t reflect.Type) bool    { return t.Kind() == reflect.Array || t.Kind() == reflect.Slice }
func isBool(t reflect.Type) bool     { return t.Kind() == reflect.Bool }
func isDuration(t reflect.Type) bool { return t == reflect.TypeOf(time.Second) }

//func isMap(t reflect.Type) bool      { return t.Kind() == reflect.Map }
//func isPtr(t reflect.Type) bool      { return t.Kind() == reflect.Ptr }
func isString(t reflect.Type) bool { return t.Kind() == reflect.String }

//func isStruct(t reflect.Type) bool   { return t.Kind() == reflect.Struct }
func isTime(t reflect.Type) bool { return t == reflect.TypeOf(time.Time{}) }
func isFloat(t reflect.Type) bool {
	return t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64
}
func isInt(t reflect.Type) bool {
	return t.Kind() == reflect.Int || t.Kind() == reflect.Int8 || t.Kind() == reflect.Int16 || t.Kind() == reflect.Int32 || t.Kind() == reflect.Int64
}
func isUint(t reflect.Type) bool {
	return t.Kind() == reflect.Uint || t.Kind() == reflect.Uint8 || t.Kind() == reflect.Uint16 || t.Kind() == reflect.Uint32 || t.Kind() == reflect.Uint64
}
func convertValue(s string, t reflect.Type, opts map[string]string) (val reflect.Value, err error) {
	s = strings.TrimSpace(s)
	var v interface{}

	switch {
	case isDuration(t):
		v, err = time.ParseDuration(s)

	case isTime(t):
		layout := opts["layout"]
		if layout == "" {
			layout = time.RFC3339
		}
		v, err = time.Parse(layout, s)

	case isBool(t):
		v, err = boolVal(s), nil

	case isString(t):
		v, err = s, nil

	case isFloat(t):
		v, err = strconv.ParseFloat(s, 64)

	case isInt(t):
		v, err = strconv.ParseInt(s, 10, 64)

	case isUint(t):
		v, err = strconv.ParseUint(s, 10, 64)

	default:
		return reflect.Zero(t), fmt.Errorf("unsupported type %s", t)
	}
	if err != nil {
		return reflect.Zero(t), err
	}
	return reflect.ValueOf(v).Convert(t), nil
}
