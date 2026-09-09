package jobs

// XM-CARD-VISIBILITY 的作业层用例：上游原话进日志、rejected 不烧重试、
// 每轮结果进 ops 观测。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// stubSyncStore 是 cards.SyncStore 的最小空壳：只有 TrackedCards 回一张卡，
// 其余全部返回空。
//
// 写在 jobs 包里而不是给生产代码加一个接口：CardSyncWorker 持有的是具体类型
// *cards.Syncer，为了测试去改生产代码的形状不划算。
type stubSyncStore struct {
	tracked []cards.CardRef
	paused  map[string]cards.AccountSyncPause
}

func (s *stubSyncStore) BeginOperation(context.Context, cards.Operation) (cards.Operation, bool, error) {
	return cards.Operation{}, false, nil
}
func (s *stubSyncStore) ResolveOperation(context.Context, cards.Operation) error { return nil }
func (s *stubSyncStore) SpentToday(context.Context, string, string, time.Time) (string, error) {
	return "0", nil
}
func (s *stubSyncStore) UpsertCard(context.Context, string, infini.Card, cards.CardAttribution) error {
	return nil
}
func (s *stubSyncStore) SetCardUsage(context.Context, cards.CardUsage) error { return nil }
func (s *stubSyncStore) StoreCardSecrets(context.Context, string, string, infini.RevealedCard) error {
	return nil
}
func (s *stubSyncStore) SetAccountSyncPause(context.Context, cards.AccountSyncPause) error {
	return nil
}
func (s *stubSyncStore) PausedAccounts(context.Context) (map[string]cards.AccountSyncPause, error) {
	return s.paused, nil
}
func (s *stubSyncStore) UnresolvedOperations(context.Context) ([]cards.Operation, error) {
	return nil, nil
}
func (s *stubSyncStore) CardStatusOf(context.Context, string, string) (string, error) {
	return "", errors.New("投影里没有")
}
func (s *stubSyncStore) TrackedCards(context.Context) ([]cards.CardRef, error) {
	return s.tracked, nil
}
func (s *stubSyncStore) UpsertTransactions(context.Context, string, string, []infini.CardTransaction) error {
	return nil
}
func (s *stubSyncStore) CardsMissingSecrets(context.Context) ([]cards.CardRef, error) {
	return nil, nil
}

const cardSyncTestAccount = "LINFENG"

// cardSyncWorkerAgainst 把 worker 接到一个假上游上。
//
// 走真的 infini.Client 而不是替身错误：本片要验的是「分类与原话确实是从上游
// 响应算出来的」，测试自己编一个 connector.Error 会让判定恒真
// （记忆：规则存在≠调用得到）。
func cardSyncWorkerAgainst(t *testing.T, h http.HandlerFunc) (*CardSyncWorker, *bytes.Buffer, *recordingObservations) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	const keyRef = "secret://infini-test/key-id"
	const secretRef = "secret://infini-test/api-secret"
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
	client := infini.NewClient(srv.URL, provider,
		secrets.MustCredentialRef(keyRef), secrets.MustCredentialRef(secretRef),
		[]string{"127.0.0.1"})

	store := &stubSyncStore{tracked: []cards.CardRef{{Account: cardSyncTestAccount, CardID: "card_1"}}}
	syncer := cards.NewSyncer(
		[]cards.Account{{ID: cardSyncTestAccount, Client: client}}, store, cards.SyncOptions{})

	var buf bytes.Buffer
	obs := &recordingObservations{}
	w := NewCardSyncWorker(slog.New(slog.NewJSONHandler(&buf, nil)), syncer).
		WithObservations(obs, "production", DefaultCardSyncInterval).
		WithClock(func() time.Time { return time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC) })
	return w, &buf, obs
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("日志不是 JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// 上游的业务码与脱敏后的 message 必须进作业日志。
//
// 旧实现下这条必红：card_sync.go 记的是 slog.String("error", err.Error())，
// 而当时的 err.Error() 是「rejected: infini POST /v2/cards/status/batch」——
// 一句不含任何上游信息的话。这就是「日志与后台都看不到」的落点。
func TestCardSyncWorkerLogsUpstreamReason(t *testing.T) {
	w, buf, _ := cardSyncWorkerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":40004,"message":"card_id not in batch scope","data":null}`))
	})

	_ = w.Work(context.Background(), &river.Job[CardSyncArgs]{})

	lines := logLines(t, buf)
	if len(lines) == 0 {
		t.Fatal("同步失败必须留下日志")
	}
	var found map[string]any
	for _, m := range lines {
		if m["msg"] == "卡片同步未完全成功" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("没找到既有的那条日志: %+v", lines)
	}
	if found["job_kind"] != CardSyncJobKind {
		t.Fatalf("既有结构化字段丢了: %+v", found)
	}
	text, _ := found["error"].(string)
	for _, want := range []string{"40004", "card_id not in batch scope"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志的 error 字段应含 %q，实际 %q", want, text)
		}
	}
	if got, _ := found["first_failure_account"].(string); got != cardSyncTestAccount {
		t.Fatalf("日志要说清是哪个账号，实际 %q", got)
	}
	if got, _ := found["first_failure_step"].(string); got == "" {
		t.Fatal("日志要说清是哪一步")
	}
	if got, _ := found["first_failure_kind"].(string); got != "rejected" {
		t.Fatalf("日志要说清分类，实际 %q", got)
	}
	if detail, _ := found["first_failure_detail"].(string); !strings.Contains(detail, "40004") {
		t.Fatalf("日志的失败简述要带上游码，实际 %q", detail)
	}
	// 整轮的按账号/步骤结果也要在同一条记录里：只有第一条失败的话，
	// 「批量红了但兜底接住了」这种形状仍然看不出来。
	round, _ := json.Marshal(found["round"])
	if !strings.Contains(string(round), cards.StepBatchStatus) {
		t.Fatalf("日志的 round 字段应含批量查状态那一步，实际 %s", round)
	}
}

// rejected 全灭 → JobCancel，不烧掉剩下的两次尝试；但错误照样返回。
func TestCardSyncWorkerCancelsInsteadOfBurningRetriesOnRejected(t *testing.T) {
	w, _, _ := cardSyncWorkerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":40004,"message":"card_id not in batch scope","data":null}`))
	})

	err := w.Work(context.Background(), &river.Job[CardSyncArgs]{})
	if err == nil {
		t.Fatal("不重试不等于静默成功，错误必须照样返回")
	}
	var cancel *rivertype.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("rejected 全灭时应终结本轮（%T: %v），而不是烧满 %d 次",
			err, err, cardSyncMaxAttempts)
	}
	if !strings.Contains(err.Error(), "40004") {
		t.Fatalf("终结的错误里也要带上游码: %v", err)
	}
}

// unavailable 全灭 → 照常重试。
func TestCardSyncWorkerStillRetriesOnUnavailable(t *testing.T) {
	w, _, _ := cardSyncWorkerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"code":1,"message":"upstream down"}`))
	})

	err := w.Work(context.Background(), &river.Job[CardSyncArgs]{})
	if err == nil {
		t.Fatal("整轮失败应报错")
	}
	var cancel *rivertype.JobCancelError
	if errors.As(err, &cancel) {
		t.Fatal("unavailable 值得重试，不该被终结")
	}
}

// 每一轮都要写一条 cards.sync.status 观测，且 value_json 里带着账号/步骤明细。
//
// 这条观测是 alerts 的 cards.sync.failed 的**唯一输入**。少了它，
// 「部分成功返回 nil」就把一个吵闹但真实的信号换成了一个安静的盲区。
func TestCardSyncWorkerWritesObservationWithPerAccountDetail(t *testing.T) {
	w, _, obs := cardSyncWorkerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":40004,"message":"card_id not in batch scope","data":null}`))
	})

	_ = w.Work(context.Background(), &river.Job[CardSyncArgs]{})

	if len(obs.written) != 1 {
		t.Fatalf("应写一条观测，实际 %d 条", len(obs.written))
	}
	o := obs.written[0]
	if o.MetricKey != MetricCardSyncStatus {
		t.Fatalf("指标键 = %q, want %q", o.MetricKey, MetricCardSyncStatus)
	}
	if o.Environment != "production" {
		t.Fatalf("环境 = %q", o.Environment)
	}
	if o.Status != ops.SyncFailed {
		t.Fatalf("整轮全失败时观测应记 failed, got %q", o.Status)
	}
	if o.LastErrorCode != "rejected" {
		t.Fatalf("last_error_code = %q, want rejected（分类而不是自由文本）", o.LastErrorCode)
	}

	accounts, ok := o.Value["accounts"].([]any)
	if !ok || len(accounts) == 0 {
		t.Fatalf("value_json 里要有按账号的明细: %+v", o.Value)
	}
	raw, _ := json.Marshal(o.Value)
	for _, want := range []string{cardSyncTestAccount, cards.StepBatchStatus, "40004"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("value_json 应含 %q，实际 %s", want, raw)
		}
	}
	// value_json 会被管理端读到并长期留在样本表里：卡号一个字节都不许进。
	if strings.Contains(string(raw), "5332281234561234") {
		t.Fatalf("value_json 里出现了卡号: %s", raw)
	}
}

// 部分成功不返回错误，但观测要标 partial——「不完整」这件事本身要可见。
func TestCardSyncWorkerPartialSuccessIsNotAJobFailure(t *testing.T) {
	// 让批量查状态失败、单查成功：兜底接住了，整轮是部分成功。
	var batchHit bool
	w, buf, obs := cardSyncWorkerAgainst(t, func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status/batch") {
			batchHit = true
			rw.Write([]byte(`{"code":40004,"message":"card_id not in batch scope","data":null}`))
			return
		}
		rw.Write([]byte(`{"code":0,"message":"ok","data":{"id":"card_1","status":"active","card_last_four":"1234"}}`))
	})

	if err := w.Work(context.Background(), &river.Job[CardSyncArgs]{}); err != nil {
		t.Fatalf("兜底接住了就不该让作业失败: %v", err)
	}
	if !batchHit {
		t.Fatal("批量接口没被打到，这条用例没验到它想验的东西")
	}
	if len(obs.written) != 1 {
		t.Fatalf("应写一条观测，实际 %d 条", len(obs.written))
	}
	o := obs.written[0]
	if o.Status != ops.SyncOK {
		t.Fatalf("部分成功时观测应记 ok, got %q", o.Status)
	}
	if !o.IsPartial {
		t.Fatal("有失败就要标 partial，否则一个绿点会把不完整盖过去")
	}

	var sawWarn bool
	for _, m := range logLines(t, buf) {
		if m["msg"] == "卡片同步部分成功" {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatal("部分成功也要留一条日志，不能静默")
	}
}

// 装配必须把观测装上——这条约束此前只活在一句注释里。
//
// 本片做了一次有意的信号让渡：部分成功返回 nil、全砸且不值得再试落
// cancelled，作业不再变红。于是「某个账号一直在失败」只剩 cards.sync.status
// 这一个承载物，而 cards.sync.failed 规则以它为唯一输入。把那一行
// .WithObservations(...) 删掉，全仓门禁照样全绿、告警从此结构上不可能响、
// 而且不会有任何报错——这正是「被信任的过期闸最危险」那一条：
// 约束只写在注释里，等于没写。
func TestCardSyncWorkerAssemblyWiresObservations(t *testing.T) {
	cfg := Config{
		Environment:      "production",
		CardSyncInterval: DefaultCardSyncInterval,
	}
	// pool 传 nil：这条用例问的是「装没装上」，不是「写得进库吗」。
	w := newCardSyncWorkerFor(cfg, nil)

	if w.observations == nil {
		t.Fatal("装配漏了观测：cards.sync.failed 从此不可能命中，且不会有任何报错")
	}
	if w.environment != cfg.Environment {
		t.Fatalf("环境没传进去 = %q, want %q（观测会落到错误的环境上）",
			w.environment, cfg.Environment)
	}
	if w.expectedInterval != cfg.CardSyncInterval {
		t.Fatalf("周期没传进去 = %v, want %v（新鲜度阈值会算错）",
			w.expectedInterval, cfg.CardSyncInterval)
	}
}
