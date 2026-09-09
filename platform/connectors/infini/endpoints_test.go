package infini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// capture 记录上游收到的请求，供断言方法、路径与请求体。
type capture struct {
	method string
	path   string
	query  string
	body   []byte
}

func captureServer(t *testing.T, respJSON string) (*Client, *capture) {
	t.Helper()
	var got capture
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.body, _ = io.ReadAll(r.Body)
		w.Write([]byte(respJSON))
	})
	return c, &got
}

func TestApplyCardPostsRequiredParams(t *testing.T) {
	c, got := captureServer(t, `{"code":0,"message":"ok","data":{"id":"app_1","status":"init","total_top_up_amount":"10.00","total_fee":"1.00","total_pay_amount":"11.00"}}`)

	app, err := c.ApplyCard(context.Background(), ApplyCardRequest{
		ProductID:   1,
		TopUpAmount: "10.00",
		TokenType:   "USDT",
		UserEmail:   "ops@example.com",
		HolderName:  "ZHANG WEI",
		Alias:       "xm-ops-001",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.method != http.MethodPost || got.path != "/v2/cards/apply" {
		t.Fatalf("请求 = %s %s, want POST /v2/cards/apply", got.method, got.path)
	}

	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"product_id", "top_up_amount", "token_type", "user_email", "holder_name"} {
		if _, ok := sent[k]; !ok {
			t.Fatalf("请求体缺少必填字段 %s: %s", k, got.body)
		}
	}
	if app.ID != "app_1" || app.Status != "init" {
		t.Fatalf("申请单解析错误: %+v", app)
	}
}

// alias 是我们的幂等信标：超时后靠 /v2/cards/list?card_alias= 精确匹配来判定
// 卡到底开没开成。所以它必须真的发出去——这一条尚未对真实端点验证，
// 若上游忽略该字段，幂等方案要退回「持卡人 + 时间窗 + 金额」的模糊匹配。
func TestApplyCardSendsAliasAsIdempotencyBeacon(t *testing.T) {
	c, got := captureServer(t, `{"code":0,"message":"ok","data":{"id":"app_1","status":"init"}}`)

	if _, err := c.ApplyCard(context.Background(), ApplyCardRequest{
		ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT",
		UserEmail: "ops@example.com", HolderName: "ZHANG WEI", Alias: "xm-ops-001",
	}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got.body), `"card_alias":"xm-ops-001"`) {
		t.Fatalf("alias 必须发给上游: %s", got.body)
	}
}

// 申请单里的金额保持文本形态：token_type 是 USDT/USDC，
// 而 money.CurrencyScale 只登记了法币——最小单位小数位无从判断时，
// 宁可不换算，也不猜 2 位或 6 位。
func TestApplyCardKeepsTokenAmountsAsText(t *testing.T) {
	c, _ := captureServer(t, `{"code":0,"message":"ok","data":{"id":"app_1","status":"init","total_top_up_amount":"10.00","total_fee":"1.00","total_pay_amount":"11.00"}}`)

	app, err := c.ApplyCard(context.Background(), ApplyCardRequest{
		ProductID: 1, TopUpAmount: "10.00", TokenType: "USDT",
		UserEmail: "ops@example.com", HolderName: "ZHANG WEI",
	})
	if err != nil {
		t.Fatal(err)
	}

	if app.TotalPayAmount != "11.00" {
		t.Fatalf("TotalPayAmount = %q, want 原样文本 \"11.00\"", app.TotalPayAmount)
	}
}

func TestListCardsUsesGetWithPagination(t *testing.T) {
	c, got := captureServer(t, `{"code":0,"message":"ok","data":{"cards":[{"id":"card_1","currency":"USD","available_balance":"1.00","created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"}],"total":1,"page":1,"page_size":20,"total_pages":1}}`)

	page, err := c.ListCards(context.Background(), ListCardsQuery{Page: 1, PageSize: 20, Alias: "xm-ops-001"})
	if err != nil {
		t.Fatal(err)
	}

	if got.method != http.MethodGet || got.path != "/v2/cards/list" {
		t.Fatalf("请求 = %s %s, want GET /v2/cards/list", got.method, got.path)
	}
	if !strings.Contains(got.query, "card_alias=xm-ops-001") {
		t.Fatalf("alias 过滤没带上: %q", got.query)
	}
	if len(page.Cards) != 1 || page.Cards[0].ID != "card_1" {
		t.Fatalf("卡片列表解析错误: %+v", page)
	}
	if page.Total != 1 {
		t.Fatalf("Total = %d, want 1", page.Total)
	}
}

func TestFreezeCardPostsID(t *testing.T) {
	c, got := captureServer(t, `{"code":0,"message":"ok","data":{"success":true,"message":"ok"}}`)

	if err := c.FreezeCard(context.Background(), "card_1"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/v2/cards/freeze" {
		t.Fatalf("请求 = %s %s, want POST /v2/cards/freeze", got.method, got.path)
	}
	if !strings.Contains(string(got.body), `"id":"card_1"`) {
		t.Fatalf("请求体 = %s", got.body)
	}
}

// 上游可能返回 code=0 但 data.success=false——信封成功不等于操作成功。
func TestFreezeCardTreatsSuccessFalseAsRejected(t *testing.T) {
	c, _ := captureServer(t, `{"code":0,"message":"ok","data":{"success":false,"message":"card already frozen"}}`)

	if err := c.FreezeCard(context.Background(), "card_1"); err == nil {
		t.Fatal("data.success=false 必须报错，不能当成功")
	}
}

func TestTopUpReturnsNewBalance(t *testing.T) {
	c, got := captureServer(t, `{"code":0,"message":"ok","data":{"tx_id":"tx_1","card_balance":"21.00"}}`)

	res, err := c.TopUpCard(context.Background(), TopUpRequest{CardID: "card_1", Amount: "10.00", TokenType: "USDT", Note: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v2/cards/top-up" {
		t.Fatalf("path = %s, want /v2/cards/top-up", got.path)
	}
	if res.TxID != "tx_1" || res.CardBalance != "21.00" {
		t.Fatalf("充值结果解析错误: %+v", res)
	}
}

// reveal 的返回值带明文卡号与 CVV。这个类型必须**打印不出来**：
// 一次 %v 就足以把卡号写进日志，而日志会被归档、会被搜索。
func TestRevealedCardNeverPrintsPlaintext(t *testing.T) {
	c, _ := captureServer(t, `{"code":0,"message":"ok","data":{"card_number":"5332281234561234","cvv":"123","expiration_mmyy":"1229","card_currency":"USD"}}`)

	revealed, err := c.RevealCard(context.Background(), "card_1")
	if err != nil {
		t.Fatal(err)
	}

	if revealed.Number != "5332281234561234" {
		t.Fatalf("调用方应能拿到卡号，got %q", revealed.Number)
	}

	for _, printed := range []string{
		fmt.Sprintf("%v", revealed),
		fmt.Sprintf("%+v", revealed),
		fmt.Sprintf("%s", revealed),
		fmt.Sprintf("%#v", revealed),
	} {
		if strings.Contains(printed, "5332281234561234") {
			t.Fatalf("格式化输出泄漏了卡号: %s", printed)
		}
		if strings.Contains(printed, "123456") {
			t.Fatalf("格式化输出泄漏了卡号片段: %s", printed)
		}
	}
}

func TestRevealedCardCVVNeverPrinted(t *testing.T) {
	c, _ := captureServer(t, `{"code":0,"message":"ok","data":{"card_number":"5332281234561234","cvv":"987","expiration_mmyy":"1229","card_currency":"USD"}}`)

	revealed, err := c.RevealCard(context.Background(), "card_1")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%+v", revealed); strings.Contains(got, "987") {
		t.Fatalf("格式化输出泄漏了 CVV: %s", got)
	}
}

func TestCardTransactionsDecodesPage(t *testing.T) {
	c, got := captureServer(t, `{"code":0,"message":"ok","data":{"transactions":[{"card_id":"card_1","type":"purchase","amount":"3.50","fee":"0.10","status":"settled","currency":"USD","merchant":"OPENAI","transaction_time":"2026-09-02T08:00:00Z","created_at":"2026-09-02T08:00:00Z","updated_at":"2026-09-02T08:00:00Z"}],"total":1,"page":1,"page_size":20,"total_pages":1}}`)

	page, err := c.CardTransactions(context.Background(), "card_1", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v2/cards/transactions" {
		t.Fatalf("path = %s", got.path)
	}
	if len(page.Transactions) != 1 {
		t.Fatalf("流水条数 = %d, want 1", len(page.Transactions))
	}
	// 3.50 USD = 350 分
	if page.Transactions[0].AmountMinor != 350 {
		t.Fatalf("AmountMinor = %d, want 350", page.Transactions[0].AmountMinor)
	}
	if page.Transactions[0].Merchant != "OPENAI" {
		t.Fatalf("Merchant = %q", page.Transactions[0].Merchant)
	}
}

// BatchStatusMax 的真相源是上游自己的规格，不是我们抄下来的常量。
//
// 把两个 Go 常量合并成一个之后，Go 与契约之间仍然可以静默漂开：
// 上游把上限改成 50、我们照旧发 100，症状是每一批都被上游拒，
// 而错误分类看起来和 LINFENG 那次一模一样。所以这条闸**读**契约文件，
// 不复述里面的数字（记忆：闸的范围要发现不要手列）。
func TestBatchStatusMaxMatchesOpenAPI(t *testing.T) {
	minItems, maxItems := readBatchCardIDsBounds(t)
	if maxItems != BatchStatusMax {
		t.Fatalf("契约 maxItems = %d，BatchStatusMax = %d；两者必须一致",
			maxItems, BatchStatusMax)
	}
	// 空清单的提前返回对应契约的 minItems：契约若改成 0，那段提前返回
	// 就不再是「注定被拒的请求不发出去」而是漏掉一次合法调用。
	if minItems != 1 {
		t.Fatalf("契约 minItems = %d，与 BatchCardStatus 的空清单提前返回不符", minItems)
	}
}

// readBatchCardIDsBounds 从契约里读 card_ids 的 minItems / maxItems。
//
// 用行扫描而不是引一个 YAML 库：yaml.v3 目前只是间接依赖，为一条测试把它
// 提成直接依赖会动 go.mod，而本片与三个切片并行。扫描范围严格限定在
// BatchGetCardStatusesRequest 这一个 schema 内，读不到就 Fatal——
// 一条「找不到就跳过」的闸等于没有闸。
func readBatchCardIDsBounds(t *testing.T) (minItems, maxItems int) {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "connectors", "infini", "openapi", "card.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读契约 %s: %v", path, err)
	}

	const schemaHeader = "    BatchGetCardStatusesRequest:"
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, "\r") == schemaHeader {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("契约里找不到 %s", strings.TrimSpace(schemaHeader))
	}

	minItems, maxItems = -1, -1
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		// 下一个同级 schema 开始就停：别把别的 schema 的边界读进来。
		if trimmed != "" && !strings.HasPrefix(line, "     ") {
			break
		}
		switch {
		case strings.HasPrefix(trimmed, "minItems:"):
			minItems = mustAtoi(t, strings.TrimSpace(strings.TrimPrefix(trimmed, "minItems:")))
		case strings.HasPrefix(trimmed, "maxItems:"):
			maxItems = mustAtoi(t, strings.TrimSpace(strings.TrimPrefix(trimmed, "maxItems:")))
		}
	}
	if minItems < 0 || maxItems < 0 {
		t.Fatalf("契约里没读到 card_ids 的 minItems/maxItems（读到 %d/%d）", minItems, maxItems)
	}
	return minItems, maxItems
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("契约里的数字 %q 解析失败: %v", s, err)
	}
	return n
}
