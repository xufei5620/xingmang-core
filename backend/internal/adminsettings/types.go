package adminsettings

import (
	"context"
	"errors"
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
)

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
	SMTPSecretConfigured     bool      `json:"credential_configured"`
	AdminCIDRs               []string  `json:"admin_cidrs"`
	Revision                 int64     `json:"revision"`
	UpdatedBy                string    `json:"updated_by"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
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
