package sms62

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 对照官方文档（https://api.62-us.com/api-docs，2026-09-06 抓取）补的测试。
// 官方只给了 order/tokens 的响应结构，其余接口只有参数；所以这里钉死的是
// **参数名、路径与错误码**，响应字段仍按宽容解析（见各方法注释）。

func serve(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return newTestClient(t, srv)
}

// /api/v1/info 的出口 IP 字段官方叫 client_ip。
//
// 此前按冻结源码读的是 ip——真上游会回一个空的出口 IP，而页面上空 IP 看起来
// 只是「这家没给」，人不会怀疑是我们读错了字段。IP 白名单排查全靠这个值。
func TestInfoReadsClientIPField(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":1,"msg":"success","request_id":"r","time":1,"data":{"client_ip":"198.51.100.7","status":1}}`))
	})
	info, err := c.GetInfo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if info.ClientIP != "198.51.100.7" {
		t.Fatalf("ClientIP = %q, want 198.51.100.7", info.ClientIP)
	}
}

// 商品详情：goods_id 是**两段**（平台-国家），与购买用的三段不同。
func TestGoodsDetailSendsTwoSegmentGoodsID(t *testing.T) {
	var gotQuery string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/v1/goods/detail" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":1,"msg":"","request_id":"r","time":1,"data":{"goods_id":"12-1","name":"US","price":"0.35","stock":9,"endday":[7,30]}}`))
	})
	detail, err := c.GetGoodsDetail(t.Context(), "12-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "goods_id=12-1" {
		t.Fatalf("query = %q", gotQuery)
	}
	if detail.ID != "12-1" || detail.Name != "US" || detail.Price != "0.35" || detail.Stock != 9 {
		t.Fatalf("detail = %+v", detail)
	}
}

// 三段 ID 传给详情接口是**参数错误**（官方 42201），本地就该挡住，
// 不要打到上游去换一个 422。
func TestGoodsDetailRejectsThreeSegmentID(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("不该发出请求")
	})
	if _, err := c.GetGoodsDetail(t.Context(), "12-1-7"); err == nil {
		t.Fatal("三段 ID 必须被拒绝")
	}
}

// 订单列表：page 默认 1，page_size 默认 20、最大 100（官方）。
func TestListOrdersClampsPageSizeToOfficialMax(t *testing.T) {
	var gotQuery string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":1,"msg":"","request_id":"r","time":1,"data":{"list":[{"order_id":4213,"goods_id":"12-1-7","num":2,"status":3,"status_text":"完成","money":"0.70","create_time":1757000000}],"total":1}}`))
	})
	page, err := c.ListOrders(t.Context(), 1, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "page_size=100") || !strings.Contains(gotQuery, "page=1") {
		t.Fatalf("query = %q, want page=1&page_size=100", gotQuery)
	}
	if len(page.Orders) != 1 || page.Orders[0].OrderID != "4213" || page.Orders[0].Quantity != 2 {
		t.Fatalf("orders = %+v", page.Orders)
	}
	if page.Total != 1 {
		t.Fatalf("total = %d", page.Total)
	}
}

// 官方错误码要翻成**能指路**的中文。
//
// 40304 与 42204 的下一步完全不同（去加 IP 白名单 vs 去充值），而上游的
// msg 是英文短语；页面上只显示「业务错误 code=40304」等于什么都没说。
func TestBusinessErrorHintsFollowOfficialTable(t *testing.T) {
	for code, want := range map[int64]string{
		40304: "白名单",
		42204: "余额",
		42205: "库存",
		40412: "订单",
	} {
		c := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":` + itoa(code) + `,"msg":"x","request_id":"r","time":1,"data":{}}`))
		})
		_, err := c.GetInfo(t.Context())
		var business *BusinessError
		if !errors.As(err, &business) {
			t.Fatalf("code %d: 期望 BusinessError, got %T", code, err)
		}
		if !strings.Contains(business.Hint(), want) {
			t.Errorf("code %d: hint %q 不含 %q", code, business.Hint(), want)
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
