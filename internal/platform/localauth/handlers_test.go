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

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// fakeAccountStore 实现 accountStore，供 Handlers 测试注入，不依赖数据库。
type fakeAccountStore struct {
	accounts         map[string]Account
	sessions         map[string]uuid.UUID
	revoked          map[string]bool
	failures         map[string]int
	createSessionErr error
	resetPasswordErr error
}

func newFakeAccountStore() *fakeAccountStore {
	return &fakeAccountStore{
		accounts: map[string]Account{}, sessions: map[string]uuid.UUID{},
		revoked: map[string]bool{}, failures: map[string]int{},
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

func (f *fakeAccountStore) CreateSession(_ context.Context, accountID uuid.UUID, _, _, _ string, _ time.Duration) (string, error) {
	if f.createSessionErr != nil {
		return "", f.createSessionErr
	}
	token := "token-" + accountID.String()
	f.sessions[token] = accountID
	return token, nil
}

func (f *fakeAccountStore) LookupSession(_ context.Context, rawToken string) (Account, error) {
	id, ok := f.sessions[rawToken]
	if !ok || f.revoked[rawToken] {
		return Account{}, ErrSessionInvalid
	}
	for _, a := range f.accounts {
		if a.ID == id {
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

type fakeAudit struct {
	events []audit.Event
}

func (f *fakeAudit) Append(_ context.Context, e audit.Event) (audit.Event, error) {
	f.events = append(f.events, e)
	return e, nil
}

func testHandlers(store accountStore, aud auditAppender) *Handlers {
	return &Handlers{
		store: store, environment: "staging", audit: aud,
		limiter: httpapi.NewRateLimiter(httpapi.RateLimitConfig{PerMinute: 10000, Burst: 10000}),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
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
}

func TestLogoutClearsCookieAndRevokesSession(t *testing.T) {
	store := newFakeAccountStore()
	acc := store.addAccount("alice", "correct-password-123")
	token, err := store.CreateSession(context.Background(), acc.ID, "staging", "ua", "1.2.3.4", sessionTTL)
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

func TestListAccountsReturnsItems(t *testing.T) {
	store := newFakeAccountStore()
	store.addAccount("alice", "correct-password-123", "staff")
	store.addAccount("bob", "correct-password-123", "admin")
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
}
