package sms62

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 本文件是对照官方文档（https://api.62-us.com/api-docs，2026-09-06）补齐的两个
// 接口：商品详情与订单列表。官方只给了参数、没给响应结构，所以响应按宽容方式
// 解析：取得到就有、取不到就空，**商品 ID 与订单 ID 这两个键是钉死的**。

// detailGoodsIDPattern 是商品详情用的两段 ID（平台-国家），官方例：12-1。
//
// 与购买用的三段（平台-国家-天数）**不是同一个东西**：三段传给详情接口是
// 官方 42201 参数错误，本地就挡住，不要打到上游去换一个 422。
var detailGoodsIDPattern = regexp.MustCompile(`^[1-9][0-9]*-[1-9][0-9]*$`)

// 订单列表分页（官方）：page 默认 1；page_size 默认 20、最大 100。
const (
	ordersDefaultPageSize = 20
	ordersMaxPageSize     = 100
)

// GoodsDetail 是单个商品的详情。
type GoodsDetail struct {
	ID    string
	Name  string
	Price string
	// Country 是国家 ID 的文本形态。
	Country string
	Stock   int64
	// Durations 是这个商品可选的天数（三段 ID 里的第三段）。
	// 官方文档没写这个字段叫什么，按常见命名尽力取；取不到就是空，
	// 页面上让人自己填天数。
	Durations []int64
}

// GetGoodsDetail 读商品详情。goodsID 是两段（平台-国家）。
func (c *Client) GetGoodsDetail(ctx context.Context, goodsID string) (GoodsDetail, error) {
	if !detailGoodsIDPattern.MatchString(goodsID) {
		return GoodsDetail{}, connector.NewError(connector.KindRejected,
			"62-US 商品详情的 ID 必须是两段正整数（平台-国家，如 12-1）", nil)
	}
	query := url.Values{"goods_id": []string{goodsID}}

	var raw map[string]any
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/goods/detail?"+query.Encode(), nil, "", &raw); err != nil {
		return GoodsDetail{}, err
	}
	detail := GoodsDetail{
		ID:      scalarString(raw, "goods_id", "id"),
		Name:    sanitizeText(scalarString(raw, "name", "goods_name", "title"), 128),
		Price:   scalarString(raw, "price", "money", "amount"),
		Country: sanitizeText(scalarString(raw, "country", "country_id"), 32),
		Stock:   scalarInt(raw, "stock", "num", "count"),
	}
	if detail.ID == "" {
		detail.ID = goodsID
	}
	for _, key := range []string{"endday", "enddays", "days", "durations"} {
		if list, ok := raw[key].([]any); ok {
			for _, v := range list {
				if n, ok := v.(json.Number); ok {
					if d, err := n.Int64(); err == nil && d > 0 {
						detail.Durations = append(detail.Durations, d)
					}
				}
			}
			break
		}
	}
	return detail, nil
}

// OrderSummary 是订单列表里的一条。
type OrderSummary struct {
	OrderID  string
	GoodsID  string
	Quantity int64
	Status   int64
	// StatusText 是上游给的状态文案；没给就空。
	StatusText string
	// AmountText 是十进制文本，不转 float。
	AmountText string
	CreatedAt  int64
}

// OrdersPage 是一页订单。
type OrdersPage struct {
	Orders   []OrderSummary
	Page     int
	PageSize int
	// Total 是上游报的总条数；取不到就是 0，**不代表没有订单**。
	Total int64
}

// ListOrders 读当前账号的订单分页列表。
//
// page_size 超过官方上限 100 时**收敛到 100 而不是报错**：这是一个只读列表，
// 多要了少给是无害的；而购买数量那种超上限必须报错，因为多买了就是多花钱。
func (c *Client) ListOrders(ctx context.Context, page, pageSize int) (OrdersPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = ordersDefaultPageSize
	}
	if pageSize > ordersMaxPageSize {
		pageSize = ordersMaxPageSize
	}
	query := url.Values{
		"page":      []string{strconv.Itoa(page)},
		"page_size": []string{strconv.Itoa(pageSize)},
	}

	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/orders?"+query.Encode(), nil, "", &raw); err != nil {
		return OrdersPage{}, err
	}
	objects, root, err := collectionObjectsWithRoot(raw, "list", "orders", "items", "data")
	if err != nil {
		return OrdersPage{}, err
	}
	out := OrdersPage{Page: page, PageSize: pageSize}
	if root != nil {
		out.Total = scalarInt(root, "total", "count")
	}
	for _, o := range objects {
		out.Orders = append(out.Orders, OrderSummary{
			OrderID:    scalarString(o, "order_id", "id"),
			GoodsID:    scalarString(o, "goods_id"),
			Quantity:   scalarInt(o, "num", "quantity", "order_num"),
			Status:     scalarInt(o, "status", "order_status"),
			StatusText: sanitizeText(scalarString(o, "status_text"), 64),
			AmountText: scalarString(o, "money", "amount", "price", "total_price"),
			CreatedAt:  scalarInt(o, "create_time", "created_at", "time"),
		})
	}
	return out, nil
}
