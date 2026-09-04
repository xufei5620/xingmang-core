package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeCardQuerier struct {
	cards []cards.CardView
	txs   []cards.TransactionView
	ops   []cards.Operation
	err   error
}

func (f *fakeCardQuerier) ListCards(ctx context.Context, account, ownerRef string) ([]cards.CardView, error) {
	return f.cards, f.err
}

func (f *fakeCardQuerier) ListTransactions(ctx context.Context, account, cardID string, limit int) ([]cards.TransactionView, error) {
	return f.txs, f.err
}

func (f *fakeCardQuerier) OperationsNeedingAttention(ctx context.Context) ([]cards.Operation, error) {
	return f.ops, f.err
}

func cardRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "ops-1", Type: principal.TypeHuman, Issuer: "test", Environment: "development",
	}))
}

// 读端点必须带新鲜度：宪法条款 12 禁止裸数字冒充实时完整数据。
func TestListCardsIncludesFreshness(t *testing.T) {
	store := &fakeCardQuerier{cards: []cards.CardView{{
		CardID: "card_1", Mask: "533228******1234", Status: "active",
		Currency: "USD", BalanceMinor: 1234,
		LastSyncedAt: time.Now().UTC().Add(-time.Hour),
	}}}

	rec := httptest.NewRecorder()
	ListCardsHandler(store, []string{"MAIN"}, 5*time.Minute)(rec, cardRequest(t, "/api/v1/cards"))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Items []struct {
			Mask      string `json:"mask"`
			Freshness struct {
				Stale bool `json:"stale"`
			} `json:"freshness"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("应返回 1 张卡, got %d", len(body.Items))
	}
	if !body.Items[0].Freshness.Stale {
		t.Fatal("一小时前同步的数据在 5 分钟周期下应标记为陈旧")
	}
}

// 最要紧的一条：读端点永远不返回完整卡号。库里就只有掩码，
// 但这条测试钉住「以后谁都别往这个响应里加明文字段」。
func TestListCardsNeverReturnsPlaintextPAN(t *testing.T) {
	store := &fakeCardQuerier{cards: []cards.CardView{{
		CardID: "card_1", Mask: "533228******1234", Status: "active",
		LastSyncedAt: time.Now().UTC(),
	}}}

	rec := httptest.NewRecorder()
	ListCardsHandler(store, []string{"MAIN"}, 5*time.Minute)(rec, cardRequest(t, "/api/v1/cards"))

	body := rec.Body.String()
	for _, forbidden := range []string{"card_number", "cvv", "expiration", "expiry"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("读端点响应里出现了卡面字段 %q: %s", forbidden, body)
		}
	}
}

// 缺身份必须被拒，不能当成「匿名可读」。
func TestListCardsRequiresPrincipal(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cards", nil)

	ListCardsHandler(&fakeCardQuerier{}, []string{"MAIN"}, 5*time.Minute)(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("缺少身份时不该返回 200")
	}
}

// 拼错的 limit 当场 400，不静默回落到默认值——静默回落会让调用方
// 拿到一份看起来对、其实没按他要求截断的列表。
func TestListCardTransactionsRejectsBadLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	ListCardTransactionsHandler(&fakeCardQuerier{})(rec, cardRequest(t, "/api/v1/cards/card_1/transactions?account=main&limit=abc"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400（响应 %s）", rec.Code, rec.Body.String())
	}
}

// 待处置操作要明确告诉前端能不能重试：让前端按状态自己推，
// 迟早会推出一个「看起来该能重试」的不确定态。
func TestAttentionOperationsExposeRetryFlag(t *testing.T) {
	store := &fakeCardQuerier{ops: []cards.Operation{{
		IdempotencyKey: "issue-1", Kind: cards.OpIssue,
		State: cards.StateUnknown, NeedsHumanReview: true,
		StartedAt: time.Now().UTC(),
	}}}

	rec := httptest.NewRecorder()
	ListCardOperationsNeedingAttentionHandler(store)(rec, cardRequest(t, "/api/v1/cards/operations/attention"))

	var body struct {
		Items []struct {
			State        string `json:"state"`
			RetryAllowed bool   `json:"retry_allowed"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("应返回 1 条, got %d", len(body.Items))
	}
	if body.Items[0].RetryAllowed {
		t.Fatal("不确定态绝不能告诉前端可以重试")
	}
}
