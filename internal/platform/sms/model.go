// Package sms 是接码中心的领域层（XM-SMS0）。
//
// 与 internal/platform/cards 同一条纪律，而且是刻意的：这两块都在花钱、
// 都有「请求发出去但没收到回复」的第三态、都靠人工对账收敛。让它们长得像，
// 排查的人就不必学两套。
package sms

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 供应商标识。**只有这两个**，与迁移里的 CHECK 约束一一对应。
const (
	ProviderSMS62 = "sms62"
	ProviderHero  = "hero_sms"
)

// AllProviders 是代码支持的全部供应商，顺序即页面上的展示顺序。
//
// **由注册表生成**（registry.go），不再手写第二份清单。进程永远把全部供应商
// 都建出来，能不能用由库里的 enabled 决定。
var AllProviders = ProviderIDs()

// 操作类型。62 只支持 purchase；其余全部是 Hero 独有。
//
// 与迁移 000040 的 CHECK 一一对应。花钱的有四个：purchase、rent、
// email_purchase、email_reorder——它们**必须**走七态台账；不花钱的
// （favorite_*、email_cancel）也走台账，为的是审计里能查到「谁做的」。
const (
	KindPurchase   = "purchase"
	KindCancel     = "cancel"
	KindFinish     = "finish"
	KindReplace    = "replace"
	KindReactivate = "reactivate"
	KindProlong    = "prolong"
	// KindRent：Hero 兼容层的租用号码（按小时计费）。**花钱。**
	KindRent = "rent"
	// 邮箱接码（Hero Emails 组）。
	KindEmailPurchase = "email_purchase"
	KindEmailCancel   = "email_cancel"
	KindEmailReorder  = "email_reorder"
	// 收藏。不花钱，但仍走 Action 留审计。
	KindFavoriteSet    = "favorite_set"
	KindFavoriteRemove = "favorite_remove"
)

// NumberState 是**平台自己的**号码状态（ADR-022 决策 4）。
//
// 上游原话保留在 Resource.Status（Hero 是 1/2/3/4/6/7/8/10，62 是「正常」之类），
// 这里是它们的统一含义。五个值够日常判断：这个号还能不能用、码到了没有。
type NumberState string

const (
	StateWaitingCode  NumberState = "waiting_code"  // 待收码
	StateCodeReceived NumberState = "code_received" // 已收码
	StateFinished     NumberState = "finished"      // 已完成
	StateCancelled    NumberState = "cancelled"     // 已取消（含退款）
	StateExpired      NumberState = "expired"       // 已过期
)

// MapHeroStatus 把 Hero 官方 ActivationStatusTypes 映射到统一状态。
//
// 含义来自官方 setStatus 文档（3 请求重发、6 完成、8 取消）与 SMS-Activate 协议的
// 惯例（1 等待、2 等待重发、4 已收到、7 过期、10 退款）。没见过的值归待收码——
// 一个不认识的状态更可能是「还在进行中」而不是「已经结束」。
func MapHeroStatus(status string) NumberState {
	switch strings.TrimSpace(status) {
	case "4":
		return StateCodeReceived
	case "6":
		return StateFinished
	case "7":
		return StateExpired
	case "8", "10":
		return StateCancelled
	default:
		return StateWaitingCode
	}
}

// 资源子类型（官方 ActivationSubtype）。
const (
	SubtypeActivation int64 = 1
	SubtypeRent       int64 = 2
)

// OperationState 是操作台账里的状态。
//
// 七态而不是三态，比卡片那套多两个：prepared 与 submitted 把「本地已建账」
// 与「已经发出去了」分开，因为进程在这两点之间崩溃的处置完全不同——
// prepared 崩了可以直接判失败（还没发出去），submitted 崩了只能落 unknown。
type OperationState string

const (
	// StatePrepared：本地已建账，**尚未发给上游**。
	StatePrepared OperationState = "prepared"
	// StateSubmitted：已经发出去了，还没拿到回复。
	StateSubmitted OperationState = "submitted"
	StateSucceeded OperationState = "succeeded"
	StateFailed    OperationState = "failed"
	// StateUnknown：不确定花没花出去。**禁止自动重试。**
	StateUnknown OperationState = "unknown"
	// 人工核对后的两个终态。
	StateReconciledSucceeded OperationState = "reconciled_succeeded"
	StateReconciledFailed    OperationState = "reconciled_failed"
)

var (
	// ErrProviderUnknown：不认识的供应商。
	ErrProviderUnknown = errors.New("sms: 未知供应商")
	// ErrProviderDisabled：这家在后台是关着的。
	//
	// 与「没验证」分开报：关着是运营的决定（去页面上打开），没验证是凭据
	// 的问题（去做连接测试）——两者的下一步完全不同。
	ErrProviderDisabled = errors.New("sms: 供应商未启用")
	// ErrProviderNotVerified：这家还没做过成功的连接测试。
	//
	// 拿一份没验证过的凭据去花钱，失败时分不清是密钥没配还是上游故障——
	// 而那两件事的处置完全不同。
	ErrProviderNotVerified = errors.New("sms: 供应商尚未通过连接测试")
	// ErrActionNotSupported：这家不支持这个动作（62 只能 purchase）。
	ErrActionNotSupported = errors.New("sms: 该供应商不支持此动作")
	// ErrPendingDuplicate：同一份请求已有未决的一笔。
	ErrPendingDuplicate = errors.New("sms: 同一请求已有未决操作")
	// ErrRetryForbidden：非 failed 的操作不允许重试。
	ErrRetryForbidden = errors.New("sms: 只有确定失败的操作才允许重试")
	// ErrCodeNotAvailable：还没有码。**这是正常状态，不是故障。**
	ErrCodeNotAvailable = errors.New("sms: 尚未收到验证码")
	// ErrExtrasNotSupported：这家没有这项扩展能力（比如 62 没有邮箱接码）。
	ErrExtrasNotSupported = errors.New("sms: 该供应商没有这项能力")
	// ErrOperationNotFound：按 ID 查不到操作。要号流程靠它判断「这一家试过没有」。
	ErrOperationNotFound = errors.New("sms: 接码操作不存在")
)

// Operation 是台账里的一笔。
type Operation struct {
	ID       string
	Provider string
	Kind     string
	State    OperationState
	// ResourceID / OrderID 是本地 UUID，不是上游 ID。
	ResourceID string
	OrderID    string
	// RequestHash 是规范化业务参数的指纹，用于未决防重。
	RequestHash string
	// ParamsSummary 给人看，进审计摘要。**不含号码与验证码。**
	ParamsSummary     string
	ProviderRequestID string
	// ProviderRef 是上游的订单 ID（62）或 activation ID（Hero）。
	// unknown 时它是人工去上游对账的唯一抓手，所以哪怕操作失败也要留住。
	ProviderRef      string
	FailureReason    string
	NeedsHumanReview bool
	ResolveNote      string
	StartedAt        time.Time
	UpdatedAt        time.Time
	// EmailID 是邮箱动作指向的本地邮箱 UUID（其他动作为空）。
	EmailID string
}

// RetryAllowed 只有确定失败的操作才为真。
//
// 与卡片那套同一条闸：unknown、submitted、prepared 一律不许重试，哪怕人很
// 确定「肯定没成功」——那种确定必须走人工核对落成 reconciled_failed，
// 而不是绕过这个判断。买号重试一次就是再花一次钱。
func (o Operation) RetryAllowed() bool {
	return o.State == StateFailed
}

// Pending 表示这笔还没有终局。
func (o Operation) Pending() bool {
	switch o.State {
	case StatePrepared, StateSubmitted, StateUnknown:
		return true
	default:
		return false
	}
}

// Resource 是一个已购买的号码。
type Resource struct {
	ID       string
	Provider string
	// ExternalID 是上游身份：Hero 的 activation ID，或 62 的 token 指纹。
	ExternalID string
	// Phone 是完整号码；列表接口不回它，只有带 sms.reveal 的调用方看得到。
	Phone     string
	PhoneMask string
	// ProviderToken 只有 62 有，取码必须带。**任何对外 DTO 都不含它。**
	ProviderToken string
	Service       string
	Country       string
	Status        string
	OrderID       string
	// OperationID 是买下它的那笔操作（迁移 000044）；导入的号为空。
	// 要号回放与成本核算都靠它。
	OperationID       string
	LastCodeAt        time.Time
	UpstreamCreatedAt time.Time
	ExpiresAt         time.Time
	SyncedAt          time.Time
	// 以下来自官方 ActivationSchema（迁移 000040）。62 全部为空。
	Operator string
	// PriceText 是十进制文本，不转 float。
	PriceText        string
	VerificationType string
	// Subtype：1 = 普通激活，2 = 租用。0 = 上游没说（62 或旧数据）。
	Subtype          int64
	CountryPhoneCode string
	// State 是统一状态（迁移 000042）。空 = 旧数据还没映射。
	State NumberState
}

// EffectiveState 是页面该显示的状态：待收码但已经过了过期时间，就是已过期。
//
// 过期不靠上游通知（两家都不会推），靠本地时钟判；只对「待收码」生效——
// 已完成 / 已取消不会因为时间流逝变成别的东西。
func (r Resource) EffectiveState(now time.Time) NumberState {
	if r.State == StateWaitingCode && !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
		return StateExpired
	}
	return r.State
}

// Email 是一次邮箱接码（Hero Emails 组，迁移 000040）。
//
// 与手机号是两种资源：按「站点 + 域名」买，状态只有 WAIT / CANCEL / SUCCESS，
// 收到的是邮件里的验证内容（Value）。
type Email struct {
	ID         string
	Provider   string
	ExternalID string
	Site       string
	Email      string
	Status     string
	// Value 是收到的验证内容；与验证码同一档敏感度，对外 DTO 由 sms.reveal 把守。
	Value    string
	CostText string
	// Currency 是 ISO 数字币种码（840=USD）。
	Currency     int64
	UpstreamDate time.Time
	Message      string
	SyncedAt     time.Time
	CreatedAt    time.Time
}

// Code 是收到的一条验证码。
type Code struct {
	ID         string
	Provider   string
	ResourceID string
	Code       string
	Sender     string
	ReceivedAt time.Time
	CreatedAt  time.Time
}

// MaskPhone 把完整号码压成掩码。
//
// 保留后四位而不是全遮：清单里区分两个号靠的就是那四位，全遮等于让清单
// 变成一列一模一样的星号。前缀也留一点，好看出是哪个国家的号。
func MaskPhone(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) <= 4 {
		return strings.Repeat("*", len(digits))
	}
	head := ""
	if len(digits) > 8 {
		head = digits[:len(digits)-8]
	}
	return head + strings.Repeat("*", len(digits)-len(head)-4) + digits[len(digits)-4:]
}

// TokenFingerprint 是 62 的号码身份：token 的 SHA-256。
//
// **不能拿它去取码**——它是单向的。用指纹而不是 token 本身当 external ID，
// 是因为 external ID 会进索引、进日志、进 URL，而 token 不能进这些地方。
func TokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CanonicalRequestHash 是未决防重用的请求指纹。
//
// **不含 operation ID**：那正是要点——换一个 UUID 重发同一份请求要被挡住。
// 也不含时间戳：同一笔业务在重试时算出的必须是同一个 hash。
//
// 它是**字符串规范化**的指纹，不是经济语义的归一器：`"1"` 与 `"1.0"` 算出
// 不同的 hash。这不是缺陷，是边界——想按金额去重需要另一层，而那一层需要
// 知道币种与标度，这里没有。
func CanonicalRequestHash(provider, kind string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(provider)
	b.WriteByte('\n')
	b.WriteString(kind)
	for _, k := range keys {
		b.WriteByte('\n')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// SupportsAction 说明某家是否支持某个动作。**由注册表的能力集推导**，
// 不再按名字 switch——两份清单迟早差一家。把不支持的动作在领域层挡掉，
// 而不是让它打到上游去换一个含糊的 404。
func SupportsAction(provider, kind string) bool {
	capability, ok := capabilityForKind(kind)
	if !ok {
		return false
	}
	spec, ok := Spec(provider)
	return ok && spec.Has(capability)
}

// ValidateProvider 挡住不认识的供应商。
//
// 走 AllProviders 而不是另写一份 switch：两份清单迟早会差一家，
// 而那种差异的症状是「页面上有这家，一点就说未知供应商」。
func ValidateProvider(provider string) error {
	if _, ok := Spec(provider); ok {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrProviderUnknown, provider)
}
