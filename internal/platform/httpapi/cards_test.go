package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeCardQuerier struct {
	cards        []cards.CardView
	txs          []cards.TransactionView
	ops          []cards.Operation
	memberEmails []string
	challenges   []cards.CardChallenge
	paused       map[string]cards.AccountSyncPause
	pausedErr    error
	err          error
}

func (f *fakeCardQuerier) PausedAccounts(ctx context.Context) (map[string]cards.AccountSyncPause, error) {
	return f.paused, f.pausedErr
}

func (f *fakeCardQuerier) ListCards(ctx context.Context, account, ownerRef string) ([]cards.CardView, error) {
	return f.cards, f.err
}

func (f *fakeCardQuerier) ListTransactions(ctx context.Context, account, cardID string, limit int) ([]cards.TransactionView, error) {
	return f.txs, f.err
}

func (f *fakeCardQuerier) RecentTransactions(ctx context.Context, limit int) ([]cards.TransactionView, error) {
	return f.txs, f.err
}

func (f *fakeCardQuerier) ActiveChallenges(ctx context.Context) ([]cards.CardChallenge, error) {
	return f.challenges, f.err
}

func (f *fakeCardQuerier) KnownMemberEmails(ctx context.Context) ([]string, error) {
	return f.memberEmails, f.err
}

func (f *fakeCardQuerier) OperationsNeedingAttention(ctx context.Context) ([]cards.Operation, error) {
	return f.ops, f.err
}

func cardRequest(t *testing.T, target string, scopes ...string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "ops-1", Type: principal.TypeHuman, Issuer: "test", Environment: "development",
		Scopes: scopes,
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

// 库里没有明文时（新卡还没拉到、或本来就没落），响应里不该出现空的
// 卡面字段——omitempty 保证它们直接不出现，而不是回一串空字符串让前端
// 把「还没拉到」显示成「卡号是空的」。
func TestListCardsOmitsPlaintextFieldsWhenAbsent(t *testing.T) {
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

// 卡面明文落库之后（产品负责人 2026-09-04 决定），读端点会把卡号回给前端。
// 但**只回给持有 card.reveal 的调用方**：只有 card.read 的人看到的仍是掩码。
//
// 这是「谁能看卡号」剩下的唯一一道闸——明文进了库、变成普通读字段之后，
// 原来那条「谁在何时看了哪张卡」的审计链就不存在了。
func TestListCardsHidesPlaintextWithoutRevealScope(t *testing.T) {
	store := &fakeCardQuerier{cards: []cards.CardView{{
		CardID: "card_1", Mask: "441357******7843",
		PAN: "4413571234567843", CVV: "123", ExpiryMMYY: "1229",
		Status: "active", Currency: "USD", LastSyncedAt: time.Now().UTC(),
	}}}

	rec := httptest.NewRecorder()
	ListCardsHandler(store, []string{"MAIN"}, 5*time.Minute)(
		rec, cardRequest(t, "/api/v1/cards", cards.PermissionRead))

	body := rec.Body.String()
	if strings.Contains(body, "4413571234567843") {
		t.Fatalf("只有 card.read 的人不该看到完整卡号: %s", body)
	}
	if strings.Contains(body, "123") && strings.Contains(body, "cvv") {
		t.Fatalf("CVV 不该出现: %s", body)
	}
	if !strings.Contains(body, "441357******7843") {
		t.Fatal("应回落到掩码")
	}
}

func TestListCardsReturnsPlaintextWithRevealScope(t *testing.T) {
	store := &fakeCardQuerier{cards: []cards.CardView{{
		CardID: "card_1", Mask: "441357******7843",
		PAN: "4413571234567843", CVV: "123", ExpiryMMYY: "1229",
		Status: "active", Currency: "USD", LastSyncedAt: time.Now().UTC(),
	}}}

	rec := httptest.NewRecorder()
	ListCardsHandler(store, []string{"MAIN"}, 5*time.Minute)(
		rec, cardRequest(t, "/api/v1/cards", cards.PermissionRead, cards.PermissionReveal))

	if !strings.Contains(rec.Body.String(), "4413571234567843") {
		t.Fatalf("持有 card.reveal 应看到完整卡号: %s", rec.Body.String())
	}
}

// 跨卡流水带**卡片名称**：那是这张表的定位列，Infini 后台也按它认卡。
//
// join 不上（卡已关停、投影行没了）时为空而不是报错——钱确实花了，
// 那条流水必须还在。
func TestAllCardTransactionsCarryCardAlias(t *testing.T) {
	at := time.Date(2026, 9, 5, 5, 29, 45, 0, time.UTC)
	rec := httptest.NewRecorder()
	ListAllCardTransactionsHandler(&fakeCardQuerier{txs: []cards.TransactionView{
		{
			Account: "LINFENG", CardID: "c1", CardAlias: "Two.V",
			Type: "Consume", AmountMinor: -20000, Currency: "USD",
			Status: "Completed", Merchant: "OPENAI", OccurredAt: at,
		},
		{
			Account: "LINFENG", CardID: "gone", CardAlias: "",
			Type: "Consume", AmountMinor: -100, Currency: "USD",
			Status: "Completed", Merchant: "OLD", OccurredAt: at,
		},
	}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cards/transactions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			CardAlias  string `json:"card_alias"`
			Account    string `json:"account"`
			Merchant   string `json:"merchant"`
			OccurredAt string `json:"occurred_at"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("两条都该在, got %+v", body.Items)
	}
	if body.Items[0].CardAlias != "Two.V" || body.Items[0].Account != "LINFENG" {
		t.Fatalf("卡片名称与账号要带上: %+v", body.Items[0])
	}
	// 已关停的卡：名称为空，但流水本身在。
	if body.Items[1].CardAlias != "" || body.Items[1].Merchant != "OLD" {
		t.Fatalf("卡没了流水也得在: %+v", body.Items[1])
	}
	if body.Items[0].OccurredAt != "2026-09-05T05:29:45Z" {
		t.Fatalf("时间应为 RFC3339 UTC, got %q", body.Items[0].OccurredAt)
	}
}

// 被暂停的账号必须在卡片列表这一屏上说清楚（XM-CARD-VISIBILITY）。
//
// 暂停会让这个账号的卡数据**按设计**一直陈旧下去。宪法 12 条要求陈旧本身
// 可见，而「为什么陈旧」必须和数据在同一屏上——否则运营看到的是一整页
// 安静的旧数据，没有任何东西说明它为什么不动了。
func TestListCardsExposesPausedAccounts(t *testing.T) {
	pausedAt := time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)
	store := &fakeCardQuerier{
		cards: []cards.CardView{{CardID: "card_1", Status: "active"}},
		paused: map[string]cards.AccountSyncPause{
			"LINFENG": {
				Account: "LINFENG", Paused: true,
				Reason:   "上游拒绝待查 XM-CARD-VISIBILITY",
				PausedBy: "human:ops", PausedAt: pausedAt,
			},
		},
	}

	rec := httptest.NewRecorder()
	ListCardsHandler(store, []string{"MAIN", "LINFENG"}, 5*time.Minute)(
		rec, cardRequest(t, "/api/v1/cards"))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		PausedAccounts []struct {
			Account  string `json:"account"`
			Reason   string `json:"reason"`
			PausedBy string `json:"paused_by"`
			PausedAt string `json:"paused_at"`
		} `json:"paused_accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.PausedAccounts) != 1 {
		t.Fatalf("应有一条暂停记录，实际 %+v", body.PausedAccounts)
	}
	got := body.PausedAccounts[0]
	if got.Account != "LINFENG" {
		t.Fatalf("account = %q", got.Account)
	}
	if got.Reason == "" {
		t.Fatal("暂停理由必须回给前端：不然徽标只写「已暂停」，等于没说")
	}
	if got.PausedBy != "human:ops" {
		t.Fatalf("paused_by = %q", got.PausedBy)
	}
	// 时间一律 UTC RFC3339（宪法条款 14）。前端要靠它算「已暂停 N 小时」——
	// 一个被忘掉的暂停必须看得出来。
	if got.PausedAt != pausedAt.Format(time.RFC3339) {
		t.Fatalf("paused_at = %q, want %q", got.PausedAt, pausedAt.Format(time.RFC3339))
	}
}

// 读不到暂停清单时回空数组，绝不假装「没有账号被暂停」。
//
// 与成员邮箱同一条纪律（缺了不该让整页 500），但后果不同：邮箱缺了只是
// 下拉少几个选项，暂停徽标缺了会让一个**故意停掉**的账号看起来只是
// 「数据有点旧」。所以这里不做任何暗示。
func TestListCardsSurvivesPausedAccountsReadFailure(t *testing.T) {
	store := &fakeCardQuerier{
		cards:     []cards.CardView{{CardID: "card_1", Status: "active"}},
		pausedErr: errors.New("boom"),
	}

	rec := httptest.NewRecorder()
	ListCardsHandler(store, []string{"MAIN"}, 5*time.Minute)(
		rec, cardRequest(t, "/api/v1/cards"))

	if rec.Code != http.StatusOK {
		t.Fatalf("暂停清单读不到不该让整页失败，状态码 = %d", rec.Code)
	}
	var body struct {
		PausedAccounts []map[string]any `json:"paused_accounts"`
		Items          []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.PausedAccounts) != 0 {
		t.Fatalf("读不到时应为空数组，实际 %+v", body.PausedAccounts)
	}
	if len(body.Items) != 1 {
		t.Fatalf("卡片主体照常返回，实际 %+v", body.Items)
	}
}
