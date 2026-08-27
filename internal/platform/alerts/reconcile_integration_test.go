package alerts_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 本文件是 XM-0033 的**端到端闭环**：真实的 ops 观测 → 真实的规则评估 →
// 真实的去重落库 → 真实的 HTTP 投递。
//
// 规格 §22.2 给 Foundation-A 定的退出条件之一是「能触发一条真实告警」。
// 这里就是那条断言：往库里写一条失败的指标观测，跑一轮 Reconcile，
// 然后验证告警开出来了、Telegram 收到了、状态落成了 delivered。
//
// 单元测试用内存假货证明了规则与编排各自是对的；只有这一层能证明它们
// **接得上**——真实的 sqlc 参数、真实的时间戳精度、真实的 HTTP 往返。

const e2eEnv = "production"

// e2eBotToken 与 notify_test.go 里那个是同一形态的假 token。
// 它必须在整条链路上一个字符都不泄漏（宪法 7 条）。
const e2eBotToken = "8987654321:AAE2ETESTtokenNOTreal00000000000000000"

func e2ePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx,
		"TRUNCATE alerts.alert, alerts.alert_silence; TRUNCATE ops.metric_observation, ops.metric_observation_sample",
	); err != nil {
		t.Fatalf("清空测试表失败: %v", err)
	}
	return pool
}

// writeObservation 往真库写一条指标观测。
func writeObservation(t *testing.T, s *ops.Store, o ops.Observation) {
	t.Helper()
	if _, err := s.Upsert(context.Background(), o); err != nil {
		t.Fatalf("写观测 %s: %v", o.MetricKey, err)
	}
}

func failedObservation(now time.Time) ops.Observation {
	// 上一次成功是半小时前：与 jobs.failureObservation 的真实形态一致
	// （失败观测保住上一次成功的痕迹）。
	lastOK := now.Add(-30 * time.Minute)
	return ops.Observation{
		MetricKey:                 "sub2api.revenue.daily",
		Source:                    "sub2api-prod",
		Environment:               e2eEnv,
		ObservedAt:                &lastOK,
		LastSuccess:               &lastOK,
		SyncedAt:                  now,
		Status:                    ops.SyncFailed,
		LastErrorCode:             "upstream_unavailable",
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{"amount_minor_units": 123456, "currency": "CNY"},
	}
}

func healthyObservation(now time.Time) ops.Observation {
	fresh := now.Add(-time.Minute)
	return ops.Observation{
		MetricKey:                 "sub2api.revenue.daily",
		Source:                    "sub2api-prod",
		Environment:               e2eEnv,
		ObservedAt:                &fresh,
		LastSuccess:               &fresh,
		SyncedAt:                  now,
		Status:                    ops.SyncOK,
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{"amount_minor_units": 123456, "currency": "CNY"},
	}
}

// fakeBot 是一个可切换成功/失败的假 Telegram Bot API。
type fakeBot struct {
	mu       sync.Mutex
	requests []map[string]any
	// fail 为真时返回 401，模拟 token 被吊销。
	fail bool
	srv  *httptest.Server
}

func newFakeBot(t *testing.T) *fakeBot {
	t.Helper()
	bot := &fakeBot{}
	bot.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bot.mu.Lock()
		defer bot.mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/bot"+e2eBotToken+"/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bot.requests = append(bot.requests, body)
		if bot.fail {
			w.WriteHeader(http.StatusUnauthorized)
			// 一个会把请求路径回显进错误描述的网关：足以让「不脱敏就泄漏」显形。
			_, _ = io.WriteString(w,
				`{"ok":false,"error_code":401,"description":"Unauthorized for `+r.URL.Path+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(bot.srv.Close)
	return bot
}

func (b *fakeBot) setFail(fail bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = fail
}

func (b *fakeBot) sent() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]map[string]any, len(b.requests))
	copy(out, b.requests)
	return out
}

func e2eNotifier(t *testing.T, bot *fakeBot) alerts.Notifier {
	t.Helper()
	const ref = "secret://alerts/telegram-bot"
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref: "XM_E2E_TOKEN"},
		secrets.WithLookup(func(name string) (string, bool) {
			return e2eBotToken, name == "XM_E2E_TOKEN"
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider: %v", err)
	}
	n, err := alerts.NewTelegramNotifier(alerts.TelegramOptions{
		TokenRef: secrets.MustCredentialRef(ref),
		ChatID:   "-1009999999999",
		Secrets:  provider,
		BaseURL:  bot.srv.URL,
		Client:   &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewTelegramNotifier: %v", err)
	}
	return alerts.NewMultiNotifier(discardTestLogger(), n)
}

type e2eFixture struct {
	ops        *ops.Store
	alerts     *alerts.Store
	bot        *fakeBot
	reconciler *alerts.Reconciler
	now        time.Time
}

func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	pool := e2ePool(t)
	bot := newFakeBot(t)
	opsStore := ops.NewStore(pool)
	alertStore := alerts.NewStore(pool)
	f := &e2eFixture{
		ops:    opsStore,
		alerts: alertStore,
		bot:    bot,
		now:    time.Now().UTC().Truncate(time.Second),
	}
	f.reconciler = alerts.NewReconciler(alerts.ReconcilerOptions{
		Store:     alertStore,
		Evaluator: alerts.NewEvaluator(opsStore, alerts.RuleConfig{}),
		Notifier:  e2eNotifier(t, bot),
		Logger:    discardTestLogger(),
		// 固定时钟：让「第二轮」「第三轮」之间的时间推进是可控的，
		// 而不是靠 sleep 去等真实时钟走动。
		Now: func() time.Time { return f.now },
	})
	return f
}

func (f *e2eFixture) tick(d time.Duration) { f.now = f.now.Add(d) }

func (f *e2eFixture) reconcile(t *testing.T) alerts.Result {
	t.Helper()
	res, err := f.reconciler.Reconcile(context.Background(), e2eEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

// TestEndToEndRealAlertFiresAndDelivers 是规格 §22.2 那条退出条件
// 「能触发一条真实告警」的可执行版本。
func TestEndToEndRealAlertFiresAndDelivers(t *testing.T) {
	f := newE2EFixture(t)
	ctx := context.Background()

	// --- 第一轮：健康，一条告警都不该有 ---
	writeObservation(t, f.ops, healthyObservation(f.now))
	if res := f.reconcile(t); res.Findings != 0 || res.Opened != 0 {
		t.Fatalf("健康时不该产生告警: %+v", res)
	}
	if len(f.bot.sent()) != 0 {
		t.Fatalf("健康时不该投递任何消息，实际 %d 条", len(f.bot.sent()))
	}

	// --- 第二轮：上游挂了 ---
	f.tick(time.Minute)
	writeObservation(t, f.ops, failedObservation(f.now))
	res := f.reconcile(t)
	if res.Opened != 1 {
		t.Fatalf("应新开一条告警: %+v", res)
	}
	if res.Delivered != 1 {
		t.Fatalf("应投递一条: %+v", res)
	}

	// 库里那条告警是对的。
	active, err := f.alerts.ListActive(ctx, e2eEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("活跃告警数 = %d, want 1", len(active))
	}
	got := active[0]
	if got.RuleKey != alerts.RuleMetricSyncFailed || got.Severity != alerts.SeverityCritical {
		t.Fatalf("告警不对: rule=%s severity=%s", got.RuleKey, got.Severity)
	}
	if got.Status != alerts.StatusOpen {
		t.Fatalf("status = %s, want OPEN", got.Status)
	}
	if got.NotifyStatus != alerts.NotifyDelivered || got.NotifiedAt == nil {
		t.Fatalf("应记为已投递并带时刻: %+v", got)
	}
	if got.SourceMetricKey != "sub2api.revenue.daily" {
		t.Fatalf("source_metric_key = %q", got.SourceMetricKey)
	}

	// Telegram 真的收到了一条，内容说得清是什么事。
	sent := f.bot.sent()
	if len(sent) != 1 {
		t.Fatalf("Bot 收到 %d 条消息, want 1", len(sent))
	}
	text, _ := sent[0]["text"].(string)
	for _, want := range []string{"CRITICAL", "sub2api.revenue.daily", e2eEnv, "upstream_unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("消息缺少 %q:\n%s", want, text)
		}
	}
	if sent[0]["chat_id"] != "-1009999999999" {
		t.Fatalf("chat_id 不对: %v", sent[0]["chat_id"])
	}

	// --- 第三轮：问题还在。去重成一条，且**不重复投递** ---
	f.tick(time.Minute)
	writeObservation(t, f.ops, failedObservation(f.now))
	res = f.reconcile(t)
	if res.Opened != 0 || res.Merged != 1 {
		t.Fatalf("第三轮应合并而不是新开: %+v", res)
	}
	if res.Delivered != 0 {
		t.Fatalf("已投递过的告警不该每轮重发: %+v", res)
	}
	if len(f.bot.sent()) != 1 {
		t.Fatalf("Bot 不该收到第二条，实际 %d 条", len(f.bot.sent()))
	}

	active, err = f.alerts.ListActive(ctx, e2eEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].FireCount != 2 {
		t.Fatalf("应去重成一条且 fire_count=2，实际 %d 条 fire_count=%d",
			len(active), active[0].FireCount)
	}

	// --- 第四轮：上游恢复。告警自动 RESOLVED ---
	f.tick(time.Minute)
	writeObservation(t, f.ops, healthyObservation(f.now))
	res = f.reconcile(t)
	if res.Resolved != 1 {
		t.Fatalf("恢复后应自动解决: %+v", res)
	}
	active, err = f.alerts.ListActive(ctx, e2eEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("恢复后不该还有活跃告警: %+v", active)
	}

	// 历史留着：那是「这周炸过一次」的证据。
	recent, err := f.alerts.ListRecent(ctx, e2eEnv, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) != 1 || recent[0].Status != alerts.StatusResolved || recent[0].ResolvedAt == nil {
		t.Fatalf("已解决的历史应留下: %+v", recent)
	}
}

// TestEndToEndNotifyFailurePersistsWithoutLeakingToken 是任务门禁里
// 「Telegram 失败落库且错误无 token」那一条。
func TestEndToEndNotifyFailurePersistsWithoutLeakingToken(t *testing.T) {
	f := newE2EFixture(t)
	ctx := context.Background()
	f.bot.setFail(true)

	writeObservation(t, f.ops, failedObservation(f.now))
	res := f.reconcile(t)
	if res.Opened != 1 {
		t.Fatalf("应新开一条告警: %+v", res)
	}
	// 投递失败**不让整轮失败**：告警已经诚实落库了。
	if res.NotifyFailed != 1 || res.Delivered != 0 {
		t.Fatalf("应记一次投递失败: %+v", res)
	}

	active, err := f.alerts.ListActive(ctx, e2eEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("活跃告警数 = %d", len(active))
	}
	got := active[0]
	if got.NotifyStatus != alerts.NotifyFailed {
		t.Fatalf("notify_status = %s, want failed", got.NotifyStatus)
	}
	if got.NotifyError == "" {
		t.Fatal("失败必须带原因——库层 CHECK 也拦它")
	}
	if got.NotifiedAt != nil {
		t.Fatalf("失败不该有投递时刻: %v", got.NotifiedAt)
	}
	// 告警本身还是 OPEN：投递失败不改变告警状态。
	if got.Status != alerts.StatusOpen {
		t.Fatalf("status = %s, want OPEN", got.Status)
	}

	// **核心断言**：落库的失败原因里不能有 token 的任何一段。
	// notify_error 会经 GET /api/v1/alerts 原样回给前端并显示在列表里。
	assertNoE2EToken(t, got.NotifyError)
	// 状态码要留着——运维要靠它判断是重试就好还是配置错了。
	if !strings.Contains(got.NotifyError, "401") {
		t.Fatalf("失败原因应保留状态码以便排查: %q", got.NotifyError)
	}

	// --- 下一轮自动重试；这次通了 ---
	f.bot.setFail(false)
	f.tick(time.Minute)
	writeObservation(t, f.ops, failedObservation(f.now))
	res = f.reconcile(t)
	if res.Delivered != 1 {
		t.Fatalf("投递失败的告警下一轮应自动重试并成功: %+v", res)
	}

	after, err := f.alerts.Get(ctx, got.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.NotifyStatus != alerts.NotifyDelivered || after.NotifyError != "" {
		t.Fatalf("重试成功后应清掉失败原因: %+v", after)
	}
}

// TestEndToEndSilenceSuppressesDelivery：静默窗口内不投递，窗口过期后补投。
func TestEndToEndSilenceSuppressesDelivery(t *testing.T) {
	f := newE2EFixture(t)
	ctx := context.Background()

	// 建一个 30 分钟的窗口，覆盖这条规则。
	if _, err := f.alerts.CreateSilence(ctx, alerts.Silence{
		RuleKey: alerts.RuleMetricSyncFailed, Environment: e2eEnv,
		Reason: "上游维护窗口", StartsAt: f.now.Add(-time.Minute), EndsAt: f.now.Add(30 * time.Minute),
		CreatedBy: "staff_alice",
	}); err != nil {
		t.Fatalf("CreateSilence: %v", err)
	}

	writeObservation(t, f.ops, failedObservation(f.now))
	res := f.reconcile(t)
	if res.Opened != 1 {
		t.Fatalf("静默不阻止告警落库（只是不投递）: %+v", res)
	}
	if res.Delivered != 0 || len(f.bot.sent()) != 0 {
		t.Fatalf("窗口内不该投递: delivered=%d sent=%d", res.Delivered, len(f.bot.sent()))
	}

	active, err := f.alerts.ListActive(ctx, e2eEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].Status != alerts.StatusSilenced {
		t.Fatalf("窗口内应为 SILENCED: %+v", active)
	}

	// --- 窗口过期，条件仍成立 ---
	f.tick(31 * time.Minute)
	writeObservation(t, f.ops, failedObservation(f.now))
	res = f.reconcile(t)
	if res.Opened != 0 {
		t.Fatalf("窗口过期不该新开一条——它从头到尾就是同一个问题: %+v", res)
	}
	if res.Delivered != 1 {
		t.Fatalf("窗口过期后应补投: %+v", res)
	}

	active, err = f.alerts.ListActive(ctx, e2eEnv)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].Status != alerts.StatusOpen {
		t.Fatalf("窗口过期后应转回 OPEN: %+v", active)
	}
	if len(f.bot.sent()) != 1 {
		t.Fatalf("Bot 应恰好收到一条（窗口内那次不算）: %d", len(f.bot.sent()))
	}
}

func assertNoE2EToken(t *testing.T, s string) {
	t.Helper()
	botID, secret, _ := strings.Cut(e2eBotToken, ":")
	for _, needle := range []string{e2eBotToken, secret, botID + ":"} {
		if strings.Contains(s, needle) {
			t.Fatalf("落库文本泄漏了 Bot Token（片段 %q）:\n%s", needle, s)
		}
	}
}
