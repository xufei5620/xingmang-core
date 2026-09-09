package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

type fakeBalanceReader struct {
	byAccount map[string]infini.AccountBalances
	errs      map[string]error
}

func (f fakeBalanceReader) AccountBalances(ctx context.Context, account string) (infini.AccountBalances, error) {
	if err, ok := f.errs[account]; ok {
		return infini.AccountBalances{}, err
	}
	return f.byAccount[account], nil
}

func serveBalances(r CardBalanceReader, accounts []string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	CardBalancesHandler(r, accounts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cards/balances", nil))
	return rec
}

// 逐账号返回可用余额。
//
// 这是**实时上游调用**而不是投影：资金池余额没有对应的同步作业，
// 也不该有——它变化频繁而且只在人要看的时候才重要。
func TestCardBalancesReturnsPerAccount(t *testing.T) {
	rec := serveBalances(fakeBalanceReader{byAccount: map[string]infini.AccountBalances{
		"CHRIS":   {USDT: "55.68", USDC: "0", USD: "0"},
		"LINFENG": {USDT: "120.50", USDC: "75.25", USD: "1000.00"},
	}}, []string{"CHRIS", "LINFENG"})

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Account string `json:"account"`
			USDT    string `json:"usdt"`
			Error   string `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("应逐账号返回, got %+v", body.Items)
	}
	// 顺序跟着配置走，不按 map 迭代——否则页面上两个账号会随机换位置。
	if body.Items[0].Account != "CHRIS" || body.Items[1].Account != "LINFENG" {
		t.Fatalf("账号顺序应与配置一致, got %+v", body.Items)
	}
	if body.Items[0].USDT != "55.68" {
		t.Fatalf("余额没透出, got %+v", body.Items[0])
	}
}

// 一个账号取不到余额，另一个照常返回。
//
// 两个账号的凭据、权限、IP 白名单都是各自独立的：一个配歪了不该让另一个
// 也看不到数。整页 500 会让运营以为「余额功能坏了」，而实际上只坏了一半。
func TestCardBalancesDegradesPerAccount(t *testing.T) {
	rec := serveBalances(fakeBalanceReader{
		byAccount: map[string]infini.AccountBalances{"LINFENG": {USDT: "1"}},
		errs:      map[string]error{"CHRIS": errors.New("ip not in whitelist")},
	}, []string{"CHRIS", "LINFENG"})

	if rec.Code != http.StatusOK {
		t.Fatalf("单账号失败不该让整页 500, got %d", rec.Code)
	}
	var body struct {
		Items []struct {
			Account string `json:"account"`
			USDT    string `json:"usdt"`
			Error   string `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Items[0].Error == "" {
		t.Fatalf("失败的账号要如实说它失败了, got %+v", body.Items[0])
	}
	// 失败原因只给分类，不回上游原文（ADR-004）。
	if strings.Contains(body.Items[0].Error, "whitelist") {
		t.Fatalf("不该把上游原文回给前端, got %q", body.Items[0].Error)
	}
	if body.Items[1].USDT != "1" {
		t.Fatalf("另一个账号应照常返回, got %+v", body.Items[1])
	}
}
