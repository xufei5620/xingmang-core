package sms

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Provider 是一家供应商的运行时装配。
type Provider struct {
	ID      string
	Adapter Adapter
}

// Adapter 是供应商无关的能力面。
//
// 两家的协议差别很大（订单制 vs activation 制、Bearer vs ApiKey、
// 买完要不要再读一次），这一层的作用就是把那些差别关在各自的实现里，
// 让 Service 只处理**七态推进**这一件事。
type Adapter interface {
	// TestConnection 只读地验证凭据。返回上游观察到的我方出口 IP（没有就空）。
	TestConnection(ctx context.Context) (clientIP string, err error)
	// ListCatalog 读库存。
	ListCatalog(ctx context.Context, filter CatalogFilter) ([]CatalogItem, error)
	// Purchase 买号。
	//
	// **返回的 resources 可能为空而 orderRef 非空**——那正是 62 的形态：
	// 买完只回订单 ID，号码要再读一次。调用方据此判断是否需要补读。
	Purchase(ctx context.Context, in PurchaseInput) (PurchaseOutcome, error)
	// ImportByUpstreamID 按上游 ID 只读导入。用于 unknown 的人工核对。
	//
	// **这条链绝不购买**：它存在的全部理由是「钱可能已经花了，把号码找回来」。
	ImportByUpstreamID(ctx context.Context, upstreamID string) ([]Resource, error)
	// FetchCode 取码。没有码时返回 ErrCodeNotAvailable（**正常状态**）。
	FetchCode(ctx context.Context, r Resource) (Code, error)
	// ExecuteAction 执行生命周期动作（仅 Hero）。
	ExecuteAction(ctx context.Context, kind string, r Resource, opt ActionOptions) (Resource, error)
}

// CatalogFilter 是库存筛选。两家的筛选维度不同，合并成一个结构体但各取所需。
type CatalogFilter struct {
	PlatformID string // 62
	Country    string // 两家
	Service    string // Hero
}

// CatalogItem 是库存里的一项。
//
// 三个价格分开保留（Hero 的 default/retail/min 是不同事实）；缺失就是缺失，
// **不补零也不补币种**——一个补出来的 0.00 会被当成"免费"。
type CatalogItem struct {
	ID           string
	Name         string
	Service      string
	Country      string
	DefaultPrice string
	RetailPrice  string
	MinimumPrice string
	Available    int64
}

// PurchaseInput 是一次购买的输入（两家的并集，各取所需）。
type PurchaseInput struct {
	// 62
	GoodsID       string
	FirstNumber   string
	NoFirstNumber string
	// Hero
	Service  string
	Country  int
	Operator string
	MaxPrice string
	Duration int
	// 两家
	Quantity int
}

// PurchaseOutcome 是购买的结果。
type PurchaseOutcome struct {
	// OrderRef 是上游订单 ID（62）；Hero 为空。
	OrderRef string
	// Resources 是已拿到的号码。**62 在这一步是空的**。
	Resources []Resource
	// RequestID 是上游的请求 ID，进台账便于对账。
	RequestID string
}

// ActionOptions 是生命周期动作的可选参数。
type ActionOptions struct {
	Duration int
}

// Notifier 把收到的验证码推出去。
//
// 与卡片那条通道同一个形状：失败只记日志，不影响取码本身返回成功——
// 丢一条推送是可接受的损失，丢一次取码不是。
type Notifier interface {
	NotifyCode(ctx context.Context, provider, phoneMask, code string) error
}

// Service 是接码中心的领域服务。
type Service struct {
	providers map[string]Provider
	order     []string
	store     Store
	notifier  Notifier
	now       func() time.Time
}

// NewService 组装。
func NewService(providers []Provider, store Store, notifier Notifier, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{
		providers: make(map[string]Provider, len(providers)),
		store:     store,
		notifier:  notifier,
		now:       now,
	}
	for _, p := range providers {
		s.providers[p.ID] = p
		s.order = append(s.order, p.ID)
	}
	return s
}

// Providers 返回已装配的供应商 id，顺序跟着配置走。
func (s *Service) Providers() []string { return append([]string(nil), s.order...) }

func (s *Service) adapter(provider string) (Adapter, error) {
	p, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProviderUnknown, provider)
	}
	return p.Adapter, nil
}

// TestConnection 做一次只读的连接测试并**记下这个事实**。
//
// 它不是纯本地无副作用的：成功会写 verified_at，而购买前会检查它。
// 这是刻意的——「验证过」必须是一个有时间戳的事实，不是一次口头确认。
func (s *Service) TestConnection(ctx context.Context, provider string) (ProviderStatus, error) {
	adapter, err := s.adapter(provider)
	if err != nil {
		return ProviderStatus{}, err
	}
	clientIP, testErr := adapter.TestConnection(ctx)

	status := ProviderStatus{Provider: provider, ClientIP: clientIP, UpdatedAt: s.now()}
	if testErr != nil {
		// 失败**不清掉上一次的 verified_at**：一次网络抖动不该让一个配好的
		// 供应商变回「从未验证」，那会连带把购买挡死。只记下这次的错误。
		prev, _ := s.store.ProviderStatus(ctx, provider)
		status.VerifiedAt = prev.VerifiedAt
		status.LastError = testErr.Error()
		if saveErr := s.store.SaveProviderStatus(ctx, status); saveErr != nil {
			return ProviderStatus{}, saveErr
		}
		return status, testErr
	}

	status.VerifiedAt = s.now()
	if err := s.store.SaveProviderStatus(ctx, status); err != nil {
		return ProviderStatus{}, err
	}
	return status, nil
}

// ListCatalog 读库存。库存**不落库**：它变化频繁，而且只在人要挑商品时才有用。
func (s *Service) ListCatalog(ctx context.Context, provider string, filter CatalogFilter) ([]CatalogItem, error) {
	adapter, err := s.adapter(provider)
	if err != nil {
		return nil, err
	}
	return adapter.ListCatalog(ctx, filter)
}

// Purchase 买号。
//
// 顺序是刻意的，每一步都在**打上游之前**：
//
//	供应商解析 → 动作支持性 → 已验证 → 参数规范化与指纹 → 未决防重
//	→ prepared → submitted → 打上游
//
// 拦晚了钱就已经花出去了。
//
// 上游成功之后的落库若失败，台账会停在 submitted——**不会可靠地立刻变成
// unknown**。下一次进程启动的 RecoverStaleOperations 才把它推到 unknown。
// 所以「持久化失败即 unknown」是流程的结果，不是当下的 DB 状态；
// 排查时不能假设看到的那条已经是 unknown。（参考实现踩过同一个坑。）
func (s *Service) Purchase(ctx context.Context, operationID, provider string, in PurchaseInput) (Operation, error) {
	adapter, err := s.adapter(provider)
	if err != nil {
		return Operation{}, err
	}
	if !SupportsAction(provider, KindPurchase) {
		return Operation{}, fmt.Errorf("%w: %s 不支持购买", ErrActionNotSupported, provider)
	}
	if err := s.requireVerified(ctx, provider); err != nil {
		return Operation{}, err
	}

	params := purchaseParams(provider, in)
	op := Operation{
		ID:            operationID,
		Provider:      provider,
		Kind:          KindPurchase,
		State:         StatePrepared,
		RequestHash:   CanonicalRequestHash(provider, KindPurchase, params),
		ParamsSummary: summarize(params),
		StartedAt:     s.now(),
		UpdatedAt:     s.now(),
	}
	if err := s.store.PrepareOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	if err := s.store.MarkSubmitted(ctx, op.ID, s.now()); err != nil {
		return Operation{}, err
	}
	op.State = StateSubmitted

	outcome, purchaseErr := adapter.Purchase(ctx, in)
	if purchaseErr != nil {
		return s.settleFailure(ctx, op, purchaseErr)
	}

	op.ProviderRequestID = outcome.RequestID
	op.ProviderRef = outcome.OrderRef

	// 62：买成功了但这一步没有号码，要再读一次。**那次读失败时钱已经花了**，
	// 所以它落 unknown 而不是 failed——哪怕上游回的是 4xx。
	resources := outcome.Resources
	if len(resources) == 0 && outcome.OrderRef != "" {
		imported, importErr := adapter.ImportByUpstreamID(ctx, outcome.OrderRef)
		if importErr != nil {
			op.State = StateUnknown
			op.NeedsHumanReview = true
			op.FailureReason = "购买已提交但补读号码失败，需人工到供应商侧核对：" + importErr.Error()
			op.UpdatedAt = s.now()
			if err := s.store.ResolveOperation(ctx, op); err != nil {
				return Operation{}, err
			}
			return op, nil
		}
		resources = imported
	}

	if orderID, err := s.saveOrder(ctx, provider, outcome, in); err == nil {
		op.OrderID = orderID
	}
	for i := range resources {
		resources[i].Provider = provider
		resources[i].OrderID = op.OrderID
		resources[i].SyncedAt = s.now()
		id, err := s.store.UpsertResource(ctx, resources[i])
		if err != nil {
			// 号码没落库 = 买到了却记不下来。落 unknown 交人工，
			// 而不是当成失败——那会诱使人再买一次。
			op.State = StateUnknown
			op.NeedsHumanReview = true
			op.FailureReason = "购买成功但号码落库失败：" + err.Error()
			op.UpdatedAt = s.now()
			_ = s.store.ResolveOperation(ctx, op)
			return op, nil
		}
		if op.ResourceID == "" {
			op.ResourceID = id
		}
	}

	op.State = StateSucceeded
	op.UpdatedAt = s.now()
	if err := s.store.ResolveOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// ExecuteAction 执行 Hero 的生命周期动作。
func (s *Service) ExecuteAction(ctx context.Context, operationID, kind, resourceID string, opt ActionOptions) (Operation, error) {
	resource, err := s.store.GetResource(ctx, resourceID)
	if err != nil {
		return Operation{}, err
	}
	adapter, err := s.adapter(resource.Provider)
	if err != nil {
		return Operation{}, err
	}
	if !SupportsAction(resource.Provider, kind) {
		return Operation{}, fmt.Errorf("%w: %s 不支持 %s", ErrActionNotSupported, resource.Provider, kind)
	}
	if err := s.requireVerified(ctx, resource.Provider); err != nil {
		return Operation{}, err
	}

	params := map[string]string{
		"resource": resource.ExternalID,
		"duration": strconv.Itoa(opt.Duration),
	}
	op := Operation{
		ID:            operationID,
		Provider:      resource.Provider,
		Kind:          kind,
		State:         StatePrepared,
		ResourceID:    resource.ID,
		RequestHash:   CanonicalRequestHash(resource.Provider, kind, params),
		ParamsSummary: summarize(params),
		StartedAt:     s.now(),
		UpdatedAt:     s.now(),
	}
	if err := s.store.PrepareOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	if err := s.store.MarkSubmitted(ctx, op.ID, s.now()); err != nil {
		return Operation{}, err
	}
	op.State = StateSubmitted

	updated, actionErr := adapter.ExecuteAction(ctx, kind, resource, opt)
	if actionErr != nil {
		return s.settleFailure(ctx, op, actionErr)
	}

	// replace 可能返回**新的 activation ID**。落新资源，而 op.ResourceID
	// 仍指向原目标——那笔操作针对的是「那张旧号」，改写它会让事后对账
	// 找不到起点。
	if updated.ExternalID != "" {
		updated.Provider = resource.Provider
		updated.SyncedAt = s.now()
		if _, err := s.store.UpsertResource(ctx, updated); err != nil {
			op.State = StateUnknown
			op.NeedsHumanReview = true
			op.FailureReason = "动作已执行但资源落库失败：" + err.Error()
			op.UpdatedAt = s.now()
			_ = s.store.ResolveOperation(ctx, op)
			return op, nil
		}
		op.ProviderRef = updated.ExternalID
	}

	op.State = StateSucceeded
	op.UpdatedAt = s.now()
	if err := s.store.ResolveOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// FetchCode 取一次码。
//
// **人发起，不是后台轮询**：常驻轮询器会在没人用的时候持续打上游，而这家的
// 限流是按密钥算的——把配额烧在没人看的号上，真要用的时候就被限流了。
//
// 取到码就落库并推送；取不到返回 ErrCodeNotAvailable，那是**正常状态**
// （码要几秒到几十秒才来），调用方据此继续等而不是报错。
func (s *Service) FetchCode(ctx context.Context, resourceID string) (Code, error) {
	resource, err := s.store.GetResource(ctx, resourceID)
	if err != nil {
		return Code{}, err
	}
	adapter, err := s.adapter(resource.Provider)
	if err != nil {
		return Code{}, err
	}

	code, err := adapter.FetchCode(ctx, resource)
	if err != nil {
		return Code{}, err
	}
	code.Provider = resource.Provider
	code.ResourceID = resource.ID
	code.CreatedAt = s.now()

	stored, isNew, err := s.store.RecordCode(ctx, code)
	if err != nil {
		return Code{}, err
	}
	if err := s.store.TouchResourceCodeAt(ctx, resource.ID, s.now()); err != nil {
		return Code{}, err
	}

	// 只推**新**的码：轮询会反复读到同一条短信，每次都推会让人在群里看到
	// 十条一样的码，然后开始忽略它们。
	if isNew && s.notifier != nil {
		if notifyErr := s.notifier.NotifyCode(ctx, resource.Provider, resource.PhoneMask, stored.Code); notifyErr != nil {
			// 推送失败只记日志、不改变取码的返回：丢一条推送是可接受的
			// 损失，丢一次取码不是。
			_ = notifyErr
		}
	}
	return stored, nil
}

// Resolve 人工核对一笔 unknown。
//
// **只改本地账本，绝不向上游重发原动作。** 结论是人输入的本地事实，后端不
// 自动证明所选资源就是原那笔购买——批量购买的逐号对账仍靠人。
func (s *Service) Resolve(ctx context.Context, operationID string, succeeded bool, resourceID, note string) (Operation, error) {
	op, err := s.store.GetOperation(ctx, operationID)
	if err != nil {
		return Operation{}, err
	}
	if op.State != StateUnknown {
		return Operation{}, fmt.Errorf("只有 unknown 的操作可以人工核对，当前是 %s", op.State)
	}
	if strings.TrimSpace(note) == "" {
		// 强制留一句话：三个月后回看这条记录时，「谁凭什么判定它成功了」
		// 只有这句话回答得了。
		return Operation{}, errors.New("人工核对必须写明依据")
	}
	if succeeded && strings.TrimSpace(resourceID) == "" && op.Kind == KindPurchase {
		return Operation{}, errors.New("判定购买成功必须关联一个本地号码")
	}

	if succeeded {
		op.State = StateReconciledSucceeded
	} else {
		op.State = StateReconciledFailed
	}
	if resourceID != "" {
		op.ResourceID = resourceID
	}
	op.ResolveNote = note
	op.NeedsHumanReview = false
	op.UpdatedAt = s.now()
	if err := s.store.ResolveOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// Recover 把进程崩溃遗留的未决操作推进到该去的地方。启动时调一次。
func (s *Service) Recover(ctx context.Context) (int, error) {
	return s.store.RecoverStaleOperations(ctx, s.now())
}

func (s *Service) requireVerified(ctx context.Context, provider string) error {
	status, err := s.store.ProviderStatus(ctx, provider)
	if err != nil {
		return err
	}
	if !status.Verified() {
		return fmt.Errorf("%w：先在页面上做一次连接测试", ErrProviderNotVerified)
	}
	return nil
}

// settleFailure 把上游错误落成 failed 或 unknown。
//
// **这个分档是整套幂等设计的支点**：failed 意味着钱没花出去、可以改参数重来；
// unknown 意味着可能已扣费、必须人工核对。判错一边就会重复买号。
func (s *Service) settleFailure(ctx context.Context, op Operation, cause error) (Operation, error) {
	op.UpdatedAt = s.now()
	op.FailureReason = cause.Error()
	if definitelyRejected(cause) {
		op.State = StateFailed
	} else {
		op.State = StateUnknown
		op.NeedsHumanReview = true
	}
	if err := s.store.ResolveOperation(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (s *Service) saveOrder(ctx context.Context, provider string, outcome PurchaseOutcome, in PurchaseInput) (string, error) {
	if outcome.OrderRef == "" {
		return "", nil
	}
	return s.store.UpsertOrder(ctx, Order{
		Provider:        provider,
		ProviderOrderID: outcome.OrderRef,
		GoodsID:         in.GoodsID,
		Quantity:        in.Quantity,
	})
}

func purchaseParams(provider string, in PurchaseInput) map[string]string {
	params := map[string]string{"quantity": strconv.Itoa(in.Quantity)}
	if provider == ProviderSMS62 {
		params["goods_id"] = in.GoodsID
		if in.FirstNumber != "" {
			params["first_number"] = in.FirstNumber
		}
		if in.NoFirstNumber != "" {
			params["no_first_number"] = in.NoFirstNumber
		}
		return params
	}
	params["service"] = in.Service
	params["country"] = strconv.Itoa(in.Country)
	if in.Operator != "" {
		params["operator"] = in.Operator
	}
	if in.MaxPrice != "" {
		params["max_price"] = in.MaxPrice
	}
	if in.Duration > 0 {
		params["duration"] = strconv.Itoa(in.Duration)
	}
	return params
}

// summarize 把参数拼成给人看的一行。**不含号码与验证码**——它进审计摘要，
// 而审计摘要会被广泛展示。
func summarize(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	// 排序让同一份参数每次拼出同一行，便于比对。
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, " ")
}
