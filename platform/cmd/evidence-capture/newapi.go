package main

import (
	"encoding/json"
	"fmt"
)

// NewAPI routes and field shapes below are the same ones already verified
// against source and used in the real (v1 ListUsers) client - see
// connectors/platformusers/upstream.go's package doc comment and
// EV-2026-08-27-newapi-read-survey.md.
const (
	// newapiRouteUsers keeps the trailing slash on purpose: router/api-router.go
	// registers "/", and this tool's read-only transport refuses redirects
	// (see httpfetch.go), so a dropped slash would fail closed as a 301
	// rather than silently following it - but there is no reason to pay
	// even that failure when the correct path is known.
	newapiRouteUsers  = "/api/user/"
	newapiRouteStatus = "/api/status"
)

// newapiUserPolicy is the complete allowlist of fields this tool will ever
// write into a NewAPI user evidence sample. Anything else NewAPI's user
// list returns - github_id, discord_id, wechat_id, telegram_id, oidc_id,
// linux_do_id, remark, setting, stripe_customer, aff_* and the rest of
// model/user.go's ~30 columns - is dropped by redactItem before it ever
// reaches a FieldRedactor. See connectors/platformusers/upstream.go
// newapiUserItem's own "刻意只解这七个" comment for the precedent this
// mirrors. "DeletedAt" (capital D, no json tag override on the upstream
// gorm.DeletedAt field) is kept as-is: it is null or a soft-delete marker,
// never PII, and is exactly the evidence NEWAPI_REAL_APPROVAL asks for
// ("soft-delete 形态").
var newapiUserPolicy = ItemPolicy{
	"id":            redactIDField(),
	"username":      redactNameField(),
	"display_name":  redactNameField(),
	"email":         redactEmailField(),
	"status":        keepRaw(),
	"quota":         redactAmountField(),
	"last_login_at": keepRaw(),
	"DeletedAt":     keepRaw(),
}

// newapiEnvelope mirrors the verified real shape of every NewAPI response:
// {success, message, data}. Business failure is reported as HTTP 200 with
// success:false (connectors/platformusers/upstream.go newapiEnvelope
// warning) - callers must check Success, not just the HTTP status.
type newapiEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// newapiPageData mirrors common.PageInfo: {items, total, ...page metadata}.
type newapiPageData struct {
	Items []json.RawMessage `json:"items"`
	Total json.RawMessage   `json:"total"`
}

func newapiCheckEnvelope(env newapiEnvelope) error {
	if !env.Success {
		return fmt.Errorf("evidence-capture: newapi upstream reported success=false: %s", env.Message)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("evidence-capture: newapi response missing data field")
	}
	return nil
}

// newapiStatus is the evidence this tool needs out of GET /api/status: the
// version string (this route is NewAPI's only version source - "唯一的
// 版本来源", connectors/platformusers/upstream.go) and quota_per_unit (the
// runtime-mutable USD conversion basis NEWAPI_REAL_APPROVAL's evidence door
// requires be documented, design doc §10). Neither value is sensitive -
// quota_per_unit is an instance-wide setting, not tied to any user.
type newapiStatus struct {
	Version      string `json:"version"`
	QuotaPerUnit int64  `json:"quota_per_unit"`
}

// parseNewAPIStatus extracts version + quota_per_unit from a GET /api/status
// response body. This route does not require authentication upstream
// (connectors/newapi/upstream.go fetchHealth: "这条路由不需要鉴权"), but
// this tool authorizes every request the same way regardless (see
// httpfetch.go) - sending an unneeded credential header to a public route
// is harmless and keeps the request path uniform across probes.
func parseNewAPIStatus(body []byte) (newapiStatus, error) {
	var env newapiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return newapiStatus{}, fmt.Errorf("evidence-capture: newapi status response is not valid JSON: %w", err)
	}
	if err := newapiCheckEnvelope(env); err != nil {
		return newapiStatus{}, err
	}
	var payload struct {
		Version      string          `json:"version"`
		QuotaPerUnit json.RawMessage `json:"quota_per_unit"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		return newapiStatus{}, fmt.Errorf("evidence-capture: newapi status response data is not the expected shape: %w", err)
	}
	unit, ok := rawJSONScalarAsString(payload.QuotaPerUnit)
	if !ok {
		return newapiStatus{}, fmt.Errorf("evidence-capture: newapi status response is missing quota_per_unit")
	}
	var out newapiStatus
	out.Version = payload.Version
	if _, err := fmt.Sscanf(unit, "%d", &out.QuotaPerUnit); err != nil {
		return newapiStatus{}, fmt.Errorf("evidence-capture: newapi quota_per_unit %q is not an integer: %w", unit, err)
	}
	if out.QuotaPerUnit <= 0 {
		// Documented upstream failure mode: the option write path can drop a
		// bad ParseFloat and leave this at 0 (connectors/newapi/upstream.go
		// quotaPerUnit's comment). Reporting it as evidence anyway would let
		// a reviewer believe 0 is a real observed conversion basis.
		return newapiStatus{}, fmt.Errorf("evidence-capture: newapi quota_per_unit=%d, cannot be used as a conversion basis", out.QuotaPerUnit)
	}
	return out, nil
}

// redactNewAPIUsersPage redacts a GET /api/user/ response body. The
// returned envelope is a full {success,message,data:{items,total}} body -
// the same shape connectors/platformusers/testdata/newapi/users_page.redacted.json
// is specified to hold (parseNewAPIUsersPage reads a page body directly,
// docs/superpowers/plans/2026-08-28-platform-user-read-v2.md Task 5 Step 2)
// - so this file can be copied there without reshaping. detail is a single
// flat redacted user object (not envelope-wrapped), or nil if the page had
// zero items after soft-deleted rows are... note: this tool does NOT drop
// soft-deleted rows the way the future real client will (design plan Task 5
// Step 4: "soft-deleted 用户不命中") - it keeps them in the sample, because
// showing a reviewer what a soft-deleted row's DeletedAt actually looks like
// on the wire *is* part of the evidence NEWAPI_REAL_APPROVAL asks for.
func redactNewAPIUsersPage(body []byte, salt []byte) (envelope, detail json.RawMessage, kept, dropped []string, err error) {
	var env newapiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: newapi users page response is not valid JSON: %w", err)
	}
	if err := newapiCheckEnvelope(env); err != nil {
		return nil, nil, nil, nil, err
	}
	var page newapiPageData
	if err := json.Unmarshal(env.Data, &page); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: newapi users page data is not the expected {items,total} shape: %w", err)
	}

	redactedItems, kept, dropped, detail, err := redactItemsPage(page.Items, newapiUserPolicy, salt)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	redactedData, err := json.Marshal(newapiPageData{Items: redactedItems, Total: page.Total})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: failed to re-marshal newapi page data: %w", err)
	}
	envelope, err = json.Marshal(newapiEnvelope{Success: true, Message: "", Data: redactedData})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: failed to re-marshal newapi envelope: %w", err)
	}
	return envelope, detail, kept, dropped, nil
}
