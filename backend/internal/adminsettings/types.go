package adminsettings

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const (
	FixedServiceItem = "技术服务"
	// MinimumMinor is the floor the minimum-invoice-amount *setting* may be
	// lowered to, not the business default. It used to be 20_000 (¥200), which
	// made the setting one-way: an administrator could raise the threshold but
	// never lower it, so a small-amount test was impossible
	// (XM-INV-SETTABLE-INVOICE-MINIMUM). The only value that is genuinely not
	// an administrator's call is a non-positive one, which is a broken setting
	// rather than a policy choice; a smaller arbitrary floor would just move
	// the same wall.
	MinimumMinor = int64(1)
	// DefaultMinimumRequestMinor is the ¥200 business default a fresh
	// installation starts at, and what migration 0002's column DEFAULT still
	// carries. Bootstrap uses it; the settings page can move it either way
	// from there.
	DefaultMinimumRequestMinor = int64(20_000)
	EligibilityStartAtRFC3339  = "2026-09-01T00:00:00+08:00"
	EligibilityDisplayTimeZone = "Asia/Shanghai"
	// UnconfiguredIssuerName is the only issuer placeholder accepted by the
	// create-only bootstrap path.
	UnconfiguredIssuerName = "待配置开票主体"
)

var RequiredEligibilityStartAt = time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC)

var unconfiguredIssuerPrefixes = [...]string{"待配置", "请替换"}

// IsIssuerConfigured is the single authority for deciding whether an issuer
// name can be persisted or captured in an immutable issue snapshot.
func IsIssuerConfigured(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	for _, prefix := range unconfiguredIssuerPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	return true
}

var (
	ErrNotConfigured    = errors.New("admin settings are not configured")
	ErrRevisionConflict = errors.New("admin settings revision conflict")
	ErrInvalidSettings  = errors.New("invalid admin settings")
	ErrSecretMissing    = errors.New("SMTP secret is not configured")
	// 这两个分开而不是合并成一个，是为了让管理端能给出两种不同的提示：一种是
	// 「这个地址写得不对」，另一种是「地址没问题，但你把它填成了发件人自己」。
	// 合并之后第二种情况读起来会像格式错误，运营会在输入框里反复改地址。
	ErrInvalidTestRecipient  = fmt.Errorf("%w: SMTP test recipient must be one exact email address", ErrInvalidSettings)
	ErrTestRecipientConflict = fmt.Errorf("%w: SMTP sender and test recipient must be different", ErrInvalidSettings)
)

// NormalizeTestRecipient 把「发送测试邮件」的收件人归一化成一个裸地址，或空串
// （空串=未配置）。
//
// 规则与这个字段被搬进管理后台之前逐字相同（原先在 httpapi 里叫
// normalizeSMTPTestRecipient，只用来校验环境变量）：**恰好一个地址**，不接受
// "显示名 <a@b>"、不接受逗号分号分隔的多个收件人、不接受任何空白或控制字符。
// 最后一条是要害——CR/LF 进了收件人就是邮件头注入。
//
// 搬到这一层的原因：地址现在由管理员在页面上填，写库之前就必须过这道校验，
// 而规则只能有一份。httpapi 侧改为调用它，库里另有形状 CHECK 兜底（迁移 0030）。
func NormalizeTestRecipient(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := mail.ParseAddress(value)
	// 只允许 ASCII 可打印字符（33..126）。这一条比「排除空白与控制字符」更严，
	// 而且必须更严：mail.ParseAddress 接受 "测试@example.com" 这类国际化地址，
	// 但本系统的投递链路没有做过 SMTPUTF8 验证，放进来只会在发信时才炸。
	// 33 起也顺带把空格挡在外面，0/13/10 这些邮件头注入字符自然不可能通过。
	if err != nil || len(value) > 320 || parsed.Name != "" || parsed.Address != value ||
		strings.IndexFunc(value, func(character rune) bool { return character < 33 || character > 126 }) >= 0 {
		return "", ErrInvalidTestRecipient
	}
	return value, nil
}

// MaskTestRecipient 把地址遮成 "abc***@example.com"。设置页的说明行用它，
// 免得一个内部邮箱在每次读设置时都原样出现在响应里。输入不成形状时返回空串。
func MaskTestRecipient(value string) string {
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return ""
	}
	local := value[:separator]
	visible := 3
	if len(local) < visible {
		visible = 1
	}
	return local[:visible] + "***@" + value[separator+1:]
}

type Settings struct {
	// IssuerName is admin-only configuration. User-facing APIs must not expose it.
	IssuerName               string    `json:"issuer_name"`
	ServiceItem              string    `json:"service_item"`
	MinimumRequestMinor      int64     `json:"minimum_request_minor"`
	EligibilityStartAt       time.Time `json:"eligibility_start_at"`
	EligibilityPolicyVersion int64     `json:"eligibility_policy_version"`
	SMTPHost                 string    `json:"smtp_host"`
	SMTPPort                 int       `json:"smtp_port"`
	SMTPFrom                 string    `json:"smtp_from"`
	SMTPFromName             string    `json:"smtp_from_name"`
	SMTPStartTLS             bool      `json:"smtp_starttls"`
	// SMTPTestRecipient 是「发送测试邮件」的固定收件人。空串=未配置，
	// 由运行时回退到环境变量 SMTP_TEST_RECIPIENT（过渡期，见迁移 0030）。
	SMTPTestRecipient    string    `json:"smtp_test_recipient"`
	SMTPSecretConfigured bool      `json:"credential_configured"`
	AdminCIDRs           []string  `json:"admin_cidrs"`
	Revision             int64     `json:"revision"`
	UpdatedBy            string    `json:"updated_by"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type UpdateInput struct {
	IssuerName          string
	MinimumRequestMinor int64
	EligibilityStartAt  time.Time
	SMTPHost            string
	SMTPPort            int
	SMTPFrom            string
	SMTPFromName        string
	SMTPStartTLS        bool
	SMTPTestRecipient   string
	AdminCIDRs          []string
}

type Actor struct {
	ID           string
	RequestID    string
	SourceIPHash string
	Reason       string
}

type SecretEnvelope struct {
	Ciphertext []byte
	KeyVersion string
}

type SMTPSecretChange string

const (
	SMTPSecretUnchanged SMTPSecretChange = "unchanged"
	SMTPSecretSet       SMTPSecretChange = "set"
	SMTPSecretClear     SMTPSecretChange = "clear"
)

// NoticeWebhookInfo 是企业微信通知地址在管理端**能显示的全部**
// （XM-INV-NOTICE-WEBHOOK-SETTING）。
//
// **没有地址本身，也没有它的任何明文片段**：整个 URL 就是凭据（企微把鉴权
// key 放在查询参数里）。"配的是哪一个"由指纹回答——可核对、不可反推，与星芒
// 平台凭据页显示指纹前缀而非值前缀是同一条纪律。真要确认配对没有，用"发送
// 测试消息"：消息到没到那个群，比看一段前缀可靠得多。
type NoticeWebhookInfo struct {
	Configured  bool      `json:"configured"`
	Fingerprint string    `json:"fingerprint"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Repository interface {
	Get(context.Context) (Settings, error)
	Update(context.Context, UpdateInput, int64, Actor) (Settings, error)
	UpdateSMTP(context.Context, UpdateInput, SMTPSecretChange, SecretEnvelope, int64, Actor) (Settings, error)
	StoreSMTPSecret(context.Context, SecretEnvelope, int64, Actor) (Settings, error)
	ClearSMTPSecret(context.Context, int64, Actor) (Settings, error)
	LoadSMTPSecret(context.Context) (SecretEnvelope, error)

	// 通知地址走**独立的表**，不并进 admin_setting_secrets：那张表的
	// smtp_secret_ciphertext 是 NOT NULL 且 ClearSMTPSecret 直接 DELETE 整行，
	// 挂上去会让"清一次 SMTP 口令"顺手抹掉通知地址。两个凭据生命周期无关。
	GetNoticeWebhook(context.Context) (NoticeWebhookInfo, error)
	StoreNoticeWebhook(context.Context, SecretEnvelope, string, Actor) (NoticeWebhookInfo, error)
	ClearNoticeWebhook(context.Context, Actor) (NoticeWebhookInfo, error)
	LoadNoticeWebhook(context.Context) (SecretEnvelope, error)
}

type SecretBox interface {
	Seal(context.Context, []byte) (SecretEnvelope, error)
	Open(context.Context, SecretEnvelope) ([]byte, error)
}
