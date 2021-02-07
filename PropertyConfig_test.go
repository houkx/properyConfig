package properyConfig

import (
	"context"
	"fmt"
	"github.com/magiconair/properties"
	"log"
	"os"
	"testing"
)

type ConfTest struct {
	testGo uint8 `properties:"test.go,default=1"`
	Port   int   `properties:"server.port,default=8000"`
}

// setter OR field
func (p *ConfTest) SetTestGo(v uint8) {
	p.testGo = v
}

type AppConf struct {
	TestGo   int            `properties:"test.go,default=1"`
	TestDef  int            `properties:"test.def,default=1024"`
	Proxy    map[string]int `properties:",default={}"`
	DefProxy map[string]int `properties:"def.proxy,default={\"a\":-1}"`
}

func (p *AppConf) Init() {
	fmt.Println("AppConf Init() Finished! TestDef =", p.TestDef,
		",Proxy =", p.Proxy, ",DefProxy =", p.DefProxy)
}

func TestConfig_api(t *testing.T) {
	obj := &ConfTest{}
	appConf := &AppConf{}
	ps, e := NewPropertyConfig("test_conf.properties", obj, appConf)
	if e != nil {
		t.Fatal(e)
	}
	ctx, _ := context.WithCancel(context.Background())
	//Loop: CheckUpdate
	ps.ScheduleCheckUpdate(ctx)
	ps.Close()
}
func TestConfig_internal(t *testing.T) {
	obj := &ConfTest{}
	vSet := make(map[string][]ValueSetter, 0)
	logger := log.New(os.Stdout, "INFO ", log.Lshortfile|log.Ldate|log.Ltime)
	var appConf = &AppConf{}
	initValueSetter(logger, obj, vSet)
	initValueSetter(logger, appConf, vSet)
	ps := properties.MustLoadString(`
          Proxy./el=9070
          Proxy./el/api/usertag=8083
          # test
          server.port=9081
          test.go=19
    `)
	setConfigValues(ps.Map(), vSet)
	t.Log("testGo =", obj.testGo)
	t.Log("Port =", obj.Port)
	t.Log("appConf.TestGo =", appConf.TestGo)
	t.Log("appConf.Proxy =", appConf.Proxy)
}
