package jobs

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// fakeAssuranceStore is an in-memory AssuranceProbeStore test double, letting
// every branch of AssuranceProbeWorker.Work be exercised without a real
// Postgres — the interface boundary (jobs.AssuranceProbeStore) exists
// precisely so this is possible.
type fakeAssuranceStore struct {
	runs  map[string]assurance.Run
	decls map[string]assurance.Declaration
	cfgs  map[string]*assurance.ConnectorProbeConfig // key: platform+"/"+environment

	recheck    assurance.GateCheck
	recheckErr error

	previousBenchmarkScore *int
	benchmarkErr           error

	results []assurance.Result

	markRunningErr, markRefusedErr, markCancelledErr, markSucceededErr, markFailedErr error
	insertResultErr                                                                   error
	getConfigErr                                                                      error
}

func newFakeAssuranceStore() *fakeAssuranceStore {
	return &fakeAssuranceStore{
		runs:  map[string]assurance.Run{},
		decls: map[string]assurance.Declaration{},
		cfgs:  map[string]*assurance.ConnectorProbeConfig{},
	}
}

func (f *fakeAssuranceStore) GetRun(_ context.Context, id string) (assurance.Run, error) {
	r, ok := f.runs[id]
	if !ok {
		return assurance.Run{}, assurance.ErrNotFound
	}
	return r, nil
}

func (f *fakeAssuranceStore) GetDeclaration(_ context.Context, id string) (assurance.Declaration, error) {
	d, ok := f.decls[id]
	if !ok {
		return assurance.Declaration{}, assurance.ErrNotFound
	}
	return d, nil
}

func (f *fakeAssuranceStore) RecheckAtExecution(context.Context, string, assurance.Declaration) (assurance.GateCheck, error) {
	return f.recheck, f.recheckErr
}

func (f *fakeAssuranceStore) MarkRunRunning(_ context.Context, id string, at time.Time) error {
	if f.markRunningErr != nil {
		return f.markRunningErr
	}
	r := f.runs[id]
	r.Status = assurance.RunStatusRunning
	r.StartedAt = &at
	f.runs[id] = r
	return nil
}

func (f *fakeAssuranceStore) MarkRunRefused(_ context.Context, id, reason string, at time.Time) error {
	if f.markRefusedErr != nil {
		return f.markRefusedErr
	}
	r := f.runs[id]
	r.Status = assurance.RunStatusRefused
	r.RefusalReason = reason
	r.FinishedAt = &at
	f.runs[id] = r
	return nil
}

func (f *fakeAssuranceStore) MarkRunCancelled(_ context.Context, id, reason string, at time.Time) error {
	if f.markCancelledErr != nil {
		return f.markCancelledErr
	}
	r := f.runs[id]
	r.Status = assurance.RunStatusCancelled
	r.RefusalReason = reason
	r.FinishedAt = &at
	f.runs[id] = r
	return nil
}

func (f *fakeAssuranceStore) MarkRunSucceeded(_ context.Context, id string, at time.Time) error {
	if f.markSucceededErr != nil {
		return f.markSucceededErr
	}
	r := f.runs[id]
	r.Status = assurance.RunStatusSucceeded
	r.FinishedAt = &at
	f.runs[id] = r
	return nil
}

func (f *fakeAssuranceStore) MarkRunFailed(_ context.Context, id string, at time.Time) error {
	if f.markFailedErr != nil {
		return f.markFailedErr
	}
	r := f.runs[id]
	r.Status = assurance.RunStatusFailed
	r.FinishedAt = &at
	f.runs[id] = r
	return nil
}

func (f *fakeAssuranceStore) InsertResult(_ context.Context, r assurance.Result) (assurance.Result, error) {
	if f.insertResultErr != nil {
		return assurance.Result{}, f.insertResultErr
	}
	r.ID = "result-" + strconv.Itoa(len(f.results))
	r.EvidenceRef = "probe-test" + strconv.Itoa(len(f.results))
	f.results = append(f.results, r)
	return r, nil
}

func (f *fakeAssuranceStore) LatestBenchmarkScore(context.Context, string, string) (*int, error) {
	return f.previousBenchmarkScore, f.benchmarkErr
}

func (f *fakeAssuranceStore) GetConnectorProbeConfig(_ context.Context, platform, environment string) (*assurance.ConnectorProbeConfig, error) {
	if f.getConfigErr != nil {
		return nil, f.getConfigErr
	}
	return f.cfgs[platform+"/"+environment], nil
}

func assuranceProbeTestJob(runID string) *river.Job[assurance.ProbeArgs] {
	return &river.Job[assurance.ProbeArgs]{
		JobRow: &rivertype.JobRow{
			ID: 501, Kind: assurance.ProbeJobKind, Queue: assurance.QueueProbe,
			Attempt: 1, MaxAttempts: 1,
		},
		Args: assurance.ProbeArgs{RunID: runID},
	}
}

func newTestAssuranceProbeWorker(store *fakeAssuranceStore, now time.Time, secretsProvider ProbeSecretResolver) *AssuranceProbeWorker {
	return NewAssuranceProbeWorker(AssuranceProbeOptions{
		Environment:  "staging",
		Store:        store,
		ProbeSecrets: secretsProvider,
		Now:          func() time.Time { return now },
	})
}

func minViableDeclaration(id string, targets []assurance.Target) assurance.Declaration {
	return assurance.Declaration{
		ID: id, Platform: "sub2api", Environment: "staging", Name: "最小可用请求",
		PromptTemplateKey: assurance.TemplateMinViableRequest, TargetHost: "api.example.test",
		Targets: targets, MaxTokens: 64, Status: assurance.DeclarationStatusActive, Version: 1,
		ExpectedShape: assurance.ExpectedShape{MinLength: 1, MaxLength: 2000, TimeoutMS: 30000},
	}
}

func TestAssuranceProbeWorkerSkipsNonPendingRun(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", Status: assurance.RunStatusSucceeded}
	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil (already-processed run should be a no-op success)", err)
	}
	if store.runs["run-1"].Status != assurance.RunStatusSucceeded {
		t.Fatal("Work() should not touch a run that is not pending")
	}
}

func TestAssuranceProbeWorkerRaceDeclarationCancelled(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	decl := minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	decl.Status = assurance.DeclarationStatusCancelled
	store.decls["decl-1"] = decl

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil", err)
	}
	got := store.runs["run-1"]
	if got.Status != assurance.RunStatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if got.RefusalReason != "race_declaration_cancelled" {
		t.Fatalf("refusal_reason = %q, want race_declaration_cancelled", got.RefusalReason)
	}
}

func TestAssuranceProbeWorkerRaceRefusedByRecheck(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	store.decls["decl-1"] = minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	store.recheck = assurance.GateCheck{Allowed: false, Reason: assurance.ReasonDailyBudgetExhausted}

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil", err)
	}
	got := store.runs["run-1"]
	if got.Status != assurance.RunStatusRefused {
		t.Fatalf("status = %q, want refused", got.Status)
	}
	if got.RefusalReason != "race_"+assurance.ReasonDailyBudgetExhausted {
		t.Fatalf("refusal_reason = %q, want race_%s", got.RefusalReason, assurance.ReasonDailyBudgetExhausted)
	}
	if len(store.results) != 0 {
		t.Fatal("a refused run must not produce any probe_result rows")
	}
}

func TestAssuranceProbeWorkerFakeModeSucceedsAllOK(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	store.decls["decl-1"] = minViableDeclaration("decl-1", []assurance.Target{
		{ChannelID: "chn-1", Model: "gpt-4o-mini"},
		{ChannelID: "chn-2", Model: "claude-sonnet-4"},
	})
	store.recheck = assurance.GateCheck{Allowed: true}
	// 没有 connector_config 行 == fake 模式默认（store.cfgs 留空）。

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil", err)
	}
	if got := store.runs["run-1"].Status; got != assurance.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", got)
	}
	if len(store.results) != 2 {
		t.Fatalf("results = %d, want 2 (one per target)", len(store.results))
	}
	for _, r := range store.results {
		if r.Status != assurance.ResultStatusOK {
			t.Errorf("target %s/%s status = %q, want ok (fake mode healthy response)", r.ChannelID, r.Model, r.Status)
		}
		if r.RunID != "run-1" {
			t.Errorf("result.RunID = %q, want run-1", r.RunID)
		}
	}
}

func TestAssuranceProbeWorkerPartialFailureDoesNotStopBatch(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	store.decls["decl-1"] = minViableDeclaration("decl-1", []assurance.Target{
		{ChannelID: "chn-1", Model: "gpt-4o-mini" + assurance.FakeFailingModelSuffix},
		{ChannelID: "chn-2", Model: "claude-sonnet-4"},
	})
	store.recheck = assurance.GateCheck{Allowed: true}

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil (single target failure must not fail the Job)", err)
	}
	if got := store.runs["run-1"].Status; got != assurance.RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded (batch ran to completion even though one target failed)", got)
	}
	if len(store.results) != 2 {
		t.Fatalf("results = %d, want 2", len(store.results))
	}
	var okCount, failedCount int
	for _, r := range store.results {
		switch r.Status {
		case assurance.ResultStatusOK:
			okCount++
		case assurance.ResultStatusFailed:
			failedCount++
			if r.ErrorKind == "" {
				t.Error("failed result must carry a structured error_kind")
			}
		}
	}
	if okCount != 1 || failedCount != 1 {
		t.Fatalf("okCount=%d failedCount=%d, want 1 and 1", okCount, failedCount)
	}
}

func TestAssuranceProbeWorkerStoreWriteFailureMarksRunFailed(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	store.decls["decl-1"] = minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	store.recheck = assurance.GateCheck{Allowed: true}
	store.insertResultErr = errors.New("boom: 数据库连接断开")

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	err := w.Work(context.Background(), assuranceProbeTestJob("run-1"))
	if err == nil {
		t.Fatal("Work() 应返回错误（存储写失败），不能被吞掉")
	}
	if got := store.runs["run-1"].Status; got != assurance.RunStatusFailed {
		t.Fatalf("status = %q, want failed", got)
	}
}

func TestAssuranceProbeWorkerRealModeMissingSecretsRefusesBeforeCallingClient(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	decl := minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	store.decls["decl-1"] = decl
	store.cfgs["sub2api/staging"] = &assurance.ConnectorProbeConfig{
		Mode: assurance.ModeReal, ProbeEnabled: true, ProbeCredentialRef: "secret://sub2api-probe/token",
		TargetAllowlist: []string{"api.example.test"},
	}
	store.recheck = assurance.GateCheck{Allowed: true}

	// w.probeSecrets 为 nil：real 模式缺 Provider，构造客户端必然失败。
	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil (client construction failure is recorded as a refused run, not a Job failure)", err)
	}
	got := store.runs["run-1"]
	if got.Status != assurance.RunStatusRefused {
		t.Fatalf("status = %q, want refused", got.Status)
	}
	if len(store.results) != 0 {
		t.Fatal("客户端都没建出来，不该产生任何 probe_result")
	}
}

// stubSecretProvider lets the RealClient construction path be exercised
// without a real file-backed SecretProvider.
type stubSecretProvider struct {
	value string
	err   error
}

func (s stubSecretProvider) Resolve(context.Context, secrets.CredentialRef, string) (secrets.SecretValue, error) {
	if s.err != nil {
		return secrets.SecretValue{}, s.err
	}
	return secrets.NewSecretValue([]byte(s.value)), nil
}

func TestAssuranceProbeWorkerRealModeBadCredentialRefRefuses(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	store.decls["decl-1"] = minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	store.cfgs["sub2api/staging"] = &assurance.ConnectorProbeConfig{
		Mode: assurance.ModeReal, ProbeEnabled: true, ProbeCredentialRef: "not-a-valid-ref",
		TargetAllowlist: []string{"api.example.test"},
	}
	store.recheck = assurance.GateCheck{Allowed: true}

	w := newTestAssuranceProbeWorker(store, time.Now(), stubSecretProvider{value: "tok"})
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil", err)
	}
	if got := store.runs["run-1"].Status; got != assurance.RunStatusRefused {
		t.Fatalf("status = %q, want refused", got)
	}
}

func TestAssuranceProbeWorkerBenchmarkSetUsesPreviousScoreForTrend(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	decl := minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	decl.PromptTemplateKey = assurance.TemplateBenchmarkSet
	store.decls["decl-1"] = decl
	store.recheck = assurance.GateCheck{Allowed: true}
	previous := 1
	store.previousBenchmarkScore = &previous

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	if err := w.Work(context.Background(), assuranceProbeTestJob("run-1")); err != nil {
		t.Fatalf("Work() = %v, want nil", err)
	}
	if len(store.results) != 1 {
		t.Fatalf("results = %d, want 1", len(store.results))
	}
	got := store.results[0]
	if got.Status != assurance.ResultStatusOK {
		t.Fatalf("status = %q, want ok (fake client answers the full 3-question set)", got.Status)
	}
	score, ok := assurance.ParseBenchmarkScore(got.Verdict)
	if !ok || score != assurance.BenchmarkQuestionCount {
		t.Fatalf("verdict = %q, want a full-marks benchmark verdict", got.Verdict)
	}
}

// slowThenExpiredClient never resolves before the caller's context is
// cancelled; used to exercise the "timeout" (as opposed to "failed") result
// classification without waiting out the real 30s per-target hard cap: the
// job-level Context passed to Work already carries a short deadline, and
// context.WithTimeout only ever *shortens* an inherited deadline, never
// extends it, so the 30s constant never actually elapses in this test.
type slowThenExpiredClient struct{}

func (slowThenExpiredClient) Complete(ctx context.Context, _ assurance.ProbeRequest) (assurance.ProbeResponse, error) {
	<-ctx.Done()
	return assurance.ProbeResponse{}, ctx.Err()
}

func TestAssuranceProbeWorkerClassifiesTimeoutDistinctFromFailed(t *testing.T) {
	store := newFakeAssuranceStore()
	store.runs["run-1"] = assurance.Run{ID: "run-1", DeclarationID: "decl-1", Status: assurance.RunStatusPending}
	store.decls["decl-1"] = minViableDeclaration("decl-1", []assurance.Target{{ChannelID: "chn-1", Model: "gpt-4o-mini"}})
	store.recheck = assurance.GateCheck{Allowed: true}

	w := newTestAssuranceProbeWorker(store, time.Now(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := w.probeOneTarget(ctx, "run-1", store.decls["decl-1"], assurance.Target{ChannelID: "chn-1", Model: "gpt-4o-mini"}, slowThenExpiredClient{})
	if err != nil {
		t.Fatalf("probeOneTarget() = %v, want nil (a probe timeout is a stored result, not a Job error)", err)
	}
	if result.Status != assurance.ResultStatusTimeout {
		t.Fatalf("status = %q, want timeout", result.Status)
	}
}
