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
// **只校验形状，不推导语义**：交接包明确说源码只校验形状，没有给出足以据此
// 生成 ID 的业务映射；而商品详情接口只接受两段 ID。所以不能把两段扩成三段，
// 那样拼出来的 ID 会指向别的商品——买错了钱照花。
var goodsIDPattern = regexp.MustCompile(`^[1-9][0-9]*-[1-9][0-9]*-[1-9][0-9]*$`)

// 数量上下限。
//
// **1–200 是上游实现方自己的安全上限，不是官方声明的最大值**（交接包 02 章
// 明说了这一点）。写在这里是为了不让一次手滑的 2000 变成两千个号的账单。
const (
	minQuantity = 1
	maxQuantity = 200
	// maxMessages 是取码一次最多读多少条。交接包用 20。
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
func (c *Client) ListGoods(ctx context.Context, platformID, country string) ([]GoodsItem, error) {
	query := url.Values{}
	if platformID != "" {
		query.Set("pingtai_id", platformID)
	}
	if country != "" {
		query.Set("country", country)
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

// OrderNumber 是订单里的一个号码。
type OrderNumber struct {
	// Phone 是完整号码。
	Phone string
	// Token 是这个号码的取码凭证。**取码必须用它**，而它本身是敏感值。
	Token string
}

// GetOrderTokens 按订单读回号码与 token。
//
// 买完必须调它才拿得到号码。**返回的订单 ID 要与请求的一致**：不校验的话，
// 上游一次串号会让我们把别人的号码记到自己的订单上。
func (c *Client) GetOrderTokens(ctx context.Context, orderID string) ([]OrderNumber, error) {
	id := strings.TrimSpace(orderID)
	if id == "" || !validFilterText(id) {
		return nil, connector.NewError(connector.KindRejected, "62-US 订单 ID 非法", nil)
	}
	query := url.Values{"order_id": []string{id}}

	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/order/tokens?"+query.Encode(), nil, "", &raw); err != nil {
		return nil, err
	}
	objects, root, err := collectionObjectsWithRoot(raw, "tokens", "items", "list", "data")
	if err != nil {
		return nil, err
	}
	// 上游在根对象上回了订单 ID 就核一遍；没回就不假装校验过。
	if got := scalarString(root, "order_id"); got != "" && got != id {
		return nil, &ProtocolError{Kind: "返回的订单 ID 与请求不一致"}
	}

	out := make([]OrderNumber, 0, len(objects))
	for _, o := range objects {
		phone := scalarString(o, "phone", "number", "phone_number")
		token := scalarString(o, "token", "order_token")
		if phone == "" || token == "" {
			return nil, &ProtocolError{Kind: "订单号码缺少 phone 或 token"}
		}
		out = append(out, OrderNumber{Phone: phone, Token: token})
	}
	return out, nil
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
