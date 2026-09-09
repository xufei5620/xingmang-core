package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
)

// fakeAssuranceProbeQuerier 是 AssuranceProbeQuerier 的测试替身——聚合逻辑
// 本身已经在 internal/platform/assurance 的集成测试里验证过，本文件只关心
// HTTP 层的权限、参数校验与响应形状。
type fakeAssuranceProbeQuerier struct {
	list    assurance.ProbeListResult
	listErr error
	history []assurance.HistoryEntry
	histErr error

	gotPlatform    string
	gotEnvironment string
	gotCursor      *time.Time
	gotLimit       int
}

func (f *fakeAssuranceProbeQuerier) ProbeList(_ context.Context, platform, environment string) (assurance.ProbeListResult, error) {
	f.gotPlatform, f.gotEnvironment = platform, environment
	return f.list, f.listErr
}

func (f *fakeAssuranceProbeQuerier) ProbeHistory(_ context.Context, platform, environment string, cursor *time.Time, limit int) ([]assurance.HistoryEntry, error) {
	f.gotPlatform, f.gotEnvironment, f.gotCursor, f.gotLimit = platform, environment, cursor, limit
	return f.history, f.histErr
}

func assuranceProbesRouter(t *testing.T, q *fakeAssuranceProbeQuerier) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{},
		ActionRegistry: action.NewRegistry(), AssuranceProbes: q,
	})
}

func sampleProbeListItem() assurance.ProbeListItem {
	verdict := "一致"
	now := time.Now().UTC()
	return assurance.ProbeListItem{
		DeclarationID: "decl-1", Name: "模型指纹", ChannelIDs: []string{"chn-1"}, ChannelNames: []string{"Claude 官方 API"},
		TargetModels: []string{"claude-sonnet-4"}, PolicyText: "按需 · 无定时",
		LastRunAt: &now, LastRunStatus: assurance.ResultStatusOK, LastRunVerdict: &verdict,
		KillSwitchState: "not_applicable_fake", CanRunNow: true,
	}
}

func TestAssuranceProbesRequiresRequestReadScope(t *testing.T) {
	h := assuranceProbesRouter(t, &fakeAssuranceProbeQuerier{list: assurance.ProbeListResult{Platform: "sub2api", Items: []assurance.ProbeListItem{sampleProbeListItem()}}})
	path := "/api/v1/platforms/sub2api/assurance/probes"

	for _, scopes := range []string{"", "ops.read", "audit.read"} {
		rec := doGet(t, h, path, scopes)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("scopes=%q 应 403, got %d", scopes, rec.Code)
		}
	}
	if rec := doGet(t, h, path, requestlog.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("有 %s 应 200, got %d (%s)", requestlog.ScopeRead, rec.Code, rec.Body.String())
	}
}

func TestAssuranceProbesResponseShape(t *testing.T) {
	q := &fakeAssuranceProbeQuerier{list: assurance.ProbeListResult{Platform: "sub2api", Items: []assurance.ProbeListItem{sampleProbeListItem()}}}
	h := assuranceProbesRouter(t, q)
	rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/probes", requestlog.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (%s)", rec.Code, rec.Body.String())
	}
	var body probeListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(body.Probes) != 1 || body.Probes[0].DeclarationID != "decl-1" {
		t.Fatalf("probes = %+v, unexpected", body.Probes)
	}
	if body.Probes[0].LastRunVerdict == nil || *body.Probes[0].LastRunVerdict != "一致" {
		t.Fatalf("last_run_verdict missing/wrong: %+v", body.Probes[0])
	}
	if body.AssertionDisclaimer == "" {
		t.Fatal("assertion_disclaimer must always be present (设计稿 §1.2.3)")
	}
	if !body.ChannelBreakdownSupported {
		t.Fatal("channel_breakdown_supported must be true: active probes are declared per channel_id, unlike the passive ASSURE0 metrics")
	}
	if body.Freshness.State != "fresh" {
		t.Fatalf("freshness.state = %q, want fresh", body.Freshness.State)
	}
	if q.gotPlatform != "sub2api" {
		t.Fatalf("querier got platform=%q, want sub2api", q.gotPlatform)
	}
}

func TestAssuranceProbesCannotRunReasonHumanText(t *testing.T) {
	item := sampleProbeListItem()
	item.CanRunNow = false
	item.CannotRunReason = assurance.ReasonDailyBudgetExhausted
	h := assuranceProbesRouter(t, &fakeAssuranceProbeQuerier{list: assurance.ProbeListResult{Platform: "sub2api", Items: []assurance.ProbeListItem{item}}})
	rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/probes", requestlog.ScopeRead)
	body := rec.Body.String()
	if strings.Contains(body, "daily_budget_exhausted") == false {
		t.Fatalf("原始 reason 枚举也应在响应体里（供前端埋点/调试），got %s", body)
	}
	if !strings.Contains(body, "预算") {
		t.Fatalf("应带人话版本文案（含“预算”二字），got %s", body)
	}
}

func TestAssuranceProbeHistoryRequiresRequestReadScope(t *testing.T) {
	h := assuranceProbesRouter(t, &fakeAssuranceProbeQuerier{})
	path := "/api/v1/platforms/sub2api/assurance/probe-history"
	if rec := doGet(t, h, path, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("无 scope 应 403, got %d", rec.Code)
	}
	if rec := doGet(t, h, path, requestlog.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("有 %s 应 200, got %d (%s)", requestlog.ScopeRead, rec.Code, rec.Body.String())
	}
}

func TestAssuranceProbeHistoryResponseShapeAndPagination(t *testing.T) {
	entries := make([]assurance.HistoryEntry, 0, 3)
	for i := 0; i < 3; i++ {
		e := assurance.HistoryEntry{DeclarationName: "模型指纹", PromptTemplateKey: assurance.TemplateModelFingerprint}
		e.ChannelID = "chn-1"
		e.Model = "claude-sonnet-4"
		e.Status = assurance.ResultStatusOK
		e.Verdict = "一致"
		e.EvidenceRef = "probe-test"
		e.ObservedAt = time.Now().UTC()
		e.CreatedAt = time.Now().UTC()
		entries = append(entries, e)
	}
	q := &fakeAssuranceProbeQuerier{history: entries}
	h := assuranceProbesRouter(t, q)

	rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/probe-history?limit=3", requestlog.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (%s)", rec.Code, rec.Body.String())
	}
	var body probeHistoryBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(body.Entries))
	}
	if body.NextCursor == nil {
		t.Fatal("next_cursor should be set when the page is exactly full (there may be more)")
	}
	if !body.ChannelBreakdownSupported {
		t.Fatal("channel_breakdown_supported must be true on the history endpoint too")
	}
	if q.gotLimit != 3 {
		t.Fatalf("querier got limit=%d, want 3", q.gotLimit)
	}

	rec2 := doGet(t, h, "/api/v1/platforms/sub2api/assurance/probe-history?cursor="+*body.NextCursor, requestlog.ScopeRead)
	if rec2.Code != http.StatusOK {
		t.Fatalf("got %d (%s)", rec2.Code, rec2.Body.String())
	}
	if q.gotCursor == nil {
		t.Fatal("querier should have received the parsed cursor")
	}
}

func TestAssuranceProbeHistoryRejectsMalformedCursor(t *testing.T) {
	h := assuranceProbesRouter(t, &fakeAssuranceProbeQuerier{})
	rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/probe-history?cursor=not-a-date", requestlog.ScopeRead)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestAssuranceProbesNotMountedWhenNil(t *testing.T) {
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{}, ActionRegistry: action.NewRegistry(),
		// AssuranceProbes 留空。
	})
	rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/probes", requestlog.ScopeRead)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("AssuranceProbes=nil 应 404（端点不挂载），got %d", rec.Code)
	}
}
