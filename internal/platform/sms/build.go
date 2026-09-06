package sms

import (
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 装配（XM-SMS2 #7）。
//
// 放在领域包而不是各自的 main 里：platform-api 与 platform-worker 必须解析出
// **同一组供应商**。两份解析迟早会分叉成「API 能买号、worker 不认识这家」，
// 而那种分叉不报错——巡检只是永远看不到那家的余额，没人会去查一个一直没数据
// 的图。卡片那边的注释写着同一条教训（两个进程必须解析出同一组账号）。

// Mode 决定这个进程打不打真实供应商。
//
// **留在环境变量里**，与「开哪几家」不同：后者是运营随时会改的决定，在管理
// 后台点（sms.provider_status.enabled）；Mode 决定会不会花真钱，做成后台可改
// 意味着一次误操作能让开发环境开始买真号，或者让生产悄悄切到替身而页面看起来
// 一切正常。
type Mode string

const (
	ModeOff  Mode = "off"
	ModeFake Mode = "fake"
	ModeReal Mode = "real"
)

// ParseMode 解析 XM_SMS_MODE。空值 = off。
func ParseMode(raw string) (Mode, error) {
	switch v := Mode(strings.ToLower(strings.TrimSpace(raw))); v {
	case "", ModeOff:
		return ModeOff, nil
	case ModeFake, ModeReal:
		return v, nil
	default:
		// 逐字列出合法值：把 XM_CARDS_MODE 写成 live 让生产下线四分钟，
		// 就是因为错误信息没说清能填什么。
		return "", fmt.Errorf("XM_SMS_MODE=%q 非法，只接受 off / fake / real", raw)
	}
}

// BuildProviders 把注册表里的每一家都建出来。off 时返回空。
//
// **不挑不选**：建适配器既不花钱也不连网，真正的闸是 requireVerified（关着的、
// 没验证过的都拦在打上游之前）。构造阶段就少建一家，会让后台点开它之后仍然
// 报「未知供应商」，而那个错误看起来像代码不支持这家。
func BuildProviders(mode Mode, secretProvider secrets.SecretProvider, now func() time.Time) ([]Provider, error) {
	if mode == ModeOff {
		return nil, nil
	}
	if now == nil {
		now = time.Now
	}
	providers := make([]Provider, 0, len(AllProviders))
	for _, id := range AllProviders {
		adapter, err := BuildAdapter(mode, id, secretProvider, now)
		if err != nil {
			return nil, err
		}
		providers = append(providers, Provider{ID: id, Adapter: adapter})
	}
	return providers, nil
}

// BuildAdapter 建一家的适配器。real 模式缺 SecretProvider 时**失败**，
// 不悄悄退回替身：一个以为在打真实上游、其实什么都没做的进程更难发现。
func BuildAdapter(mode Mode, provider string, secretProvider secrets.SecretProvider, now func() time.Time) (Adapter, error) {
	if now == nil {
		now = time.Now
	}
	if mode == ModeFake {
		return NewFakeAdapter(provider, now), nil
	}
	if secretProvider == nil {
		return nil, fmt.Errorf("XM_SMS_MODE=real 需要 SecretProvider")
	}
	// 构造函数在注册表里（ADR-022）：接第三家不改这里。
	spec, ok := Spec(provider)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderUnknown, provider)
	}
	ref, err := secrets.ParseCredentialRef(spec.CredentialRef())
	if err != nil {
		return nil, fmt.Errorf("供应商 %s 的密钥引用 %q 非法: %w", provider, spec.CredentialRef(), err)
	}
	return spec.Build(secretProvider, ref, now)
}
