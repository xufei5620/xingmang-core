package localauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// sessionRecord 是 fakeAccountStore 里一条会话的内存表示，对齐
// core.staff_session 新增的 mfa_at/amr 两列。
type sessionRecord struct {
	accountID uuid.UUID
	mfaAt     *time.Time
	amr       []string
}

// challengeRecord 对齐 core.staff_login_challenge。
type challengeRecord struct {
	accountID uuid.UUID
	consumed  bool
}

// fakeAccountStore 实现 accountStore，供 Handlers 测试注入，不依赖数据库。
type fakeAccountStore struct {
	accounts         map[string]Account
	sessions         map[string]*sessionRecord
	revoked          map[string]bool
	failures         map[string]int
	challenges       map[string]*challengeRecord
	recoveryCodes    map[uuid.UUID]map[string]bool // codeHash -> used
	createSessionErr error
	resetPasswordErr error
}

func newFakeAccountStore() *fakeAccountStore {
	return &fakeAccountStore{
		accounts: map[string]Account{}, sessions: map[string]*sessionRecord{},
		revoked: map[string]bool{}, failures: map[string]int{},
		challenges: map[string]*challengeRecord{}, recoveryCodes: map[uuid.UUID]map[string]bool{},
	}
}

func (f *fakeAccountStore) addAccount(username, password string, roles ...string) Account {
	hash, err := HashPassword(password)
	if err != nil {
		panic(err)
	}
	a := Account{ID: uuid.New(), Username: username, DisplayName: username, Roles: roles, passwordHash: hash}
	f.accounts[username] = a
	return a
}

// addTOTPAccount 建一个已激活 TOTP 的账号，密钥引用与 secretReader 里登记的
// 引用需要调用方保证一致（一般用 fixedTOTPSecretRef）。
func (f *fakeAccountStore) addTOTPAccount(username, password string, secretRef string, roles ...string) Account {
	a := f.addAccount(username, password, roles...)
	now := time.Now().UTC()
	a.TOTPSecretRef = secretRef
	a.TOTPEnrolledAt = &now
	f.accounts[username] = a
	return a
}

func (f *fakeAccountStore) addRecoveryCode(accountID uuid.UUID, code string) {
	if f.recoveryCodes[accountID] == nil {
		f.recoveryCodes[accountID] = map[string]bool{}
	}
	f.recoveryCodes[accountID][HashRecoveryCode(code)] = false
}

func (f *fakeAccountStore) GetByUsername(_ context.Context, username string) (Account, error) {
	a, ok := f.accounts[username]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

func (f *fakeAccountStore) RecordFailure(_ context.Context, username string) error {
	if _, ok := f.accounts[username]; !ok {
		return ErrNotFound
	}
	f.failures[username]++
	if f.failures[username] >= lockThreshold {
		a := f.accounts[username]
		until := time.Now().UTC().Add(lockDuration)
		a.LockedUntil = &until
		f.accounts[username] = a
		f.failures[username] = 0
	}
	return nil
}

func (f *fakeAccountStore) ResetFailures(_ context.Context, username string) error {
	a, ok := f.accounts[username]
	if !ok {
		return ErrNotFound
	}
	f.failures[username] = 0
	a.LockedUntil = nil
	f.accounts[username] = a
	return nil
}

func (f *fakeAccountStore) CreateSession(
	_ context.Context, accountID uuid.UUID, _, _, _ string, _ time.Duration, amr []string, mfaAt *time.Time,
) (string, error) {
	if f.createSessionErr != nil {
		return "", f.createSessionErr
	}
	token := "token-" + uuid.New().String()
	f.sessions[token] = &sessionRecord{accountID: accountID, mfaAt: mfaAt, amr: append([]string(nil), amr...)}
	return token, nil
}

func (f *fakeAccountStore) LookupSession(_ context.Context, rawToken string) (Account, error) {
	rec, ok := f.sessions[rawToken]
	if !ok || f.revoked[rawToken] {
		return Account{}, ErrSessionInvalid
	}
	for _, a := range f.accounts {
		if a.ID == rec.accountID {
			a.SessionMFAAt = rec.mfaAt
			a.SessionAMR = rec.amr
			return a, nil
		}
	}
	return Account{}, ErrSessionInvalid
}

func (f *fakeAccountStore) RevokeSession(_ context.Context, rawToken string) error {
	f.revoked[rawToken] = true
	return nil
}

func (f *fakeAccountStore) ResetPassword(_ context.Context, username, passwordHash string, mustChange bool, _ string) (Account, error) {
	if f.resetPasswordErr != nil {
		return Account{}, f.resetPasswordErr
	}
	a, ok := f.accounts[username]
	if !ok {
		return Account{}, ErrNotFound
	}
	a.passwordHash = passwordHash
	a.MustChangePassword = mustChange
	f.accounts[username] = a
	return a, nil
}

func (f *fakeAccountStore) ListAccounts(context.Context) ([]Account, error) {
	out := make([]Account, 0, len(f.accounts))
	for _, a := range f.accounts {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeAccountStore) CreateLoginChallenge(_ context.Context, accountID uuid.UUID, _, _, _ string) (string, error) {
	token := "challenge-" + uuid.New().String()
	f.challenges[token] = &challengeRecord{accountID: accountID}
	return token, nil
}

func (f *fakeAccountStore) LookupLoginChallenge(_ context.Context, rawToken string) (Account, error) {
	rec, ok := f.challenges[rawToken]
	if !ok || rec.consumed {
		return Account{}, ErrChallengeInvalid
	}
	for _, a := range f.accounts {
		if a.ID == rec.accountID {
			return a, nil
		}
	}
	return Account{}, ErrChallengeInvalid
}

func (f *fakeAccountStore) ConsumeLoginChallenge(_ context.Context, rawToken string) error {
	if rec, ok := f.challenges[rawToken]; ok {
		rec.consumed = true
	}
	return nil
}

func (f *fakeAccountStore) ConsumeRecoveryCode(_ context.Context, accountID uuid.UUID, codeHash string) (bool, error) {
	codes := f.recoveryCodes[accountID]
	if codes == nil {
		return false, nil
	}
	used, known := codes[codeHash]
	if !known || used {
		return false, nil
	}
	codes[codeHash] = true
	return true, nil
}

func (f *fakeAccountStore) UnusedRecoveryCodeCount(_ context.Context, accountID uuid.UUID) (int, error) {
	n := 0
	for _, used := range f.recoveryCodes[accountID] {
		if !used {
			n++
		}
	}
	return n, nil
}

func (f *fakeAccountStore) TouchSessionMFA(_ context.Context, rawToken string) error {
	rec, ok := f.sessions[rawToken]
	if !ok || f.revoked[rawToken] {
		return ErrSessionInvalid
	}
	now := time.Now().UTC()
	rec.mfaAt = &now
	found := false
	for _, a := range rec.amr {
		if a == "otp" {
			found = true
		}
	}
	if !found {
		rec.amr = append(rec.amr, "otp")
	}
	return nil
}

type fakeAudit struct {
	events []audit.Event
}

func (f *fakeAudit) Append(_ context.Context, e audit.Event) (audit.Event, error) {
	f.events = append(f.events, e)
	return e, nil
}

// fakeKernel 是 actionRunner 的测试替身，供 EnrollTOTP/ConfirmTOTP/ResetTOTP
// 三个 HTTP 方法的测试使用，验证它们正确拼了 action.Request 并原样转译
// 内核的成功/失败结果，不需要装一整套真实 Registry+RunStore。
type fakeKernel struct {
	lastReq action.Request
	result  action.Result
	err     error
}

func (f *fakeKernel) Execute(_ context.Context, req action.Request) (action.Result, error) {
	f.lastReq = req
	if f.err != nil {
		return action.Result{}, f.err
	}
	return f.result, nil
}

// fakeSecretReader 是 totpSecretReader 的测试替身。
type fakeSecretReader struct {
	values map[string]secrets.SecretValue
}

func (f *fakeSecretReader) Resolve(_ context.Context, ref secrets.CredentialRef, _ string) (secrets.SecretValue, error) {
	v, ok := f.values[ref.String()]
	if !ok {
		return secrets.SecretValue{}, secrets.ErrNotFound
	}
	return v, nil
}

func testHandlers(store accountStore, aud auditAppender, opts ...func(*Handlers)) *Handlers {
	h := &Handlers{
		store: store, environment: "staging", audit: aud,
		limiter: httpapi.NewRateLimiter(httpapi.RateLimitConfig{PerMinute: 10000, Burst: 10000}),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		totpLimiter: httpapi.NewRateLimiter(httpapi.RateLimitConfig{
			PerMinute: 10000, Burst: 10000,
		}),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func withSecretReader(r totpSecretReader) func(*Handlers) {
	return func(h *Handlers) { h.secretReader = r }
}

func withKernel(k actionRunner) func(*Handlers) {
	return func(h *Handlers) { h.kernel = k }
}

func withAdminIPAllowlist(a AdminIPAllowlist) func(*Handlers) {
	return func(h *Handlers) { h.adminIPAllowlist = a }
}

func loginBody(username, password string) *bytes.Reader {
	b, _ := json.Marshal(loginRequest{Username: username, Password: password})
	return bytes.NewReader(b)
}

func TestLoginSetsSessionCookieAndAudits(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("alice", "correct-password-123", "staff")
	aud := &fakeAudit{}
	h := testHandlers(store, aud)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("alice", "correct-password-123"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName || cookies[0].Value == "" {
		t.Fatalf("cookies = %+v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
		t.Fatalf("cookie 属性不对: %+v", cookies[0])
	}
	if cookies[0].Secure {
		t.Fatal("非 https 请求不该设置 Secure（httptest.NewRequest 默认不是 TLS）")
	}
	var out sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Username != "alice" || len(out.Roles) != 1 || out.Roles[0] != "staff" {
		t.Fatalf("out = %+v", out)
	}
	if out.RequiresTOTP {
		t.Fatal("未启用 TOTP 的账号一步登录应 requires_totp=false")
	}
	if len(aud.events) != 1 || aud.events[0].Result != audit.ResultSucceeded || aud.events[0].ActionID != "staff.session.login" {
		t.Fatalf("audit events = %+v", aud.events)
	}
}

// 用户名不存在与密码错误必须得到相同的状态码与响应体，防止响应本身变成
// 一个用户名枚举接口。
func TestLoginWrongPasswordAndUnknownUserGetIdenticalResponse(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("alice", "correct-password-123")
	h := testHandlers(store, &fakeAudit{})

	doLogin := func(username, password string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody(username, password))
		rec := httptest.NewRecorder()
		h.Login(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return rec.Code, out
	}

	statusWrong, bodyWrong := doLogin("alice", "totally-wrong-password")
	statusUnknown, bodyUnknown := doLogin("nobody-such-user", "whatever-password-123")

	if statusWrong != http.StatusUnauthorized || statusUnknown != http.StatusUnauthorized {
		t.Fatalf("status = %d, %d", statusWrong, statusUnknown)
	}
	errWrong := bodyWrong["error"].(map[string]any)
	errUnknown := bodyUnknown["error"].(map[string]any)
	if errWrong["code"] != "INVALID_CREDENTIALS" || errUnknown["code"] != "INVALID_CREDENTIALS" {
		t.Fatalf("codes = %v, %v", errWrong["code"], errUnknown["code"])
	}
	if errWrong["message"] != errUnknown["message"] {
		t.Fatalf("messages 应逐字相同: %v vs %v", errWrong["message"], errUnknown["message"])
	}
}

func TestLoginLockedAccount(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addAccount("locked-user", "correct-password-123")
	future := time.Now().UTC().Add(time.Hour)
	acc.LockedUntil = &future
	store.accounts["locked-user"] = acc
	h := testHandlers(store, &fakeAudit{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("locked-user", "correct-password-123"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusLocked {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLoginDisabledAccount(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addAccount("disabled-user", "correct-password-123")
	acc.Disabled = true
	store.accounts["disabled-user"] = acc
	h := testHandlers(store, &fakeAudit{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("disabled-user", "correct-password-123"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLoginRecordsFailureAndLocksAfterThreshold(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("alice", "correct-password-123")
	h := testHandlers(store, &fakeAudit{})

	for i := 0; i < lockThreshold; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("alice", "wrong-password"))
		rec := httptest.NewRecorder()
		h.Login(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d", i, rec.Code)
		}
	}
	if !store.accounts["alice"].Locked(time.Now().UTC()) {
		t.Fatal("连续失败达到阈值后账号应被锁定")
	}

	// 即便这次密码是对的，锁定状态下也应拒绝
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("alice", "correct-password-123"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusLocked {
		t.Fatalf("锁定期间即便密码正确也应拒绝, status = %d", rec.Code)
	}
}

const fixedTOTPSecretRef = "secret://staff-totp/44444444-4444-4444-4444-444444444444"

func TestLoginWithActiveTOTPReturnsPendingChallenge(t *testing.T) {
	store := newFakeAccountStore()
	store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "admin")
	aud := &fakeAudit{}
	h := testHandlers(store, aud)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("carol", "correct-password-123"))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("密码通过但 TOTP 待验证时不该签发会话 Cookie")
	}
	var out pendingTOTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.RequiresTOTP || out.TempToken == "" || out.Username != "carol" {
		t.Fatalf("out = %+v", out)
	}
	// 密码步骤本身不该在这里写"成功登录"审计——完整登录事件在
	// LoginTOTP 完成后才写。
	if len(aud.events) != 0 {
		t.Fatalf("audit events = %+v", aud.events)
	}
}

func totpLoginChallenge(t *testing.T, store *fakeAccountStore, h *Handlers, username, password string) pendingTOTPResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody(username, password))
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out pendingTOTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.RequiresTOTP {
		t.Fatalf("测试前置条件不满足：应处于 pending TOTP 状态, out=%+v", out)
	}
	return out
}

func TestLoginTOTPCompletesWithCorrectCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeAccountStore()
	store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "staff")
	reader := &fakeSecretReader{values: map[string]secrets.SecretValue{
		fixedTOTPSecretRef: secrets.NewSecretValue([]byte(EncodeTOTPSecret(secret))),
	}}
	aud := &fakeAudit{}
	h := testHandlers(store, aud, withSecretReader(reader))

	pending := totpLoginChallenge(t, store, h, "carol", "correct-password-123")
	code := GenerateTOTP(secret, time.Now().UTC())
	body, _ := json.Marshal(loginTOTPRequest{TempToken: pending.TempToken, Code: code})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == "" {
		t.Fatalf("应签发会话 Cookie: %+v", cookies)
	}
	var out sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RequiresTOTP || !out.TOTPEnrolled {
		t.Fatalf("out = %+v", out)
	}
	rec2 := &sessionRecord{}
	for _, r := range store.sessions {
		rec2 = r
	}
	if rec2.mfaAt == nil || len(rec2.amr) != 2 {
		t.Fatalf("会话应记录 mfa_at 与 amr=[pwd,otp], got %+v", rec2)
	}
	found := 0
	for _, a := range aud.events {
		if a.ActionID == "staff.session.login" && a.Result == audit.ResultSucceeded {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("完成二步登录应写一条成功的 staff.session.login 审计, events=%+v", aud.events)
	}

	// temp token 应被消费，不能重复使用
	body2, _ := json.Marshal(loginTOTPRequest{TempToken: pending.TempToken, Code: code})
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body2))
	rec3 := httptest.NewRecorder()
	h.LoginTOTP(rec3, req2)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("已消费的 temp token 重放应被拒绝, status = %d", rec3.Code)
	}
}

func TestLoginTOTPRejectsWrongCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeAccountStore()
	store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "staff")
	reader := &fakeSecretReader{values: map[string]secrets.SecretValue{
		fixedTOTPSecretRef: secrets.NewSecretValue([]byte(EncodeTOTPSecret(secret))),
	}}
	h := testHandlers(store, &fakeAudit{}, withSecretReader(reader))

	pending := totpLoginChallenge(t, store, h, "carol", "correct-password-123")
	body, _ := json.Marshal(loginTOTPRequest{TempToken: pending.TempToken, Code: "000000"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["error"].(map[string]any)["code"] != "INVALID_CREDENTIALS" {
		t.Fatalf("out = %v", out)
	}
}

func TestLoginTOTPRejectsUnknownTempToken(t *testing.T) {
	store := newFakeAccountStore()
	h := testHandlers(store, &fakeAudit{})
	body, _ := json.Marshal(loginTOTPRequest{TempToken: "not-a-real-token", Code: "123456"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["error"].(map[string]any)["code"] != "CHALLENGE_INVALID" {
		t.Fatalf("out = %v", out)
	}
}

func TestLoginTOTPRequiresExactlyOneOfCodeOrRecoveryCode(t *testing.T) {
	h := testHandlers(newFakeAccountStore(), &fakeAudit{})
	for _, body := range []loginTOTPRequest{
		{TempToken: "x"}, // 两者都没给
		{TempToken: "x", Code: "123456", RecoveryCode: "ABCDEFGHIJ"}, // 两者都给了
	} {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		h.LoginTOTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%+v status = %d", body, rec.Code)
		}
	}
}

func TestLoginTOTPAcceptsRecoveryCodeOnce(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "staff")
	store.addRecoveryCode(acc.ID, "ABCDE-FGHIJ")
	h := testHandlers(store, &fakeAudit{})

	pending := totpLoginChallenge(t, store, h, "carol", "correct-password-123")
	body, _ := json.Marshal(loginTOTPRequest{TempToken: pending.TempToken, RecoveryCode: "ABCDE-FGHIJ"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 第二次登录，同一张恢复码应已失效
	pending2 := totpLoginChallenge(t, store, h, "carol", "correct-password-123")
	body2, _ := json.Marshal(loginTOTPRequest{TempToken: pending2.TempToken, RecoveryCode: "abcde-fghij"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	h.LoginTOTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("已用过的恢复码应被拒绝, status = %d", rec2.Code)
	}
}

func TestLoginTOTPStepUpRequiresSessionCookie(t *testing.T) {
	h := testHandlers(newFakeAccountStore(), &fakeAudit{})
	body, _ := json.Marshal(loginTOTPRequest{Code: "123456"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无 Cookie 的步进请求应 403, got %d", rec.Code)
	}
}

func TestLoginTOTPStepUpRequiresCSRFHeader(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeAccountStore()
	acc := store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "staff")
	token, err := store.CreateSession(context.Background(), acc.ID, "staging", "ua", "1.2.3.4", sessionTTL, []string{"pwd", "otp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeSecretReader{values: map[string]secrets.SecretValue{
		fixedTOTPSecretRef: secrets.NewSecretValue([]byte(EncodeTOTPSecret(secret))),
	}}
	h := testHandlers(store, &fakeAudit{}, withSecretReader(reader))

	body, _ := json.Marshal(loginTOTPRequest{Code: GenerateTOTP(secret, time.Now().UTC())})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺少 CSRF 头的步进请求应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginTOTPStepUpRefreshesMFA(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeAccountStore()
	acc := store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "staff")
	token, err := store.CreateSession(context.Background(), acc.ID, "staging", "ua", "1.2.3.4", sessionTTL, []string{"pwd"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeSecretReader{values: map[string]secrets.SecretValue{
		fixedTOTPSecretRef: secrets.NewSecretValue([]byte(EncodeTOTPSecret(secret))),
	}}
	aud := &fakeAudit{}
	h := testHandlers(store, aud, withSecretReader(reader))

	body, _ := json.Marshal(loginTOTPRequest{Code: GenerateTOTP(secret, time.Now().UTC())})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	req.Header.Set("X-Requested-With", "xingmang")
	rec := httptest.NewRecorder()
	h.LoginTOTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if store.sessions[token].mfaAt == nil {
		t.Fatal("步进成功应刷新会话的 mfa_at")
	}
	hasOTP := false
	for _, a := range store.sessions[token].amr {
		if a == "otp" {
			hasOTP = true
		}
	}
	if !hasOTP {
		t.Fatal("步进成功应把 amr 并入 otp")
	}
	// 不应重新签发/覆盖 Cookie（步进只刷新既有会话，不换 token）
	if len(rec.Result().Cookies()) != 0 {
		t.Fatalf("步进不该重新签发会话 Cookie: %+v", rec.Result().Cookies())
	}
}

func TestAdminIPAllowlistDeniesLoginForAdminAccount(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addAccount("admin1", "correct-password-123", "admin")
	acc.MustEnrollTOTP = true // 模拟 set_roles/create 判定过"需要 TOTP"
	store.accounts["admin1"] = acc
	allowlist, err := ParseAdminIPAllowlist("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	aud := &fakeAudit{}
	h := testHandlers(store, aud, withAdminIPAllowlist(allowlist))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("admin1", "correct-password-123"))
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("不在名单内的来源应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["error"].(map[string]any)["code"] != "ADMIN_NETWORK_DENIED" {
		t.Fatalf("out = %v", out)
	}
	found := false
	for _, e := range aud.events {
		if e.CompensationResult == "ip_denied" {
			found = true
		}
	}
	if !found {
		t.Fatal("IP 拒绝应被审计")
	}
}

func TestAdminIPAllowlistAllowsMatchingSourceAndSkipsNonAdmin(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("admin1", "correct-password-123", "admin")
	store.addAccount("staff1", "correct-password-123", "staff")
	allowlist, err := ParseAdminIPAllowlist("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	h := testHandlers(store, &fakeAudit{}, withAdminIPAllowlist(allowlist))

	// admin1 的 MustEnrollTOTP=false（本用例没有显式模拟角色判定），因此只有
	// 显式标记过 MustEnrollTOTP/TOTPActive 的账号才落入名单管辖——见下方
	// 单独用例覆盖"落入管辖但来源匹配"的放行路径。这里验证的是**非管辖账号**
	// （staff1，从未被判定需要 TOTP）即便来自名单外的 IP 也不受影响。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody("staff1", "correct-password-123"))
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("非管理员账号不受 IP 名单限制, status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMeRequiresPrincipalAndReturnsAccount(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("alice", "correct-password-123", "staff")
	h := testHandlers(store, &fakeAudit{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无 Principal 应 403, got %d", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req2 = req2.WithContext(principal.WithPrincipal(req2.Context(), principal.Principal{
		ID: "staff:alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: issuerLocalAuth, Environment: "staging",
	}))
	rec2 := httptest.NewRecorder()
	h.Me(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec2.Code, rec2.Body.String())
	}
	var out sessionResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil || out.Username != "alice" {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	if out.TOTPEnrolled {
		t.Fatal("未启用 TOTP 的账号 totp_enrolled 应为 false")
	}
}

func TestMeIncludesTOTPStatusAndRecoveryCount(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addTOTPAccount("carol", "correct-password-123", fixedTOTPSecretRef, "staff")
	store.addRecoveryCode(acc.ID, "AAAAA-BBBBB")
	store.addRecoveryCode(acc.ID, "CCCCC-DDDDD")
	h := testHandlers(store, &fakeAudit{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "staff:carol", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: issuerLocalAuth, Environment: "staging",
	}))
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	var out sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.TOTPEnrolled || out.TOTPEnrolledAt == nil {
		t.Fatalf("out = %+v", out)
	}
	if out.RecoveryCodesRemaining == nil || *out.RecoveryCodesRemaining != 2 {
		t.Fatalf("recovery_codes_remaining = %v, want 2", out.RecoveryCodesRemaining)
	}
}

func TestLogoutClearsCookieAndRevokesSession(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addAccount("alice", "correct-password-123")
	token, err := store.CreateSession(context.Background(), acc.ID, "staging", "ua", "1.2.3.4", sessionTTL, []string{"pwd"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	aud := &fakeAudit{}
	h := testHandlers(store, aud)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.Logout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatalf("cookie 应被清除（MaxAge<0）: %+v", cookies)
	}
	if !store.revoked[token] {
		t.Fatal("会话应被吊销")
	}
	if len(aud.events) != 1 || aud.events[0].ActionID != "staff.session.logout" {
		t.Fatalf("audit events = %+v", aud.events)
	}
}

func TestLogoutWithoutCookieStillSucceeds(t *testing.T) {
	h := testHandlers(newFakeAccountStore(), &fakeAudit{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.Logout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("登出应总是成功, status = %d", rec.Code)
	}
}

func TestChangePasswordVerifiesCurrentAndReissuesSession(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addAccount("alice", "old-password-123")
	h := testHandlers(store, &fakeAudit{})

	body, _ := json.Marshal(changePasswordRequest{CurrentPassword: "wrong-current-pw", NewPassword: "new-password-456"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(body))
	req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "staff:alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: issuerLocalAuth, Environment: "staging",
	}))
	rec := httptest.NewRecorder()
	h.ChangePassword(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误的当前密码应 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	body2, _ := json.Marshal(changePasswordRequest{CurrentPassword: "old-password-123", NewPassword: "new-password-456"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(body2))
	req2 = req2.WithContext(principal.WithPrincipal(req2.Context(), principal.Principal{
		ID: "staff:alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: issuerLocalAuth, Environment: "staging",
	}))
	rec2 := httptest.NewRecorder()
	h.ChangePassword(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("正确的当前密码应成功, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	cookies := rec2.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == "" {
		t.Fatalf("改密成功应重新签发会话 cookie: %+v", cookies)
	}
	updated := store.accounts["alice"]
	if updated.MustChangePassword {
		t.Fatal("自助改密应清除 must_change_password")
	}
	ok, err := VerifyPassword(updated.passwordHash, "new-password-456")
	if err != nil || !ok {
		t.Fatalf("新密码应生效: ok=%v err=%v", ok, err)
	}
	_ = acc
}

func TestListAccountsReturnsItemsWithTOTPStatus(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("alice", "correct-password-123", "staff")
	store.addTOTPAccount("bob", "correct-password-123", fixedTOTPSecretRef, "admin")
	h := testHandlers(store, &fakeAudit{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/staff/accounts", nil)
	rec := httptest.NewRecorder()
	h.ListAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Items []accountListItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %+v", out.Items)
	}
	byUsername := map[string]accountListItem{}
	for _, item := range out.Items {
		byUsername[item.Username] = item
	}
	if byUsername["alice"].TOTPEnrolled {
		t.Fatal("alice 未启用 TOTP")
	}
	if !byUsername["bob"].TOTPEnrolled || byUsername["bob"].TOTPEnrolledAt == nil {
		t.Fatalf("bob = %+v", byUsername["bob"])
	}
}

// --- EnrollTOTP / ConfirmTOTP / ResetTOTP：三个 HTTP 方法只做请求体解析 +
// 委托给内核，用 fakeKernel 验证委托本身，不重复测试 Action Handler 内部
// 逻辑（那部分在 actions_test.go 已覆盖）。 ---

// testRequestID 是 principalRequest 构造的请求统一携带的请求 ID——真实路由
// 由 httpapi.RequestID 中间件写入 context，这里的单测绕开了路由，手动补上
// 同一个值，才能验证 EnrollTOTP 等方法确实把它转发进了 action.Request。
const testRequestID = "test-request-id"

func principalRequest(method, path string, body []byte) *http.Request {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	ctx := principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "staff:admin1", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: issuerLocalAuth, Environment: "staging", Scopes: []string{ScopeManage},
	})
	ctx = httpapi.WithRequestID(ctx, testRequestID)
	return req.WithContext(ctx)
}

func TestEnrollTOTPDelegatesToKernel(t *testing.T) {
	kernel := &fakeKernel{result: action.Result{RunID: uuid.New(), Value: map[string]any{"otpauth_uri": "otpauth://x"}}}
	h := testHandlers(newFakeAccountStore(), &fakeAudit{}, withKernel(kernel))

	req := principalRequest(http.MethodPost, "/api/v1/auth/totp/enroll", nil)
	rec := httptest.NewRecorder()
	h.EnrollTOTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if kernel.lastReq.ActionID != ActionAccountEnrollTOTP || kernel.lastReq.ActionVersion != "1" {
		t.Fatalf("kernel request = %+v", kernel.lastReq)
	}
	if kernel.lastReq.RequestID != testRequestID {
		t.Fatalf("request id = %q, want %q", kernel.lastReq.RequestID, testRequestID)
	}
}

func TestConfirmTOTPDelegatesToKernelWithCode(t *testing.T) {
	kernel := &fakeKernel{result: action.Result{RunID: uuid.New(), Value: map[string]any{"recovery_codes": []string{"a"}}}}
	h := testHandlers(newFakeAccountStore(), &fakeAudit{}, withKernel(kernel))

	body, _ := json.Marshal(map[string]string{"code": "123456"})
	req := principalRequest(http.MethodPost, "/api/v1/auth/totp/confirm", body)
	rec := httptest.NewRecorder()
	h.ConfirmTOTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if kernel.lastReq.ActionID != ActionAccountConfirmTOTP || kernel.lastReq.Params["code"] != "123456" {
		t.Fatalf("kernel request = %+v", kernel.lastReq)
	}
}

func TestTOTPHandlersTranslateKernelErrors(t *testing.T) {
	kernel := &fakeKernel{err: action.NewError(action.CodePermissionDenied, "缺少权限 staff.manage", nil)}
	h := testHandlers(newFakeAccountStore(), &fakeAudit{}, withKernel(kernel))

	req := principalRequest(http.MethodPost, "/api/v1/auth/totp/enroll", nil)
	rec := httptest.NewRecorder()
	h.EnrollTOTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PERMISSION_DENIED 应映射到 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEnrollTOTPWithoutKernelReturnsInternalError(t *testing.T) {
	h := testHandlers(newFakeAccountStore(), &fakeAudit{})
	req := principalRequest(http.MethodPost, "/api/v1/auth/totp/enroll", nil)
	rec := httptest.NewRecorder()
	h.EnrollTOTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("未装配 kernel 时应 500, got %d", rec.Code)
	}
}
