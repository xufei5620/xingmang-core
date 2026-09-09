package platformusers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Sub2API real GetUser v2 单元测试(XM-USERS-V2-REAL,Task 4)。
//
// 证据门:docs/approvals/SUB2_REAL_APPROVAL.md(APPROVED 2026-09-03)。
// 证据目录 docs/evidence/users-real/sub2api/20260902T054723Z/
// (SHA256SUMS: users_page 02013b4f7761cbd6…、user_detail d250d91f403b4dba…、
// version 8f3991ad282def8b…)——testdata/sub2api 下的两个固件文件是该证据
// 目录的逐字节拷贝(diff 已在提交前核对)。

// sub2V2TestNow 是本文件所有网络层用例共用的固定时钟。这是本文件自己的
// 局部变量,不是 realclient_test.go 的 testNow——那个常量属于外部测试包
// platformusers_test,本文件是内部测试包 platformusers(需要直接调用
// parseSub2UsersPage/sub2V2AllowedPath 等未导出符号),两者不共享标识符。
var sub2V2TestNow = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

func readSub2Testdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "sub2api", name))
	if err != nil {
		t.Fatalf("读取 testdata: %v", err)
	}
	return body
}

// ---------------------------------------------------------------------------
// 纯函数:parseSub2UsersPage / sub2V2AllowedPath
// ---------------------------------------------------------------------------

func TestSub2APIV2GetUserUsesExactIDAndFixedPoint(t *testing.T) {
	body := readSub2Testdata(t, "users_page.redacted.json")
	ref := UserRef{Platform: SourceSub2API, ID: "u_aaaa185f870148e7"}
	observedAt := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	got, err := parseSub2UsersPage(body, ref, observedAt)
	if err != nil || got.Ref.ID != ref.ID || !got.User.Balance.Known {
		t.Fatalf("detail=%+v err=%v", got, err)
	}
	// 脱敏规则把余额的每一位数字都清零,唯独首位因为 JSON 不允许多位数字面量
	// 带前导零而写成 1(证据 README"脱敏应用"一节)——原始形状是十进制字面量
	// 1.0,sub2apiBalanceScale=8 位小数换算、再折算到 platformCurrencyScale=2
	// 位小数后应恰好是 100(1.00 美元)。这条断言同时覆盖"定点解析,不经
	// float"这条纪律:decimalToMinorUnits 从不经过 float64。
	if got.User.Balance != KnownAmount(100, platformCurrency) {
		t.Fatalf("balance=%+v, want 100 分(1.00 美元)", got.User.Balance)
	}
	if got.User.Status != StatusActive {
		t.Fatalf("status=%q, want active", got.User.Status)
	}
	wantLastActive := parseUpstreamTime("2026-09-02T11:59:39.556509+08:00")
	if !got.User.LastActiveAt.Equal(wantLastActive) {
		t.Fatalf("last_active_at=%v, want %v", got.User.LastActiveAt, wantLastActive)
	}
	if !strings.Contains(got.User.EmailMasked, MaskedSegment) {
		t.Fatalf("email 必须打码,得到 %q", got.User.EmailMasked)
	}
	// SUB2_REAL_APPROVAL 只批准六个字段(id/email/username/status/balance/
	// last_active_at);其余流水字段与 created_at(证据里明确列为 dropped)
	// 必须保持未知/零值,不能编一个"看起来有数据"的值出来。
	if got.User.PeriodRecharge.Known || got.User.PeriodConsumed.Known || got.User.Last30dConsumed.Known {
		t.Fatalf("v2 契约只批准六个字段,流水必须保持未知: %+v", got.User)
	}
	if !got.RegisteredAt.IsZero() {
		t.Fatalf("created_at 是 dropped 字段,RegisteredAt 必须保持零值,得到 %v", got.RegisteredAt)
	}
	if got.Snapshot.ObservedAt != observedAt || got.Snapshot.Source != SourceSub2API {
		t.Fatalf("snapshot=%+v", got.Snapshot)
	}
}

func TestSub2APIV2GetUserNotFoundForUnknownIDOnPage(t *testing.T) {
	body := readSub2Testdata(t, "users_page.redacted.json")
	_, err := parseSub2UsersPage(body, UserRef{Platform: SourceSub2API, ID: "u_does_not_exist"}, time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

func TestSub2APIV2PermanentlyRejectsMockUsageRoute(t *testing.T) {
	if sub2V2AllowedPath("/api/v1/admin/users/u_10241/usage") {
		t.Fatal("known mock route must remain blocked (EV-2026-08-27-sub2api-read-survey.md)")
	}
	if !sub2V2AllowedPath("/api/v1/admin/users") {
		t.Fatal("the one route this reader actually calls must stay allowed")
	}
	if sub2V2AllowedPath("/api/v1/admin/dashboard/realtime") ||
		sub2V2AllowedPath("/api/v1/admin/redeem-codes/stats") {
		t.Fatal("other known-mock endpoints must also stay blocked")
	}
}

// ---------------------------------------------------------------------------
// 网络层:分页扫描、423 合规门、ctx 取消
// ---------------------------------------------------------------------------

const (
	sub2TestCredRef = "secret://platformusers-test/sub2-v2"
	sub2TestToken   = "test-only.invalid-token"
)

func sub2TestSecretProvider(t *testing.T) secrets.SecretProvider {
	t.Helper()
	const envVar = "XM_TEST_SUB2_V2_TOKEN"
	p, err := secrets.NewEnvProvider(
		map[string]string{sub2TestCredRef: envVar},
		secrets.WithLookup(func(name string) (string, bool) {
			if name != envVar {
				return "", false
			}
			return sub2TestToken, true
		}),
	)
	if err != nil {
		t.Fatalf("装配 env provider: %v", err)
	}
	return p
}

func newSub2TestRealClient(t *testing.T, server *httptest.Server, now time.Time) *RealClient {
	t.Helper()
	u := server.URL
	host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	c, err := NewRealClient(RealConfig{
		Source:          SourceSub2API,
		Endpoint:        u,
		CredentialRef:   sub2TestCredRef,
		TargetAllowlist: []string{host},
		Secrets:         sub2TestSecretProvider(t),
	}, WithBaseTransport(server.Client().Transport), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	return c
}

func sub2RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != sub2TestToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized","data":null}`))
			return
		}
		next(w, r)
	}
}

func sub2Page(items string, total int) string {
	return fmt.Sprintf(`{"code":0,"message":"success","data":{"items":[%s],"total":%d}}`, items, total)
}

func sub2Item(id, email, username, status, balance, lastActive string) string {
	la := "null"
	if lastActive != "" {
		la = `"` + lastActive + `"`
	}
	return fmt.Sprintf(`{"id":%q,"email":%q,"username":%q,"status":%q,"balance":%s,"last_active_at":%s}`,
		id, email, username, status, balance, la)
}

// TestRealClientSub2APIGetUserScansAcrossPages:目标用户在第二页,证明
// getSub2APIUser 真的会翻页,而不是只看第一页就报 ErrNotFound。
func TestRealClientSub2APIGetUserScansAcrossPages(t *testing.T) {
	var pagesSeen []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", sub2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pagesSeen = append(pagesSeen, page)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			_, _ = w.Write([]byte(sub2Page(sub2Item("1", "a@example.test", "alice", "active", "10.00", ""), 2)))
		case "2":
			_, _ = w.Write([]byte(sub2Page(sub2Item("2", "b@example.test", "bob", "active", "5.00", ""), 2)))
		default:
			t.Fatalf("unexpected page %q", page)
		}
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newSub2TestRealClient(t, server, sub2V2TestNow)

	detail, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "2"}})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if detail.User.Username != "bob" {
		t.Fatalf("detail=%+v, want bob (page 2)", detail)
	}
	if len(pagesSeen) != 2 || pagesSeen[0] != "1" || pagesSeen[1] != "2" {
		t.Fatalf("pagesSeen=%v, want [1 2]", pagesSeen)
	}
	if len(detail.Capabilities) != 1 || detail.Capabilities[0] != CapabilityUserDetailRead {
		t.Fatalf("capabilities=%v, want [detail_read]", detail.Capabilities)
	}
}

// TestRealClientSub2APIGetUserNotFoundAfterExhaustingAllPages:两页都翻完,
// 目标 ID 始终没出现 → 必须是 ErrNotFound,不是 ErrLookupIncomplete
// (设计文档 §5.1:"若只能扫描列表,必须完整耗尽才能返回 ErrNotFound")。
func TestRealClientSub2APIGetUserNotFoundAfterExhaustingAllPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", sub2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			_, _ = w.Write([]byte(sub2Page(sub2Item("1", "a@example.test", "alice", "active", "10.00", ""), 2)))
		case "2":
			_, _ = w.Write([]byte(sub2Page(sub2Item("2", "b@example.test", "bob", "active", "5.00", ""), 2)))
		default:
			t.Fatalf("unexpected page %q", page)
		}
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newSub2TestRealClient(t, server, sub2V2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "does-not-exist"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

// TestRealClientSub2APIGetUserLookupIncompleteWhenBudgetExceeded:上游一直
// 声称还有更多页(total 远大于翻页预算能拉到的条数),预算耗尽后必须是
// ErrLookupIncomplete,不能被误判成 ErrNotFound(设计文档 §5.1)。
func TestRealClientSub2APIGetUserLookupIncompleteWhenBudgetExceeded(t *testing.T) {
	var requests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", sub2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		// total 远大于 sub2apiMaxPages*1 条,永远"翻不完",且这一条 id 永远
		// 不等于我们要找的那个 ID。
		_, _ = w.Write([]byte(sub2Page(sub2Item("never-matches", "a@example.test", "alice", "active", "1.00", ""), 999999)))
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newSub2TestRealClient(t, server, sub2V2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_target"}})
	if !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("err=%v, want ErrLookupIncomplete", err)
	}
	if requests != sub2apiMaxPages {
		t.Fatalf("requests=%d, want 恰好翻页预算 %d 次", requests, sub2apiMaxPages)
	}
}

// TestRealClientSub2APIGetUserComplianceGuardIsTypedAndNeverPosts:423 必须
// 被翻译成可识别的合规门错误,而且这条链路结构上不可能发出
// POST /admin/compliance/accept——测试服务器压根没有注册处理 POST 请求
// 的 handler,任何 POST 都会得到 405,证明本方法从未尝试发它。
func TestRealClientSub2APIGetUserComplianceGuardIsTypedAndNeverPosts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusLocked)
		_, _ = w.Write([]byte(`{"code":"ADMIN_COMPLIANCE_ACK_REQUIRED","message":"compliance ack required","data":null}`))
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newSub2TestRealClient(t, server, sub2V2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}})
	if !errors.Is(err, ErrSub2APIComplianceConfirmationRequired) {
		t.Fatalf("err=%v, want ErrSub2APIComplianceConfirmationRequired", err)
	}
	if connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("KindOf(err)=%q, want auth(423 仍归 classifyStatus 已有的 auth 分类)", connector.KindOf(err))
	}
	// 上游原始错误正文(可能带业务细节)不得进入我们自己的哨兵错误文本。
	if strings.Contains(err.Error(), "ADMIN_COMPLIANCE_ACK_REQUIRED") {
		t.Fatalf("上游正文不应逐字出现在错误文本里: %v", err)
	}
}

func TestRealClientSub2APIGetUserPropagatesContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cancelled context 不应该发出任何请求")
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newSub2TestRealClient(t, server, sub2V2TestNow)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetUser(ctx, GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestRealClientSub2APIGetUserRejectsCrossPlatformRef(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	client := newSub2TestRealClient(t, server, sub2V2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "u_10241"}})
	if err == nil {
		t.Fatal("sub2api 客户端不应该接受 newapi 的 Ref")
	}
}
