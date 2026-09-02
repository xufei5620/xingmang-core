package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
)

// fakeAssuranceQuerier 是 ChannelAssuranceQuerier 的测试替身，不依赖真实磁盘
// 数据——聚合算法本身已经在 connectors/reqlog 与 channelassurance 两层测过，
// 本文件只关心 HTTP 层的权限、参数校验与响应形状。
type fakeAssuranceQuerier struct {
	overview    reqlog.AssuranceResult
	overviewErr error
	history     []reqlog.AssuranceResult
	historyErr  error

	gotPlatform string
	gotPreset   reqlog.AssuranceWindowPreset
}

func (f *fakeAssuranceQuerier) Overview(_ context.Context, platform string, preset reqlog.AssuranceWindowPreset) (reqlog.AssuranceResult, error) {
	f.gotPlatform, f.gotPreset = platform, preset
	return f.overview, f.overviewErr
}

func (f *fakeAssuranceQuerier) History(_ context.Context, platform string) ([]reqlog.AssuranceResult, error) {
	f.gotPlatform = platform
	return f.history, f.historyErr
}

func sampleAssuranceResult() reqlog.AssuranceResult {
	p50 := int64(80)
	return reqlog.AssuranceResult{
		Source:                    "sub2api",
		RequestCount:              10,
		StatusClasses:             reqlog.StatusClassCounts{Success: 9, ServerError: 1},
		Duration:                  reqlog.LatencyPercentiles{SampleCount: 10, P50MS: &p50},
		Models:                    []reqlog.ModelBreakdownRow{{Model: "claude-3", RequestCount: 10}},
		ChannelBreakdownSupported: false,
		ChannelBreakdownReason:    reqlog.ChannelBreakdownUnsupportedReason,
		SpannedDays:               1,
	}
}

func assuranceRouter(t *testing.T, q *fakeAssuranceQuerier) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{},
		ActionRegistry: action.NewRegistry(), ChannelAssurance: q,
	})
}

func TestAssuranceOverviewRequiresRequestReadScope(t *testing.T) {
	h := assuranceRouter(t, &fakeAssuranceQuerier{overview: sampleAssuranceResult()})
	path := "/api/v1/platforms/sub2api/assurance/overview?window=1h"

	for _, scopes := range []string{"", "ops.read", "audit.read"} {
		rec := doGet(t, h, path, scopes)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("scopes=%q 应 403, got %d", scopes, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), requestlog.ScopeRead) {
			t.Fatalf("403 文案应指明缺 %s: %s", requestlog.ScopeRead, rec.Body.String())
		}
	}
	if rec := doGet(t, h, path, requestlog.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("有 %s 应 200, got %d (%s)", requestlog.ScopeRead, rec.Code, rec.Body.String())
	}
}

func TestAssuranceOverviewValidatesWindowParam(t *testing.T) {
	h := assuranceRouter(t, &fakeAssuranceQuerier{overview: sampleAssuranceResult()})

	for _, window := range []string{"15m", "1h", "24h"} {
		rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/overview?window="+window, requestlog.ScopeRead)
		if rec.Code != http.StatusOK {
			t.Fatalf("window=%s 应 200, got %d (%s)", window, rec.Code, rec.Body.String())
		}
	}
	for _, window := range []string{"", "15min", "7d", "1H"} {
		rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/overview?window="+window, requestlog.ScopeRead)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("window=%q 应 400, got %d (%s)", window, rec.Code, rec.Body.String())
		}
	}
}

func TestAssuranceOverviewResponseShape(t *testing.T) {
	q := &fakeAssuranceQuerier{overview: sampleAssuranceResult()}
	h := assuranceRouter(t, q)
	rec := doGet(t, h, "/api/v1/platforms/sub2api/assurance/overview?window=24h", requestlog.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body assuranceOverviewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if body.ChannelBreakdownSupported {
		t.Fatal("channel_breakdown_supported 应为 false")
	}
	if body.ChannelBreakdownReason == "" {
		t.Fatal("channel_breakdown_reason 不能为空")
	}
	if body.Window != "24h" {
		t.Fatalf("window 应原样回显请求参数, got %q", body.Window)
	}
	if body.RequestCount != 10 {
		t.Fatalf("request_count = %d, want 10", body.RequestCount)
	}
	if len(body.Models) != 1 || body.Models[0].Model != "claude-3" {
		t.Fatalf("models 未正确透传: %+v", body.Models)
	}
	if body.Freshness.State == "" {
		t.Fatal("freshness.state 不能为空——规格 §9.1 禁止裸数字冒充实时完整数据")
	}
	if q.gotPlatform != "sub2api" || q.gotPreset != reqlog.AssuranceWindow24h {
		t.Fatalf("Service 收到的入参不对: platform=%q preset=%q", q.gotPlatform, q.gotPreset)
	}
	// 响应体里不该出现渠道字段被悄悄编出一个假值的迹象
	if strings.Contains(rec.Body.String(), `"channel":`) {
		t.Fatalf("响应不该包含编造的渠道字段: %s", rec.Body.String())
	}
}

func TestAssuranceOverviewPropagatesServiceError(t *testing.T) {
	notFound := action.NewError(action.CodeNotRegistered, "平台 \"cpa\" 没有请求数据", nil)
	h := assuranceRouter(t, &fakeAssuranceQuerier{overviewErr: notFound})
	rec := doGet(t, h, "/api/v1/platforms/cpa/assurance/overview?window=1h", requestlog.ScopeRead)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Service 层的 NotRegistered 应映射为 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAssuranceHistoryRequiresRequestReadScope(t *testing.T) {
	h := assuranceRouter(t, &fakeAssuranceQuerier{history: make([]reqlog.AssuranceResult, 7)})
	path := "/api/v1/platforms/sub2api/assurance/history"

	if rec := doGet(t, h, path, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("无权限应 403, got %d", rec.Code)
	}
	if rec := doGet(t, h, path, requestlog.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("有权限应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAssuranceHistoryResponseShape(t *testing.T) {
	day := sampleAssuranceResult()
	day.Day = "2026-08-31"
	other := sampleAssuranceResult()
	other.Day = "2026-08-30"
	other.RequestCount = 0
	other.MissingDays = 1
	q := &fakeAssuranceQuerier{history: []reqlog.AssuranceResult{other, day}}
	h := assuranceRouter(t, q)

	rec := doGet(t, h, "/api/v1/platforms/newapi/assurance/history", requestlog.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body assuranceHistoryBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(body.Days) != 2 {
		t.Fatalf("len(days) = %d, want 2", len(body.Days))
	}
	if body.Days[0].Day != "2026-08-30" || !body.Days[0].Missing {
		t.Fatalf("days[0] 应是缺目录的那天: %+v", body.Days[0])
	}
	if body.Days[1].Day != "2026-08-31" || body.Days[1].Missing {
		t.Fatalf("days[1] 应是完整的那天: %+v", body.Days[1])
	}
	// 任意一天覆盖不全时整份响应的新鲜度也应标 partial（不能被完整的那天掩盖）
	if !body.Freshness.IsPartial || body.Freshness.State != "partial" {
		t.Fatalf("Freshness 应反映出至少一天覆盖不全: %+v", body.Freshness)
	}
	if q.gotPlatform != "newapi" {
		t.Fatalf("Service 收到的 platform = %q, want newapi", q.gotPlatform)
	}
}

// TestAssuranceEndpointsNotMountedWhenNil 核对 ChannelAssurance 为 nil 时
// （reqlog 不是 file 模式）两个端点整组不挂载——与 RequestLogs 同一条纪律,
// 端点不存在（404）比端点存在却一调就 500 诚实。
func TestAssuranceEndpointsNotMountedWhenNil(t *testing.T) {
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{},
		ActionRegistry: action.NewRegistry(), // ChannelAssurance 留空
	})
	for _, path := range []string{
		"/api/v1/platforms/sub2api/assurance/overview?window=1h",
		"/api/v1/platforms/sub2api/assurance/history",
	} {
		rec := doGet(t, h, path, requestlog.ScopeRead)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 应 404（未挂载）, got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}
