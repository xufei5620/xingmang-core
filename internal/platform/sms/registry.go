package sms

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/herosms"
	"github.com/xufei5620/xingmang-platform/connectors/sms62"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 供应商注册表（ADR-022 决策 2）。
//
// 此前供应商名字写死在八处：五张表的 CHECK、AllProviders、SupportsAction 的
// switch、凭据引用、两个适配器、替身、页面标签。接第三家要改迁移加七处代码。
// 现在**接一家 = 一个连接器包 + 下面 registry 里的一行**：能力集取代按名字的
// switch，凭据引用由 ID 推导，构造函数在这里，数据库不再对名字做 CHECK
// （迁移 000041），写路径由本注册表校验兜底。

// Capability 是一项能力。页面按能力渲染按钮，不按供应商名字判断——否则会出现
// 「62 出现在租用页然后报不支持」。
type Capability string

const (
	// CapPurchase：买号（sms.number.purchase）。
	CapPurchase Capability = "purchase"
	// CapLifecycle：取消 / 完成 / 换号 / 重激活 / 延长。
	CapLifecycle Capability = "lifecycle"
	// CapRent：按小时租号。
	CapRent Capability = "rent"
	// CapEmail：邮箱接码。
	CapEmail Capability = "email"
	// CapCatalog：库存 / 目录 / 价格。
	CapCatalog Capability = "catalog"
	// CapHistory：历史与统计。
	CapHistory Capability = "history"
	// CapBalance：余额。
	CapBalance Capability = "balance"
	// CapFavorites：收藏。
	CapFavorites Capability = "favorites"
	// CapOrders：上游订单列表与商品详情（62 的订单制）。
	CapOrders Capability = "orders"
	// CapToken：取码要带上游 token（62）。决定 sms_resource.provider_token 是否必填，
	// 以及 token 落库那条纪律是否适用。
	CapToken Capability = "token"
)

// ProviderSpec 是一家供应商的全部静态事实。
type ProviderSpec struct {
	ID    string
	Label string
	// Capabilities 是能力集。SupportsAction 与页面按钮都从它推导。
	Capabilities []Capability
	// Build 用真实凭据构造适配器。fake 模式不走它。
	Build func(provider secrets.SecretProvider, ref secrets.CredentialRef, now func() time.Time) (Adapter, error)
}

// Has 判断有没有某项能力。
func (p ProviderSpec) Has(c Capability) bool {
	for _, have := range p.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// CredentialRef 由 ID 推导：secret://<id>/api-key，下划线换连字符。
//
// **推导而不是配置**：少一个可以配错的地方；而 ref 的 scope 规则不收下划线
// （XM-SMS0 补正时踩过：hero_sms 直接拼进去解析不过，只在 real 模式炸）。
func (p ProviderSpec) CredentialRef() string {
	return "secret://" + strings.ReplaceAll(strings.ToLower(p.ID), "_", "-") + "/api-key"
}

// registry 是唯一的供应商清单。顺序即页面展示顺序。
var registry = []ProviderSpec{
	{
		ID: ProviderSMS62, Label: "62-US",
		Capabilities: []Capability{CapPurchase, CapCatalog, CapOrders, CapToken},
		Build: func(provider secrets.SecretProvider, ref secrets.CredentialRef, now func() time.Time) (Adapter, error) {
			host, err := hostOf(sms62.ProductionBaseURL)
			if err != nil {
				return nil, err
			}
			return NewSMS62Adapter(sms62.NewClient(provider, ref, []string{host}), now), nil
		},
	},
	{
		ID: ProviderHero, Label: "Hero-SMS",
		Capabilities: []Capability{CapPurchase, CapLifecycle, CapRent, CapEmail, CapCatalog, CapHistory, CapBalance, CapFavorites},
		Build: func(provider secrets.SecretProvider, ref secrets.CredentialRef, now func() time.Time) (Adapter, error) {
			host, err := hostOf(herosms.ProductionBaseURL)
			if err != nil {
				return nil, err
			}
			return NewHeroAdapter(herosms.NewClient(provider, ref, []string{host}), now), nil
		},
	},
}

// Providers 返回全部供应商（副本，顺序稳定）。
func Providers() []ProviderSpec {
	return append([]ProviderSpec(nil), registry...)
}

// Spec 按 ID 取一家。
func Spec(id string) (ProviderSpec, bool) {
	for _, p := range registry {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderSpec{}, false
}

// ProviderIDs 是 ID 清单，顺序同注册表。
func ProviderIDs() []string {
	out := make([]string, 0, len(registry))
	for _, p := range registry {
		out = append(out, p.ID)
	}
	return out
}

// ProvidersWith 是拥有某项能力的供应商 ID（给 Action 的 provider 枚举用）。
func ProvidersWith(c Capability) []string {
	var out []string
	for _, p := range registry {
		if p.Has(c) {
			out = append(out, p.ID)
		}
	}
	return out
}

// Label 是页面标签；不认识的 ID 原样返回。
func Label(id string) string {
	if p, ok := Spec(id); ok {
		return p.Label
	}
	return id
}

// capabilityForKind 把操作类型映射到能力。
func capabilityForKind(kind string) (Capability, bool) {
	switch kind {
	case KindPurchase:
		return CapPurchase, true
	case KindCancel, KindFinish, KindReplace, KindReactivate, KindProlong:
		return CapLifecycle, true
	case KindRent:
		return CapRent, true
	case KindEmailPurchase, KindEmailCancel, KindEmailReorder:
		return CapEmail, true
	case KindFavoriteSet, KindFavoriteRemove:
		return CapFavorites, true
	default:
		return "", false
	}
}

// hostOf 从固定端点取主机名给写通道的 allowlist。
//
// allowlist 只放这一个主机：写通道的 fail-closed 语义要求它非空，
// 而放宽到多个主机没有任何业务理由。带密钥的请求必须走 TLS。
func hostOf(raw string) (string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme != "https" {
		return "", fmt.Errorf("接码端点必须是 https，当前 %q", raw)
	}
	host := rest
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return "", fmt.Errorf("接码端点 %q 无法解析主机名", raw)
	}
	return host, nil
}

// sortedCapabilities 给对外 DTO 一个稳定顺序。
func sortedCapabilities(p ProviderSpec) []string {
	out := make([]string, 0, len(p.Capabilities))
	for _, c := range p.Capabilities {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}

// Capabilities 是某家的能力名清单（稳定顺序），给 /sms/providers 用。
func Capabilities(id string) []string {
	p, ok := Spec(id)
	if !ok {
		return nil
	}
	return sortedCapabilities(p)
}
