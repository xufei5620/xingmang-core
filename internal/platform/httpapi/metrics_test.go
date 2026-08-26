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

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

type fakeMetricLister struct {
	gotEnv string
	items  []ops.Observation
	err    error
}

func (f *fakeMetricLister) ListByEnvironment(_ context.Context, env string) ([]ops.Observation, error) {
	f.gotEnv = env
	return f.items, f.err
}

func metricAt(key string, observedAgo time.Duration, threshold int32) ops.Observation {
	at := time.Now().UTC().Add(-observedAgo)
	return ops.Observation{
		ID: uuid.New(), MetricKey: key, Source: "sub2api-prod",
		Environment: "development", ObservedAt: &at, SyncedAt: at,
		Watermark: "wm-1", Status: ops.SyncOK, LastSuccess: &at,
		StalenessThresholdSeconds: threshold,
		Value:                     map[string]any{"amount_minor": 123456, "currency": "CNY"},
	}
}

func TestListMetricsAlwaysIncludesFreshness(t *testing.T) {
	// 规格 §9.1 铁律：禁止裸数字冒充实时完整数据。
	// 每一项都必须带 freshness，且 state 非空——防止将来有人加「精简模式」绕过。
	lister := &fakeMetricLister{items: []ops.Observation{
		metricAt("sub2api.revenue.daily", 60*time.Second, 1800),
		metricAt("sub2api.channel.balance", 2*time.Hour, 1800),
	}}
	h := testRouterWithMetrics(t, &fakeExecutor{}, nil, lister)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			MetricKey string         `json:"metric_key"`
			Value     map[string]any `json:"value"`
			Freshness struct {
				State            string  `json:"state"`
				StalenessSeconds *int64  `json:"staleness_seconds"`
				ThresholdSeconds int32   `json:"threshold_seconds"`
				ObservedAt       *string `json:"observed_at"`
			} `json:"freshness"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(got.Items) != 2 {
		t.Fatalf("应有 2 项: %+v", got.Items)
	}
	for _, it := range got.Items {
		if it.Freshness.State == "" {
			t.Fatalf("%s 缺少新鲜度状态——这是裸数字，规格 §9.1 禁止", it.MetricKey)
		}
		if it.Freshness.ThresholdSeconds == 0 {
			t.Fatalf("%s 缺少阈值，前端无法解释 staleness", it.MetricKey)
		}
		if it.Value == nil {
			t.Fatalf("%s 缺少值", it.MetricKey)
		}
	}
	// 60 秒前采集、阈值 1800 → fresh；2 小时前 → stale
	if got.Items[0].Freshness.State != "stale" && got.Items[1].Freshness.State != "stale" {
		t.Fatalf("应有一项为 stale: %+v", got.Items)
	}
}

func TestListMetricsUninitializedHasNullStaleness(t *testing.T) {
	o := metricAt("sub2api.revenue.daily", 0, 1800)
	o.ObservedAt = nil
	o.LastSuccess = nil
	h := testRouterWithMetrics(t, &fakeExecutor{}, nil, &fakeMetricLister{items: []ops.Observation{o}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"state":"uninitialized"`) {
		t.Fatalf("从未采集应为 uninitialized: %s", body)
	}
	if !strings.Contains(body, `"staleness_seconds":null`) {
		t.Fatalf("从未采集时 staleness 应为 null（前端据此显示「未初始化」）: %s", body)
	}
}

func TestListMetricsDefaultsToPrincipalEnvironment(t *testing.T) {
	lister := &fakeMetricLister{}
	h := testRouterWithMetrics(t, &fakeExecutor{}, nil, lister)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	devHeaders(req, "ops.read")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if lister.gotEnv != "development" {
		t.Fatalf("未指定 environment 时应用 Principal 的环境，got %q", lister.gotEnv)
	}
}

func TestListMetricsRejectsUnknownEnvironmentAndRequiresPrincipal(t *testing.T) {
	h := testRouterWithMetrics(t, &fakeExecutor{}, nil, &fakeMetricLister{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics?environment=prod", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法环境应 400, got %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec2.Code)
	}
}

func TestListMetricsHidesStoreErrorDetail(t *testing.T) {
	h := testRouterWithMetrics(t, &fakeExecutor{}, nil, &fakeMetricLister{
		err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}
