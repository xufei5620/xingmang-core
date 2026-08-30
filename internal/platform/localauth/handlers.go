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

// accountStore 是 Handlers 依赖的最小面（*Store 满足），便于测试注入假实现。
type accountStore interface {
	GetByUsername(ctx context.Context, username string) (Account, error)
	RecordFailure(ctx context.Context, username string) error
	ResetFailures(ctx context.Context, username string) error
	CreateSession(ctx context.Context, accountID uuid.UUID, environment, userAgent, ip string, ttl time.Duration) (string, error)
	LookupSession(ctx context.Context, rawToken string) (Account, error)
	RevokeSession(ctx context.Context, rawToken string) error
	ResetPassword(ctx context.Context, username, passwordHash string, mustChange bool, actor string) (Account, error)
	ListAccounts(ctx context.Context) ([]Account, error)
}

// auditAppender 是 Handlers 依赖的审计写入面（*audit.Store 满足）。
type auditAppender interface {
	Append(ctx context.Context, e audit.Event) (audit.Event, error)
}

// Handlers 实现 /api/v1/auth/* 与 /api/v1/staff/accounts 的 HTTP 处理器。
type Handlers struct {
	store       accountStore
	environment string
	audit       auditAppender
	limiter     *httpapi.RateLimiter
	logger      *slog.Logger
}

// NewHandlers 装配处理器。auditStore 可以是 nil（此时不写审计，仅用于不需要
// 审计断言的测试；生产装配必须传真实的 *audit.Store)。
func NewHandlers(store *Store, environment string, auditStore auditAppender, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{
		store: store, environment: environment, audit: auditStore, logger: logger,
		limiter: httpapi.NewRateLimiter(httpapi.RateLimitConfig{
			PerMinute: loginRateLimitPerMinute, Burst: loginRateLimitBurst,
		}),
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResponse struct {
	Username           string   `json:"username"`
	DisplayName        string   `json:"display_name"`
	Roles              []string `json:"roles"`
	MustChangePassword bool     `json:"must_change_password"`
}

func toSessionResponse(a Account) sessionResponse {
	return sessionResponse{
		Username:           a.Username,
		DisplayName:        a.DisplayName,
		Roles:              append([]string(nil), a.Roles...),
		MustChangePassword: a.MustChangePassword,
	}
}

func writeAuthError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	httpapi.WriteJSON(w, status, httpapi.ErrorResponse{Error: httpapi.ErrorBody{
		Code: code, Message: message, RequestID: httpapi.RequestIDFrom(r.Context()),
	}})
}

// clientIP 取客户端地址：优先 X-Forwarded-For 首个值（栈的入口是宿主
// Nginx 反代，见 deploy/nginx），取不到则回落到 RemoteAddr 的主机部分。
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

// Login 处理 POST /api/v1/auth/login。不要求 Principal——登录本身就是在
// 建立身份。失败对"用户名不存在"与"密码错误"给同一个状态码、同一句文案、
// 同量级耗时（见下方 dummyPasswordHash 调用），避免响应本身或响应时延
// 变成一个用户名枚举接口；账号锁定 / 停用有各自更具体的状态码——这两者
// 已经在响应内容上与"密码错误"区分开了，不需要额外的计时保护。
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

	if rerr := h.store.ResetFailures(ctx, acc.Username); rerr != nil {
		h.logger.ErrorContext(ctx, "reset_login_failures_failed",
			slog.String("module", "localauth"), slog.Any("err", rerr))
	}
	token, err := h.store.CreateSession(ctx, acc.ID, h.environment, r.UserAgent(), ip, sessionTTL)
	if err != nil {
		h.logger.ErrorContext(ctx, "create_session_failed",
			slog.String("module", "localauth"), slog.Any("err", err))
		writeAuthError(w, r, http.StatusInternalServerError, "INTERNAL", "服务内部错误")
		return
	}
	setSessionCookie(w, r, token, sessionTTL)
	h.recordLoginAudit(ctx, r, acc.Username, true, "")
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(acc))
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
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(acc))
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword 处理 POST /api/v1/auth/password：自助改密，要求 Principal。
// 成功后签发一个新会话（ResetPassword 会吊销全部旧会话，含本次请求正用着
// 的那一个）——否则改完密码的人会当场被登出，体验很怪。
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
	token, serr := h.store.CreateSession(ctx, updated.ID, h.environment, r.UserAgent(), clientIP(r), sessionTTL)
	if serr != nil {
		h.logger.ErrorContext(ctx, "create_session_failed", slog.String("module", "localauth"), slog.Any("err", serr))
	} else {
		setSessionCookie(w, r, token, sessionTTL)
	}
	h.recordSimpleAudit(ctx, r, "staff.account.change_password", username)
	httpapi.WriteJSON(w, http.StatusOK, toSessionResponse(updated))
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
		}
		if a.LockedUntil != nil {
			s := a.LockedUntil.UTC().Format(time.RFC3339)
			item.LockedUntil = &s
		}
		if a.LastLoginAt != nil {
			s := a.LastLoginAt.UTC().Format(time.RFC3339)
			item.LastLoginAt = &s
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
	if h.audit == nil {
		return
	}
	ev := audit.Event{
		OccurredAt: time.Now().UTC(), PrincipalID: "staff:" + username, PrincipalType: principal.TypeHuman,
		ActionID: actionID, ActionVersion: actionVersion, ActionRunID: uuid.New(),
		ResourceType: resourceStaffAccount, ResourceID: username, Environment: h.environment,
		RequestID: httpapi.RequestIDFrom(ctx), SourceIP: clientIP(r), Result: audit.ResultSucceeded,
	}
	if _, err := h.audit.Append(ctx, ev); err != nil {
		h.logger.ErrorContext(ctx, "audit_append_failed", slog.String("module", "localauth"),
			slog.String("event", actionID), slog.Any("err", err))
	}
}
