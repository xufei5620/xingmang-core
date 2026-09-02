package consoleassertion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	// financeReadScope 与 finance.ScopeRead 同值。本包不 import finance 只为
	// 复用一个字符串常量——与 internal/platform/localauth/actions.go 的同名
	// 常量是同一取舍（该文件的注释解释了理由：两个仅有的调用方各自持有一份
	// 比引入依赖更直接）。CR-0006 正文明确：断言签发要求持有 finance.read。
	financeReadScope = "finance.read"

	// defaultStepUpMaxAge：XM-AUTH-TOTP0 交接文档「XM-INVCON1 将调用的接口」
	// 一节与 CR-0006 冻结技术规格全文都只提到 10 分钟一个数字（"建议
	// maxAge=10*time.Minute，与开票侧 AdminPolicy.StepUpMaxAge 现有默认值
	// 一致"）；本切片任务书转述成了 15 分钟，与两份权威文档不一致——按"当场
	// 记录分歧、以冻结规格与前一切片的显式建议为准"处理，采用 10 分钟
	// （仍可经 Config.StepUpMaxAge 覆盖，供部署侧按需调整）。
	defaultStepUpMaxAge = 10 * time.Minute

	maxAssertionRequestBytes = 1 << 10

	resourceConsoleAssertion = "console_assertion"
	auditActionIssue         = "staff.console_assertion.issue"
	auditActionVersion       = "1"

	// truncatedNonceLen 是审计里截断展示的 nonce 长度（spec §7："断言的
	// nonce（截断展示，完整值不落审计）"）；12 个 base64url 字符（对应 9 字节
	// 熵）足够运维核对"这条审计对应的是哪一次签发"，不足以被用来重建完整
	// nonce 去猜一次重放。
	truncatedNonceLen = 12
)

// allowedScopes 是 spec §3.2 里 scope claim 的三个合法值。
var allowedScopes = map[string]struct{}{"sub2api": {}, "newapi": {}, "global": {}}

// Config 是签发端点的运行期配置（cmd/platform-api/config.go 从环境变量解析）。
type Config struct {
	// Issuer 是 iss claim 的精确值（控制台自己的公开 https 来源），
	// 经 ValidateIssuer 校验/规整后传入。
	Issuer string
	// Audience 为空时使用 DefaultAudience。
	Audience string
	// StepUpMaxAge <=0 时使用 defaultStepUpMaxAge。
	StepUpMaxAge time.Duration
}

func (c Config) audience() string {
	if c.Audience == "" {
		return DefaultAudience
	}
	return c.Audience
}

func (c Config) stepUpMaxAge() time.Duration {
	if c.StepUpMaxAge <= 0 {
		return defaultStepUpMaxAge
	}
	return c.StepUpMaxAge
}

// ValidateIssuer 校验并规整 iss claim 值：必须是不带路径（除了裸 "/"）、
// 查询、片段、用户信息的精确 https 来源——与前端 EmbeddedConsoleFrame 已经
// 对 invoiceConsoleOrigin 采用的"干净 https 来源"形状要求同一条纪律，只是
// 协议方向相反（那边校验的是要嵌入谁，这里校验的是"我自己是谁"）。
func ValidateIssuer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("issuer 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("issuer 不是合法 URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", errors.New("issuer 必须是 https 来源")
	}
	if u.Host == "" {
		return "", errors.New("issuer 缺少 host")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("issuer 必须是不带路径/查询/片段的来源")
	}
	return "https://" + u.Host, nil
}

// staffAccountLookup 是 Handlers 依赖的最小面（*localauth.Store 满足）——
// 只需要 GetByUsername 拿到 sub（core.staff_account.id）与账号真实角色；
// finance.read 判定用 principal.Scopes（Resolver 早已翻译过，见
// localauth.Resolver.Resolve），不需要在这里重新翻译一遍角色到 scope。
type staffAccountLookup interface {
	GetByUsername(ctx context.Context, username string) (localauth.Account, error)
}

// assertionSigner 是 Handlers 依赖的最小签名面（*Signer 满足），
// 便于测试注入假实现。
type assertionSigner interface {
	Sign(now time.Time, in ClaimsInput) (assertion string, expiresAt time.Time, nonce string, err error)
}

// auditAppender 是 Handlers 依赖的审计写入面（*audit.Store 满足）。
type auditAppender interface {
	Append(ctx context.Context, e audit.Event) (audit.Event, error)
}

// Handlers 实现 POST /api/v1/auth/console-assertion（CR-0006 b 条的控制台侧
// 签发端点；开票侧同路径的兑换端点是另一个系统的另一个实现,不在本包）。
type Handlers struct {
	signer           assertionSigner
	accounts         staffAccountLookup
	adminIPAllowlist localauth.AdminIPAllowlist
	cfg              Config
	environment      string
	audit            auditAppender
	logger           *slog.Logger
	now              func() time.Time
}

// NewHandlers 装配 Handlers。signerImpl 可以是 nil（未启用/未装配签名器时,
// Issue 对每个请求都回 500 INTERNAL——生产装配必须在构造前保证非 nil,见
// cmd/platform-api 的接线：XM_INVOICE_CONSOLE_ASSERTION_ENABLED=false 时
// 整个 Handlers 都不会被构造,路由层不挂载这条端点,不会走到这里）。
// auditStore 可以是 nil（不写审计，仅测试用）。
func NewHandlers(
	signerImpl assertionSigner, accounts staffAccountLookup, adminIPAllowlist localauth.AdminIPAllowlist,
	cfg Config, environment string, auditStore auditAppender, logger *slog.Logger,
) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{
		signer: signerImpl, accounts: accounts, adminIPAllowlist: adminIPAllowlist,
		cfg: cfg, environment: environment, audit: auditStore, logger: logger, now: time.Now,
	}
}

type issueRequest struct {
	Scope string `json:"scope"`
}

type issueResponse struct {
	Assertion string `json:"assertion"`
	ExpiresAt string `json:"expires_at"`
}

// Issue 处理 POST /api/v1/auth/console-assertion（spec §3.4/§5.1）。挂在
// httpapi 路由的 RequirePrincipal 组内：xm_session 与 CSRF 头
// （X-Requested-With: xingmang）的校验由 localauth.Resolver.Resolve 对非只读
// 方法统一强制,这里不重复判断,与 localauth.Handlers 自身受保护端点同一纪律。
//
// 校验顺序刻意固定：参数形状 → finance.read → IP 名单 → TOTP 新鲜度——先判
// "请求本身合不合法"，再判"这个人配不配"，与 httpapi/authz.go
// resolveEnvironment 的既定顺序注释同一条理由："参数就不合法"和"不许你读"
// 是两类问题，混在一起会让 400 变成 403。
func (h *Handlers) Issue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := principal.FromContext(ctx)
	if !ok {
		// 正常情况下 RequirePrincipal 已经拦下了；这里是纵深防御——万一有人
		// 把本 handler 挂在了 RequirePrincipal 之外（同 httpapi.RequireScope
		// 的同款兜底注释）。
		writeAssertionError(w, r, http.StatusUnauthorized, "PERMISSION_DENIED", "缺少身份")
		return
	}

	var body issueRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAssertionError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "请求体不是合法 JSON 对象")
		return
	}
	scope := strings.TrimSpace(body.Scope)
	if _, ok := allowedScopes[scope]; !ok {
		writeAssertionError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "scope 必须是 sub2api/newapi/global 之一")
		return
	}

	if !p.HasScope(financeReadScope) {
		h.recordAudit(ctx, r, p, scope, "", false, "finance_scope_missing")
		writeAssertionError(w, r, http.StatusForbidden, "FINANCE_SCOPE_REQUIRED", "当前账号缺少 finance.read 权限")
		return
	}
	if !h.adminIPAllowlist.Allowed(clientIP(r)) {
		h.recordAudit(ctx, r, p, scope, "", false, "ip_denied")
		writeAssertionError(w, r, http.StatusForbidden, "ADMIN_NETWORK_DENIED", "当前网络不在管理员访问名单内")
		return
	}
	if !localauth.RequireFreshOTP(p, h.cfg.stepUpMaxAge(), h.now()) {
		h.recordAudit(ctx, r, p, scope, "", false, "step_up_required")
		writeAssertionError(w, r, http.StatusForbidden, "ADMIN_STEP_UP_REQUIRED", "二次验证已过期，请重新验证 TOTP 后重试")
		return
	}
	if h.signer == nil {
		h.logger.ErrorContext(ctx, "console_assertion_signer_unavailable", slog.String("module", "consoleassertion"))
		writeAssertionError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}

	username := strings.TrimPrefix(p.ID, "staff:")
	acc, err := h.accounts.GetByUsername(ctx, username)
	if err != nil {
		h.logger.ErrorContext(ctx, "console_assertion_account_lookup_failed",
			slog.String("module", "consoleassertion"), slog.Any("err", err))
		writeAssertionError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}

	compact, expiresAt, nonce, err := h.signer.Sign(h.now(), ClaimsInput{
		Issuer: h.cfg.Issuer, Audience: h.cfg.audience(), Subject: acc.ID.String(),
		Username: acc.Username, Roles: acc.Roles, Scope: scope,
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "console_assertion_sign_failed",
			slog.String("module", "consoleassertion"), slog.Any("err", err))
		writeAssertionError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}

	h.recordAudit(ctx, r, p, scope, nonce, true, "")
	httpapi.WriteJSON(w, http.StatusOK, issueResponse{
		Assertion: compact, ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

func decodeJSONBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxAssertionRequestBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeAssertionError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	httpapi.WriteJSON(w, status, httpapi.ErrorResponse{Error: httpapi.ErrorBody{
		Code: code, Message: message, RequestID: httpapi.RequestIDFrom(r.Context()),
	}})
}

// clientIP 取客户端地址：优先 X-Forwarded-For 首个值，取不到回落到
// RemoteAddr 的主机部分。与 internal/platform/localauth 里同名函数逐字同一
// 实现（该包未导出它；两处各自持有一份比新增一个只有两个调用方的共享包更
// 直接，同 cmd/staff-bootstrap 对 generatedAlphabet 常量的取舍一致）——
// XM-AUTH-TOTP0 交接文档要求两处校验须使用同一条 XFF 解析纪律，这里满足的
// 是"同一条纪律"而不是"同一份代码"，行为上逐字一致。
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if idx := strings.IndexByte(fwd, ','); idx >= 0 {
			fwd = fwd[:idx]
		}
		if ip := strings.TrimSpace(fwd); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// recordAudit 写一条 staff.console_assertion.issue 审计事件（spec §7）。
// 失败分支的 nonce 恒为空——那些请求从未生成过断言，没有 nonce 可截断。
func (h *Handlers) recordAudit(
	ctx context.Context, r *http.Request, p principal.Principal, scope, nonce string,
	succeeded bool, failureReason string,
) {
	if h.audit == nil {
		return
	}
	resourceID := ""
	if len(nonce) > 0 {
		resourceID = nonce[:min(truncatedNonceLen, len(nonce))]
	}
	ev := audit.Event{
		OccurredAt: h.now().UTC(), PrincipalID: p.ID, PrincipalType: p.Type,
		ActionID: auditActionIssue, ActionVersion: auditActionVersion, ActionRunID: uuid.New(),
		ResourceType: resourceConsoleAssertion, ResourceID: resourceID, Environment: h.environment,
		RequestID: httpapi.RequestIDFrom(ctx), SourceIP: clientIP(r), Result: audit.ResultSucceeded,
		AfterSummary: map[string]any{"scope": scope},
	}
	if !succeeded {
		ev.Result = audit.ResultFailed
		ev.CompensationResult = failureReason
	}
	if _, err := h.audit.Append(ctx, ev); err != nil {
		h.logger.ErrorContext(ctx, "audit_append_failed", slog.String("module", "consoleassertion"),
			slog.String("event", auditActionIssue), slog.Any("err", err))
	}
}
