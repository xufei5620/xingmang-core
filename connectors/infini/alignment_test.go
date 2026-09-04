package infini

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// 对照官方 CARDS.md 补齐的三处能力（XM-CARD5）。

// 流水要采到外币原始金额与结算时间。
//
// 一张 USD 卡在欧元商户消费，amount 是折成 USD 的，transaction_amount 才是
// EUR 原值——没有它看不出跨境消费与汇率加价；settled_at 分开「已授权」与
// 「已结算」，授权可以被撤销，只有结算了的才是真正扣掉的钱。
func TestCardTransactionsCarryForeignAmountAndSettlement(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"transactions": []map[string]any{{
				"card_id": "c-1", "type": "Consume", "amount": "10.50", "fee": "0",
				"status": "Completed", "currency": "USD", "merchant": "Amazon DE",
				"transaction_time": 1786417200,
				"transaction_amount": "9.80", "transaction_currency": "EUR",
				"settled_at": 1786503600,
			}},
			"total": 1, "page": 1, "page_size": 20, "total_pages": 1,
		}})
	})

	page, err := c.CardTransactions(context.Background(), "c-1", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	tx := page.Transactions[0]
	if tx.TransactionAmount != "9.80" || tx.TransactionCurrency != "EUR" {
		t.Fatalf("外币原值没采到: %+v", tx)
	}
	if tx.SettledAt == "" {
		t.Fatalf("结算时间没采到: %+v", tx)
	}
	// 外币金额保持文本：币种可能是任何一种，逐一验证标度不现实，
	// 而这一列目前只展示、不计算。
	if tx.AmountMinor != 1050 {
		t.Fatalf("本币金额应仍按标度换成 minor units: %d", tx.AmountMinor)
	}
}

// 没有外币字段的流水（国内消费、充值）照常解析，不能因为缺字段报错。
func TestCardTransactionsToleratesMissingForeignFields(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"transactions": []map[string]any{{
				"card_id": "c-1", "type": "TopUp", "amount": "1", "fee": "0",
				"status": "Completed", "currency": "USD", "transaction_time": 1786417200,
			}},
			"total": 1, "page": 1, "page_size": 20, "total_pages": 1,
		}})
	})
	page, err := c.CardTransactions(context.Background(), "c-1", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.Transactions[0].TransactionCurrency != "" || page.Transactions[0].SettledAt != "" {
		t.Fatalf("缺失字段应为空而不是编造: %+v", page.Transactions[0])
	}
}

// 批量状态：一次拿回多张卡的状态，结果按请求顺序。
//
// 同步作业此前每张卡一次调用；卡一多就是几十次/轮，而上游限流阈值未知。
// 文档：POST /v2/cards/status/batch，1~100 张。
func TestBatchCardStatusPostsIDsAndKeepsOrder(t *testing.T) {
	var gotBody map[string]any
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/cards/status/batch" {
			t.Errorf("路径/方法不对: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"cards": []map[string]any{
				{"card_id": "c-2", "status": "pending"},
				{"card_id": "c-1", "status": "active"},
			},
		}})
	})

	got, err := c.BatchCardStatus(context.Background(), []string{"c-2", "c-1"})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := gotBody["card_ids"].([]any)
	if len(ids) != 2 || ids[0] != "c-2" {
		t.Fatalf("请求体应带 card_ids 且保序: %v", gotBody)
	}
	if got["c-1"] != "active" || got["c-2"] != "pending" {
		t.Fatalf("状态映射错: %v", got)
	}
}

// 超过 100 张必须在本地拒绝，不能把一个注定被拒的请求打到上游。
func TestBatchCardStatusRejectsOver100Locally(t *testing.T) {
	called := false
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })

	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "c"
	}
	if _, err := c.BatchCardStatus(context.Background(), ids); err == nil {
		t.Fatal("101 张应被本地拒绝")
	}
	if called {
		t.Fatal("不该打到上游")
	}
	// 空清单同样本地处理，不发请求。
	if got, err := c.BatchCardStatus(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("空清单应返回空映射: %v %v", got, err)
	}
	if called {
		t.Fatal("空清单不该打到上游")
	}
}

// 账户可用余额：GET /v2/funds/balances，三个币种都是十进制文本。
func TestAccountBalancesReadsThreeCurrencies(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/funds/balances" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"available_balance_usdt": "55.68",
			"available_balance_usdc": "0",
			"available_balance_usd":  "12.00",
		}})
	})

	b, err := c.AccountBalances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b.USDT != "55.68" || b.USDC != "0" || b.USD != "12.00" {
		t.Fatalf("余额解析错: %+v", b)
	}
}

// 替身也得实现这两个新方法，且行为与真实客户端同构。
func TestFakeSupportsBatchStatusAndBalances(t *testing.T) {
	f := NewFake()
	app, err := f.ApplyCard(context.Background(), ApplyCardRequest{
		ProductID: 1, TopUpAmount: "1", TokenType: "USDT",
		UserEmail: "a@b.c", HolderName: "A", Alias: "x-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.BatchCardStatus(context.Background(), []string{app.ID, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if got[app.ID] == "" {
		t.Fatalf("已开的卡应有状态: %v", got)
	}
	if _, ok := got["missing"]; ok {
		t.Fatal("不存在的卡不该出现在结果里")
	}
	if _, err := f.AccountBalances(context.Background()); err != nil {
		t.Fatal(err)
	}
}
