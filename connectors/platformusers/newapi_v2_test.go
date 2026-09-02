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

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// NewAPI real GetUser v2 单元测试(XM-USERS-V2-REAL,Task 5)。
//
// 证据门:docs/approvals/NEWAPI_REAL_APPROVAL.md(APPROVED 2026-09-03)。
// 证据目录 docs/evidence/users-real/newapi/20260902T054724Z/
// (SHA256SUMS: users_page 58b5701b7230bf6a…、user_detail 2e344cfed070d83e…、
// version 19eb35ae14b75c70…)——testdata/newapi 下的两个固件文件是该证据
// 目录的逐字节拷贝(diff 已在提交前核对)。批准记录的观测 quota_per_unit=500000。

var newAPIV2TestNow = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

func readNewAPITestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "newapi", name))
	if err != nil {
		t.Fatalf("读取 testdata: %v", err)
	}
	return body
}

// ---------------------------------------------------------------------------
// 纯函数:parseNewAPIUsersPage / newAPIV2AllowedPath
// ---------------------------------------------------------------------------

func TestNewAPIV2GetUserUsesExactIDAndMasksContact(t *testing.T) {
	body := readNewAPITestdata(t, "users_page.redacted.json")
	ref := UserRef{Platform: SourceNewAPI, ID: "u_f0994cbe39f51186"}
	observedAt := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	// 批准记录的观测 quota_per_unit 是 500000(NEWAPI_REAL_APPROVAL"证据"
	// 一节);测试显式传入这个值,而不是让 parseNewAPIUsersPage 内部悄悄假设
	// 它——见该函数的注释,这正是它比 plan 示意签名多一个参数的原因。
	const observedQuotaPerUnit = 500000
	got, err := parseNewAPIUsersPage(body, ref, observedQuotaPerUnit, observedAt)
	if err != nil || got.Ref.ID != ref.ID {
		t.Fatalf("detail=%+v err=%v", got, err)
	}
	if got.User.EmailMasked == "" || strings.Contains(got.User.EmailMasked, "real@") {
		t.Fatalf("email 必须打码,得到 %q", got.User.EmailMasked)
	}
	if !strings.Contains(got.User.EmailMasked, MaskedSegment) {
		t.Fatalf("email 必须含打码占位段,得到 %q", got.User.EmailMasked)
	}
	if got.User.Status != StatusActive {
		t.Fatalf("status=%q, want active(上游 status=1)", got.User.Status)
	}
	// 固件里这一条 quota=0 → 换算成美元也应该是已知的 0,而不是未知。
	if !got.User.Balance.Known || got.User.Balance.MinorUnits != 0 {
		t.Fatalf("balance=%+v, want known 0", got.User.Balance)
	}
	wantLastLogin := time.Unix(1788279799, 0).UTC()
	if !got.User.LastActiveAt.Equal(wantLastLogin) {
		t.Fatalf("last_login_at=%v, want %v", got.User.LastActiveAt, wantLastLogin)
	}
	if got.User.PeriodRecharge.Known || got.User.PeriodConsumed.Known || got.User.Last30dConsumed.Known {
		t.Fatalf("v2 契约只批准八个字段,流水必须保持未知: %+v", got.User)
	}
	if !got.RegisteredAt.IsZero() {
		t.Fatalf("created_at 是 dropped 字段,RegisteredAt 必须保持零值,得到 %v", got.RegisteredAt)
	}
}

func TestNewAPIV2GetUserNotFoundForUnknownIDOnPage(t *testing.T) {
	body := readNewAPITestdata(t, "users_page.redacted.json")
	_, err := parseNewAPIUsersPage(body, UserRef{Platform: SourceNewAPI, ID: "u_does_not_exist"}, 500000, time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

// TestNewAPIV2GetUserIgnoresSoftDeletedUser:本次批准的脱敏证据没有观测到
// 真实的软删除样本(NEWAPI_REAL_APPROVAL"soft-delete 形态"一节:5 条样本
// DeletedAt 全为 null),所以这里用一份最小合成 JSON 单独验证软删除分支——
// 字段形状取自 upstream.go 的 newapiUserItem 定义,不是来自任何真实抓包,
// 只把 DeletedAt 从 null 换成一个非 null 的时间串。
func TestNewAPIV2GetUserIgnoresSoftDeletedUser(t *testing.T) {
	body := []byte(`{"success":true,"message":"","data":{"items":[
	  {"id":"u_deleted","username":"synthetic","display_name":"","email":"a***@example.test",
	   "status":1,"quota":0,"last_login_at":0,"DeletedAt":"2026-01-01T00:00:00Z"}
	],"total":1}}`)
	_, err := parseNewAPIUsersPage(body, UserRef{Platform: SourceNewAPI, ID: "u_deleted"}, 500000, time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删除用户应视为不存在,err=%v, want ErrNotFound", err)
	}
}

func TestNewAPIV2WriteLikeGETsStayBlocked(t *testing.T) {
	for _, path := range []string{"/api/user/token", "/api/user/aff", "/api/user/epay/notify"} {
		if newAPIV2AllowedPath(path) {
			t.Fatalf("write-like GET allowed: %s", path)
		}
	}
	if !newAPIV2AllowedPath("/api/user/") || !newAPIV2AllowedPath("/api/status") {
		t.Fatal("the two routes this reader actually calls must stay allowed")
	}
}

// ---------------------------------------------------------------------------
// 网络层:分页扫描、quota_per_unit 现读、ctx 取消
// ---------------------------------------------------------------------------

const (
	newAPIV2TestCredRef = "secret://platformusers-test/newapi-v2"
	newAPIV2TestToken   = "test-only.invalid-token"
)

func newAPIV2TestSecretProvider(t *testing.T) secrets.SecretProvider {
	t.Helper()
	const envVar = "XM_TEST_NEWAPI_V2_TOKEN"
	p, err := secrets.NewEnvProvider(
		map[string]string{newAPIV2TestCredRef: envVar},
		secrets.WithLookup(func(name string) (string, bool) {
			if name != envVar {
				return "", false
			}
			return newAPIV2TestToken, true
		}),
	)
	if err != nil {
		t.Fatalf("装配 env provider: %v", err)
	}
	return p
}

func newNewAPIV2TestRealClient(t *testing.T, server *httptest.Server, now time.Time) *RealClient {
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
		Source:          SourceNewAPI,
		Endpoint:        u,
		CredentialRef:   newAPIV2TestCredRef,
		TargetAllowlist: []string{host},
		Secrets:         newAPIV2TestSecretProvider(t),
	}, WithBaseTransport(server.Client().Transport), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	return c
}

func newAPIV2Status(quotaPerUnit int64) string {
	return fmt.Sprintf(`{"success":true,"message":"","data":{"version":"v1.0.0-rc.30","start_time":0,"quota_per_unit":%d}}`, quotaPerUnit)
}

func newAPIV2Page(items string, total int) string {
	return fmt.Sprintf(`{"success":true,"message":"","data":{"items":[%s],"total":%d}}`, items, total)
}

func newAPIV2Item(id, username, email string, status int, quota, lastLogin int64) string {
	return fmt.Sprintf(`{"id":%q,"username":%q,"display_name":"","email":%q,"status":%d,"quota":%d,"last_login_at":%d,"DeletedAt":null}`,
		id, username, email, status, quota, lastLogin)
}

func newAPIV2RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+newAPIV2TestToken {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":false,"message":"unauthorized","data":null}`))
			return
		}
		next(w, r)
	}
}

// TestRealClientNewAPIGetUserScansAcrossPages:目标用户在第二页。
func TestRealClientNewAPIGetUserScansAcrossPages(t *testing.T) {
	var pagesSeen []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(newAPIV2Status(500000)))
	})
	mux.HandleFunc("GET /api/user/", newAPIV2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("p")
		pagesSeen = append(pagesSeen, page)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			_, _ = w.Write([]byte(newAPIV2Page(newAPIV2Item("1", "alice", "a@example.test", 1, 1000000, 100), 2)))
		case "2":
			_, _ = w.Write([]byte(newAPIV2Page(newAPIV2Item("2", "bob", "b@example.test", 1, 500000, 200), 2)))
		default:
			t.Fatalf("unexpected page %q", page)
		}
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	detail, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "2"}})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if detail.User.Username != "bob" {
		t.Fatalf("detail=%+v, want bob (page 2)", detail)
	}
	if len(pagesSeen) != 2 || pagesSeen[0] != "1" || pagesSeen[1] != "2" {
		t.Fatalf("pagesSeen=%v, want [1 2]", pagesSeen)
	}
}

// TestRealClientNewAPIGetUserNotFoundAfterExhaustingAllPages。
func TestRealClientNewAPIGetUserNotFoundAfterExhaustingAllPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(newAPIV2Status(500000)))
	})
	mux.HandleFunc("GET /api/user/", newAPIV2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("p")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			_, _ = w.Write([]byte(newAPIV2Page(newAPIV2Item("1", "alice", "a@example.test", 1, 1000000, 100), 1)))
		default:
			t.Fatalf("unexpected page %q", page)
		}
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "does-not-exist"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

// TestRealClientNewAPIGetUserLookupIncompleteWhenBudgetExceeded。
func TestRealClientNewAPIGetUserLookupIncompleteWhenBudgetExceeded(t *testing.T) {
	var requests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(newAPIV2Status(500000)))
	})
	mux.HandleFunc("GET /api/user/", newAPIV2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(newAPIV2Page(newAPIV2Item("never-matches", "x", "x@example.test", 1, 0, 0), 999999)))
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "u_target"}})
	if !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("err=%v, want ErrLookupIncomplete", err)
	}
	if requests != newapiMaxPages {
		t.Fatalf("requests=%d, want 恰好翻页预算 %d 次", requests, newapiMaxPages)
	}
}

// TestRealClientNewAPIGetUserRereadsQuotaPerUnitEveryCall:两次独立的
// GetUser 调用之间,/api/status 返回的 quota_per_unit 发生变化——第二次
// 调用算出的余额必须反映新基数,证明 RealClient 没有把这个运行期可变的值
// 跨调用缓存下来(NEWAPI_REAL_APPROVAL"证据"一节的硬约束)。
func TestRealClientNewAPIGetUserRereadsQuotaPerUnitEveryCall(t *testing.T) {
	quotaPerUnit := int64(500000)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(newAPIV2Status(quotaPerUnit)))
	})
	mux.HandleFunc("GET /api/user/", newAPIV2RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(newAPIV2Page(newAPIV2Item("1", "alice", "a@example.test", 1, 1000000, 100), 1)))
	}))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	first, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "1"}})
	if err != nil {
		t.Fatalf("第一次 GetUser: %v", err)
	}
	if first.User.Balance != KnownAmount(200, platformCurrency) { // 1000000/500000=2.00 美元=200 分
		t.Fatalf("first balance=%+v, want 200 分(qpu=500000)", first.User.Balance)
	}

	quotaPerUnit = 250000 // 上游 root 通过后台把基数改小了一半
	second, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "1"}})
	if err != nil {
		t.Fatalf("第二次 GetUser: %v", err)
	}
	if second.User.Balance != KnownAmount(400, platformCurrency) { // 1000000/250000=4.00 美元=400 分
		t.Fatalf("second balance=%+v, want 400 分(qpu 已改成 250000,不应沿用第一次缓存的基数)", second.User.Balance)
	}
}

func TestRealClientNewAPIGetUserPropagatesContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cancelled context 不应该发出任何请求")
	})
	mux.HandleFunc("GET /api/user/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cancelled context 不应该发出任何请求")
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetUser(ctx, GetUserQuery{Ref: UserRef{Platform: SourceNewAPI, ID: "u_10241"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestRealClientNewAPIGetUserRejectsCrossPlatformRef(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	_, err := client.GetUser(context.Background(), GetUserQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}})
	if err == nil {
		t.Fatal("newapi 客户端不应该接受 sub2api 的 Ref")
	}
}

// TestNewAPIV2CapabilitiesDoNotInheritDailyOrKey:real 端的 NewAPI 只声明
// detail_read,不应该像 Fake 端的 Sub2API 那样带 daily/key——两者的能力
// 集合来源不同(Fake 是"演示要不要秀这个面板",real 是"是否已实现且过了
// contracttest"),但断言方式一致:声明的能力必须与实现的接口对得上。
func TestNewAPIV2CapabilitiesDoNotInheritDailyOrKey(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	client := newNewAPIV2TestRealClient(t, server, newAPIV2TestNow)

	caps := client.V2Capabilities()
	if len(caps) != 1 || caps[0] != CapabilityUserDetailRead {
		t.Fatalf("V2Capabilities=%v, want [detail_read] only", caps)
	}
	if _, ok := any(client).(DailyUsageReader); ok {
		t.Fatal("RealClient 不应该实现 DailyUsageReader(real 端未获批)")
	}
	if _, ok := any(client).(KeyMetadataReader); ok {
		t.Fatal("RealClient 不应该实现 KeyMetadataReader(real 端未获批)")
	}
}
