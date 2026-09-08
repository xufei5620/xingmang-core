package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/publishing"
)

var publishingNow = time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)

type fakePublishingQuerier struct {
	drafts    []publishing.Draft
	channels  []publishing.Channel
	assets    []publishing.Asset
	records   []publishing.PublishRecord
	revisions []publishing.Revision
	gotFilter publishing.DraftFilter
}

func (f *fakePublishingQuerier) ListDrafts(_ context.Context, filter publishing.DraftFilter) ([]publishing.Draft, error) {
	f.gotFilter = filter
	return f.drafts, nil
}

func (f *fakePublishingQuerier) GetDraft(_ context.Context, id uuid.UUID) (publishing.Draft, error) {
	for _, d := range f.drafts {
		if d.ID == id {
			return d, nil
		}
	}
	return publishing.Draft{}, publishing.ErrNotFound
}

func (f *fakePublishingQuerier) ListRevisions(context.Context, uuid.UUID) ([]publishing.Revision, error) {
	return f.revisions, nil
}

func (f *fakePublishingQuerier) AssetsForDraft(context.Context, uuid.UUID) ([]publishing.Asset, error) {
	return f.assets, nil
}

func (f *fakePublishingQuerier) ListAssets(context.Context, int32) ([]publishing.Asset, error) {
	return f.assets, nil
}

func (f *fakePublishingQuerier) ListChannels(context.Context, int32) ([]publishing.Channel, error) {
	return f.channels, nil
}

func (f *fakePublishingQuerier) ListPublishRecords(context.Context, int32) ([]publishing.PublishRecord, error) {
	return f.records, nil
}

// fakeDelivery 让用例能把「哪些平台能发」拨到任意值——那正是缺席断言的变异靶点。
type fakeDelivery struct{ platforms []publishing.Platform }

func (f fakeDelivery) PlatformsWithDeliverer() []publishing.Platform { return f.platforms }

func publishingRouter(t *testing.T, q PublishingQuerier, d PublishingDelivery) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:            discardLogger(),
		Service:           "platform-api",
		Environment:       "development",
		DB:                fakePinger{},
		Resolver:          res,
		Kernel:            &fakeExecutor{},
		ActionRegistry:    action.NewRegistry(),
		Publishing:        q,
		PublishingDeliver: d,
	})
}

func getPublishing(t *testing.T, h http.Handler, path, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func seedChannel() publishing.Channel {
	return publishing.Channel{
		ID: uuid.New(), Environment: "development", Platform: publishing.PlatformX,
		Handle: "@xingmang", DisplayName: "星芒官方", Purpose: "产品公告",
		CredentialRef: "secret://publishing-x/mkt-token", Status: publishing.ChannelActive,
		CreatedBy: "editor-1", CreatedAt: publishingNow, UpdatedAt: publishingNow,
	}
}

// 渠道列表逐行报告「现在能不能真的发出去」，今天全是 false。
//
// 先 await 正向锚点（这一行**确实返回了**，字段齐全）再断言 can_deliver 为假：
// 一个根本没返回的行当然也「不能发」，那种绿是假的。
//
// **变异验证**：把 fakeDelivery 的 platforms 换成 {PlatformX}，本用例在
// 「can_deliver 为真」那一行变红（不是在锚点上）。已实测。
func TestPublishingChannelsReportNoDeliverer(t *testing.T) {
	ch := seedChannel()
	q := &fakePublishingQuerier{channels: []publishing.Channel{ch}}
	h := publishingRouter(t, q, fakeDelivery{})

	rec := getPublishing(t, h, "/api/v1/publishing/channels", publishing.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应当是 200，got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			ID                   string `json:"id"`
			Handle               string `json:"handle"`
			CredentialRef        string `json:"credential_ref"`
			CredentialRefPresent bool   `json:"credential_ref_present"`
			CanDeliver           bool   `json:"can_deliver"`
		} `json:"items"`
		PlatformsWithDeliverer []string `json:"platforms_with_deliverer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// 正向锚点：这一行真的回来了。
	if len(body.Items) != 1 || body.Items[0].ID != ch.ID.String() {
		t.Fatalf("应当返回那一条渠道，got %+v", body.Items)
	}
	if body.Items[0].Handle != "@xingmang" {
		t.Fatalf("账号标识没返回: %+v", body.Items[0])
	}
	// 引用**原样回显**（它不是秘密），并给出一个布尔让页面不必判断空串。
	if body.Items[0].CredentialRef != "secret://publishing-x/mkt-token" {
		t.Fatalf("凭据引用应当原样回显，got %q", body.Items[0].CredentialRef)
	}
	if !body.Items[0].CredentialRefPresent {
		t.Fatal("credential_ref_present 应当为真")
	}

	// 锚点站住了，下面才是这条用例要证明的事。
	if body.Items[0].CanDeliver {
		t.Fatal("can_deliver 为真：平台没有出站投递器，这一行不该说自己能发")
	}
	if len(body.PlatformsWithDeliverer) != 0 {
		t.Fatalf("platforms_with_deliverer 应当是空数组，got %v", body.PlatformsWithDeliverer)
	}
}

// 变异的靶心与对照组：把「有投递器」拨上去，上面那条断言必须不再成立。
//
// 常驻代码而不是一次性手工验证——手工验过一次，下一个人重写 handler 时不会
// 再验一次。
func TestPublishingChannelsDeliverabilityIsMutationChecked(t *testing.T) {
	ch := seedChannel()
	q := &fakePublishingQuerier{channels: []publishing.Channel{ch}}

	t.Run("靶心：有投递器时这一行说得出自己能发", func(t *testing.T) {
		h := publishingRouter(t, q, fakeDelivery{platforms: []publishing.Platform{publishing.PlatformX}})
		rec := getPublishing(t, h, "/api/v1/publishing/channels", publishing.ScopeRead)
		var body struct {
			Items []struct {
				CanDeliver bool `json:"can_deliver"`
			} `json:"items"`
			PlatformsWithDeliverer []string `json:"platforms_with_deliverer"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if len(body.Items) != 1 || !body.Items[0].CanDeliver {
			t.Fatalf("变异没生效：can_deliver 仍然是假，那么缺席用例的绿说明不了任何事，got %+v", body.Items)
		}
		if len(body.PlatformsWithDeliverer) != 1 || body.PlatformsWithDeliverer[0] != "x" {
			t.Fatalf("变异没生效：platforms_with_deliverer = %v", body.PlatformsWithDeliverer)
		}
	})

	t.Run("对照组：依赖为 nil 时按不能发处理（fail closed）", func(t *testing.T) {
		// 依赖没接上时**不能**默认「能发」——那是最坏的一种答不上来。
		h := publishingRouter(t, q, nil)
		rec := getPublishing(t, h, "/api/v1/publishing/channels", publishing.ScopeRead)
		var body struct {
			Items []struct {
				CanDeliver bool `json:"can_deliver"`
			} `json:"items"`
			PlatformsWithDeliverer []string `json:"platforms_with_deliverer"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if len(body.Items) != 1 || body.Items[0].CanDeliver {
			t.Fatalf("投递依赖为 nil 时应当按不能发处理，got %+v", body.Items)
		}
		if body.PlatformsWithDeliverer == nil {
			t.Fatal("platforms_with_deliverer 应当是空数组而不是 null")
		}
	})
}

// 发布记录如实说「未投递」，并把那句解释原样带出来。
func TestPublishingRecordsReportNotDelivered(t *testing.T) {
	rec0 := publishing.PublishRecord{
		ID: uuid.New(), DraftID: uuid.New(), DraftVersion: 2, ChannelID: uuid.New(),
		RequestedBy: "publisher-1", Result: publishing.ResultNotDelivered,
		Detail: publishing.NotDeliveredReason, CreatedAt: publishingNow,
	}
	q := &fakePublishingQuerier{records: []publishing.PublishRecord{rec0}}
	h := publishingRouter(t, q, fakeDelivery{})

	rec := getPublishing(t, h, "/api/v1/publishing/records", publishing.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应当是 200，got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			ID           string `json:"id"`
			DraftVersion int    `json:"draft_version"`
			Result       string `json:"result"`
			Delivered    bool   `json:"delivered"`
			ExternalRef  string `json:"external_ref"`
			Detail       string `json:"detail"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// 正向锚点。
	if len(body.Items) != 1 || body.Items[0].ID != rec0.ID.String() {
		t.Fatalf("应当返回那一条记录，got %+v", body.Items)
	}
	// 钉住的是「记录钉了哪一版」——事后改草稿不改写这条记录。
	if body.Items[0].DraftVersion != 2 {
		t.Fatalf("draft_version 应当是 2，got %d", body.Items[0].DraftVersion)
	}

	if body.Items[0].Delivered {
		t.Fatal("delivered 为真：平台没有出站投递器")
	}
	if body.Items[0].Result != string(publishing.ResultNotDelivered) {
		t.Fatalf("result 应当是 %s，got %q", publishing.ResultNotDelivered, body.Items[0].Result)
	}
	// 只断枚举值不够，文案也逐字——把「没有这个能力」改软成「投递中」时
	// 枚举值不会跟着变。
	if body.Items[0].Detail != publishing.NotDeliveredReason {
		t.Fatalf("detail 应逐字等于\n  %q\ngot\n  %q",
			publishing.NotDeliveredReason, body.Items[0].Detail)
	}
	// 「平台返回编号」这一列今天必须是空的：一条带编号的未投递记录会让人
	// 以为发出去了。
	if body.Items[0].ExternalRef != "" {
		t.Fatalf("未投递的记录不该带平台返回编号，got %q", body.Items[0].ExternalRef)
	}
}

// 内容日历的排期区间原样传给仓储，不在 Go 里筛。
func TestPublishingDraftsPassScheduleWindowToStore(t *testing.T) {
	q := &fakePublishingQuerier{}
	h := publishingRouter(t, q, fakeDelivery{})

	rec := getPublishing(t, h,
		"/api/v1/publishing/drafts?scheduled_from=2026-10-01T00:00:00Z&scheduled_to=2026-11-01T00:00:00Z&status=SCHEDULED",
		publishing.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应当是 200，got %d: %s", rec.Code, rec.Body.String())
	}
	if q.gotFilter.Status != publishing.DraftScheduled {
		t.Fatalf("status 没传下去，got %q", q.gotFilter.Status)
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if !q.gotFilter.ScheduledFrom.Equal(want) {
		t.Fatalf("scheduled_from 没传下去，got %v", q.gotFilter.ScheduledFrom)
	}
	if !q.gotFilter.ScheduledTo.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("scheduled_to 没传下去，got %v", q.gotFilter.ScheduledTo)
	}
}

// 认不出来的 status 当场拒绝，不静默回落成「不过滤」。
//
// 静默回落的症状是「筛了跟没筛一样」，运营会以为库里就这些。
func TestPublishingDraftsRejectUnknownStatus(t *testing.T) {
	q := &fakePublishingQuerier{}
	h := publishingRouter(t, q, fakeDelivery{})

	rec := getPublishing(t, h, "/api/v1/publishing/drafts?status=PUBLISHED", publishing.ScopeRead)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("认不出来的 status 应当是 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 文案也逐字：只断状态码的话，把提示改成一句没用的通用话不会被发现。
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应: %v（body=%s）", err, rec.Body.String())
	}
	if body.Error.Code != string(action.CodeInvalidParams) {
		t.Fatalf("code = %q, want %q", body.Error.Code, action.CodeInvalidParams)
	}
	const wantMsg = "参数 status 只接受 DRAFT / SCHEDULED / ARCHIVED"
	if body.Error.Message != wantMsg {
		t.Fatalf("message = %q, want %q", body.Error.Message, wantMsg)
	}
}

// 缺 publishing.read 时读不到任何一格。
func TestPublishingEndpointsRequireReadScope(t *testing.T) {
	q := &fakePublishingQuerier{channels: []publishing.Channel{seedChannel()}}
	h := publishingRouter(t, q, fakeDelivery{})

	for _, path := range []string{
		"/api/v1/publishing/drafts",
		"/api/v1/publishing/assets",
		"/api/v1/publishing/channels",
		"/api/v1/publishing/records",
	} {
		// 正向锚点：**带着 scope 时读得到**，否则下面的 403 可能只是路由没挂上。
		if rec := getPublishing(t, h, path, publishing.ScopeRead); rec.Code != http.StatusOK {
			t.Fatalf("%s 带 scope 时应当 200，got %d: %s", path, rec.Code, rec.Body.String())
		}
		if rec := getPublishing(t, h, path, "ops.read"); rec.Code != http.StatusForbidden {
			t.Fatalf("%s 缺 publishing.read 应当 403，got %d", path, rec.Code)
		}
	}
}

// Publishing 依赖为 nil 时整组端点不挂载（404），而不是挂上去一调就 500。
func TestPublishingEndpointsUnmountedWhenQuerierMissing(t *testing.T) {
	h := publishingRouter(t, nil, nil)
	rec := getPublishing(t, h, "/api/v1/publishing/channels", publishing.ScopeRead)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("没有 Querier 时应当 404（端点不存在），got %d: %s", rec.Code, rec.Body.String())
	}
}
