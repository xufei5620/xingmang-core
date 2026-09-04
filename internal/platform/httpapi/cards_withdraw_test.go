package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
)

type fakeWithdrawQuerier struct {
	addresses []cards.WithdrawAddress
	records   []cards.WithdrawRecord
	err       error
}

func (f fakeWithdrawQuerier) ListWithdrawAddresses(ctx context.Context) ([]cards.WithdrawAddress, error) {
	return f.addresses, f.err
}

func (f fakeWithdrawQuerier) RecentWithdrawals(ctx context.Context, limit int) ([]cards.WithdrawRecord, error) {
	return f.records, f.err
}

func serveWithdrawAddresses(q WithdrawQuerier) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ListWithdrawAddressesHandler(q).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/v1/cards/withdraw/addresses", nil))
	return rec
}

// 地址清单按账号返回，且**带完整地址**。
//
// 与审计摘要那边刻意不同：审计摘要会被广泛展示，所以不带地址；
// 这个端点由 fund.withdraw 把守，而能提现的人本来就要核对地址——
// 让他去别处查地址，等于逼他在页面之外做核对。
func TestWithdrawAddressesReturnFullAddress(t *testing.T) {
	rec := serveWithdrawAddresses(fakeWithdrawQuerier{addresses: []cards.WithdrawAddress{
		{ID: "a1", Account: "LINFENG", Chain: "TRON", Address: "TFullAddress123", Label: "冷钱包"},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			ID      string `json:"address_id"`
			Account string `json:"account"`
			Chain   string `json:"chain"`
			Address string `json:"address"`
			Label   string `json:"label"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Address != "TFullAddress123" {
		t.Fatalf("应带完整地址, got %+v", body.Items)
	}
	if body.Items[0].Label != "冷钱包" {
		t.Fatalf("标签应回传, got %+v", body.Items[0])
	}
}

// 空清单回空数组而不是 null：前端 .map 一个 null 会白屏。
func TestWithdrawAddressesEmptyIsArray(t *testing.T) {
	rec := serveWithdrawAddresses(fakeWithdrawQuerier{})
	if got := rec.Body.String(); !json.Valid([]byte(got)) || got == "" {
		t.Fatalf("响应非法: %q", got)
	}
	var body struct {
		Items []any `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Items == nil {
		t.Fatal("空清单必须是 []，不是 null")
	}
}

// 提现历史带状态与时间，时间统一 RFC3339 UTC。
func TestRecentWithdrawalsSerializesTimes(t *testing.T) {
	at := time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	ListWithdrawalsHandler(fakeWithdrawQuerier{records: []cards.WithdrawRecord{{
		RequestID: "w-1", Account: "LINFENG", Chain: "TRON", TokenType: "USDT",
		Amount: "100", AddressID: "a1", Address: "TFullAddress123",
		Status: "completed", TxHash: "0xabc", StartedAt: at, UpdatedAt: at,
	}}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cards/withdrawals", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			RequestID string `json:"request_id"`
			Status    string `json:"status"`
			TxHash    string `json:"tx_hash"`
			StartedAt string `json:"started_at"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("应返回一条, got %+v", body.Items)
	}
	if body.Items[0].StartedAt != "2026-09-05T08:30:00Z" {
		t.Fatalf("时间应为 RFC3339 UTC, got %q", body.Items[0].StartedAt)
	}
	if body.Items[0].TxHash != "0xabc" {
		t.Fatalf("链上哈希应回传（人要拿它去区块浏览器核对）, got %+v", body.Items[0])
	}
}
