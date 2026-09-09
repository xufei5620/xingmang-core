package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Sub2API routes and field shapes below are the same ones already verified
// against source and used in the real (v1 ListUsers) client - see
// connectors/platformusers/upstream.go's package doc comment and
// EV-2026-08-27-sub2api-read-survey.md. This file does not re-derive them;
// it only redacts what those requests return.
const (
	sub2apiRouteVersion = "/api/v1/admin/system/version"
	sub2apiRouteUsers   = "/api/v1/admin/users"
)

// sub2apiUserPolicy is the complete allowlist of fields this tool will ever
// write into a Sub2API user evidence sample. Anything else Sub2API's admin
// API returns - role, frozen_balance, concurrency, allowed_groups, notes,
// group_rates, or any field added in a version newer than the one this was
// written against - is dropped by redactItem before it ever reaches a
// FieldRedactor. See connectors/platformusers/upstream.go sub2apiUserItem's
// own "刻意只解这六个" comment for the precedent this mirrors.
var sub2apiUserPolicy = ItemPolicy{
	"id":             redactIDField(),
	"email":          redactEmailField(),
	"username":       redactNameField(),
	"status":         keepRaw(),
	"balance":        redactAmountField(),
	"last_active_at": keepRaw(),
}

// sub2apiEnvelope mirrors the verified real shape of every /api/v1 response:
// {code, message, data} (connectors/platformusers/upstream.go sub2apiEnvelope,
// backend/internal/pkg/response/response.go Success/Paginated).
type sub2apiEnvelope struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// sub2apiPageData mirrors response.PaginatedData: {items, total, ...}. Only
// items/total are read; any other pagination field (page/page_size/pages)
// is not something this tool needs to redact since it is small non-PII
// metadata this tool itself controls the input side of (it asked for page=1).
type sub2apiPageData struct {
	Items []json.RawMessage `json:"items"`
	Total json.RawMessage   `json:"total"`
}

func sub2apiCheckEnvelope(env sub2apiEnvelope) error {
	code := strings.Trim(strings.TrimSpace(string(env.Code)), `"`)
	if code != "" && code != "0" {
		return fmt.Errorf("evidence-capture: sub2api upstream reported business code %s", code)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("evidence-capture: sub2api response missing data field")
	}
	return nil
}

// parseSub2APIVersion extracts the version string from a
// GET /api/v1/admin/system/version response body. That route is documented
// to return only one field (connectors/sub2api/upstream.go fetchVersion:
// "这条路由只给 version 一个字段"), so there is nothing else to redact here -
// the version string itself is not sensitive, it is the primary evidence
// SUB2_REAL_APPROVAL asks for.
func parseSub2APIVersion(body []byte) (string, error) {
	var env sub2apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("evidence-capture: sub2api version response is not valid JSON: %w", err)
	}
	if err := sub2apiCheckEnvelope(env); err != nil {
		return "", err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		return "", fmt.Errorf("evidence-capture: sub2api version response data is not the expected shape: %w", err)
	}
	v := strings.TrimSpace(payload.Version)
	if v == "" {
		return "", fmt.Errorf("evidence-capture: sub2api version response had an empty version string")
	}
	return v, nil
}

// redactSub2APIUsersPage redacts a GET /api/v1/admin/users response body.
// The returned envelope is a full {code,message,data:{items,total}} body -
// the same shape connectors/platformusers/testdata/sub2api/users_page.redacted.json
// is specified to hold
// (docs/superpowers/plans/2026-08-28-platform-user-read-v2.md Task 4 Step 2:
// parseSub2UsersPage reads a page body directly) - so this file can be
// copied there without reshaping. detail is a single flat redacted user
// object (not envelope-wrapped), or nil if the page had zero items.
func redactSub2APIUsersPage(body []byte, salt []byte) (envelope, detail json.RawMessage, kept, dropped []string, err error) {
	var env sub2apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: sub2api users page response is not valid JSON: %w", err)
	}
	if err := sub2apiCheckEnvelope(env); err != nil {
		return nil, nil, nil, nil, err
	}
	var page sub2apiPageData
	if err := json.Unmarshal(env.Data, &page); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: sub2api users page data is not the expected {items,total} shape: %w", err)
	}

	redactedItems, kept, dropped, detail, err := redactItemsPage(page.Items, sub2apiUserPolicy, salt)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	redactedData, err := json.Marshal(sub2apiPageData{Items: redactedItems, Total: page.Total})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: failed to re-marshal sub2api page data: %w", err)
	}
	envelope, err = json.Marshal(sub2apiEnvelope{Code: json.RawMessage("0"), Message: "", Data: redactedData})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("evidence-capture: failed to re-marshal sub2api envelope: %w", err)
	}
	return envelope, detail, kept, dropped, nil
}
