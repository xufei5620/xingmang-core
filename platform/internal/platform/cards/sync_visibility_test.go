package cards

// XM-CARD-VISIBILITY 的领域层用例：上游原话可见、rejected 不重试、
// 部分成功可见、批量上限只钉一处、按账号暂停。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// realClientAgainst 造一个**真的** infini.Client 指向假上游。
//
// 不用 infini.Fake：本片要验的正是 do() 的信封解析路径——上游 200 + 非零 code
// 被解成什么、message 有没有被带出来。用替身直接编一个 connector.Error，
// 判定就成了「测试自己造的分类」，穿不过真正要验的那段代码
// （记忆：规则存在≠调用得到）。
func realClientAgainst(t *testing.T, h http.HandlerFunc) *infini.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	const keyRef = "secret://infini-test/key-id"
	const secretRef = "secret://infini-test/api-secret"
	// 凭据经 CredentialRef + 注入的 lookup 取得：不碰真实环境变量，
	// 也不让任何明文出现在结构体字段或错误文本里（宪法条款 7）。
	provider, err := secrets.NewEnvProvider(
		map[string]string{keyRef: "XM_TEST_INFINI_KEY_ID", secretRef: "XM_TEST_INFINI_SECRET"},
		secrets.WithLookup(func(name string) (string, bool) {
			switch name {
			case "XM_TEST_INFINI_KEY_ID":
				return "testkey", true
			case "XM_TEST_INFINI_SECRET":
				return "testsecret", true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("造 secrets provider: %v", err)
	}
	return infini.NewClient(srv.URL, provider,
		secrets.MustCredentialRef(keyRef), secrets.MustCredentialRef(secretRef),
		[]string{"127.0.0.1"})
}

// 上游 200 + 非零业务码：码与脱敏后的 message 必须一路带到作业错误串里。
//
// 这是本片的核心用例。旧实现下它必红：connector.Error.Error() 只回
// "kind: op"，message 与 code 只活在 Unwrap 链里，于是作业错误串是
// 「刷新卡状态: 账号 main 批量查状态: rejected: infini POST /v2/cards/status/batch」
// ——一个数字都没有，正是 2026-09-08 那三天里唯一能看到的东西。
func TestSyncErrorCarriesUpstreamCodeAndMessage(t *testing.T) {
	client := realClientAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":40004,"message":"card_id not in batch scope","data":null}`))
	})

	store := newMemStore()
	if err := store.UpsertCard(context.Background(), testAccount,
		infini.Card{ID: "card_1", Status: "active"}, CardAttribution{}); err != nil {
		t.Fatal(err)
	}

	round, err := newSyncer(client, store, issueNow).RunOnce(context.Background())
	if err == nil {
		t.Fatal("这一轮没有任何一步成功，应作为错误上报")
	}

	for _, want := range []string{
		"40004",                      // 上游业务码
		"card_id not in batch scope", // 上游原话（脱敏后）
		"rejected",                   // 分类没有因为带上原话而丢
		"/v2/cards/status/batch",     // 出错的操作
		"账号 " + testAccount + " 批量查状态", // 领域层的定位
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("作业错误串应含 %q，实际 %q", want, err.Error())
		}
	}

	failures := round.Failures()
	if len(failures) == 0 {
		t.Fatal("失败必须留在结果里")
	}
	if failures[0].Kind != connector.KindRejected {
		t.Fatalf("分类 = %q, want rejected（带上原话不该改判）", failures[0].Kind)
	}
	if !strings.Contains(failures[0].Detail, "40004") {
		t.Fatalf("结果里的 Detail 也要带码，实际 %q", failures[0].Detail)
	}
}

// alwaysFailClient 让某个账号的每一次上游调用都失败。
//
// infini.Fake 只有 FailNext（一次性）与 FailReveal（只管 reveal），
// 没有「这个账号每次都失败」的开关；在测试里包一层比改 fake.go 安全——
// 改 fake.go 会牵动所有用它的既有用例。
type alwaysFailClient struct {
	infini.CardClient
	err error
}

func (c *alwaysFailClient) ListCards(context.Context, infini.ListCardsQuery) (infini.CardPage, error) {
	return infini.CardPage{}, c.err
}

func (c *alwaysFailClient) BatchCardStatus(context.Context, []string) (map[string]string, error) {
	return nil, c.err
}

func (c *alwaysFailClient) CardStatus(context.Context, string) (infini.Card, error) {
	return infini.Card{}, c.err
}

func (c *alwaysFailClient) RevealCard(context.Context, string) (infini.RevealedCard, error) {
	return infini.RevealedCard{}, c.err
}

func (c *alwaysFailClient) CardTransactions(context.Context, string, int, int) (infini.TransactionPage, error) {
	return infini.TransactionPage{}, c.err
}

// rejected 全灭时整轮不可重试；unavailable 全灭时可重试。
//
// 分类**从真上游响应算出来**（200+code 40004 与 HTTP 503），不是测试自己编的
// connector.Error——中间层伪造入参会让判定恒真。
func TestRoundRetryabilityFollowsUpstreamClassification(t *testing.T) {
	cases := map[string]struct {
		handler   http.HandlerFunc
		retryable bool
		wantKind  connector.ErrorKind
	}{
		"上游明确拒绝": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"code":40004,"message":"card_id not in batch scope","data":null}`))
			},
			retryable: false,
			wantKind:  connector.KindRejected,
		},
		"上游不可用": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"code":1,"message":"upstream down"}`))
			},
			retryable: true,
			wantKind:  connector.KindUnavailable,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := realClientAgainst(t, tc.handler)
			store := newMemStore()
			if err := store.UpsertCard(context.Background(), testAccount,
				infini.Card{ID: "card_1", Status: "active"}, CardAttribution{}); err != nil {
				t.Fatal(err)
			}

			round, err := newSyncer(client, store, issueNow).RunOnce(context.Background())
			if err == nil {
				t.Fatal("整轮全失败应报错")
			}
			if !round.AllFailed() {
				t.Fatalf("这一轮没有任何一步成功: %+v", round.Steps)
			}
			if got := round.Failures()[0].Kind; got != tc.wantKind {
				t.Fatalf("分类 = %q, want %q", got, tc.wantKind)
			}
			if round.Retryable() != tc.retryable {
				t.Fatalf("Retryable() = %v, want %v", round.Retryable(), tc.retryable)
			}
		})
	}
}

// 混合：一个账号被拒、一个账号网络抖动 → 整轮仍算可重试。
//
// 只要还有一件事值得再试就别把整个作业取消掉。
func TestMixedFailuresStayRetryable(t *testing.T) {
	var res RoundResult
	res.fail("A", StepBatchStatus, connector.NewError(connector.KindRejected, "op", nil))
	res.fail("B", StepBatchStatus, connector.NewError(connector.KindUnavailable, "op", nil))
	if !res.Retryable() {
		t.Fatal("还有 unavailable 时整轮应可重试")
	}
	if !res.AllFailed() {
		t.Fatal("两条都失败且没有成功，应算整轮失败")
	}
}

// twoAccountSyncer 造一个双账号 Syncer，账号顺序固定为 backup 在前。
//
// 顺序刻意让坏账号排前面：这样「第一个账号失败即中断」这种回归会让
// 好账号的断言当场发红。
func twoAccountSyncer(store *memStore, backup, main infini.CardClient) *Syncer {
	return NewSyncer([]Account{
		{ID: "backup", Client: backup},
		{ID: "main", Client: main},
	}, store, SyncOptions{
		UnknownGrace: 30 * time.Minute,
		Now:          func() time.Time { return issueNow },
	})
}

// 一个账号坏掉不该拖垮另一个，而且部分成功要在结果里看得出来。
func TestSyncIsolatesFailingAccountAndReportsPartialSuccess(t *testing.T) {
	store := newMemStore()

	good := infini.NewFake()
	good.SeedCard(infini.Card{ID: "upstream-main", Alias: "A", Status: "active"})
	bad := &alwaysFailClient{
		CardClient: infini.NewFake(),
		err: connector.NewErrorWithDetail(connector.KindRejected,
			"infini POST /v2/cards/status/batch",
			"upstream code 40004: card_id not in batch scope", errors.New("raw")),
	}

	round, err := twoAccountSyncer(store, bad, good).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("一个账号坏掉不该让整轮报错: %v", err)
	}

	if _, ok := store.cards["upstream-main"]; !ok {
		t.Fatal("坏账号不该拖垮好账号：main 的卡应该被发现")
	}
	if got := store.cardAccount["upstream-main"]; got != "main" {
		t.Fatalf("发现的卡归属 = %q, want main", got)
	}
	if !round.AnySucceeded() {
		t.Fatal("main 成功了，AnySucceeded 应为真")
	}
	if round.AllFailed() {
		t.Fatal("部分成功不是整轮失败")
	}

	var sawBackupFailure bool
	for _, s := range round.Failures() {
		if s.Account == "backup" {
			sawBackupFailure = true
			if !strings.Contains(s.Detail, "40004") {
				t.Fatalf("坏账号的失败要带上游原话，实际 %q", s.Detail)
			}
		}
		if s.Account == "main" {
			t.Fatalf("main 不该有失败: %+v", s)
		}
	}
	if !sawBackupFailure {
		t.Fatal("backup 的失败必须留在结果里")
	}
}

// batchOnlyFailClient 只让批量查状态失败，单查照常——用来验证兜底路径。
type batchOnlyFailClient struct {
	infini.CardClient
	err         error
	statusCalls int
}

func (c *batchOnlyFailClient) BatchCardStatus(context.Context, []string) (map[string]string, error) {
	return nil, c.err
}

func (c *batchOnlyFailClient) CardStatus(ctx context.Context, id string) (infini.Card, error) {
	c.statusCalls++
	return c.CardClient.CardStatus(ctx, id)
}

// 「批量失败后退回逐张查」的既有缓解要保留，并且在结果里看得见。
//
// 2026-09-08 的记录里，卡状态其实一直在被这条兜底刷新，红的只是作业状态，
// 而没有任何地方看得出来。「批量红了但兜底接住了」与「两条都断了」的处置
// 完全不同——前者不该叫人起床。
func TestBatchFailureFallsBackToPerCardAndIsVisible(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()

	app, err := fake.ApplyCard(context.Background(), infini.ApplyCardRequest{
		ProductID: 1, TopUpAmount: "1", TokenType: "USDT",
		UserEmail: "a@b.c", HolderName: "A", Alias: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	card, err := fake.CardStatus(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCard(context.Background(), testAccount, card, CardAttribution{}); err != nil {
		t.Fatal(err)
	}

	client := &batchOnlyFailClient{
		CardClient: fake,
		err: connector.NewErrorWithDetail(connector.KindUnavailable,
			"infini POST /v2/cards/status/batch", "http 503: down", errors.New("raw")),
	}
	round, err := newSyncer(client, store, issueNow).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("兜底接住了就不该整轮报错: %v", err)
	}
	if client.statusCalls == 0 {
		t.Fatal("批量失败后必须退回逐张查")
	}

	var batchFailed, fallbackRecovered bool
	for _, s := range round.Steps {
		if s.Step == StepBatchStatus && !s.OK {
			batchFailed = true
		}
		if s.Step == StepBatchStatusFallback && s.OK && s.Recovered > 0 {
			fallbackRecovered = true
		}
	}
	if !batchFailed {
		t.Fatalf("批量失败要记进结果: %+v", round.Steps)
	}
	if !fallbackRecovered {
		t.Fatalf("兜底救回来的卡数要记进结果（否则分不清「同步坏了」和「兜底接住了」）: %+v", round.Steps)
	}
}

// chunkRecordingClient 记录每一批发出去的卡数。
type chunkRecordingClient struct {
	infini.CardClient
	chunks []int
	errs   []error
}

func (c *chunkRecordingClient) BatchCardStatus(ctx context.Context, ids []string) (map[string]string, error) {
	got, err := c.CardClient.BatchCardStatus(ctx, ids)
	if err != nil {
		c.errs = append(c.errs, err)
		return nil, err
	}
	c.chunks = append(c.chunks, len(ids))
	return got, nil
}

// 分批步长与连接器的批量上限必须是同一个数。
//
// 这条**不比较两个常量**（同源时那会恒真），而是让分批步长与本地前置拒绝
// 在同一条真实调用链上对撞：任一侧被改动都会红。
func TestBatchChunkNeverExceedsConnectorLimit(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()

	total := infini.BatchStatusMax + 1
	for i := 0; i < total; i++ {
		app, err := fake.ApplyCard(context.Background(), infini.ApplyCardRequest{
			ProductID: 1, TopUpAmount: "1", TokenType: "USDT",
			UserEmail: "a@b.c", HolderName: "A", Alias: "x",
		})
		if err != nil {
			t.Fatal(err)
		}
		card, err := fake.CardStatus(context.Background(), app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertCard(context.Background(), testAccount, card, CardAttribution{}); err != nil {
			t.Fatal(err)
		}
	}

	client := &chunkRecordingClient{CardClient: fake}
	if _, err := newSyncer(client, store, issueNow).RunOnce(context.Background()); err != nil {
		t.Fatalf("同步不该报错: %v", err)
	}

	want := []int{infini.BatchStatusMax, 1}
	if len(client.chunks) != len(want) {
		t.Fatalf("批次 = %v, want %v", client.chunks, want)
	}
	for i, n := range want {
		if client.chunks[i] != n {
			t.Fatalf("第 %d 批 = %d 张, want %d（步长要恰好顶到上限，既不超也不保守压低）",
				i+1, client.chunks[i], n)
		}
	}
	if len(client.errs) != 0 {
		t.Fatalf("不该有任何一批被本地前置拒绝: %v", client.errs)
	}
}

// rejectingBatchClient 让批量查状态每次都被上游明确拒绝，单查照常。
type rejectingBatchClient struct {
	infini.CardClient
	batchCalls int
}

func (c *rejectingBatchClient) BatchCardStatus(context.Context, []string) (map[string]string, error) {
	c.batchCalls++
	return nil, connector.NewErrorWithDetail(connector.KindRejected,
		"infini POST /v2/cards/status/batch",
		"upstream code 40004: card_id not in batch scope", errors.New("raw"))
}

// 上游明确拒绝之后，同一账号的后续批次一批都不再发。
//
// 再发 100 张不是一个不同的请求，它注定被同样地拒掉：白烧配额，
// 还在日志里多出几条看起来像上游故障的记录。
func TestRejectedBatchStopsRemainingChunksForThatAccount(t *testing.T) {
	fake := infini.NewFake()
	fake.ActivateOnApply()
	store := newMemStore()

	for i := 0; i < infini.BatchStatusMax+1; i++ {
		app, err := fake.ApplyCard(context.Background(), infini.ApplyCardRequest{
			ProductID: 1, TopUpAmount: "1", TokenType: "USDT",
			UserEmail: "a@b.c", HolderName: "A", Alias: "x",
		})
		if err != nil {
			t.Fatal(err)
		}
		card, err := fake.CardStatus(context.Background(), app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertCard(context.Background(), testAccount, card, CardAttribution{}); err != nil {
			t.Fatal(err)
		}
	}

	client := &rejectingBatchClient{CardClient: fake}
	if _, err := newSyncer(client, store, issueNow).RunOnce(context.Background()); err != nil {
		t.Fatalf("兜底接住了就不该整轮报错: %v", err)
	}
	if client.batchCalls != 1 {
		t.Fatalf("上游拒绝后该账号不该继续发批次，实际发了 %d 批", client.batchCalls)
	}
}
