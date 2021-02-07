package properyConfig


import (
	"context"
	"fmt"
	"testing"
	"time"
)

type ConfTest struct {
	testGo uint8 `properties:"test.go,default=1"`
	Port   int   `properties:"server.port,default=8000"`
}

func (p *ConfTest) SetTestGo(v uint8) {
	p.testGo = v
}

type AppConf struct {
	notConf  bool
	TestGo   int            `properties:"test.go,default=1"`
	TestDef  int            `properties:"test.def,default=1024"`
	Proxy    map[string]int `properties:",default={}"`
	DefProxy map[string]int `properties:"def.proxy,default={\"a\":-1}"`
}

func (p *AppConf) Init() {
	fmt.Println("AppConf Init() Finished! TestDef =", p.TestDef,
		",Proxy =", p.Proxy, ",DefProxy =", p.DefProxy, p.notConf)
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
	time.Sleep(time.Second * 65)
	ps.Close()
}
