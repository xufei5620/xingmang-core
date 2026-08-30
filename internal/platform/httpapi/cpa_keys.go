package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/cpa"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// CPAKeysQuerier is CPA's per-key usage read capability
// (*cpa.ReadClient satisfies it directly — KeyUsage is already its exact
// shape, so no service-layer adapter sits between the connector and this
// handler the way platformusers.Service does for the sub2api/newapi family).
type CPAKeysQuerier interface {
	KeyUsage(ctx context.Context, day string) (cpa.KeyUsagePage, error)
}

// ScopeCPAKeysRead is the permission required to read CPA's per-key usage.
//
// Deliberately the same **string** as platformusers.ScopeRead
// ("platform.users.read") rather than an import of that package's constant:
// CPA's "用户管理" tab shows API-key usage (a hash + alias + counters), not
// platformusers.Service's end-user identities — platformusers.ParseSource
// explicitly excludes "cpa" (connectors/platformusers/contract.go), because
// CPA has no concept of an end user with an email at all. This endpoint is
// intentionally not routed through that domain's Service/Client plumbing.
// Reusing the scope *value* is a deliberate authorization choice, not
// laziness: an operator already trusted to see any other platform's
// end-user financial detail is trusted to see which API keys are using CPA
// and how much — minting a separate scope name would only add an
// authorization string nobody's role mapping grants yet, for no additional
// isolation (same reasoning as alerts.ScopeRead reusing ops.ScopeRead).
const ScopeCPAKeysRead = "platform.users.read"

// cpaKeyUsageItem is one API key's usage for the requested business day.
//
// Fields are listed explicitly, not a direct serialization of
// cpa.KeyUsageRow (spec §18.4: the response body is a contract, not a
// struct's reflection).
type cpaKeyUsageItem struct {
	// APIKeyHash is the raw, one-way hash from usage_events.api_key_hash —
	// never a credential, never reversible (constitution §7; see
	// connectors/cpa's package doc for what is never mounted at all).
	APIKeyHash          string `json:"api_key_hash"`
	Alias               string `json:"alias"`
	RequestCount        int64  `json:"request_count"`
	TokensIn            int64  `json:"tokens_in"`
	TokensOut           int64  `json:"tokens_out"`
	TokensCacheRead     int64  `json:"tokens_cache_read"`
	TokensCacheCreation int64  `json:"tokens_cache_creation"`
	// Cost reuses moneyItem (profit_daily.go) rather than the plainer
	// amountBody (users.go): CPA cost is int64 @ money.MicroScale (micro-USD),
	// not a currency's own native minor unit — moneyItem is the one wire
	// shape in this package that carries an explicit `scale` alongside the
	// amount, so the frontend never has to hardcode "6" to interpret it
	// (see moneyItem's doc comment: a drifted hardcoded scale is a 100x
	// error that reports no error at all). nil = unpriced, not $0.
	Cost *moneyItem `json:"cost"`
	// UnpricedRequestCount > 0 means Cost is a partial sum, not the full
	// day's spend for this key — see cpa.KeyUsageRow's doc comment.
	UnpricedRequestCount int64   `json:"unpriced_request_count"`
	LastUsedAt           *string `json:"last_used_at"`
}

type cpaKeyUsagePageResponse struct {
	BusinessDay   string                     `json:"business_day"`
	Items         []cpaKeyUsageItem          `json:"items"`
	TotalKeyCount int64                      `json:"total_key_count"`
	Truncated     bool                       `json:"truncated"`
	Snapshot      userDetailSnapshotResponse `json:"snapshot"`
}

func actionCPAUnavailable() error {
	return action.NewError(action.CodeAdvancedControlsRequired, "CPA 用量数据源未接入", nil)
}

// wrapCPAError maps connectors/cpa's connector.Error classification onto the
// action.Error vocabulary WriteError already understands.
//
// This handler talks directly to a cpa.ReadClient — there is no
// service-layer adapter between it and the connector the way
// platformusers.Service sits between httpapi and connectors/sub2api|newapi
// (CPA's "users" data has no cross-platform service abstraction to justify
// one). Without this mapping every connector error would fall through
// WriteError's default case (action.CodeInternal → HTTP 500), losing the
// distinction between "the query was malformed" (400) and "the file
// couldn't be read right now" (502) that connector.ErrorKind already
// carries — see response.go's safeMessage, which only recognizes
// *action.Error.
func wrapCPAError(err error) error {
	if err == nil {
		return nil
	}
	var ae *action.Error
	if errors.As(err, &ae) {
		return err
	}
	switch connector.KindOf(err) {
	case connector.KindBadResponse:
		return action.NewError(action.CodeInvalidParams, "CPA 用量查询参数无效", err)
	case connector.KindNotSupported:
		return action.NewError(action.CodeAdvancedControlsRequired, "CPA 用量数据源当前不可用", err)
	default:
		return action.NewError(action.CodeExecutionFailed, "CPA 用量数据源读取失败", err)
	}
}

// toCPAKeyUsagePageBody converts the connector page to the wire contract.
//
// Fails closed on a Snapshot that looks uninitialized (zero ObservedAt or
// empty Instance/Source) — same defensive shape as toKeyMetadataPageBody:
// a connector bug that returns a zero Snapshot must not be serialized as if
// it were a real, fresh read.
func toCPAKeyUsagePageBody(page cpa.KeyUsagePage) (cpaKeyUsagePageResponse, error) {
	if page.Snapshot.ObservedAt.IsZero() || strings.TrimSpace(page.Snapshot.Instance) == "" {
		return cpaKeyUsagePageResponse{}, action.NewError(action.CodeExecutionFailed, "CPA 用量数据源返回了未初始化的快照", nil)
	}
	out := cpaKeyUsagePageResponse{
		BusinessDay:   page.BusinessDay,
		Items:         make([]cpaKeyUsageItem, 0, len(page.Rows)),
		TotalKeyCount: page.TotalKeyCount,
		Truncated:     page.Truncated,
		Snapshot: userDetailSnapshotResponse{
			ObservedAt: page.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano),
			Source:     page.Snapshot.Instance,
			Watermark:  page.Snapshot.Watermark,
			IsPartial:  page.Snapshot.IsPartial,
		},
	}
	for _, row := range page.Rows {
		item := cpaKeyUsageItem{
			APIKeyHash: row.APIKeyHash, Alias: row.Alias,
			RequestCount: row.RequestCount, TokensIn: row.TokensIn, TokensOut: row.TokensOut,
			TokensCacheRead: row.TokensCacheRead, TokensCacheCreation: row.TokensCacheCreation,
			UnpricedRequestCount: row.UnpricedRequestCount,
		}
		item.Cost = moneyOf(row.CostMicros, cpa.Currency)
		if row.LastUsedAt != nil {
			v := row.LastUsedAt.UTC().Format(time.RFC3339Nano)
			item.LastUsedAt = &v
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ListCPAKeysHandler serves GET /api/v1/platforms/cpa/keys?day=.
//
// day defaults to "today" in UTC (see connectors/cpa's package doc on the
// unverified assumption that usage_events.timestamp_ms is UTC epoch
// milliseconds) rather than requiring the caller to compute it — every other
// "day" query param in this codebase leaves that computation to the server
// so a browser's local timezone never leaks into a business-day boundary.
func ListCPAKeysHandler(q CPAKeysQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if q == nil {
			WriteError(w, r, actionCPAUnavailable())
			return
		}
		day := strings.TrimSpace(r.URL.Query().Get("day"))
		if day == "" {
			day = time.Now().UTC().Format("2006-01-02")
		} else if _, err := time.Parse("2006-01-02", day); err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "day 必须形如 YYYY-MM-DD", err))
			return
		}

		page, err := q.KeyUsage(r.Context(), day)
		if err != nil {
			WriteError(w, r, wrapCPAError(err))
			return
		}
		out, err := toCPAKeyUsagePageBody(page)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	}
}
