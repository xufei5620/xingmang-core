package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/publishing"
)

// PublishingQuerier 是「内容发布」页的只读查询面（*publishing.PgStore 满足）。
//
// 写路径**不在这里**：草稿、素材、渠道与发布全部走 publishing.* Action，
// 由内核裁决权限、风险等级与审批（宪法 2 条）。这一层只取数。
type PublishingQuerier interface {
	ListDrafts(ctx context.Context, f publishing.DraftFilter) ([]publishing.Draft, error)
	GetDraft(ctx context.Context, id uuid.UUID) (publishing.Draft, error)
	ListRevisions(ctx context.Context, draftID uuid.UUID) ([]publishing.Revision, error)
	AssetsForDraft(ctx context.Context, draftID uuid.UUID) ([]publishing.Asset, error)
	ListAssets(ctx context.Context, limit int32) ([]publishing.Asset, error)
	ListChannels(ctx context.Context, limit int32) ([]publishing.Channel, error)
	ListPublishRecords(ctx context.Context, limit int32) ([]publishing.PublishRecord, error)
}

// PublishingDelivery 报告「哪些平台真的能发出去」（*publishing.Service 满足）。
//
// 做成依赖而不是让前端假定为空：假定的那一刻，「还没接投递」就从一个可验证的
// 事实退化成一句写死的文案，接上投递器的那天没人会想起来改它。
type PublishingDelivery interface {
	PlatformsWithDeliverer() []publishing.Platform
}

const defaultPublishingLimit int32 = 200

type publishingDraftItem struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Status         string   `json:"status"`
	CurrentVersion int      `json:"current_version"`
	ScheduledAt    string   `json:"scheduled_at"`
	AssetIDs       []string `json:"asset_ids"`
	CreatedBy      string   `json:"created_by"`
	UpdatedAt      string   `json:"updated_at"`
}

func publishingTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toDraftItem(d publishing.Draft) publishingDraftItem {
	ids := make([]string, 0, len(d.AssetIDs))
	for _, id := range d.AssetIDs {
		ids = append(ids, id.String())
	}
	return publishingDraftItem{
		ID: d.ID.String(), Title: d.Title, Body: d.Body, Status: string(d.Status),
		CurrentVersion: d.CurrentVersion, ScheduledAt: publishingTime(d.ScheduledAt),
		AssetIDs: ids, CreatedBy: d.CreatedBy,
		UpdatedAt: d.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type publishingAssetItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	URI       string `json:"uri"`
	Note      string `json:"note"`
	UpdatedAt string `json:"updated_at"`
}

func toAssetItem(a publishing.Asset) publishingAssetItem {
	return publishingAssetItem{
		ID: a.ID.String(), Name: a.Name, Kind: string(a.Kind), URI: a.URI,
		Note: a.Note, UpdatedAt: a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type publishingChannelItem struct {
	ID          string `json:"id"`
	Platform    string `json:"platform"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	Purpose     string `json:"purpose"`
	// CredentialRef 是**引用**（secret://…），从来不是凭据本身。回显它让运营
	// 看得出这个渠道绑的是哪一条；明文不经过任何一层（宪法 7 条）。
	CredentialRef string `json:"credential_ref"`
	// CredentialRefPresent 让页面不必自己判断空串语义。
	CredentialRefPresent bool   `json:"credential_ref_present"`
	Status               string `json:"status"`
	Note                 string `json:"note"`
	// CanDeliver 是这个渠道**现在能不能真的发出去**。今天恒为 false。
	//
	// 逐渠道给而不是页面上挂一句总的说明：说明会被读一次就忽略，
	// 而每一行都写着「未接投递」的表格骗不了人。
	CanDeliver bool   `json:"can_deliver"`
	UpdatedAt  string `json:"updated_at"`
}

type publishingRecordItem struct {
	ID           string `json:"id"`
	DraftID      string `json:"draft_id"`
	DraftVersion int    `json:"draft_version"`
	ChannelID    string `json:"channel_id"`
	ScheduledAt  string `json:"scheduled_at"`
	RequestedBy  string `json:"requested_by"`
	Result       string `json:"result"`
	// Delivered 由服务端按领域方法算，不让前端自己比 result 字符串。
	Delivered bool `json:"delivered"`
	// ExternalRef 是「平台返回编号」，Detail 是那句解释。前者今天恒为空串，
	// 页面据此把那一列显示成「—」并说明在等什么，而不是留一个空格子。
	ExternalRef string `json:"external_ref"`
	Detail      string `json:"detail"`
	CreatedAt   string `json:"created_at"`
}

func toRecordItem(r publishing.PublishRecord) publishingRecordItem {
	return publishingRecordItem{
		ID: r.ID.String(), DraftID: r.DraftID.String(), DraftVersion: r.DraftVersion,
		ChannelID: r.ChannelID.String(), ScheduledAt: publishingTime(r.ScheduledAt),
		RequestedBy: r.RequestedBy, Result: string(r.Result), Delivered: r.Delivered(),
		ExternalRef: r.ExternalRef, Detail: r.Detail,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// requirePublishingPrincipal 取身份并核对环境。
func requirePublishingPrincipal(r *http.Request) (principal.Principal, error) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	if _, err := resolveEnvironment(r, p); err != nil {
		return principal.Principal{}, err
	}
	return p, nil
}

// parsePublishingWindow 解析内容日历的排期区间（左闭右开，RFC3339）。
func parsePublishingWindow(r *http.Request) (from, to time.Time, err error) {
	parse := func(key string) (time.Time, error) {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			return time.Time{}, nil
		}
		ts, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			return time.Time{}, action.NewError(action.CodeInvalidParams,
				"参数 "+key+" 必须是带时区的 RFC3339 时刻", perr)
		}
		return ts.UTC(), nil
	}
	if from, err = parse("scheduled_from"); err != nil {
		return
	}
	to, err = parse("scheduled_to")
	return
}

// ListPublishingDraftsHandler 列出草稿；带排期区间时就是内容日历的数据源。
func ListPublishingDraftsHandler(store PublishingQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := requirePublishingPrincipal(r); err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseListLimit(r.URL.Query().Get("limit"), defaultPublishingLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		from, to, err := parsePublishingWindow(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		filter := publishing.DraftFilter{
			ScheduledFrom: from, ScheduledTo: to, Limit: limit,
		}
		if status := r.URL.Query().Get("status"); status != "" {
			switch publishing.DraftStatus(status) {
			case publishing.DraftDraft, publishing.DraftScheduled, publishing.DraftArchived:
				filter.Status = publishing.DraftStatus(status)
			default:
				WriteError(w, r, action.NewError(action.CodeInvalidParams,
					"参数 status 只接受 DRAFT / SCHEDULED / ARCHIVED", nil))
				return
			}
		}
		items, err := store.ListDrafts(r.Context(), filter)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]publishingDraftItem, 0, len(items))
		for _, d := range items {
			out = append(out, toDraftItem(d))
		}
		effective := publishing.ClampListLimit(limit)
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out, "limit": effective,
			"truncated": int32(len(out)) >= effective,
		})
	}
}

// GetPublishingDraftHandler 取一条草稿的详情：正文、素材与全部修订。
func GetPublishingDraftHandler(store PublishingQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := requirePublishingPrincipal(r); err != nil {
			WriteError(w, r, err)
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "draftID"))
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "草稿 id 不是合法 UUID", err))
			return
		}
		draft, err := store.GetDraft(r.Context(), id)
		if err != nil {
			WriteError(w, r, translatePublishingError(err))
			return
		}
		revisions, err := store.ListRevisions(r.Context(), id)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		assets, err := store.AssetsForDraft(r.Context(), id)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		revOut := make([]map[string]any, 0, len(revisions))
		for _, rev := range revisions {
			revOut = append(revOut, map[string]any{
				"version": rev.Version, "title": rev.Title, "body": rev.Body,
				"scheduled_at": publishingTime(rev.ScheduledAt), "note": rev.Note,
				"created_by": rev.CreatedBy,
				"created_at": rev.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		assetOut := make([]publishingAssetItem, 0, len(assets))
		for _, a := range assets {
			assetOut = append(assetOut, toAssetItem(a))
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"draft": toDraftItem(draft), "revisions": revOut, "assets": assetOut,
		})
	}
}

// ListPublishingAssetsHandler 列出素材引用。
func ListPublishingAssetsHandler(store PublishingQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := requirePublishingPrincipal(r); err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseListLimit(r.URL.Query().Get("limit"), defaultPublishingLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := store.ListAssets(r.Context(), limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]publishingAssetItem, 0, len(items))
		for _, a := range items {
			out = append(out, toAssetItem(a))
		}
		effective := publishing.ClampListLimit(limit)
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out, "limit": effective,
			"truncated": int32(len(out)) >= effective,
		})
	}
}

// ListPublishingChannelsHandler 列出渠道账号，并逐行报告「能不能真的发出去」。
//
// delivery 为 nil 时**一律按不能发处理**（fail closed）：这一列回答的是
// 「配好了之后内容会不会真的出去」，答不上来时说「能发」是最坏的一种错。
func ListPublishingChannelsHandler(store PublishingQuerier, delivery PublishingDelivery) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := requirePublishingPrincipal(r); err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseListLimit(r.URL.Query().Get("limit"), defaultPublishingLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := store.ListChannels(r.Context(), limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		deliverable := map[publishing.Platform]bool{}
		if delivery != nil {
			for _, p := range delivery.PlatformsWithDeliverer() {
				deliverable[p] = true
			}
		}
		out := make([]publishingChannelItem, 0, len(items))
		for _, c := range items {
			out = append(out, publishingChannelItem{
				ID: c.ID.String(), Platform: string(c.Platform), Handle: c.Handle,
				DisplayName: c.DisplayName, Purpose: c.Purpose,
				CredentialRef: c.CredentialRef, CredentialRefPresent: c.CredentialRef != "",
				Status: string(c.Status), Note: c.Note,
				CanDeliver: deliverable[c.Platform],
				UpdatedAt:  c.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}
		effective := publishing.ClampListLimit(limit)
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out, "limit": effective,
			"truncated": int32(len(out)) >= effective,
			// 整页级别的那一句：**平台此刻能把内容发到哪些平台**。空数组
			// 就是「一个都发不出去」，前端据此渲染那条横幅。
			"platforms_with_deliverer": platformStrings(delivery),
		})
	}
}

// platformStrings 把「能发的平台」摊平成字符串数组，nil 时给空数组而不是 null。
func platformStrings(delivery PublishingDelivery) []string {
	out := make([]string, 0)
	if delivery == nil {
		return out
	}
	for _, p := range delivery.PlatformsWithDeliverer() {
		out = append(out, string(p))
	}
	return out
}

// ListPublishingRecordsHandler 列出发布记录。
func ListPublishingRecordsHandler(store PublishingQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := requirePublishingPrincipal(r); err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseListLimit(r.URL.Query().Get("limit"), defaultPublishingLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := store.ListPublishRecords(r.Context(), limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]publishingRecordItem, 0, len(items))
		for _, rec := range items {
			out = append(out, toRecordItem(rec))
		}
		effective := publishing.ClampListLimit(limit)
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": out, "limit": effective,
			"truncated": int32(len(out)) >= effective,
		})
	}
}

// translatePublishingError 把领域的「不存在」翻成读路径的 404。
//
// 读路径上 CodeNotRegistered 就是通用的「资源不存在」（见 action/errors.go
// 对该码的注释：那 13 处读用法保留）。Action handler 里不用它。
func translatePublishingError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, publishing.ErrNotFound) {
		return action.NewError(action.CodeNotRegistered, "草稿不存在", err)
	}
	return err
}
