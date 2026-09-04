package infini

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 提现带**真正的幂等键**：request_id 是 UUID，重投返回原结果并标 is_duplicate。
// 卡 API 完全没有这个能力（我们靠 card_alias 硬凑），所以提现的不确定态
// 反而比开卡好处理——超时后按 request_id 查状态就有答案。
func TestWithdrawSendsRequestIDAndReadsDuplicate(t *testing.T) {
	var got map[string]any
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/funds/withdraw" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"request_id": got["request_id"], "status": "pending", "is_duplicate": true,
		}})
	})

	res, err := c.Withdraw(context.Background(), WithdrawRequest{
		RequestID: "e94b4e88-36c2-4550-907e-839742cf5fae",
		Chain:     "TRON", TokenType: "USDT", Amount: "10.00",
		WalletAddress: "TXYZ", Note: "月度提现",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["request_id"] != "e94b4e88-36c2-4550-907e-839742cf5fae" {
		t.Fatalf("必须把幂等键发出去: %v", got)
	}
	if !res.IsDuplicate || res.Status != "pending" {
		t.Fatalf("重投标记与状态要读出来: %+v", res)
	}
}

// 空幂等键必须本地拒绝。
//
// 文档说 request_id 可省略、由上游生成——但那样**重试就没有幂等保障**了。
// 提现不可逆，一次没有幂等键的重试可能就是转两次。
func TestWithdrawRefusesEmptyRequestID(t *testing.T) {
	called := false
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })

	_, err := c.Withdraw(context.Background(), WithdrawRequest{
		Chain: "TRON", TokenType: "USDT", Amount: "1", WalletAddress: "TXYZ",
	})
	if err == nil {
		t.Fatal("没有幂等键必须拒绝——上游会替我们生成一个，那等于放弃重试保护")
	}
	if called {
		t.Fatal("不该打到上游")
	}
}

// 查状态：GET 带查询串，签名路径必须含它（文档明写）。
func TestWithdrawStatusReadsChainAndFees(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("request_id") == "" {
			t.Error("应带 request_id")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"request_id": "r-1", "status": "completed", "amount": "11",
			"actual_amount": "10.9", "transaction_hash": "0xabc",
			"chain": "ETHEREUM", "token_type": "USDT",
			"gas_fee": "0.08", "gas_fee_currency": "USD",
			"fx_fee": "0.02", "fx_fee_currency": "USD",
		}})
	})

	st, err := c.WithdrawStatus(context.Background(), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "completed" || st.TransactionHash != "0xabc" {
		t.Fatalf("状态解析错: %+v", st)
	}
	// 手续费分 gas 与 fx 两项：跨币种提现两者都有，只显示一个数会让人
	// 对不上账。
	if st.GasFee != "0.08" || st.FXFee != "0.02" {
		t.Fatalf("两项手续费都要读出来: %+v", st)
	}
	if st.ActualAmount != "10.9" {
		t.Fatalf("到账金额与扣款金额是两个数: %+v", st)
	}
}

// 手续费表：开提现表单前要能告诉人「这条链收多少」。
func TestWithdrawFeesListsChainTokenPairs(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"fees": []map[string]any{
				{"chain": "TRON", "token_type": "USDT", "withdraw_fee": "1"},
				{"chain": "ETHEREUM", "token_type": "USDT", "withdraw_fee": "2"},
			},
		}})
	})

	fees, err := c.WithdrawFees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fees) != 2 || fees[0].Chain != "TRON" || fees[0].WithdrawFee != "1" {
		t.Fatalf("手续费表解析错: %+v", fees)
	}
}

// 上游用 HTTP 200 + 非零 code 表示业务失败（余额不足这类），必须报错。
func TestWithdrawTreatsBusinessCodeAsError(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 30003, "message": "insufficient available balance", "data": nil,
		})
	})

	_, err := c.Withdraw(context.Background(), WithdrawRequest{
		RequestID: "r-1", Chain: "TRON", TokenType: "USDT",
		Amount: "1000000", WalletAddress: "TXYZ",
	})
	if err == nil {
		t.Fatal("余额不足必须报错")
	}
	// 30003 是「上游明确拒绝」，钱确定没出去——归 rejected 而不是 unavailable。
	if connector.KindOf(err) != connector.KindRejected {
		t.Fatalf("分类 = %q, want rejected", connector.KindOf(err))
	}
}
