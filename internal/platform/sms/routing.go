package sms

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// 路由规则（ADR-022 决策 3，XM-SMS2 #5）。
//
// 「要号」默认由系统按规则选供应商，人只在想指定时才选。规则按「服务 × 国家」
// 配，值是供应商的优先级列表——第一家失败回落下一家，每家最多试一次（回落
// 逻辑在要号流程里，这里只负责「按什么顺序」）——与单价上限。
//
// 规则不在环境变量里：换供应商、某家挂了先停掉，都是运营随时会改的决定，
// 与供应商开关同一条纪律（迁移 000038 的注释）。

// RouteAny 是服务或国家的通配值。
const RouteAny = "*"

// RoutingRule 是一条路由规则。同一环境下「服务 × 国家」唯一。
type RoutingRule struct {
	ID string
	// Service 是服务代号（Hero 的 service、62 的平台）；RouteAny = 任意服务。
	// 存小写：Hero 的代号本就是小写，大小写不同的两条规则不该并存。
	Service string
	// Country 是国家（Hero 的数字 ID、62 的国家段）；RouteAny = 任意国家。
	Country string
	// Providers 是优先级顺序，第一家优先。
	Providers []string
	// MaxUnitPriceText 是单价上限，十进制文本；空 = 不限。
	//
	// 按各家**自己的币种**比较（62 是 USD，Hero 是账户币种），不折算——
	// 与成本统计同口径。跨币种的上限没有意义，配的人要知道这条规则指向哪家。
	MaxUnitPriceText string
	Enabled          bool
	UpdatedAt        time.Time
}

// Route 是一次「要号」解析出的结果。
type Route struct {
	// Providers 是要依次尝试的供应商。人指定了就只有那一家。
	Providers []string
	// RuleID 是命中的规则；空 = 没有规则，用的是装配顺序。
	RuleID string
	// MaxUnitPriceText 来自命中的规则；空 = 不限。
	MaxUnitPriceText string
}

var (
	// ErrRoutingRuleInvalid：规则本身不合法（服务 / 国家为空、供应商未知或重复、
	// 单价不是正的十进制文本）。
	ErrRoutingRuleInvalid = errors.New("sms: 路由规则不合法")
	// ErrRoutingRuleNotFound：要删的规则不存在（或已被删）。
	ErrRoutingRuleNotFound = errors.New("sms: 路由规则不存在")
)

// unitPricePattern：正的十进制文本，最多六位小数（与 numeric 列一致）。
// 不接受符号与币种：上限按供应商自己的币种比较，写「$1」只会让人以为能跨币种。
var unitPricePattern = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,6})?$`)

// NormalizeRoutingRule 去空白、服务小写、丢掉空的供应商项。
func NormalizeRoutingRule(r RoutingRule) RoutingRule {
	r.Service = strings.ToLower(strings.TrimSpace(r.Service))
	r.Country = strings.TrimSpace(r.Country)
	r.MaxUnitPriceText = strings.TrimSpace(r.MaxUnitPriceText)
	providers := make([]string, 0, len(r.Providers))
	for _, p := range r.Providers {
		if p = strings.TrimSpace(p); p != "" {
			providers = append(providers, p)
		}
	}
	r.Providers = providers
	return r
}

// ValidateRoutingRule 校验一条规则。configured 是**已装配的**供应商：规则指向一家
// 这个进程根本没有的供应商，要号时只会一路回落到失败。
func ValidateRoutingRule(r RoutingRule, configured []string) error {
	r = NormalizeRoutingRule(r)
	if r.Service == "" {
		return fmt.Errorf("%w：服务不能为空（任意服务写 %s）", ErrRoutingRuleInvalid, RouteAny)
	}
	if r.Country == "" {
		return fmt.Errorf("%w：国家不能为空（任意国家写 %s）", ErrRoutingRuleInvalid, RouteAny)
	}
	if len(r.Providers) == 0 {
		return fmt.Errorf("%w：至少要有一家供应商", ErrRoutingRuleInvalid)
	}
	seen := make(map[string]bool, len(r.Providers))
	for _, p := range r.Providers {
		if seen[p] {
			return fmt.Errorf("%w：供应商 %s 重复", ErrRoutingRuleInvalid, p)
		}
		seen[p] = true
		if !slices.Contains(configured, p) {
			return fmt.Errorf("%w：供应商 %s 未装配", ErrRoutingRuleInvalid, p)
		}
		if !SupportsAction(p, KindPurchase) {
			return fmt.Errorf("%w：供应商 %s 不能买号", ErrRoutingRuleInvalid, p)
		}
	}
	if r.MaxUnitPriceText != "" {
		if !unitPricePattern.MatchString(r.MaxUnitPriceText) {
			return fmt.Errorf("%w：单价上限要是十进制数字（如 0.35），不带符号与币种", ErrRoutingRuleInvalid)
		}
		if strings.Trim(r.MaxUnitPriceText, "0.") == "" {
			return fmt.Errorf("%w：单价上限要大于 0（不限就留空）", ErrRoutingRuleInvalid)
		}
	}
	return nil
}

// ResolveRoute 按规则解析一次要号该走哪几家。
//
// 命中顺序：精确 > 服务通配国家 > 国家通配服务 > 全通配 > 默认顺序。
// 服务比国家优先：同一个服务在各国的供应商偏好通常一致（哪家对这个平台成功率
// 高），「某国一律走某家」是更粗的兜底。
//
// preferred 非空 = 人指定了：只用那一家，但单价上限仍按命中的规则——人指定
// 的是「走哪家」，不是「不管多贵」。
func ResolveRoute(rules []RoutingRule, defaults []string, service, country, preferred string) Route {
	service = strings.ToLower(strings.TrimSpace(service))
	country = strings.TrimSpace(country)
	preferred = strings.TrimSpace(preferred)

	var best *RoutingRule
	bestRank := len(rankNames)
	for i := range rules {
		r := &rules[i]
		if !r.Enabled {
			continue
		}
		rank := routeRank(r, service, country)
		if rank >= 0 && rank < bestRank {
			best, bestRank = r, rank
		}
	}

	route := Route{}
	if best != nil {
		route.RuleID = best.ID
		route.MaxUnitPriceText = best.MaxUnitPriceText
		route.Providers = append([]string(nil), best.Providers...)
	} else {
		route.Providers = append([]string(nil), defaults...)
	}
	if preferred != "" {
		route.Providers = []string{preferred}
	}
	return route
}

var rankNames = [...]string{"精确", "服务通配国家", "国家通配服务", "全通配"}

func routeRank(r *RoutingRule, service, country string) int {
	switch {
	case r.Service == service && r.Country == country:
		return 0
	case r.Service == service && r.Country == RouteAny:
		return 1
	case r.Service == RouteAny && r.Country == country:
		return 2
	case r.Service == RouteAny && r.Country == RouteAny:
		return 3
	}
	return -1
}

// SetRoutingRule 新建或覆盖一条规则（同「服务 × 国家」只有一条）。
func (s *Service) SetRoutingRule(ctx context.Context, r RoutingRule) (RoutingRule, error) {
	r = NormalizeRoutingRule(r)
	if err := ValidateRoutingRule(r, s.order); err != nil {
		return RoutingRule{}, err
	}
	r.UpdatedAt = s.now()
	id, err := s.store.UpsertRoutingRule(ctx, r)
	if err != nil {
		return RoutingRule{}, err
	}
	r.ID = id
	return r, nil
}

// RemoveRoutingRule 删一条规则。不存在就是 ErrRoutingRuleNotFound。
func (s *Service) RemoveRoutingRule(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrRoutingRuleNotFound
	}
	return s.store.RemoveRoutingRule(ctx, strings.TrimSpace(id))
}

// ListRoutingRules 列出本环境全部规则（含停用的）。
func (s *Service) ListRoutingRules(ctx context.Context) ([]RoutingRule, error) {
	return s.store.ListRoutingRules(ctx)
}

// Route 用库里的规则解析一次要号。没有规则时按装配顺序。
func (s *Service) Route(ctx context.Context, service, country, preferred string) (Route, error) {
	if preferred = strings.TrimSpace(preferred); preferred != "" {
		if _, ok := s.providers[preferred]; !ok {
			return Route{}, fmt.Errorf("%w: %s", ErrProviderUnknown, preferred)
		}
	}
	rules, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return Route{}, err
	}
	return ResolveRoute(rules, s.order, service, country, preferred), nil
}
