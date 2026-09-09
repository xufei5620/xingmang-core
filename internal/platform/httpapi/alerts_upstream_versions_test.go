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

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
)

type fakeAckLister struct {
	gotEnv string
	items  []alerts.UpstreamVersionAck
	err    error
}

func (f *fakeAckLister) ListUpstreamVersionAcksOrdered(
	_ context.Context, env string,
) ([]alerts.UpstreamVersionAck, error) {
	f.gotEnv = env
	return f.items, f.err
}

func ackRouter(t *testing.T, lister UpstreamVersionAckLister) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:              discardLogger(),
		Service:             "platform-api",
		Environment:         "development",
		DB:                  fakePinger{},
		Resolver:            res,
		Kernel:              &fakeExecutor{},
		ActionRegistry:      action.NewRegistry(),
		UpstreamVersionAcks: lister,
	})
}

func getAcks(t *testing.T, h http.Handler, query, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/upstream-versions"+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sampleAck() alerts.UpstreamVersionAck {
	return alerts.UpstreamVersionAck{
		Environment:    "development",
		MetricKey:      "sub2api.connector.health",
		Version:        "0.2.3",
		Source:         "sub2api-prod",
		AcknowledgedBy: "staff_alice",
		AcknowledgedAt: time.Date(2026, 9, 9, 3, 40, 0, 0, time.UTC),
		// 仓储层**确实**带着 note 回来——下面那条缺席断言要的就是这个前提：
		// 不是「没有 note 可回显」，是「有也不回显」。
		Note: ackNoteMarker,
	}
}

// ackNoteMarker 是一个只可能来自 note 的标记串：它不出现在本用例任何别的
// 字段里，所以在响应体里找到它就等于找到了 note。
const ackNoteMarker = "备注只该出现在审计事件里"

// TestListUpstreamVersionAcksMakesTheSuppressorVisible：抑制器必须看得见。
//
// 「已核对的上游版本」是一个永久抑制器：它让 upstream.version.changed 对该
// 版本不再命中、既有告警在下一轮被解决。在这个端点之前，系统里**没有任何
// 读路径**能回答「哪些版本被标成已核对了、谁标的、什么时候标的」——只有
// 评估器每轮的 version_ack_suppressed 计数能证明「有东西被压住了」，
// 却说不出是什么。一个看不见的抑制器与一个不留痕的抑制器是同一种病。
func TestListUpstreamVersionAcksMakesTheSuppressorVisible(t *testing.T) {
	lister := &fakeAckLister{items: []alerts.UpstreamVersionAck{sampleAck()}}
	rec := getAcks(t, ackRouter(t, lister), "", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Items []struct {
			MetricKey      string `json:"metric_key"`
			Version        string `json:"version"`
			Source         string `json:"source"`
			AcknowledgedBy string `json:"acknowledged_by"`
			AcknowledgedAt string `json:"acknowledged_at"`
		} `json:"items"`
		RevokeAction string `json:"revoke_action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1: %s", len(body.Items), rec.Body.String())
	}
	got := body.Items[0]
	// 「谁、什么时候、把哪条上游的哪个版本标成了已核对」四件事缺一不可——
	// 少任何一件，这份清单都回答不了「这条抑制该不该继续存在」。
	if got.MetricKey != "sub2api.connector.health" || got.Version != "0.2.3" {
		t.Fatalf("清单内容不对: %+v", got)
	}
	if got.AcknowledgedBy != "staff_alice" || got.Source != "sub2api-prod" {
		t.Fatalf("要说清是谁标的、来源是哪条上游: %+v", got)
	}
	if got.AcknowledgedAt != "2026-09-09T03:40:00Z" {
		t.Fatalf("时刻必须是 UTC RFC3339: %q", got.AcknowledgedAt)
	}
	// 撤销入口写在响应里：读到这份清单的人下一个问题必然是「点错了怎么办」。
	if body.RevokeAction != alerts.ActionRevokeUpstreamVersion {
		t.Fatalf("revoke_action = %q, want %q", body.RevokeAction, alerts.ActionRevokeUpstreamVersion)
	}
	// 环境来自调用者身份，不是参数。
	if lister.gotEnv != "development" {
		t.Fatalf("查询环境 = %q, want development", lister.gotEnv)
	}
}

// TestListUpstreamVersionAcksDoesNotEchoTheNote：note **不得**出现在这条
// ops.read 的响应里。
//
// 理由是同一份代码里已经写死的一条：note 是 ≤200 字节的自由文本，除长度外
// 没有形态校验，「形状上装得下凭据」正是
// alerts.upstream_version.acknowledge 被永久锁在 L1 的第一条理由
// （alerts/actions.go 的 acknowledgeUpstreamVersionDef）。那条理由禁的是
// 「给 note 开一条展示给人看的通道」；本端点只要 alerts.ScopeRead，而 staff
// 这个粗粒度角色就带着它（docs/modules/httpapi/PERMISSIONS.md），
// 审计事件的读路径却单独要 audit.ScopeRead。回显 note 等于把它从后一档
// 掉到前一档——两处注释就此互相矛盾，而互相矛盾的两处注释里必有一处会被
// 拿去做相反的决定。
//
// 缺席型断言的两个前提都写在这条用例里，缺一它就会恒真（memory
// 「缺席型断言要做变异验证」）：(1) 仓储层确实带着 note 回来
// （sampleAck 填了 ackNoteMarker）；(2) 投影确实渲染了
// （acknowledged_by 在场）。
func TestListUpstreamVersionAcksDoesNotEchoTheNote(t *testing.T) {
	rec := getAcks(t, ackRouter(t, &fakeAckLister{
		items: []alerts.UpstreamVersionAck{sampleAck()},
	}), "", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// 在场断言：投影真的渲染了这一条，否则下面两条缺席断言只是在空响应上恒真。
	if !strings.Contains(body, `"acknowledged_by":"staff_alice"`) {
		t.Fatalf("这一条压根没渲染出来，缺席断言会恒真: %s", body)
	}

	// 缺席断言之一：字段本身不存在。
	if strings.Contains(body, `"note"`) {
		t.Fatalf("响应里不得出现 note 字段（它只能走 audit.read）: %s", body)
	}
	// 缺席断言之二：内容也没有换个键名溜出去。
	if strings.Contains(body, ackNoteMarker) {
		t.Fatalf("note 的内容出现在响应里了（哪怕换了键名也不行）: %s", body)
	}
}

func TestListUpstreamVersionAcksEmptyReturnsEmptyArray(t *testing.T) {
	rec := getAcks(t, ackRouter(t, &fakeAckLister{}), "", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// 空数组而不是 null：一条都没核对过是一个明确的答案。
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("应是空数组: %s", rec.Body.String())
	}
}

func TestListUpstreamVersionAcksRequiresScopeAndEnvironment(t *testing.T) {
	h := ackRouter(t, &fakeAckLister{items: []alerts.UpstreamVersionAck{sampleAck()}})

	if rec := getAcks(t, h, "", "finance.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("缺 ops.read 应 403，实际 %d", rec.Code)
	}
	// 跨环境读取一律拒绝（规格 §20.5），与 /alerts 同一条闸。
	if rec := getAcks(t, h, "?environment=production", "ops.read"); rec.Code == http.StatusOK {
		t.Fatalf("跨环境读取不该成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestUpstreamVersionAcksEndpointIsNotMountedWithoutADependency：nil 时端点
// 不存在（404），而不是存在却一调就 500。
func TestUpstreamVersionAcksEndpointIsNotMountedWithoutADependency(t *testing.T) {
	rec := getAcks(t, ackRouter(t, nil), "", "ops.read")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未装配时应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListUpstreamVersionAcksHidesStoreErrorDetails(t *testing.T) {
	lister := &fakeAckLister{err: errors.New("dial tcp 10.0.0.7:5432: connect: refused")}
	rec := getAcks(t, ackRouter(t, lister), "", "ops.read")
	if rec.Code < 500 {
		t.Fatalf("仓储错误应是 5xx，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Fatalf("响应体不该带内网地址: %s", rec.Body.String())
	}
}
