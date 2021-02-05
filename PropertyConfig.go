package config

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/magiconair/properties"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"
)

/**
 这个包主管配置注入和更新, 发生变更的配置会调用注册对象的set方法
 */
type PropertyConfig struct {
	logger        *log.Logger
	configUrl     string
	confType      reflect.Type
	observers     []reflect.Value
	lastConfigs   map[string]string
	lastVersion   string
	isRestConfig  bool
	scheduleTimer *time.Timer
	running       bool
	isJson        bool
}

const CallInitMethod = "Init"
/**
 @configUrl -- 配置文件地址
 @confType -- 配置类,包含配置定义
 @observers -- 接口配置通知的观察者,会调用相应的Set方法
 */
func NewPropertyConfig(configUrl string, confType reflect.Type, observers ...interface{}) (p *PropertyConfig, err error) {

	logger := log.New(os.Stdout, "INFO ", log.Lshortfile|log.Ldate|log.Ltime)
	var vs = make([]reflect.Value, len(observers))
	for i, observer := range observers {
		vs[i] = reflect.ValueOf(observer)
	}
	p = &PropertyConfig{configUrl: configUrl,
		confType:     confType,
		observers:    vs,
		logger:       logger,
		isRestConfig: strings.HasPrefix(configUrl, "http"),
		isJson:       false,
	}
	logger.Println("configUrl =", configUrl)
	// 初始通知
	err = p.notifyObservers()
	if err == nil {
		for _, observer := range p.observers {
			// 首次注入完配置, 调用每个观察者的 Init()方法
			if mv := observer.MethodByName(CallInitMethod); mv.IsValid() {
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
	conf, e := DecodeNew(hashMap, p.confType)
	if e == nil {
		if errIface, isErr := conf.Interface().(error); isErr {
			if errMsg := errIface.Error(); len(errMsg) > 0 {
				p.logger.Println("configError:", errMsg)
				e = errors.New(errMsg)
			}
		}
		if e == nil {
			curmap := hashMap.Map()
			p.notifyChange(conf, p.lastConfigs, curmap)
			p.lastConfigs = curmap
		}
	} else {
		p.logger.Println("decodeConfigErr:", e)
	}
	return e
}

/**
  尝试通过Set方法调用通知观察者

  @param conf    - 最新的配置对象
  @param oldConf - 旧的配置
  @param newConf - 新的配置
 */
func (p *PropertyConfig) notifyChange(conf reflect.Value, oldConf, newConf map[string]string) (int, int) {
	if conf.Kind() == reflect.Ptr {
		conf = conf.Elem()
	}
	// 用反射对比新旧两个对象的字段有哪些不同，然后归到变更的集合，最后通知所有的观察者
	var changedPrefix map[string]string
	if oldConf == nil || len(oldConf) == 0 {
		// 旧的配置为空，为初始化的情况，每个setter方法都应该被调用
		numFields := conf.NumField()
		changedPrefix = make(map[string]string, numFields)
		for i := 0; i < numFields; i++ {
			changedPrefix[conf.Type().Field(i).Name] = "1"
		}
	} else if newConf != nil && len(newConf) > 0 {
		changedPrefix = make(map[string]string)
		// 以新配置为基准，处理修改和新增的情况
		for k, v := range newConf {
			if v2, existsOld := oldConf[k]; !existsOld || v != v2 {
				changedPrefix[kPrev(k)] = "1"
			}
		}
		// 以旧配置为基准，处理删除的情况
		for k := range oldConf {
			if _, exists := newConf[k]; !exists {
				changedPrefix[kPrev(k)] = "0"
			}
		}
	} else {
		return 0, 0
	}
	var changes = 0
	for prefix := range changedPrefix {
		if field := conf.FieldByName(prefix); field.IsValid() {
			if upper0 := strings.ToUpper(prefix[0:1]); upper0 != prefix[0:1] {
				prefix = upper0 + prefix[1:]
			}
			var setter = "Set" + prefix
			for _, observer := range p.observers {
				if mv := observer.MethodByName(setter); mv.IsValid() {
					changes++
					var tp = observer.Type().Elem()
					var value = reflect.ValueOf(field.Interface())
					p.logger.Printf("invoke: %s/%s:: %s (%+v)\n", tp.PkgPath(), tp.Name(), setter, value.Interface())
					mv.Call([]reflect.Value{value})
				}
			}
		}
	}
	return len(changedPrefix), changes
}
func kPrev(k string) string {
	var prefix string
	if dot := strings.IndexByte(k, '.'); dot > 0 {
		prefix = k[0:dot]
	} else {
		prefix = k
	}
	return prefix
}
