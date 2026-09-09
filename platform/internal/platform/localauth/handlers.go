package localauth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// sessionTTL 是 Cookie 与会话行的有效期（任务书：12h）。
const sessionTTL = 12 * time.Hour

// maxAuthBodyBytes 限制登录/改密请求体大小；两者的字段都很短，
// 不该有理由超过几 KB（规格 §18.1-4：外部输入必须有大小上限）。
const maxAuthBodyBytes = 4 << 10

// 登录端点专用限流：按客户端 IP 分桶,不是按 Principal——登录之前还没有身份
// 可用来分桶。数字比 XM-R011 默认的 120/分钟收紧很多：它挡的是暴力破解,
// 不是"看板刷新太快";连续 5 次失败本身还会触发 15 分钟账号锁定
// （Store.RecordFailure),这里只是同一问题的第二道、更早生效的防线。
const (
	loginRateLimitPerMinute = 20
	loginRateLimitBurst     = 10
)

// totpRateLimitPerMinute/Burst 是 TOTP 码/恢复码校验专用限流,按**目标账号
// 用户名**分桶（不是 IP）：一枚 temp token 或一个已登录会话本来就已经绑定了
// 唯一账号，攻击者换 IP 不能换掉这层限制。6 位数字码的搜索空间只有 100
// 万——不配合账号锁定（RecordFailure,5 次锁 15 分钟）单靠这层限流不够，
// 两层一起生效（同登录密码的两层防线同一设计）。
const (
	totpRateLimitPerMinute = 10
	totpRateLimitBurst     = 5
)

// accountStore 是 Handlers 依赖的最小面（*Store 满足），便于测试注入假实现。
type accountStore interface {
	GetByUsername(ctx context.Context, username string) (Account, error)
	RecordFailure(ctx context.Context, username string) error
	ResetFailures(ctx context.Context, username string) error
	CreateSession(ctx context.Context, accountID uuid.UUID, environment, userAgent, ip string, ttl time.Duration, amr []string, mfaAt *time.Time) (string, error)
	LookupSession(ctx context.Context, rawToken string) (Account, error)
	RevokeSession(ctx context.Context, rawToken string) error
	ResetPassword(ctx context.Context, username, passwordHash string, mustChange bool, actor string) (Account, error)
	ListAccounts(ctx context.Context) ([]Account, error)

	// XM-AUTH-TOTP0：登录第二步（TOTP/恢复码）与步进。
	CreateLoginChallenge(ctx context.Context, accountID uuid.UUID, environment, userAgent, ip string) (string, error)
	LookupLoginChallenge(ctx context.Context, rawToken string) (Account, error)
	ConsumeLoginChallenge(ctx context.Context, rawToken string) error
	ConsumeRecoveryCode(ctx context.Context, accountID uuid.UUID, codeHash string) (bool, error)
	UnusedRecoveryCodeCount(ctx context.Context, accountID uuid.UUID) (int, error)
	TouchSessionMFA(ctx context.Context, rawToken string) error
}

// auditAppender 是 Handlers 依赖的审计写入面（*audit.Store 满足）。
type auditAppender interface {
	Append(ctx context.Context, e audit.Event) (audit.Event, error)
}

// actionRunner 是 Handlers 执行 TOTP 三个 Action（enroll/confirm/reset）依赖
// 的最小面（*action.Kernel 满足）。不直接持有 *action.Kernel 具体类型是为了
// 测试能注入假执行器，不需要装一整套 Registry+RunStore+AuditSink。
type actionRunner interface {
	Execute(ctx context.Context, req action.Request) (action.Result, error)
}

// Handlers 实现 /api/v1/auth/* 与 /api/v1/staff/accounts 的 HTTP 处理器。
type Handlers struct {
	store       accountStore
	environment string
	audit       auditAppender
	limiter     *httpapi.RateLimiter
	logger      *slog.Logger

	// XM-AUTH-TOTP0 新增依赖。
	kernel           actionRunner
	secretReader     totpSecretReader
	totpLimiter      *httpapi.RateLimiter
	adminIPAllowlist AdminIPAllowlist
}

// NewHandlers 装配处理器。auditStore 可以是 nil（此时不写审计，仅用于不需要
// 审计断言的测试；生产装配必须传真实的 *audit.Store)。
//
// kernel 用于执行 staff.account.enroll_totp/confirm_totp/reset_totp 三个
// Action（EnrollTOTP/ConfirmTOTP/ResetTOTP 三个 HTTP 方法内部调用，不新开
// 一条绕过 Action 内核的写路径）；secretReader 供 Login/LoginTOTP 校验二步
// 验证码时读回当前生效的密钥；adminIPAllowlist 见 AdminIPAllowlist 类型注释，
// 零值＝不启用。
func NewHandlers(
	store *Store, environment string, auditStore auditAppender, logger *slog.Logger,
	kernel actionRunner, secretReader totpSecretReader, adminIPAllowlist AdminIPAllowlist,
) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{
		store: store, environment: environment, audit: auditStore, logger: logger,
		limiter: httpapi.NewRateLimiter(httpapi.RateLimitConfig{
			PerMinute: loginRateLimitPerMinute, Burst: loginRateLimitBurst,
		}),
		kernel:       kernel,
		secretReader: secretReader,
		totpLimiter: httpapi.NewRateLimiter(httpapi.RateLimitConfig{
			PerMinute: totpRateLimitPerMinute, Burst: totpRateLimitBurst,
		}),
		adminIPAllowlist: adminIPAllowlist,
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionResponse 是登录/改密/查询自己成功后的统一响应形状。
type sessionResponse struct {
	Username           string   `json:"username"`
	DisplayName        string   `json:"display_name"`
	Roles              []string `json:"roles"`
	MustChangePassword bool     `json:"must_change_password"`
	// RequiresTOTP 恒为 false——与 pendingTOTPResponse.RequiresTOTP=true 相对，
	// 前端用这个字段区分"已经拿到会话"与"密码通过、还差一步"两种 200 响应，
	// 不必去猜"有没有 temp_token 字段"。
	RequiresTOTP bool `json:"requires_totp"`
	// TOTPEnrolled/MustEnrollTOTP 供账号安全状态展示与 RequireAuth 强制引导
	// （前端语义与既有 must_change_password 完全对称）。
	TOTPEnrolled   bool    `json:"totp_enrolled"`
	MustEnrollTOTP bool    `json:"must_enroll_totp"`
	TOTPEnrolledAt *string `json:"totp_enrolled_at,omitempty"`
	// RecoveryCodesRemaining 只在已启用 TOTP 时给出——账号安全页据此提醒
	// "恢复码即将用尽"，不返回任何码本身。
	RecoveryCodesRemaining *int `json:"recovery_codes_remaining,omitempty"`
}

// pendingTOTPResponse 是"密码已验证、TOTP 待验证"的中间态响应。
type pendingTOTPResponse struct {
	RequiresTOTP bool   `json:"requires_totp"`
	TempToken    string `json:"temp_token"`
	Username     string `json:"username"`
}

func toSessionResponse(a Account, recoveryRemaining *int) sessionResponse {
	resp := sessionResponse{
		Username:           a.Username,
		DisplayName:        a.DisplayName,
		Roles:              append([]string(nil), a.Roles...),
		MustChangePassword: a.MustChangePassword,
		TOTPEnrolled:       a.TOTPActive(),
		MustEnrollTOTP:     a.MustEnrollTOTP,
	}
	if a.TOTPEnrolledAt != nil {
		s := a.TOTPEnrolledAt.UTC().Format(time.RFC3339)
		resp.TOTPEnrolledAt = &s
	}
	if a.TOTPActive() {
		resp.RecoveryCodesRemaining = recoveryRemaining
	}
	return resp
}

func writeAuthError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	httpapi.WriteJSON(w, status, httpapi.ErrorResponse{Error: httpapi.ErrorBody{
		Code: code, Message: message, RequestID: httpapi.RequestIDFrom(r.Context()),
	}})
}

// clientIP 取客户端地址：优先 X-Forwarded-For 首个值（栈的入口是宿主
// Nginx 反代，见 deploy/nginx），取不到则回落到 RemoteAddr 的主机部分。
//
// 这是本包唯一的 XFF 解析点——XM-AUTH-TOTP0 的管理员 IP 名单校验
// （adminIPCheck）复用它，不重新解析一遍，与"两处校验须使用同一条
// X-Forwarded-For 纪律"的既定要求一致。
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

// isHTTPS 判断本次请求是否经 TLS——直连或反代都算。
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: -1,
	})
}

func decodeJSONBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxAuthBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// accountNeedsAdminIPCheck 判断账号是否落入管理员 IP 名单的管辖范围
// （CR-0006 c 条）。MustEnrollTOTP 或 TOTPActive 任一为真，都说明这个账号
// 在某个时刻被 rolesRequireTOTP 判定为"持有 staff.manage 或 finance.read
// 一类角色"（见 actions.go），天然圈住"管理员"这个人群，不需要在这里
// 重新解一次角色到 scope 的翻译。
func accountNeedsAdminIPCheck(a Account) bool {
	return a.MustEnrollTOTP || a.TOTPActive()
}

// adminIPDenied 在需要时校验来源 IP，返回 true 表示应当拒绝（且已经完成
// 审计与响应写入，调用方直接 return）。
func (h *Handlers) adminIPDenied(w http.ResponseWriter, r *http.Request, acc Account) bool {
	if !accountNeedsAdminIPCheck(acc) {
		return false
	}
	if h.adminIPAllowlist.Allowed(clientIP(r)) {
		return false
	}
	h.recordLoginAudit(r.Context(), r, acc.Username, false, "ip_denied")
	writeAuthError(w, r, http.StatusForbidden, "ADMIN_NETWORK_DENIED", "当前网络不在管理员访问名单内")
	return true
}

// Login 处理 POST /api/v1/auth/login。不要求 Principal——登录本身就是在
// 建立身份。失败对"用户名不存在"与"密码错误"给同一个状态码、同一句文案、
// 同量级耗时（见下方 dummyPasswordHash 调用），避免响应本身或响应时延
// 变成一个用户名枚举接口；账号锁定 / 停用有各自更具体的状态码——这两者
// 已经在响应内容上与"密码错误"区分开了，不需要额外的计时保护。
//
// XM-AUTH-TOTP0：密码校验通过后分两条路径——账号已激活 TOTP 的，不直接
// 签发会话，转而创建一个 5 分钟有效的登录挑战（temp token），返回
// pendingTOTPResponse，前端据此转到二步验证；未激活的（含"待启用"）走
// 原有的一步登录。
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if allowed, retryAfter := h.limiter.Allow(ip); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		writeAuthError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "登录尝试过于频繁，请稍后重试")
		return
	}

	var body loginRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAuthError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "请求体不是合法 JSON 对象")
		return
	}
	username := strings.TrimSpace(body.Username)
	ctx := r.Context()
	now := time.Now().UTC()

	acc, err := h.store.GetByUsername(ctx, username)
	if err != nil {
		_, _ = VerifyPassword(dummyPasswordHash(), body.Password)
		h.recordLoginAudit(ctx, r, username, false, "unknown_user")
		writeAuthError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码不正确")
		return
	}

	if acc.Disabled {
		h.recordLoginAudit(ctx, r, acc.Username, false, "account_disabled")
		writeAuthError(w, r, http.StatusForbidden, "ACCOUNT_DISABLED", "账号已停用")
		return
	}
	if acc.Locked(now) {
		h.recordLoginAudit(ctx, r, acc.Username, false, "account_locked")
		writeAuthError(w, r, http.StatusLocked, "ACCOUNT_LOCKED", "账号已锁定，请稍后重试或联系管理员")
		return
	}

	ok, verr := VerifyPassword(acc.passwordHash, body.Password)
	if verr != nil || !ok {
		if ferr := h.store.RecordFailure(ctx, acc.Username); ferr != nil {
			h.logger.ErrorContext(ctx, "record_login_failure_failed",
				slog.String("module", "localauth"), slog.Any("err", ferr))
		}
		h.recordLoginAudit(ctx, r, acc.Username, false, "bad_password")
		writeAuthError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码不正确")
		return
	}

	// 密码正确起，才校验管理员 IP 名单——未过密码的请求不该借这一步探测
	// "这个网络对哪些账号生效"（同 Login 其余分支"先密码后其它状态"的顺序）。
	if h.adminIPDenied(w, r, acc) {
		return
	}

	if rerr := h.store.ResetFailures(ctx, acc.Username); rerr != nil {
		h.logger.ErrorContext(ctx, "reset_login_failures_failed",
			slog.String("module", "localauth"), slog.Any("err", rerr))
	}

	if acc.TOTPActive() {
		token, cerr := h.store.CreateLoginChallenge(ctx, acc.ID, h.environment, r.UserAgent(), ip)
		if cerr != nil {
			h.logger.ErrorContext(ctx, "create_login_challenge_failed",
				slog.String("module", "localauth"), slog.Any("err", cerr))
			writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, pendingTOTPResponse{
			RequiresTOTP: true, TempToken: token, Username: acc.Username,
		})
		return
	}

	token, err := h.store.CreateSession(ctx, acc.ID, h.environment, r.UserAgent(), ip, sessionTTL,
		[]string{"pwd"}, nil)
	if err != nil {
		h.logger.ErrorContext(ctx, "create_session_failed",
			slog.String("module", "localauth"), slog.Any("err", err))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	setSessionCookie(w, r, token, sessionTTL)
	h.recordLoginAudit(ctx, r, acc.Username, true, "")
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(acc, nil))
}

type loginTOTPRequest struct {
	// TempToken 非空＝完成登录第二步（配合 Login 返回的 pendingTOTPResponse）；
	// 为空＝步进刷新（要求已有有效 xm_session Cookie，见 LoginTOTP 注释）。
	TempToken    string `json:"temp_token"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

// LoginTOTP 处理 POST /api/v1/auth/login/totp，对应 CR-0006 技术规格
// §4.2 时序图里"重新 POST .../login/totp {temp_token 或当前会话, code}"
// 这一步——同一个端点服务两种场景，由请求体是否带 temp_token 区分：
//
//  1. 完成登录第二步（temp_token 非空）：不要求 Principal，本身就是在
//     建立身份的最后一步；校验 code 或 recovery_code 其一，通过后签发正式
//     会话（amr=[pwd,otp]，mfa_at=now）并消费掉这个一次性 temp token。
//  2. 步进刷新（temp_token 为空）：要求已有有效 xm_session Cookie + CSRF
//     头（与其它写请求同一条纪律，但这里不经统一的 RequirePrincipal
//     中间件——该中间件建立的是"这次调用的 Principal"，而这里要动的是
//     "**这个会话本身**的 mfa_at"，语义更贴近 Resolver.Resolve 直接读
//     Cookie 的做法）；校验通过后调用 TouchSessionMFA，供 XM-INVCON1 一类
//     "断言签发前必须证明 TOTP 仍新鲜"的场景使用。
//
// 两种场景都受 totpLimiter（按账号分桶）与账号锁定（RecordFailure）双重
// 限制；错误码区分"temp token/会话本身不合法"（CHALLENGE_INVALID /
// PERMISSION_DENIED）与"合法但码不对"（INVALID_CREDENTIALS）——前者是
// "请重新走一遍登录"的信号，后者是"再试一次"的信号,对前端是有意义的
// 区分,不落入"区分披露"红线（同 ACCOUNT_LOCKED/ACCOUNT_DISABLED 已经在
// 与 INVALID_CREDENTIALS 区分对外文案的既有先例）。
func (h *Handlers) LoginTOTP(w http.ResponseWriter, r *http.Request) {
	var body loginTOTPRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAuthError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "请求体不是合法 JSON 对象")
		return
	}
	code := strings.TrimSpace(body.Code)
	recoveryCode := strings.TrimSpace(body.RecoveryCode)
	if (code == "") == (recoveryCode == "") {
		writeAuthError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "code 与 recovery_code 必须恰好提供一个")
		return
	}

	if strings.TrimSpace(body.TempToken) != "" {
		h.completeLoginTOTP(w, r, strings.TrimSpace(body.TempToken), code, recoveryCode)
		return
	}
	h.stepUpTOTP(w, r, code, recoveryCode)
}

func (h *Handlers) completeLoginTOTP(w http.ResponseWriter, r *http.Request, tempToken, code, recoveryCode string) {
	ctx := r.Context()
	ip := clientIP(r)

	acc, err := h.store.LookupLoginChallenge(ctx, tempToken)
	if err != nil {
		writeAuthError(w, r, http.StatusUnauthorized, "CHALLENGE_INVALID", "登录请求已过期，请重新登录")
		return
	}
	if allowed, retryAfter := h.totpLimiter.Allow(acc.Username); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		writeAuthError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "验证尝试过于频繁，请稍后重试")
		return
	}
	if acc.Locked(time.Now().UTC()) {
		writeAuthError(w, r, http.StatusLocked, "ACCOUNT_LOCKED", "账号已锁定，请稍后重试或联系管理员")
		return
	}

	if !h.verifyTOTPOrRecovery(ctx, acc, code, recoveryCode) {
		if ferr := h.store.RecordFailure(ctx, acc.Username); ferr != nil {
			h.logger.ErrorContext(ctx, "record_login_failure_failed",
				slog.String("module", "localauth"), slog.Any("err", ferr))
		}
		h.recordLoginAudit(ctx, r, acc.Username, false, "totp_invalid")
		writeAuthError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "验证码不正确")
		return
	}

	if h.adminIPDenied(w, r, acc) {
		return
	}
	if rerr := h.store.ResetFailures(ctx, acc.Username); rerr != nil {
		h.logger.ErrorContext(ctx, "reset_login_failures_failed",
			slog.String("module", "localauth"), slog.Any("err", rerr))
	}
	if cerr := h.store.ConsumeLoginChallenge(ctx, tempToken); cerr != nil {
		h.logger.ErrorContext(ctx, "consume_login_challenge_failed",
			slog.String("module", "localauth"), slog.Any("err", cerr))
	}

	mfaAt := time.Now().UTC()
	token, serr := h.store.CreateSession(ctx, acc.ID, h.environment, r.UserAgent(), ip, sessionTTL,
		[]string{"pwd", "otp"}, &mfaAt)
	if serr != nil {
		h.logger.ErrorContext(ctx, "create_session_failed",
			slog.String("module", "localauth"), slog.Any("err", serr))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	setSessionCookie(w, r, token, sessionTTL)
	h.recordLoginAudit(ctx, r, acc.Username, true, "")
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(acc, h.recoveryRemaining(ctx, acc)))
}

func (h *Handlers) stepUpTOTP(w http.ResponseWriter, r *http.Request, code, recoveryCode string) {
	ctx := r.Context()
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", msgMissingSession)
		return
	}
	if cerr := requireCSRFHeader(r); cerr != nil {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", msgMissingCSRF)
		return
	}
	acc, err := h.store.LookupSession(ctx, cookie.Value)
	if err != nil {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", msgInvalidSession)
		return
	}
	if !acc.TOTPActive() {
		writeAuthError(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "账号尚未启用 TOTP")
		return
	}
	if allowed, retryAfter := h.totpLimiter.Allow(acc.Username); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		writeAuthError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "验证尝试过于频繁，请稍后重试")
		return
	}

	if !h.verifyTOTPOrRecovery(ctx, acc, code, recoveryCode) {
		if ferr := h.store.RecordFailure(ctx, acc.Username); ferr != nil {
			h.logger.ErrorContext(ctx, "record_login_failure_failed",
				slog.String("module", "localauth"), slog.Any("err", ferr))
		}
		h.recordSimpleAuditResult(ctx, r, "staff.session.step_up", acc.Username, false)
		writeAuthError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "验证码不正确")
		return
	}
	if h.adminIPDenied(w, r, acc) {
		return
	}
	if rerr := h.store.ResetFailures(ctx, acc.Username); rerr != nil {
		h.logger.ErrorContext(ctx, "reset_login_failures_failed",
			slog.String("module", "localauth"), slog.Any("err", rerr))
	}
	if terr := h.store.TouchSessionMFA(ctx, cookie.Value); terr != nil {
		h.logger.ErrorContext(ctx, "touch_session_mfa_failed",
			slog.String("module", "localauth"), slog.Any("err", terr))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	h.recordSimpleAuditResult(ctx, r, "staff.session.step_up", acc.Username, true)
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(acc, h.recoveryRemaining(ctx, acc)))
}

// verifyTOTPOrRecovery 校验 code（动态码）或 recoveryCode（恢复码）其一。
// 调用方已经保证两者恰好提供一个（LoginTOTP 的互斥校验）。
func (h *Handlers) verifyTOTPOrRecovery(ctx context.Context, acc Account, code, recoveryCode string) bool {
	if recoveryCode != "" {
		ok, err := h.store.ConsumeRecoveryCode(ctx, acc.ID, HashRecoveryCode(recoveryCode))
		if err != nil {
			h.logger.ErrorContext(ctx, "consume_recovery_code_failed",
				slog.String("module", "localauth"), slog.Any("err", err))
			return false
		}
		return ok
	}
	if h.secretReader == nil || acc.TOTPSecretRef == "" {
		return false
	}
	secret, err := resolveTOTPSecret(ctx, h.secretReader, acc.TOTPSecretRef, totpLoginPurpose)
	if err != nil {
		h.logger.ErrorContext(ctx, "resolve_totp_secret_failed",
			slog.String("module", "localauth"), slog.Any("err", err))
		return false
	}
	return VerifyTOTP(secret, code, time.Now().UTC())
}

func (h *Handlers) recoveryRemaining(ctx context.Context, acc Account) *int {
	if !acc.TOTPActive() {
		return nil
	}
	n, err := h.store.UnusedRecoveryCodeCount(ctx, acc.ID)
	if err != nil {
		h.logger.ErrorContext(ctx, "count_recovery_codes_failed",
			slog.String("module", "localauth"), slog.Any("err", err))
		return nil
	}
	return &n
}

// Logout 处理 POST /api/v1/auth/logout。总是清 Cookie 并返回 200——即便
// 会话早已失效，"登出"这个动作对调用方而言也应该总是成功。
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		username := ""
		if acc, lerr := h.store.LookupSession(ctx, cookie.Value); lerr == nil {
			username = acc.Username
		}
		if rerr := h.store.RevokeSession(ctx, cookie.Value); rerr != nil {
			h.logger.ErrorContext(ctx, "revoke_session_failed",
				slog.String("module", "localauth"), slog.Any("err", rerr))
		}
		if username != "" {
			h.recordSimpleAudit(ctx, r, "staff.session.logout", username)
		}
	}
	clearSessionCookie(w, r)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Me 处理 GET /api/v1/auth/me。要求 Principal（挂在 RequirePrincipal 之后）。
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", msgMissingSession)
		return
	}
	acc, err := h.store.GetByUsername(r.Context(), strings.TrimPrefix(p.ID, "staff:"))
	if err != nil {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "会话对应的账号不存在")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(acc, h.recoveryRemaining(r.Context(), acc)))
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword 处理 POST /api/v1/auth/password：自助改密，要求 Principal。
// 成功后签发一个新会话（ResetPassword 会吊销全部旧会话，含本次请求正用着
// 的那一个）——否则改完密码的人会当场被登出，体验很怪。
//
// 新会话的 amr/mfa_at 沿用改密前这个会话本身的状态（同一次浏览器会话改密
// 不该重新要求过一遍 TOTP）：TOTP 已激活的账号视为已过 pwd+otp，
// mfa_at 置为改密的这一刻；未激活的账号只有 pwd。
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", msgMissingSession)
		return
	}
	username := strings.TrimPrefix(p.ID, "staff:")

	var body changePasswordRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAuthError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "请求体不是合法 JSON 对象")
		return
	}
	ctx := r.Context()
	acc, err := h.store.GetByUsername(ctx, username)
	if err != nil {
		writeAuthError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "会话对应的账号不存在")
		return
	}
	ok2, verr := VerifyPassword(acc.passwordHash, body.CurrentPassword)
	if verr != nil || !ok2 {
		writeAuthError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "当前密码不正确")
		return
	}
	if perr := ValidatePasswordPolicy(body.NewPassword); perr != nil {
		writeAuthError(w, r, http.StatusBadRequest, "INVALID_PARAMS", perr.Error())
		return
	}
	hash, herr := HashPassword(body.NewPassword)
	if herr != nil {
		h.logger.ErrorContext(ctx, "hash_password_failed", slog.String("module", "localauth"), slog.Any("err", herr))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	updated, err := h.store.ResetPassword(ctx, username, hash, false, p.ID)
	if err != nil {
		h.logger.ErrorContext(ctx, "reset_password_failed", slog.String("module", "localauth"), slog.Any("err", err))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	amr := []string{"pwd"}
	var mfaAt *time.Time
	if updated.TOTPActive() {
		amr = []string{"pwd", "otp"}
		now := time.Now().UTC()
		mfaAt = &now
	}
	token, serr := h.store.CreateSession(ctx, updated.ID, h.environment, r.UserAgent(), clientIP(r), sessionTTL, amr, mfaAt)
	if serr != nil {
		h.logger.ErrorContext(ctx, "create_session_failed", slog.String("module", "localauth"), slog.Any("err", serr))
	} else {
		setSessionCookie(w, r, token, sessionTTL)
	}
	h.recordSimpleAudit(ctx, r, "staff.account.change_password", username)
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(updated, h.recoveryRemaining(ctx, updated)))
}

// --- TOTP 自助启用 / 确认 / 管理员重置：三个端点各自委托给同名 Action，
// 内核负责权限、环境、Schema 校验与审计（宪法 2 条：写操作只走 Action），
// 这里只做请求体解析与响应形状整理。 ---

type enrollTOTPResponse struct {
	OTPAuthURI   string `json:"otpauth_uri"`
	SecretBase32 string `json:"secret_base32"`
	Issuer       string `json:"issuer"`
	Username     string `json:"username"`
}

// EnrollTOTP 处理 POST /api/v1/auth/totp/enroll。要求 Principal（挂在
// RequirePrincipal 之后，因此天然带 CSRF 头校验）；参数皆来自会话本身，
// 请求体应为空对象。
func (h *Handlers) EnrollTOTP(w http.ResponseWriter, r *http.Request) {
	h.runTOTPAction(w, r, ActionAccountEnrollTOTP, nil, func(res action.Result) any {
		return res.Value
	})
}

// ConfirmTOTP 处理 POST /api/v1/auth/totp/confirm：{"code":"123456"}。
func (h *Handlers) ConfirmTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAuthError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "请求体不是合法 JSON 对象")
		return
	}
	h.runTOTPAction(w, r, ActionAccountConfirmTOTP, map[string]any{"code": body.Code}, func(res action.Result) any {
		return res.Value
	})
}

// runTOTPAction 是 EnrollTOTP/ConfirmTOTP 共用的执行骨架（管理员重置他人
// TOTP 走既有的通用 /actions/{id}/versions/{version}/execute 入口，不经
// 这里）：拼 action.Request、
// 调用内核、按内核错误（含 Schema/权限/环境）统一写错误响应
// （httpapi.WriteError 已经实现了 action.Code -> HTTP 状态的映射，不在这里
// 重新判一遍）。
func (h *Handlers) runTOTPAction(
	w http.ResponseWriter, r *http.Request, actionID string, params map[string]any,
	project func(action.Result) any,
) {
	if h.kernel == nil {
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	if params == nil {
		params = map[string]any{}
	}
	res, err := h.kernel.Execute(r.Context(), action.Request{
		ActionID: actionID, ActionVersion: actionVersion,
		RequestID: httpapi.RequestIDFrom(r.Context()), Params: params,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, project(res))
}

type accountListItem struct {
	Username           string   `json:"username"`
	DisplayName        string   `json:"display_name"`
	Roles              []string `json:"roles"`
	Disabled           bool     `json:"disabled"`
	MustChangePassword bool     `json:"must_change_password"`
	LockedUntil        *string  `json:"locked_until"`
	LastLoginAt        *string  `json:"last_login_at"`
	CreatedAt          string   `json:"created_at"`
	// XM-AUTH-TOTP0：账号管理页据此展示每个账号的 TOTP 状态，并决定是否
	// 出示"重置 TOTP"入口（对应 staff.account.reset_totp@1）。
	TOTPEnrolled   bool    `json:"totp_enrolled"`
	MustEnrollTOTP bool    `json:"must_enroll_totp"`
	TOTPEnrolledAt *string `json:"totp_enrolled_at"`
}

// ListAccounts 处理 GET /api/v1/staff/accounts（要求 staff.manage scope，
// 由路由层的 RequireScope 裁决，本处理器不重复判断权限）。
func (h *Handlers) ListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.store.ListAccounts(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "list_accounts_failed",
			slog.String("module", "localauth"), slog.Any("err", err))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	items := make([]accountListItem, 0, len(accounts))
	for _, a := range accounts {
		item := accountListItem{
			Username: a.Username, DisplayName: a.DisplayName,
			Roles:              append([]string(nil), a.Roles...),
			Disabled:           a.Disabled,
			MustChangePassword: a.MustChangePassword,
			CreatedAt:          a.CreatedAt.UTC().Format(time.RFC3339),
			TOTPEnrolled:       a.TOTPActive(),
			MustEnrollTOTP:     a.MustEnrollTOTP,
		}
		if a.LockedUntil != nil {
			s := a.LockedUntil.UTC().Format(time.RFC3339)
			item.LockedUntil = &s
		}
		if a.LastLoginAt != nil {
			s := a.LastLoginAt.UTC().Format(time.RFC3339)
			item.LastLoginAt = &s
		}
		if a.TOTPEnrolledAt != nil {
			s := a.TOTPEnrolledAt.UTC().Format(time.RFC3339)
			item.TOTPEnrolledAt = &s
		}
		items = append(items, item)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// recordLoginAudit 直接调审计仓储（不经 Action 内核——登录本身还没有
// Principal，内核的执行入口要求先有身份）。
func (h *Handlers) recordLoginAudit(ctx context.Context, r *http.Request, username string, succeeded bool, failureReason string) {
	if h.audit == nil {
		return
	}
	ev := audit.Event{
		OccurredAt: time.Now().UTC(), PrincipalID: "staff:" + username, PrincipalType: principal.TypeHuman,
		ActionID: "staff.session.login", ActionVersion: actionVersion, ActionRunID: uuid.New(),
		ResourceType: resourceStaffAccount, ResourceID: username, Environment: h.environment,
		RequestID: httpapi.RequestIDFrom(ctx), SourceIP: clientIP(r), Result: audit.ResultSucceeded,
	}
	if !succeeded {
		ev.Result = audit.ResultFailed
		ev.CompensationResult = failureReason
	}
	if _, err := h.audit.Append(ctx, ev); err != nil {
		h.logger.ErrorContext(ctx, "audit_append_failed", slog.String("module", "localauth"),
			slog.String("event", "staff.session.login"), slog.Any("err", err))
	}
}

// recordSimpleAudit 记一条总是"成功"的审计事件（登出、自助改密）。
func (h *Handlers) recordSimpleAudit(ctx context.Context, r *http.Request, actionID, username string) {
	h.recordSimpleAuditResult(ctx, r, actionID, username, true)
}

// recordSimpleAuditResult 记一条审计事件，成败由调用方指定（步进校验失败
// 时同样值得留痕，不是"登出"那种总是成功的动作）。
func (h *Handlers) recordSimpleAuditResult(ctx context.Context, r *http.Request, actionID, username string, succeeded bool) {
	if h.audit == nil {
		return
	}
	ev := audit.Event{
		OccurredAt: time.Now().UTC(), PrincipalID: "staff:" + username, PrincipalType: principal.TypeHuman,
		ActionID: actionID, ActionVersion: actionVersion, ActionRunID: uuid.New(),
		ResourceType: resourceStaffAccount, ResourceID: username, Environment: h.environment,
		RequestID: httpapi.RequestIDFrom(ctx), SourceIP: clientIP(r), Result: audit.ResultSucceeded,
	}
	if !succeeded {
		ev.Result = audit.ResultFailed
	}
	if _, err := h.audit.Append(ctx, ev); err != nil {
		h.logger.ErrorContext(ctx, "audit_append_failed", slog.String("module", "localauth"),
			slog.String("event", actionID), slog.Any("err", err))
	}
}
