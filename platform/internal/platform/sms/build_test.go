package sms

import (
	"strings"
	"testing"
	"time"
)

// 装配（XM-SMS2 #7）：platform-api 与 platform-worker **必须解析出同一组供应商**。
//
// 两个进程各写一份解析，迟早会分叉成「API 能买号、worker 不认识这家」——那时
// 巡检不会报错，只是永远看不到那家的余额，而没人会去查一个「一直没数据」的图。

func TestParseModeAcceptsOnlyThreeValues(t *testing.T) {
	for raw, want := range map[string]Mode{
		"":       ModeOff,
		"off":    ModeOff,
		" OFF ":  ModeOff,
		"fake":   ModeFake,
		"real":   ModeReal,
		"  Real": ModeReal,
	} {
		got, err := ParseMode(raw)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	// 错误信息要**逐字列出合法值**：把 XM_CARDS_MODE 写成 live 让生产下线
	// 四分钟，就是因为错误信息没说清能填什么。
	_, err := ParseMode("live")
	if err == nil {
		t.Fatal("live 必须被拒绝")
	}
	for _, want := range []string{"off", "fake", "real"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息要列出 %q: %q", want, err.Error())
		}
	}
}

// 两家**永远都建**，能不能用由库里的开关决定：建适配器不花钱也不连网。
func TestBuildProvidersBuildsEveryRegisteredProvider(t *testing.T) {
	providers, err := BuildProviders(ModeFake, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != len(AllProviders) {
		t.Fatalf("建出来 %d 家, want %d", len(providers), len(AllProviders))
	}
	for i, id := range AllProviders {
		if providers[i].ID != id || providers[i].Adapter == nil {
			t.Fatalf("第 %d 家不对: %+v", i, providers[i])
		}
	}
}

func TestBuildProvidersOffBuildsNothing(t *testing.T) {
	providers, err := BuildProviders(ModeOff, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 0 {
		t.Fatalf("off 不该建任何一家, got %+v", providers)
	}
}

// real 模式没有 SecretProvider 就是装配漏了：**失败**而不是悄悄退回替身，
// 那会让一个以为在打真实上游的进程其实什么都没做。
func TestBuildProvidersRealNeedsSecretProvider(t *testing.T) {
	if _, err := BuildProviders(ModeReal, nil, time.Now); err == nil {
		t.Fatal("real 模式缺 SecretProvider 必须失败")
	}
}
