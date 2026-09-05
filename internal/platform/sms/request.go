package sms

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// 「要号」流程（ADR-022 决策 3，XM-SMS2 #6）。
//
// 服务 + 国家 + 数量（+ 可选指定供应商）→ 按规则选供应商 → 买 → 落资源。
// 失败按规则回落下一家，**每家最多试一次**；结果未知就停——钱可能已经花了，
// 再试下一家就是双倍花钱。自动取码由页面驱动（每 15 秒），这里不轮询。
//
// 幂等：同一个 request_id 再来一次是**回放**。每家的尝试用
// attemptOperationID(request_id, provider) 作台账 ID，确定性推导——重试时
// 先查台账，试过的家不再打上游。这是 XM-SMS4 接入规范的基础。

// RequestInput 是一次要号。
type RequestInput struct {
	// Service 是服务代号（Hero 的 service；62 按商品名匹配，纯数字当平台 ID）。
	Service string
	// Country 必须具体（Hero 的数字 ID / 62 的国家段），不接受通配。
	Country  string
	Quantity int
	// Provider 非空 = 人指定，只试这一家；空 = 按路由规则。
	Provider string
}

// RequestAttempt 是对一家供应商的一次尝试。
type RequestAttempt struct {
	Provider    string
	OperationID string
	// State 为空 = 没打上游（跳过：关着、没验证、翻译不了、超上限）。
	State       OperationState
	ProviderRef string
	Reason      string
	// Replayed = 这一笔来自台账（同一个 request_id 的重试）。
	Replayed bool
}

// RequestOutcome 是要号的结果。
type RequestOutcome struct {
	RequestID string
	Service   string
	Country   string
	Quantity  int
	// RuleID 是命中的路由规则；空 = 按装配顺序。
	RuleID string
	// State 只有三种：succeeded / failed / unknown。unknown 必须人工核对。
	State OperationState
	// Provider / OperationID / ResourceIDs 是赢家（succeeded）或需要核对的那一家（unknown）。
	Provider    string
	OperationID string
	ResourceIDs []string
	Attempts    []RequestAttempt
}

// ErrRequestInvalid：要号的输入不合法。
var ErrRequestInvalid = errors.New("sms: 要号请求不合法")

// maxRequestQuantity 与 62 官方单次上限一致。
const maxRequestQuantity = 200

// requestNamespace 是尝试台账 ID 的 UUIDv5 命名空间。**不要改**：改了同一个
// request_id 的回放就找不到旧尝试，重试会变成重买。
var requestNamespace = uuid.MustParse("6f1c9a3e-5b0e-4a2f-9c1d-3d2b7e8a9f10")

// attemptOperationID 由（request_id, provider）确定性推导台账 ID。
func attemptOperationID(requestID, provider string) string {
	return uuid.NewSHA1(requestNamespace, []byte(requestID+"\x00"+provider)).String()
}

// RequestNumber 要号。
func (s *Service) RequestNumber(ctx context.Context, requestID string, in RequestInput) (RequestOutcome, error) {
	requestID = strings.TrimSpace(requestID)
	in.Service = strings.ToLower(strings.TrimSpace(in.Service))
	in.Country = strings.TrimSpace(in.Country)
	in.Provider = strings.TrimSpace(in.Provider)
	switch {
	case requestID == "":
		return RequestOutcome{}, fmt.Errorf("%w：request_id 不能为空（由调用方稳定生成，重试带同一个）", ErrRequestInvalid)
	case in.Service == "" || in.Service == RouteAny:
		return RequestOutcome{}, fmt.Errorf("%w：服务不能为空", ErrRequestInvalid)
	case in.Country == "" || in.Country == RouteAny:
		return RequestOutcome{}, fmt.Errorf("%w：国家要具体，不接受通配", ErrRequestInvalid)
	case in.Quantity < 1 || in.Quantity > maxRequestQuantity:
		return RequestOutcome{}, fmt.Errorf("%w：数量要在 1–%d 之间", ErrRequestInvalid, maxRequestQuantity)
	}
	if in.Provider != "" {
		if _, ok := s.providers[in.Provider]; !ok {
			return RequestOutcome{}, fmt.Errorf("%w：供应商 %s 未装配", ErrRequestInvalid, in.Provider)
		}
	}

	route, err := s.Route(ctx, in.Service, in.Country, in.Provider)
	if err != nil {
		return RequestOutcome{}, err
	}

	// 配额在**打上游之前**查：拦晚了钱就已经花出去了（与买号那几道闸同一条）。
	// 人不受配额约束，checkQuota 直接放行。
	//
	// 回放（同一个 request_id 重试）**不重复计数**：那不是新的一次要号，而且
	// 一个正好卡在配额线上的重试会拿不回它已经买到的号——重试反而丢号是最坏的
	// 一种护栏。
	replay, err := s.hasPriorAttempt(ctx, requestID, route.Providers)
	if err != nil {
		return RequestOutcome{}, err
	}
	if !replay {
		if err := s.checkQuota(ctx, in.Quantity); err != nil {
			return RequestOutcome{}, err
		}
	}
	out := RequestOutcome{
		RequestID: requestID, Service: in.Service, Country: in.Country, Quantity: in.Quantity,
		RuleID: route.RuleID, State: StateFailed,
	}

	for _, provider := range route.Providers {
		attempt := RequestAttempt{Provider: provider, OperationID: attemptOperationID(requestID, provider)}

		// 回放：这家在同一个 request_id 下已经试过。
		prior, err := s.store.GetOperation(ctx, attempt.OperationID)
		switch {
		case err == nil:
			attempt.Replayed = true
			attempt.State, attempt.ProviderRef, attempt.Reason = prior.State, prior.ProviderRef, prior.FailureReason
			out.Attempts = append(out.Attempts, attempt)
			if s.settleRequest(ctx, &out, provider, prior, nil) {
				return out, nil
			}
			continue
		case !errors.Is(err, ErrOperationNotFound):
			return RequestOutcome{}, err
		}

		pin, err := s.purchaseInputFor(ctx, provider, in, route.MaxUnitPriceText)
		if err != nil {
			attempt.Reason = err.Error()
			out.Attempts = append(out.Attempts, attempt)
			continue
		}
		op, ids, err := s.purchase(ctx, attempt.OperationID, provider, pin)
		if err != nil {
			// 未决防重是硬边界：另一个进程正在为同一个请求打这家上游。
			if errors.Is(err, ErrPendingDuplicate) {
				return RequestOutcome{}, err
			}
			// 关着 / 没验证 / 不支持购买：没打上游，算「试过了」，回落下一家。
			attempt.Reason = err.Error()
			out.Attempts = append(out.Attempts, attempt)
			continue
		}
		attempt.State, attempt.ProviderRef, attempt.Reason = op.State, op.ProviderRef, op.FailureReason
		out.Attempts = append(out.Attempts, attempt)
		if s.settleRequest(ctx, &out, provider, op, ids) {
			return out, nil
		}
	}
	return out, nil
}

// hasPriorAttempt 说明这个 request_id 已经试过至少一家：那么这次调用是回放。
func (s *Service) hasPriorAttempt(ctx context.Context, requestID string, providers []string) (bool, error) {
	for _, provider := range providers {
		_, err := s.store.GetOperation(ctx, attemptOperationID(requestID, provider))
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, ErrOperationNotFound):
			return false, err
		}
	}
	return false, nil
}

// settleRequest 按一笔尝试的结果决定要不要停。成功停；未知也停（钱可能花了）；
// 明确失败才回落下一家。
func (s *Service) settleRequest(ctx context.Context, out *RequestOutcome, provider string, op Operation, ids []string) bool {
	switch op.State {
	case StateSucceeded, StateReconciledSucceeded:
		out.State, out.Provider, out.OperationID = StateSucceeded, provider, op.ID
		if len(ids) == 0 {
			ids = s.resourceIDsOf(ctx, op)
		}
		out.ResourceIDs = ids
		return true
	case StateUnknown, StatePrepared, StateSubmitted:
		// prepared / submitted 只会出现在回放里：另一个进程还在打这家上游。
		// 对我们来说它就是未知，一样不能再试下一家。
		out.State, out.Provider, out.OperationID = StateUnknown, provider, op.ID
		return true
	}
	return false
}

func (s *Service) resourceIDsOf(ctx context.Context, op Operation) []string {
	rs, err := s.store.ListResourcesByOperation(ctx, op.ID)
	if err != nil || len(rs) == 0 {
		if op.ResourceID != "" {
			return []string{op.ResourceID}
		}
		return nil
	}
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	return ids
}

// purchaseInputFor 把「服务 + 国家 + 数量」翻成某一家的购买入参。
//
// 两家对「服务」的理解不同：Hero 有服务与国家两个维度，62 只有商品。翻译放在
// 这里而不是适配器：适配器只管协议，不该知道路由的单价上限怎么用。
// 接第三家要在这里加一个分支（见 docs/modules/sms/INTEGRATION.md）。
func (s *Service) purchaseInputFor(ctx context.Context, provider string, in RequestInput, capText string) (PurchaseInput, error) {
	switch provider {
	case ProviderHero:
		country, err := strconv.Atoi(in.Country)
		if err != nil || country < 0 {
			return PurchaseInput{}, fmt.Errorf("Hero 的国家要是数字 ID，收到 %q", in.Country)
		}
		return PurchaseInput{Service: in.Service, Country: country, Quantity: in.Quantity, MaxPrice: capText}, nil
	case ProviderSMS62:
		adapter, err := s.adapter(provider)
		if err != nil {
			return PurchaseInput{}, err
		}
		filter := CatalogFilter{Country: in.Country}
		if isDigits(in.Service) {
			filter.PlatformID = in.Service
		}
		items, err := adapter.ListCatalog(ctx, filter)
		if err != nil {
			return PurchaseInput{}, fmt.Errorf("62 读商品失败：%w", err)
		}
		best, reason := pick62Goods(items, in, capText)
		if best == nil {
			return PurchaseInput{}, errors.New(reason)
		}
		return PurchaseInput{GoodsID: best.ID, Quantity: in.Quantity}, nil
	}
	return PurchaseInput{}, fmt.Errorf("%s 还没有「服务 + 国家」到购买入参的翻译", provider)
}

// pick62Goods 在 62 的商品里挑：匹配服务与国家、有货、上限内、最便宜。
//
// 服务是纯数字就按平台 ID（商品 ID 的第一段）匹配，否则按商品名包含匹配。
// 没有价格的商品不买——不知道多少钱的东西不能自动下单。
func pick62Goods(items []CatalogItem, in RequestInput, capText string) (*CatalogItem, string) {
	var capPrice *big.Rat
	if capText != "" {
		capPrice, _ = parseDecimal(capText)
	}
	var best *CatalogItem
	var bestPrice *big.Rat
	matched, noStock, overCap, noPrice := 0, 0, 0, 0
	for i := range items {
		it := &items[i]
		if !matches62(it, in) {
			continue
		}
		matched++
		if it.Available < int64(in.Quantity) {
			noStock++
			continue
		}
		price, ok := parseDecimal(it.DefaultPrice)
		if !ok {
			noPrice++
			continue
		}
		if capPrice != nil && price.Cmp(capPrice) > 0 {
			overCap++
			continue
		}
		if best == nil || price.Cmp(bestPrice) < 0 {
			best, bestPrice = it, price
		}
	}
	if best != nil {
		return best, ""
	}
	switch {
	case matched == 0:
		return nil, fmt.Sprintf("62 没有匹配「%s / %s」的商品", in.Service, in.Country)
	case overCap > 0:
		return nil, fmt.Sprintf("62 匹配的商品单价都超过上限 %s", capText)
	case noStock > 0:
		return nil, fmt.Sprintf("62 匹配的商品库存不足（要 %d 个）", in.Quantity)
	default:
		return nil, "62 匹配的商品没有价格，不能自动下单"
	}
}

func matches62(it *CatalogItem, in RequestInput) bool {
	if it.Country != "" && it.Country != in.Country {
		return false
	}
	if isDigits(in.Service) {
		return strings.HasPrefix(it.ID, in.Service+"-")
	}
	return strings.Contains(strings.ToLower(it.Name), in.Service)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseDecimal 解析十进制文本；不是数字或为负都算没有价格。
func parseDecimal(text string) (*big.Rat, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok || r.Sign() < 0 {
		return nil, false
	}
	return r, true
}
