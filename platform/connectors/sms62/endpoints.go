package sms62

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// goodsIDPattern 是购买用的商品 ID 形状：三段正整数。
//
// 官方文档（2026-09-06）钉死了语义：**平台-国家-天数**，例 12-1-7；
// 商品详情接口用的是前两段（平台-国家，例 12-1）。所以两段 ID 要变成可购买的
// 三段，必须由人补上天数——代码不替人挑一个默认天数，那等于替人决定花多少钱。
var goodsIDPattern = regexp.MustCompile(`^[1-9][0-9]*-[1-9][0-9]*-[1-9][0-9]*$`)

// 数量上下限。
//
// 1–200 **现在是官方声明的上限**（文档：num 默认 1，最大 200；2026-09-06 核对）。
// 此前交接包说这只是实现方自己的安全上限，那句话过时了。
const (
	minQuantity = 1
	maxQuantity = 200
	// maxMessages 是取码一次最多读多少条。官方：limit 默认 5，最大 20。
	maxMessages = 20
)

// GoodsItem 是库存里的一项。
//
// 字段刻意少：这份投影只用来让人挑一个商品去买，多带的字段既没人看，
// 也会在上游改形状时变成一处要维护的猜测。
type GoodsItem struct {
	ID      string
	Name    string
	Price   string
	Country string
	Stock   int64
}

// ListGoods 读库存。
//
// 两个筛选参数都是**整数**（上游叫 pingtai_id / country）；传 0 表示不筛。
// 展示字段（名称、价格、库存）的键名参考实现没有钉死——它只钉了 goods_id，
// 其余按常见命名尽力取，取不到就是空。这一点值得知道：真实上游若用别的
// 键名，这几列会是空的，而**商品 ID 仍然是对的**，不影响购买。
func (c *Client) ListGoods(ctx context.Context, platformID, country int64) ([]GoodsItem, error) {
	if platformID < 0 || country < 0 {
		return nil, connector.NewError(connector.KindRejected, "62-US 库存筛选参数非法", nil)
	}
	query := url.Values{}
	if platformID > 0 {
		query.Set("pingtai_id", strconv.FormatInt(platformID, 10))
	}
	if country > 0 {
		query.Set("country", strconv.FormatInt(country, 10))
	}
	path := "/api/v1/goods"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, path, nil, "", &raw); err != nil {
		return nil, err
	}
	objects, err := collectionObjects(raw, "goods", "items", "list", "data")
	if err != nil {
		return nil, err
	}

	out := make([]GoodsItem, 0, len(objects))
	for _, o := range objects {
		out = append(out, GoodsItem{
			ID:      scalarString(o, "goods_id", "id"),
			Name:    sanitizeText(scalarString(o, "name", "goods_name", "title"), 128),
			Price:   scalarString(o, "price", "money", "amount"),
			Country: sanitizeText(scalarString(o, "country", "country_code"), 32),
			Stock:   scalarInt(o, "stock", "num", "count"),
		})
	}
	return out, nil
}

// PurchaseInput 是一次购买的输入。
type PurchaseInput struct {
	GoodsID string
	Num     int
	// FirstNumber / NoFirstNumber 是号段前缀筛选，可选。
	FirstNumber   string
	NoFirstNumber string
}

// PurchaseResult 是购买的**全部**返回：一个订单 ID。
//
// **注意这里没有号码。** 62 是订单制：买完只回订单 ID，号码与 token 要再读
// 一次 GetOrderTokens 才拿得到。那第二次读失败时（哪怕是 4xx），钱已经花了，
// 调用方必须落 unknown 交人工核对，绝不能重试购买。
type PurchaseResult struct {
	ProviderMeta
	OrderID string
}

// PurchaseNumbers 买号。**这是花钱的写操作，没有任何重试。**
func (c *Client) PurchaseNumbers(ctx context.Context, in PurchaseInput) (PurchaseResult, error) {
	goodsID := strings.TrimSpace(in.GoodsID)
	if !goodsIDPattern.MatchString(goodsID) {
		return PurchaseResult{}, connector.NewError(connector.KindRejected,
			"62-US 商品 ID 必须是三段正整数（如 1-2-3）", nil)
	}
	if in.Num < minQuantity || in.Num > maxQuantity {
		return PurchaseResult{}, connector.NewError(connector.KindRejected,
			fmt.Sprintf("62-US 购买数量必须在 %d–%d 之间", minQuantity, maxQuantity), nil)
	}
	for _, v := range []string{in.FirstNumber, in.NoFirstNumber} {
		if !validFilterText(v) {
			return PurchaseResult{}, connector.NewError(connector.KindRejected, "62-US 号段筛选非法", nil)
		}
	}

	form := url.Values{}
	form.Set("goods_id", goodsID)
	form.Set("num", strconv.Itoa(in.Num))
	if s := strings.TrimSpace(in.FirstNumber); s != "" {
		form.Set("first_number", s)
	}
	if s := strings.TrimSpace(in.NoFirstNumber); s != "" {
		form.Set("no_first_number", s)
	}

	var data struct {
		OrderID json.RawMessage `json:"order_id"`
	}
	meta, err := c.do(ctx, http.MethodPost, "/api/v1/get",
		[]byte(form.Encode()), "application/x-www-form-urlencoded", &data)
	if err != nil {
		return PurchaseResult{}, err
	}
	orderID := providerIDFromJSON(data.OrderID)
	if orderID == "" {
		// 买成功了但订单 ID 解不出来——这是最坏的一种成功：钱花了，
		// 而我们连去哪儿对账都不知道。判成协议错误让它落 unknown。
		return PurchaseResult{}, &ProtocolError{Kind: "购买响应缺少订单 ID"}
	}
	return PurchaseResult{ProviderMeta: meta, OrderID: orderID}, nil
}

// OrderTokens 是一个订单的补读结果。
//
// **响应是结构化的，不是通用集合**：它带 order_id / order_status /
// order_num / total 四个自校验字段，而 total 必须等于 tokens 的条数。
// 上游自己给了这个交叉校验，不用就浪费了——数量对不上时我们宁可判协议
// 错误让它落 unknown，也不要把半份号码当成全部收下。
type OrderTokens struct {
	ProviderMeta
	OrderID string
	// OrderStatus / OrderNum 是上游对这个订单的说法。
	// OrderNum 是下单数量，Total 是本次返回的 token 数——
	// 前者小于后者说明上游给多了，那是协议异常。
	OrderStatus int64
	OrderNum    int
	Numbers     []OrderNumber
}

// OrderNumber 是订单里的一个号码。
type OrderNumber struct {
	// Phone 是完整号码。上游字段名是 number（不是 phone）。
	Phone string
	// Token 是这个号码的取码凭证。**取码必须用它**，而它本身是敏感值：
	// 不进日志、不进对外 DTO，落库时也只有取码那条路会读它。
	Token string
	// PlatformID / PlatformText 是这个号所属的平台（上游叫 pingtai）。
	PlatformID   int64
	PlatformText string
	Status       int64
	StatusText   string
}

// GetOrderTokens 按订单读回号码与 token。
//
// 买完必须调它才拿得到号码。三处校验都不能省：
//
//  1. **返回的 order_id 要与请求的一致**——上游一次串号会让我们把别人的
//     号码记到自己的订单上，而那种错不会报错；
//  2. total 必须等于 tokens 的条数；
//  3. order_num（下单量）不能小于 total（返回量）。
func (c *Client) GetOrderTokens(ctx context.Context, orderID string) (OrderTokens, error) {
	id := strings.TrimSpace(orderID)
	if id == "" || !validFilterText(id) {
		return OrderTokens{}, connector.NewError(connector.KindRejected, "62-US 订单 ID 非法", nil)
	}
	query := url.Values{"order_id": []string{id}}

	var data struct {
		OrderID     json.RawMessage `json:"order_id"`
		OrderStatus json.RawMessage `json:"order_status"`
		OrderNum    json.RawMessage `json:"order_num"`
		Total       json.RawMessage `json:"total"`
		Tokens      []struct {
			Token        string          `json:"token"`
			Number       string          `json:"number"`
			PlatformID   json.RawMessage `json:"pingtai"`
			PlatformText string          `json:"pingtai_text"`
			Status       json.RawMessage `json:"status"`
			StatusText   string          `json:"status_text"`
		} `json:"tokens"`
	}
	meta, err := c.do(ctx, http.MethodGet, "/api/v1/order/tokens?"+query.Encode(), nil, "", &data)
	if err != nil {
		return OrderTokens{}, err
	}

	responseOrderID := providerIDFromJSON(data.OrderID)
	if responseOrderID == "" || responseOrderID != id {
		return OrderTokens{}, &ProtocolError{Kind: "返回的订单 ID 与请求不一致"}
	}
	orderStatus, _ := decodeJSONInteger(data.OrderStatus)
	orderNum, numErr := decodeJSONInteger(data.OrderNum)
	total, totalErr := decodeJSONInteger(data.Total)
	if numErr != nil || totalErr != nil ||
		int(total) != len(data.Tokens) || orderNum < total || total > maxQuantity {
		return OrderTokens{}, &ProtocolError{Kind: "订单号码数量自校验不通过"}
	}

	numbers := make([]OrderNumber, 0, len(data.Tokens))
	for _, raw := range data.Tokens {
		if strings.TrimSpace(raw.Token) == "" || strings.TrimSpace(raw.Number) == "" {
			return OrderTokens{}, &ProtocolError{Kind: "订单号码缺少 number 或 token"}
		}
		platformID, _ := decodeJSONInteger(raw.PlatformID)
		status, _ := decodeJSONInteger(raw.Status)
		numbers = append(numbers, OrderNumber{
			Phone:        strings.TrimSpace(raw.Number),
			Token:        strings.TrimSpace(raw.Token),
			PlatformID:   platformID,
			PlatformText: sanitizeText(raw.PlatformText, 128),
			Status:       status,
			StatusText:   sanitizeText(raw.StatusText, 128),
		})
	}
	return OrderTokens{
		ProviderMeta: meta, OrderID: responseOrderID,
		OrderStatus: orderStatus, OrderNum: int(orderNum), Numbers: numbers,
	}, nil
}

// Message 是一条短信。
//
// **正文不落库**：它只在这次调用的返回值里存在，用完就丢。持久层没有正文列。
type Message struct {
	Text string
	// StructuredCode 是上游自己给的验证码字段；有它就不用从正文里猜。
	StructuredCode string
	Sender         string
	ReceivedAt     int64
}

// GetMessages 读某个号码的短信。
//
// token 进 query 是这家协议的要求（与 Hero 不同，Hero 的密钥只在 Header）。
// 因此**不能笼统说"我们所有请求的 URL 都不含敏感值"**——这一条是例外，
// 它会进上游的访问日志。
func (c *Client) GetMessages(ctx context.Context, token string, limit int) ([]Message, error) {
	if strings.TrimSpace(token) == "" || !validFilterText(token) {
		return nil, connector.NewError(connector.KindRejected, "62-US 取码 token 非法", nil)
	}
	if limit < 1 || limit > maxMessages {
		limit = maxMessages
	}
	query := url.Values{"limit": []string{strconv.Itoa(limit)}, "token": []string{token}}

	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/msg?"+query.Encode(), nil, "", &raw); err != nil {
		return nil, err
	}
	objects, err := collectionObjects(raw, "messages", "items", "list", "data")
	if err != nil {
		return nil, err
	}
	if len(objects) > maxMessages {
		return nil, &ProtocolError{Kind: "短信条数超出上限"}
	}

	out := make([]Message, 0, len(objects))
	for _, o := range objects {
		text := scalarString(o, "message", "msg", "content", "sms")
		code := scalarString(o, "verification_code", "sms_code", "otp", "code")
		if code != "" && !isCodeCandidate(code) {
			// 上游那个字段里装的不是验证码形状的东西，就当没有——
			// 把一段商户名当验证码填进去，比没有验证码糟得多。
			code = ""
		}
		out = append(out, Message{
			Text:           text,
			StructuredCode: code,
			Sender:         sanitizeText(scalarString(o, "sender", "from"), 128),
			ReceivedAt:     scalarInt(o, "time", "timestamp", "created_at"),
		})
	}
	return out, nil
}

// validFilterText 挡住控制字符与超长值。
func validFilterText(v string) bool {
	if len(v) > 256 {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
