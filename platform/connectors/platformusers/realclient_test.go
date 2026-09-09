package platformusers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/connectors/platformusers/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 真实客户端的契约合规与形状映射测试(XM-USERS-REAL)。
//
// 判据与 connectors/sub2api、connectors/newapi 的真实客户端测试同一条:
// 起一个本地假上游(httptest.NewTLSServer),喂它从上游源码核对出来的真实
// 响应形状(见 upstream.go 顶部注释的核对依据),断言真实客户端解出正确的
// 契约字段。被测的是**真实的那条路径**:真实的传输层护栏、真实的 HTTP
// 解析、真实的错误分类、真实的金额换算。假的只有上游本身。

// realClientCredentialRef / realClientToken 是测试用的凭据引用与取值。
//
// token 是明显的占位符(不含 sk- 前缀、不是 32 位十六进制),不是真凭据的
// 高熵形态——gitleaks 的 generic-api-key 规则对着长得像密钥的字面量误报,
// 仓库约定是不在测试固件里放这种字面量(见项目记忆的"gitleaks 误报"条目)。
const (
	realClientCredentialRef = "secret://platformusers-test/readonly"
	realClientToken         = "test-only.invalid-token"
)

// testNow 是本文件所有用例共用的固定时钟。
//
// 必须固定:ActiveToday 按业务日(CST +08:00)判定"今天",固定时钟才能让
// 固件里的 last_active_at/last_login_at 与断言的期望值可重放。
var testNow = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

func realClientSecretProvider(t *testing.T) secrets.SecretProvider {
	t.Helper()
	const envVar = "XM_TEST_PLATFORMUSERS_TOKEN"
	p, err := secrets.NewEnvProvider(
		map[string]string{realClientCredentialRef: envVar},
		secrets.WithLookup(func(name string) (string, bool) {
			if name != envVar {
				return "", false
			}
			return realClientToken, true
		}),
	)
	if err != nil {
		t.Fatalf("装配 env provider: %v", err)
	}
	return p
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析测试服务器地址: %v", err)
	}
	return u.Hostname()
}

// newRealClient 装配一个指向 server 的真实客户端。
//
// 固定时钟 + 信任 httptest 的自签证书是唯一被换掉的两样(WithBaseTransport
// 注释里的纪律同样适用于这里):只读方法限制、主机 allowlist、拒绝重定向
// 依旧是生产那一份代码。allowlist 默认取 server 自己的主机;传
// allowlistHost 覆盖它,用来测试 allowlist 拒绝的路径。
func newRealClient(t *testing.T, server *httptest.Server, source string, allowlistHost string) *platformusers.RealClient {
	t.Helper()
	if allowlistHost == "" {
		allowlistHost = hostOf(t, server.URL)
	}
	cfg := platformusers.RealConfig{
		Source:          source,
		Endpoint:        server.URL,
		CredentialRef:   realClientCredentialRef,
		TargetAllowlist: []string{allowlistHost},
		Secrets:         realClientSecretProvider(t),
	}
	c, err := platformusers.NewRealClient(cfg,
		platformusers.WithBaseTransport(server.Client().Transport),
		platformusers.WithClock(func() time.Time { return testNow }),
	)
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func sub2apiEnvelope(data string) string {
	return fmt.Sprintf(`{"code":0,"message":"success","data":%s}`, data)
}

func newapiEnvelope(data string) string {
	return fmt.Sprintf(`{"success":true,"message":"","data":%s}`, data)
}

// ---------------------------------------------------------------------------
// 合规判据:真实客户端必须通过与 Fake 同一套契约套件
// ---------------------------------------------------------------------------

// requireBearerLikeAuth 包一层鉴权校验:请求头对不上期望值就答 401。
//
// 没有这一层,下面的固件对任何请求(包括完全没带凭据的)都会来者不拒,
// 于是"客户端到底有没有把凭据放进正确的头"这件事就成了一条永远不会失败的
// 断言。包上这一层之后,header 名字或取值方案(X-Api-Key vs Authorization:
// Bearer)只要写错,本文件其余所有映射测试都会因为 401 而一起变红。
func requireHeaderEquals(header, want string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(header) != want {
			writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED","message":"bad credential"}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sub2apiTwoUserFixture() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/admin/users", requireHeaderEquals("X-Api-Key", realClientToken,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") != "1" {
				writeJSON(w, http.StatusOK, sub2apiEnvelope(`{"items":[],"total":2,"page":2,"page_size":1000,"pages":1}`))
				return
			}
			writeJSON(w, http.StatusOK, sub2apiEnvelope(fmt.Sprintf(`{"items":[
			  {"id":1,"email":"a@example.test","username":"alice","status":"active","balance":120.50000000,"last_active_at":%q},
			  {"id":2,"email":"b@example.test","username":"bob","status":"disabled","balance":-3.25,"last_active_at":null}
			],"total":2,"page":1,"page_size":1000,"pages":1}`, testNow.Format(time.RFC3339))))
		})))
	return mux
}

func TestRealClientSatisfiesContractSub2API(t *testing.T) {
	server := httptest.NewTLSServer(sub2apiTwoUserFixture())
	t.Cleanup(server.Close)

	contracttest.Run(t, "real-sub2api", platformusers.SourceSub2API, func() platformusers.ReadClient {
		return newRealClient(t, server, platformusers.SourceSub2API, "")
	})
}

func newapiTwoActiveUserFixture(quotaPerUnit int64) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, newapiEnvelope(fmt.Sprintf(`{"version":"v1.0.0","start_time":0,"quota_per_unit":%d}`, quotaPerUnit)))
	})
	// 只有用户列表端点挂了 AdminAuth;/api/status 在真实上游里不需要鉴权
	// (见 upstream.go 的 routeStatus 注释),固件照实区分,不给 /api/status 加锁。
	mux.Handle("GET /api/user/", requireHeaderEquals("Authorization", "Bearer "+realClientToken,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("p") != "1" {
				writeJSON(w, http.StatusOK, newapiEnvelope(`{"items":[],"total":3,"page":2,"page_size":100}`))
				return
			}
			writeJSON(w, http.StatusOK, newapiEnvelope(fmt.Sprintf(`{"items":[
			  {"id":1,"username":"alice","display_name":"Alice A","email":"a@example.test","status":1,"quota":1000000,"last_login_at":%d,"DeletedAt":null},
			  {"id":2,"username":"bob","display_name":"","email":"b@example.test","status":2,"quota":250000,"last_login_at":0,"DeletedAt":null},
			  {"id":3,"username":"charlie","display_name":"","email":"c@example.test","status":1,"quota":999999999,"last_login_at":0,"DeletedAt":"2026-01-01T00:00:00Z"}
			],"total":3,"page":1,"page_size":100}`, testNow.Unix())))
		})))
	return mux
}

func TestRealClientSatisfiesContractNewAPI(t *testing.T) {
	server := httptest.NewTLSServer(newapiTwoActiveUserFixture(500000))
	t.Cleanup(server.Close)

	contracttest.Run(t, "real-newapi", platformusers.SourceNewAPI, func() platformusers.ReadClient {
		return newRealClient(t, server, platformusers.SourceNewAPI, "")
	})
}

// ---------------------------------------------------------------------------
// 上游形状映射
// ---------------------------------------------------------------------------

func TestRealClientSub2APIMapsUpstreamShape(t *testing.T) {
	server := httptest.NewTLSServer(sub2apiTwoUserFixture())
	t.Cleanup(server.Close)
	client := newRealClient(t, server, platformusers.SourceSub2API, "")

	page, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceSub2API})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page.Users) != 2 {
		t.Fatalf("用户数 = %d, want 2: %+v", len(page.Users), page.Users)
	}

	// 默认排序 balance_desc:alice(120.50) 在 bob(-3.25) 之前
	alice, bob := page.Users[0], page.Users[1]
	if alice.ID != "1" || alice.Username != "alice" {
		t.Fatalf("alice 映射错了: %+v", alice)
	}
	if want := platformusers.MaskEmail("a@example.test"); alice.EmailMasked != want {
		t.Fatalf("alice 邮箱打码 = %q, want %q", alice.EmailMasked, want)
	}
	if alice.Status != platformusers.StatusActive {
		t.Fatalf("alice 状态 = %q, want active", alice.Status)
	}
	if alice.Balance != platformusers.KnownAmount(12050, "USD") {
		t.Fatalf("alice 余额 = %+v, want 12050 分(120.50 美元)", alice.Balance)
	}
	if alice.PeriodRecharge.Known || alice.PeriodConsumed.Known || alice.Last30dConsumed.Known {
		t.Fatalf("v1 契约给不出逐用户流水,应保持 Known=false: %+v", alice)
	}
	if alice.TokenPrefix != "" {
		t.Fatalf("列表端点没有 API Key 字段,TokenPrefix 应为空串,得到 %q", alice.TokenPrefix)
	}
	if !alice.LastActiveAt.Equal(testNow) {
		t.Fatalf("alice 最后活跃 = %v, want %v", alice.LastActiveAt, testNow)
	}

	if bob.ID != "2" || bob.Status != platformusers.StatusDisabled {
		t.Fatalf("bob 映射错了: %+v", bob)
	}
	if bob.Balance != platformusers.KnownAmount(-325, "USD") {
		t.Fatalf("bob 余额 = %+v, want -325 分(允许透支)", bob.Balance)
	}
	if !bob.LastActiveAt.IsZero() {
		t.Fatalf("bob 的 last_active_at 是 null,应保持零值,得到 %v", bob.LastActiveAt)
	}

	// 全体口径:120.50 - 3.25 = 117.25 美元,不受任何筛选影响
	if page.TotalBalance != platformusers.KnownAmount(11725, "USD") {
		t.Fatalf("总余额 = %+v, want 11725 分", page.TotalBalance)
	}
	// 只有 alice 的 last_active_at 落在"今天"
	if !page.ActiveToday.Known || page.ActiveToday.Value != 1 {
		t.Fatalf("今日活跃 = %+v, want 1", page.ActiveToday)
	}
	if page.Snapshot.Source != platformusers.SourceSub2API {
		t.Fatalf("Snapshot.Source = %q, want %q", page.Snapshot.Source, platformusers.SourceSub2API)
	}
	if page.Snapshot.ObservedAt.IsZero() {
		t.Fatal("Snapshot.ObservedAt 不该是零值")
	}
}

func TestRealClientNewAPIMapsUpstreamShape(t *testing.T) {
	server := httptest.NewTLSServer(newapiTwoActiveUserFixture(500000))
	t.Cleanup(server.Close)
	client := newRealClient(t, server, platformusers.SourceNewAPI, "")

	page, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceNewAPI})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	// charlie 是软删除用户(DeletedAt 非 null),必须被排除
	if len(page.Users) != 2 {
		t.Fatalf("用户数 = %d, want 2(软删除用户必须被排除): %+v", len(page.Users), page.Users)
	}

	alice, bob := page.Users[0], page.Users[1]
	// quota 1,000,000 / quota_per_unit 500,000 = 2.00 美元 = 200 分
	if alice.ID != "1" || alice.Username != "Alice A" {
		t.Fatalf("alice 映射错了(display_name 应当覆盖 username): %+v", alice)
	}
	if alice.Balance != platformusers.KnownAmount(200, "USD") {
		t.Fatalf("alice 余额 = %+v, want 200 分(quota 1000000 / qpu 500000)", alice.Balance)
	}
	if alice.Status != platformusers.StatusActive {
		t.Fatalf("alice 状态(NewAPI status=1) = %q, want active", alice.Status)
	}
	if alice.LastActiveAt.Unix() != testNow.Unix() {
		t.Fatalf("alice 最后活跃 = %v, want unix %d", alice.LastActiveAt, testNow.Unix())
	}

	// display_name 为空串时应当回落到 username
	if bob.ID != "2" || bob.Username != "bob" {
		t.Fatalf("bob 映射错了(display_name 为空应回落到 username): %+v", bob)
	}
	if bob.Status != platformusers.StatusDisabled {
		t.Fatalf("bob 状态(NewAPI status=2) = %q, want disabled", bob.Status)
	}
	// quota 250,000 / 500,000 = 0.50 美元 = 50 分
	if bob.Balance != platformusers.KnownAmount(50, "USD") {
		t.Fatalf("bob 余额 = %+v, want 50 分", bob.Balance)
	}
	if !bob.LastActiveAt.IsZero() {
		t.Fatalf("bob 的 last_login_at=0,应保持零值,得到 %v", bob.LastActiveAt)
	}

	// 全体口径**不含软删除用户**:(1,000,000+250,000)/500,000 = 2.50 美元 = 250 分
	if page.TotalBalance != platformusers.KnownAmount(250, "USD") {
		t.Fatalf("总余额 = %+v, want 250 分(软删除用户的 quota 不计入)", page.TotalBalance)
	}
	if !page.ActiveToday.Known || page.ActiveToday.Value != 1 {
		t.Fatalf("今日活跃 = %+v, want 1", page.ActiveToday)
	}
}

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

func TestRealClientClassifiesAuthFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED","message":"invalid key"}`)
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newRealClient(t, server, platformusers.SourceSub2API, "")

	_, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceSub2API})
	if connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("401 的分类 = %q, want auth(err=%v)", connector.KindOf(err), err)
	}
}

func TestRealClientClassifiesBadResponse(t *testing.T) {
	t.Run("非 JSON 正文", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `not json at all`)
		})
		server := httptest.NewTLSServer(mux)
		t.Cleanup(server.Close)
		client := newRealClient(t, server, platformusers.SourceSub2API, "")

		_, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceSub2API})
		if connector.KindOf(err) != connector.KindBadResponse {
			t.Fatalf("非 JSON 正文的分类 = %q, want bad_response(err=%v)", connector.KindOf(err), err)
		}
	})

	t.Run("形状不符(NewAPI success=false)", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, newapiEnvelope(`{"version":"v1.0.0","start_time":0,"quota_per_unit":500000}`))
		})
		mux.HandleFunc("GET /api/user/", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `{"success":false,"message":"nope","data":null}`)
		})
		server := httptest.NewTLSServer(mux)
		t.Cleanup(server.Close)
		client := newRealClient(t, server, platformusers.SourceNewAPI, "")

		_, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceNewAPI})
		if connector.KindOf(err) != connector.KindBadResponse {
			t.Fatalf("success=false 的分类 = %q, want bad_response(err=%v)", connector.KindOf(err), err)
		}
	})
}

// TestRealClientEnforcesTargetAllowlist:目标 allowlist 是硬护栏,在传输层
// 拒绝,而不是靠 RealClient 自己记得哪个主机"应该"是对的。
func TestRealClientEnforcesTargetAllowlist(t *testing.T) {
	server := httptest.NewTLSServer(sub2apiTwoUserFixture())
	t.Cleanup(server.Close)
	// allowlist 指向一个与实际服务器不同的主机:请求会被 ReadOnlyTransport
	// 在发出前拒绝,而不是真的打到别处去。
	client := newRealClient(t, server, platformusers.SourceSub2API, "api.somewhere-else.test")

	_, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceSub2API})
	if connector.KindOf(err) != connector.KindForbiddenTarget {
		t.Fatalf("allowlist 之外的分类 = %q, want forbidden_target(err=%v)", connector.KindOf(err), err)
	}
}

// TestRealClientRefusesRedirects:上游一个 302 就能把请求引到 allowlist
// 之外,所以重定向一律拒绝,而且分类必须是 forbidden_target。
func TestRealClientRefusesRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.test/api/v1/admin/users", http.StatusFound)
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newRealClient(t, server, platformusers.SourceSub2API, "")

	_, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceSub2API})
	if connector.KindOf(err) != connector.KindForbiddenTarget {
		t.Fatalf("重定向被拒的分类 = %q, want forbidden_target(err=%v)", connector.KindOf(err), err)
	}
}

// TestRealClientTruncationMarksTotalCountUnknown:翻页预算耗尽时,
// TotalCount 必须退化成 Unknown,而不是把"翻到页数上限为止"的条数冒充成
// 符合条件的总数(宪法 12 条)。TotalBalance 仍然给出(下界也要报),
// 但 Watermark 必须能看出这次读取不完整。
func TestRealClientTruncationMarksTotalCountUnknown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			page = 1
		}
		// 每一页都给一条真实数据、且 total 远大于能翻到的页数上限——
		// 循环会在预算耗尽(page == sub2apiMaxPages)后停手,而不是因为
		// "翻完了"停手。
		writeJSON(w, http.StatusOK, sub2apiEnvelope(fmt.Sprintf(
			`{"items":[{"id":%d,"email":"u%d@example.test","username":"u%d","status":"active","balance":1.00}],"total":999999,"page":%d,"page_size":1000,"pages":1000}`,
			page, page, page, page)))
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := newRealClient(t, server, platformusers.SourceSub2API, "")

	page, err := client.ListUsers(t.Context(), platformusers.ListFilter{Source: platformusers.SourceSub2API, Limit: platformusers.MaxLimit})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if page.TotalCount.Known {
		t.Fatalf("翻页预算耗尽时 TotalCount 应为 Unknown,得到 %+v", page.TotalCount)
	}
	if !strings.Contains(page.Snapshot.Watermark, "truncated") {
		t.Fatalf("水位应能看出这次读取不完整,得到 %q", page.Snapshot.Watermark)
	}
}
