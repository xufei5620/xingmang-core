package main

import (
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// 供应商清单**不来自环境变量**。
//
// 「开哪几家」是运营随时会改的决定（换供应商、某家挂了先停掉），
// 不是这个进程的部署形态。落进环境变量意味着每次改都要改服务器配置再重启，
// 而重启期间整个平台不可用——代价与这个决定的分量完全不匹配。
// 开关现在在 sms.provider_status.enabled，管理后台直接点。
func TestLoadSMSConfigNeedsNoProviderList(t *testing.T) {
	env := map[string]string{"XM_SMS_MODE": "fake"}
	cfg, err := loadSMSConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("只给 XM_SMS_MODE 就该够了, got %v", err)
	}
	if cfg.Mode != smsModeFake {
		t.Fatalf("mode = %q, want fake", cfg.Mode)
	}
}

// 留在 .env 里的旧变量要**报错**，不能默默忽略。
//
// 一个还写着 XM_SMS_PROVIDERS=sms62 的配置文件，会让人确信 hero_sms 已经被
// 关掉了。默默忽略它就是让那个误解一直成立到有人真的花错了钱；启动时喊出来
// 才能把人送到真正的开关那儿去。
func TestLoadSMSConfigRejectsRetiredProviderListVar(t *testing.T) {
	env := map[string]string{"XM_SMS_MODE": "fake", "XM_SMS_PROVIDERS": "sms62"}
	_, err := loadSMSConfig(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("残留的 XM_SMS_PROVIDERS 必须让启动失败")
	}
	// 错误信息要指路，不能只说「不认识这个变量」。
	if !strings.Contains(err.Error(), "管理后台") {
		t.Fatalf("错误信息要告诉人开关在哪, got %q", err.Error())
	}
}

// 两家**永远都建**，能不能用由库里的开关决定。
//
// 建适配器不花钱也不连网；真正的闸在 requireVerified。让构造随配置变化，
// 等于在后台点开一家之后还要重启进程才生效——那正是这次要消灭的东西。
func TestBuildSMSProvidersAlwaysBuildsEveryKnownProvider(t *testing.T) {
	providers, err := buildSMSProviders(smsModeFake, nil)
	if err != nil {
		t.Fatalf("fake 模式建适配器: %v", err)
	}
	got := make([]string, 0, len(providers))
	for _, p := range providers {
		got = append(got, p.ID)
	}
	want := []string{sms.ProviderSMS62, sms.ProviderHero}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("建出来的供应商 = %v, want %v", got, want)
	}
}

// 每一家推出来的密钥引用都必须**解析得过**。
//
// scope 的规则是 ^[a-z0-9][a-z0-9-]{0,63}$——**不含下划线**。
// 而供应商 id 是 hero_sms，直接拼进去得到的 secret://hero_sms/api-key 是非法的。
// 这个错只在 XM_SMS_MODE=real 时才炸，而且症状是「密钥引用非法」，看起来
// 像运营把引用填错了，实际上运营根本没得填——它是推导出来的。
func TestSMSCredentialRefIsParseableForEveryProvider(t *testing.T) {
	for _, id := range sms.AllProviders {
		ref := smsCredentialRef(id)
		if _, err := secrets.ParseCredentialRef(ref); err != nil {
			t.Errorf("供应商 %s 的密钥引用 %q 解析失败: %v", id, ref, err)
		}
	}
}

// 接码要用的每一条凭据都必须出现在「密钥引用」页上。
//
// 缺一条的症状很难查：功能整体是通的，只有那一条对应的能力静默失灵
// （比如推送不发，而取码本身正常），因为「没配」在这套代码里一律是安静跳过。
// 页面上有槽位，运营才知道有这么一样东西要填。
func TestSMSExpectedCredentialsCoverEveryRefWeResolve(t *testing.T) {
	listed := map[string]bool{}
	for _, e := range smsExpectedCredentials(smsConfig{Mode: smsModeReal}) {
		listed[e.Ref] = true
	}
	want := []string{smsNotifyWebhookRef}
	for _, id := range sms.AllProviders {
		want = append(want, smsCredentialRef(id))
	}
	for _, ref := range want {
		if !listed[ref] {
			t.Errorf("密钥引用页缺少 %q——运营没地方填它", ref)
		}
	}
}

// mode=off 时不登记：那些槽位对应的能力整个没挂载，
// 列出来只会让人填一份永远不会被读的值。
func TestSMSExpectedCredentialsEmptyWhenOff(t *testing.T) {
	if got := smsExpectedCredentials(smsConfig{Mode: smsModeOff}); len(got) != 0 {
		t.Fatalf("mode=off 不该登记凭据, got %d 条", len(got))
	}
}
