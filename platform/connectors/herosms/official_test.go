package herosms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 对照官方 OpenAPI（https://hero-sms.com/docs/v1/openapi.json，2026-09-06 抓取，
// 50 个操作）补的测试。夹具里的字段名**逐字来自 schema**，不是猜的。

type capture struct {
	method string
	path   string
	query  string
	body   string
}

func serveJSON(t *testing.T, status int, payload string, got *capture) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			raw, _ := io.ReadAll(r.Body)
			*got = capture{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(raw)}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv)
	// 兼容层与现代层在同一个测试服务器上，靠路径区分。
	c.legacyBaseURL = srv.URL + "/stubs/handler_api.php"
	return c
}

// OTP 列表：GET /activations/{id}/otp，data 是 ActivationOtpSchema 数组。
func TestListOTPsUsesOfficialFieldNames(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"data":[
	  {"id":"o1","smsCode":"123456","smsText":"Your code is 123456","receivedAt":"2026-09-06T01:02:03Z","type":"sms","phoneFrom":"Google","service":"go"},
	  {"id":"o2","smsCode":null,"smsText":null,"receivedAt":"2026-09-06T01:00:00Z","type":"call","phoneFrom":"","service":"go"}
	]}`, &got)
	otps, err := c.ListOTPs(t.Context(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/activations/42/otp" {
		t.Fatalf("path = %s", got.path)
	}
	if len(otps) != 2 || otps[0].Code != "123456" || otps[0].Sender != "Google" || otps[0].Type != "sms" {
		t.Fatalf("otps = %+v", otps)
	}
	// smsCode 为 null 的那条要保留（它是一次来电类验证），码为空。
	if otps[1].Code != "" || otps[1].Type != "call" {
		t.Fatalf("null 码那条 = %+v", otps[1])
	}
}

// 历史：from/to 必填（官方 date 格式），响应带 totals 与 meta.hasMore。
func TestHistoryRequiresDateRangeAndReadsTotals(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"data":[{"id":7,"createDate":"2026-09-01 10:00:00","service":"go","country":12,"phone":"79990000000","moreCodes":"","cost":0.35,"status":6,"phoneCode":"7","currency":840}],
	  "totals":[{"sum":0.35,"successCount":1}],"meta":{"page":1,"size":20,"sort":{"id":"desc"},"total":1,"hasMore":false}}`, &got)
	page, err := c.ListHistory(t.Context(), HistoryQuery{From: "2026-09-01", To: "2026-09-06", Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"from=2026-09-01", "to=2026-09-06", "page=1", "size=20"} {
		if !strings.Contains(got.query, want) {
			t.Errorf("query %q 缺 %q", got.query, want)
		}
	}
	if len(page.Items) != 1 || page.Items[0].ID != "7" || page.Items[0].Cost != "0.35" || page.Items[0].Status != 6 {
		t.Fatalf("items = %+v", page.Items)
	}
	if page.TotalSum != "0.35" || page.SuccessCount != 1 || page.HasMore {
		t.Fatalf("totals/meta = %+v", page)
	}
	if _, err := c.ListHistory(t.Context(), HistoryQuery{}); err == nil {
		t.Fatal("缺 from/to 必须本地拒绝")
	}
}

// 延长历史与自定义时长目录。
func TestProlongHistoryAndCustomDurations(t *testing.T) {
	c := serveJSON(t, 200, `{"data":[{"duration":{"value":4,"unit":"hour"},"price":0.5,"createdAt":"2026-09-05T00:00:00Z"}]}`, nil)
	hist, err := c.GetProlongHistory(t.Context(), "42")
	if err != nil || len(hist) != 1 || hist[0].Duration != 4 || hist[0].Unit != "hour" || hist[0].Price != "0.5" {
		t.Fatalf("hist = %+v err = %v", hist, err)
	}

	c = serveJSON(t, 200, `{"go":{"12":72},"tg":{"0":24}}`, nil)
	durations, err := c.GetCustomDurations(t.Context())
	if err != nil || durations["go"]["12"] != 72 || durations["tg"]["0"] != 24 {
		t.Fatalf("durations = %+v err = %v", durations, err)
	}
}

// 统计：date 必填。
func TestStatsRequiresDate(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"data":{"12":{"go":{"count":3,"sum":1.05}}}}`, &got)
	stats, err := c.GetStats(t.Context(), "2026-09-06")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.query, "date=2026-09-06") || got.path != "/activations/stats" {
		t.Fatalf("req = %+v", got)
	}
	if len(stats) == 0 {
		t.Fatal("stats 为空")
	}
	if _, err := c.GetStats(t.Context(), ""); err == nil {
		t.Fatal("缺 date 必须本地拒绝")
	}
}

// offers 的 verificationType 是路径参数，只认 sms / call。
func TestOffersSupportCallVerificationType(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"data":{"go":{"12":{"prices":{"default":0.4,"retail":0.5,"min":0.3},"counts":{"total":10}}}}}`, &got)
	if _, err := c.ListOffersByType(t.Context(), "call", "", ""); err != nil {
		t.Fatal(err)
	}
	if got.path != "/activations/offers/call" {
		t.Fatalf("path = %s", got.path)
	}
	if _, err := c.ListOffersByType(t.Context(), "voice", "", ""); err == nil {
		t.Fatal("未知的 verificationType 必须本地拒绝")
	}
}

// 购买可以指定 verificationType 与 fixedPrice，二者都要进请求体。
func TestPurchaseSendsVerificationTypeAndFixedPrice(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"data":[{"id":1,"status":1,"phone":"79990000000","service":"go","country":12,"countryPhoneCode":7,"operator":"any","price":0.4,"createdAt":"2026-09-06T00:00:00Z","expiredAt":"2026-09-06T00:20:00Z","verificationType":"call","subtype":1,"otpList":[]}]}`, &got)
	acts, err := c.PurchaseActivations(t.Context(), PurchaseInput{
		Service: "go", Country: 12, Amount: 1, MaxPrice: "0.40", FixedPrice: true, VerificationType: "call",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["verificationType"] != "call" || body["fixedPrice"] != true {
		t.Fatalf("body = %v", body)
	}
	if acts[0].Operator != "any" || acts[0].VerificationType != "call" || acts[0].CountryPhoneCode != 7 {
		t.Fatalf("activation 没带上官方新字段: %+v", acts[0])
	}
}

// 收藏：PUT 带 operator，DELETE 无体；路径里的 service/country 都要转义。
func TestFavorites(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"data":{"id":5,"service":"go","country":12,"countryName":"Russia","serviceName":"Google","operator":"any","isFavorite":true}}`, &got)
	fav, err := c.SetFavorite(t.Context(), "go", 12, "any")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPut || got.path != "/activations/favorites/services/go/countries/12" || !strings.Contains(got.body, `"operator":"any"`) {
		t.Fatalf("req = %+v", got)
	}
	if !fav.IsFavorite || fav.ServiceName != "Google" {
		t.Fatalf("fav = %+v", fav)
	}
	c = serveJSON(t, 204, ``, &got)
	if err := c.RemoveFavorite(t.Context(), "go", 12); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodDelete {
		t.Fatalf("method = %s", got.method)
	}
}

// 邮箱接码：购买回 201，状态枚举 WAIT/CANCEL/SUCCESS，批量最多 10。
func TestEmailsLifecycle(t *testing.T) {
	var got capture
	c := serveJSON(t, 201, `{"data":{"id":9,"site":"example.com","email":"a1@mail.tld","status":"WAIT","value":null,"cost":0.2,"currency":840,"date":"2026-09-06T00:00:00Z","message":null}}`, &got)
	em, err := c.PurchaseEmail(t.Context(), "example.com", "mail.tld")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/emails" || !strings.Contains(got.body, `"site":"example.com"`) {
		t.Fatalf("req = %+v", got)
	}
	if em.ID != "9" || em.Email != "a1@mail.tld" || em.Status != "WAIT" || em.Cost != "0.2" || em.Currency != 840 {
		t.Fatalf("email = %+v", em)
	}

	if _, err := c.PurchaseEmailBatch(t.Context(), "example.com", "mail.tld", 11, ""); err == nil {
		t.Fatal("批量超过 10 必须本地拒绝")
	}

	c = serveJSON(t, 200, `{"data":[{"name":"mail.tld","cost":0.2,"count":100}]}`, &got)
	domains, err := c.ListEmailDomains(t.Context(), "example.com")
	if err != nil || len(domains) != 1 || domains[0].Name != "mail.tld" || domains[0].Cost != "0.2" || domains[0].Count != 100 {
		t.Fatalf("domains = %+v err = %v", domains, err)
	}
	if !strings.Contains(got.query, "site=example.com") {
		t.Fatalf("query = %q", got.query)
	}

	c = serveJSON(t, 204, ``, &got)
	if err := c.CancelEmail(t.Context(), "9"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodDelete || got.path != "/emails/9" {
		t.Fatalf("req = %+v", got)
	}
}

// 兼容层：getBalance 回的是 `ACCESS_BALANCE:<amount>` 这样的字符串，
// 密钥走 query 的 api_key（这是官方为兼容老软件保留的形态，与现代层不同）。
func TestLegacyBalanceParsesAccessBalance(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `ACCESS_BALANCE:12.50`, &got)
	balance, err := c.GetBalance(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if balance != "12.50" {
		t.Fatalf("balance = %q", balance)
	}
	if !strings.HasPrefix(got.path, "/stubs/handler_api.php") || !strings.Contains(got.query, "action=getBalance") {
		t.Fatalf("req = %+v", got)
	}
	// 兼容层的密钥在 query 里（官方 apiKeyQuery），**绝不能落进日志**——
	// 这里只验证它确实按官方方式发出去了。
	if !strings.Contains(got.query, "api_key=test-key") {
		t.Fatalf("兼容层要带 api_key query: %q", got.query)
	}

	// 非预期形态（比如 BAD_KEY）要报错而不是解出一个空余额。
	c = serveJSON(t, 200, `BAD_KEY`, nil)
	if _, err := c.GetBalance(t.Context()); err == nil {
		t.Fatal("BAD_KEY 必须报错")
	}
}

// 兼容层目录：国家、服务、运营商、价格。
func TestLegacyCatalogs(t *testing.T) {
	c := serveJSON(t, 200, `[{"id":12,"rus":"Россия","eng":"Russia","chn":"俄罗斯","visible":1,"retry":1},{"id":0,"rus":"","eng":"Hidden","chn":"","visible":0,"retry":0}]`, nil)
	countries, err := c.GetCountries(t.Context())
	if err != nil || len(countries) != 2 || countries[0].ID != 12 || countries[0].NameCN != "俄罗斯" || countries[1].Visible {
		t.Fatalf("countries = %+v err = %v", countries, err)
	}

	var got capture
	c = serveJSON(t, 200, `[{"code":"go","name":"Google"},{"code":"tg","name":"Telegram"}]`, &got)
	services, err := c.GetServicesList(t.Context(), 12, "cn")
	if err != nil || len(services) != 2 || services[1].Code != "tg" {
		t.Fatalf("services = %+v err = %v", services, err)
	}
	if !strings.Contains(got.query, "country=12") || !strings.Contains(got.query, "lang=cn") {
		t.Fatalf("query = %q", got.query)
	}

	c = serveJSON(t, 200, `{"status":"success","countryOperators":{"12":["mts","beeline"]}}`, nil)
	operators, err := c.GetOperators(t.Context(), 12)
	if err != nil || len(operators["12"]) != 2 {
		t.Fatalf("operators = %+v err = %v", operators, err)
	}

	c = serveJSON(t, 200, `{"12":{"go":{"cost":0.35,"count":120,"physicalCount":80}}}`, nil)
	prices, err := c.GetPrices(t.Context(), "go", 12)
	if err != nil || prices["12"]["go"].Cost != "0.35" || prices["12"]["go"].Count != 120 {
		t.Fatalf("prices = %+v err = %v", prices, err)
	}
}

// 租用：getRentNumber 花钱；duration 必填（小时）。
func TestLegacyRentNumberRequiresDuration(t *testing.T) {
	var got capture
	c := serveJSON(t, 200, `{"activationId":"555","serviceCode":"go","phoneNumber":"79990000001","activationCost":1.5,"currency":840,"countryCode":12,"countryPhoneCode":7,"canGetAnotherSms":true,"activationTime":"2026-09-06T00:00:00Z","activationEndTime":"2026-09-06T04:00:00Z","activationOperator":"mts","verificationType":"sms","subtype":2,"status":1}`, &got)
	act, err := c.GetRentNumber(t.Context(), RentInput{Service: "go", Country: 12, DurationHours: 4, Operator: "mts"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"action=getRentNumber", "service=go", "country=12", "duration=4", "operator=mts"} {
		if !strings.Contains(got.query, want) {
			t.Errorf("query %q 缺 %q", got.query, want)
		}
	}
	if act.ID != "555" || act.Phone != "79990000001" || act.Subtype != 2 || act.Price != "1.5" || act.ExpiresAt != "2026-09-06T04:00:00Z" {
		t.Fatalf("act = %+v", act)
	}
	if _, err := c.GetRentNumber(t.Context(), RentInput{Service: "go", Country: 12}); err == nil {
		t.Fatal("缺 duration 必须本地拒绝")
	}
}

// 兼容层的错误体是 {title,details,info}，同样按 4xx/5xx 分档。
func TestLegacyErrorsSplitLikeModern(t *testing.T) {
	c := serveJSON(t, 402, `{"title":"NO_BALANCE","details":"x","info":{}}`, nil)
	_, err := c.GetBalance(t.Context())
	var rejected *RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("402 应为 RejectedError, got %T %v", err, err)
	}
}

// 生产实测：custom-durations 外面包了一层 data（官方 schema 没写）。
// 两种形状都要认；按 schema 解会把 "data" 当成服务名。
func TestCustomDurationsUnwrapsDataEnvelope(t *testing.T) {
	c := serveJSON(t, 200, `{"data":{"md":{"0":72},"tg":{"12":24}}}`, nil)
	got, err := c.GetCustomDurations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got["md"]["0"] != 72 || got["tg"]["12"] != 24 {
		t.Fatalf("got = %v", got)
	}
	if _, has := got["data"]; has {
		t.Fatal("data 不该被当成一个服务")
	}
}

// 生产实测：不带筛选的 getPrices 超过 1 MiB。兼容层的上限要比现代层高。
func TestLegacyAcceptsMultiMegabytePriceList(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			sb.WriteString(`,`)
		}
		sb.WriteString(fmt.Sprintf(`"%d":{"go":{"cost":0.35,"count":%d,"physicalCount":1}}`, i, i))
	}
	sb.WriteString(`}`)
	body := sb.String()
	if len(body) < 1<<20 {
		t.Fatalf("夹具不够大：%d 字节", len(body))
	}
	c := serveJSON(t, 200, body, nil)
	prices, err := c.GetPrices(t.Context(), "", 0)
	if err != nil {
		t.Fatalf("大响应体必须能解: %v", err)
	}
	if len(prices) != 20000 {
		t.Fatalf("解出 %d 个国家", len(prices))
	}
}
