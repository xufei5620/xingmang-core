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
// 进程**永远把这两家都建出来**，能不能用由库里的 enabled 决定。
// 让构造随配置变化，等于在后台开一家之后还要重启进程才生效。
var AllProviders = []string{ProviderSMS62, ProviderHero}

// 操作类型。62 只支持 purchase；其余五个是 Hero 独有的生命周期动作。
const (
	KindPurchase   = "purchase"
	KindCancel     = "cancel"
	KindFinish     = "finish"
	KindReplace    = "replace"
	KindReactivate = "reactivate"
	KindProlong    = "prolong"
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
	ProviderToken     string
	Service           string
	Country           string
	Status            string
	OrderID           string
	LastCodeAt        time.Time
	UpstreamCreatedAt time.Time
	ExpiresAt         time.Time
	SyncedAt          time.Time
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

// SupportsAction 说明某家是否支持某个动作。
//
// 62 只有 purchase：它没有取消/完成/替换/延长的接口。把不支持的动作在
// 领域层挡掉，而不是让它打到上游去换一个含糊的 404。
func SupportsAction(provider, kind string) bool {
	if kind == KindPurchase {
		return provider == ProviderSMS62 || provider == ProviderHero
	}
	return provider == ProviderHero
}

// ValidateProvider 挡住不认识的供应商。
//
// 走 AllProviders 而不是另写一份 switch：两份清单迟早会差一家，
// 而那种差异的症状是「页面上有这家，一点就说未知供应商」。
func ValidateProvider(provider string) error {
	for _, id := range AllProviders {
		if id == provider {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrProviderUnknown, provider)
}
