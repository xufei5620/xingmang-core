package herosms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// servicePattern 是服务代号：2–4 位小写字母或数字。
var servicePattern = regexp.MustCompile(`^[a-z0-9]{2,4}$`)

// normalizeVerificationType 只认官方枚举 sms / call；空 = sms。
func normalizeVerificationType(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "sms":
		return "sms", nil
	case "call":
		return "call", nil
	default:
		return "", connector.NewError(connector.KindRejected, "Hero-SMS 验证类型只接受 sms / call", nil)
	}
}

const (
	minQuantity = 1
	// maxQuantity 同 62：**这是我们自己的安全上限，不是官方声明的最大值**。
	maxQuantity = 200
	maxCountry  = 999
	// lookupMaxPages / lookupPageSize 是按 activation ID 反查时的扫描范围。
	//
	// 扫完还没找到时**必须区分两种情形**：翻到不满的一页说明确实没有；
	// 连续扫满 lookupMaxPages 页说明是我们没扫够，那时不能回"不存在"。
	lookupMaxPages = 5
	lookupPageSize = 100
)

// Activation 是一个已购买的号码。
type Activation struct {
	ID      string
	Phone   string
	Service string
	Country int64
	Status  string
	// Price 是十进制文本。**不转 float**：金额全程文本，与全仓一致。
	Price     string
	CreatedAt string
	ExpiresAt string
	// 以下四个来自官方 ActivationSchema（2026-09-06 核对），冻结源码里没有。
	Operator         string
	CountryPhoneCode int64
	// VerificationType 是 sms / call。
	VerificationType string
	// Subtype：1 = 普通激活，2 = 租用（官方 ActivationSubtype）。
	Subtype int64
}

// Offer 是库存里的一项。
//
// 三个价格与库存数是**不同的事实**，刻意分开保留：把 default 当成"本次成交价"
// 会让人按一个不成立的数字做预算。缺失就是缺失，不补零。
type Offer struct {
	Service        string
	Country        int64
	DefaultPrice   string
	RetailPrice    string
	MinimumPrice   string
	AvailableCount int64
}

// TestConnection 是只读的连接测试。
//
// 用列一页 activation 而不是查余额：这家没有余额端点，硬凑一个"余额"出来
// 只会是编的。它证明的只有"密钥能过鉴权"，不证明账户里有钱。
func (c *Client) TestConnection(ctx context.Context) error {
	_, _, err := c.ListActivations(ctx, 1, 1)
	return err
}

// ListOffers 读库存。
//
// **响应是 service → country → offer 的嵌套 map，不是数组。** 这一点值得
// 单独说：按数组解会拿到空集合而不是报错，于是「库存为空」和「解析方式错了」
// 长得一模一样。
func (c *Client) ListOffers(ctx context.Context, services, countries string) ([]Offer, error) {
	return c.ListOffersByType(ctx, "sms", services, countries)
}

// ListOffersByType 按验证类型读库存。verificationType 是官方路径参数，只认
// sms / call；别的值本地拒绝，不打到上游去换一个 404。
func (c *Client) ListOffersByType(ctx context.Context, verificationType, services, countries string) ([]Offer, error) {
	kind, err := normalizeVerificationType(verificationType)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if services != "" {
		query.Set("services", services)
	}
	if countries != "" {
		query.Set("countries", countries)
	}
	path := "/activations/offers/" + kind
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var body struct {
		Data map[string]map[string]wireOffer `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return nil, err
	}

	out := make([]Offer, 0, len(body.Data))
	for service, byCountry := range body.Data {
		for country, offer := range byCountry {
			countryCode, _ := strconv.ParseInt(country, 10, 64)
			out = append(out, Offer{
				Service: service,
				Country: countryCode,
				// 三个价格是**不同的事实**，分开保留：把 default 当成
				// 「本次成交价」会让人按一个不成立的数字做预算。
				DefaultPrice:   offer.Prices.Default.String(),
				RetailPrice:    offer.Prices.Retail.String(),
				MinimumPrice:   offer.Prices.Min.String(),
				AvailableCount: jsonInt(offer.Counts.Total),
			})
		}
	}
	// map 迭代顺序不定，排序让页面上每次刷新的顺序一致。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Country < out[j].Country
	})
	return out, nil
}

// wireOffer 贴着上游 JSON 的形状。
type wireOffer struct {
	Prices struct {
		Default json.Number `json:"default"`
		Retail  json.Number `json:"retail"`
		Min     json.Number `json:"min"`
	} `json:"prices"`
	Counts struct {
		Total json.RawMessage `json:"total"`
	} `json:"counts"`
}

func jsonInt(raw json.RawMessage) int64 {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0
	}
	v, err := n.Int64()
	if err != nil {
		return 0
	}
	return v
}

// PurchaseInput 是一次购买的输入。
type PurchaseInput struct {
	Service  string
	Country  int
	Amount   int
	Operator string
	// MaxPrice 是十进制文本；空表示不限价。
	MaxPrice   string
	FixedPrice bool
	// Duration 是小时数；0 表示用上游默认。
	Duration       int
	ResellerUserID string
	// VerificationType 是 sms / call；空 = sms。
	//
	// 此前固定 sms、不接受调用方指定。官方 schema 明确它是购买参数，
	// call（语音验证）是另一种计费与交付方式——所以做成**显式参数**，
	// 由页面上一个明确的选项决定，而不是默认值悄悄决定。
	VerificationType string
}

// PurchaseActivations 买号。**花钱的写操作，没有任何重试。**
//
// 与 62 不同：这一次调用就拿到号码，没有"买完再读一次"的窗口。
func (c *Client) PurchaseActivations(ctx context.Context, in PurchaseInput) ([]Activation, error) {
	service := strings.ToLower(strings.TrimSpace(in.Service))
	if !servicePattern.MatchString(service) {
		return nil, connector.NewError(connector.KindRejected, "Hero-SMS 服务代号必须是 2–4 位小写字母或数字", nil)
	}
	if in.Country < 0 || in.Country > maxCountry {
		return nil, connector.NewError(connector.KindRejected, "Hero-SMS 国家代码超出范围", nil)
	}
	if in.Amount < minQuantity || in.Amount > maxQuantity {
		return nil, connector.NewError(connector.KindRejected,
			fmt.Sprintf("Hero-SMS 购买数量必须在 %d–%d 之间", minQuantity, maxQuantity), nil)
	}

	type request struct {
		Service  string `json:"service"`
		Country  int    `json:"country"`
		Amount   int    `json:"amount"`
		Operator string `json:"operator,omitempty"`
		// MaxPrice 编成 JSON number 但本地始终是十进制文本：
		// 金额禁止经过 float（宪法 13）。
		MaxPrice         *json.Number `json:"maxPrice,omitempty"`
		FixedPrice       bool         `json:"fixedPrice,omitempty"`
		Duration         int          `json:"duration,omitempty"`
		VerificationType string       `json:"verificationType"`
		ResellerUserID   string       `json:"resellerUserId,omitempty"`
	}
	verificationType, err := normalizeVerificationType(in.VerificationType)
	if err != nil {
		return nil, err
	}
	body := request{
		Service: service, Country: in.Country, Amount: in.Amount,
		Operator: strings.TrimSpace(in.Operator), FixedPrice: in.FixedPrice,
		Duration:         in.Duration,
		VerificationType: verificationType,
		ResellerUserID:   strings.TrimSpace(in.ResellerUserID),
	}
	if p := strings.TrimSpace(in.MaxPrice); p != "" {
		n := json.Number(p)
		body.MaxPrice = &n
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, connector.NewError(connector.KindRejected, "编码 Hero-SMS 购买请求失败", err)
	}

	var resp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/activations", encoded, &resp); err != nil {
		return nil, err
	}
	activations, err := parseActivations(resp.Data)
	if err != nil {
		return nil, err
	}
	// 数量对不上就是不确定：可能买了一部分。让它落 unknown 交人工，
	// 而不是把拿到的几个当成全部收下。
	if len(activations) != in.Amount {
		return nil, &ProtocolError{Kind: fmt.Sprintf("购买数量不符（要 %d 回 %d）", in.Amount, len(activations))}
	}
	return activations, nil
}

// ListActivations 分页列出 **active** 的号码。
func (c *Client) ListActivations(ctx context.Context, page, size int) ([]Activation, bool, error) {
	if page < 1 || size < 1 || size > lookupPageSize {
		return nil, false, connector.NewError(connector.KindRejected, "Hero-SMS 分页参数非法", nil)
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "size": {strconv.Itoa(size)}}

	var resp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/activations?"+query.Encode(), nil, &resp); err != nil {
		return nil, false, err
	}
	activations, err := parseActivations(resp.Data)
	if err != nil {
		return nil, false, err
	}
	// 满页意味着后面可能还有。
	return activations, len(activations) >= size, nil
}

// ErrLookupIncomplete 表示扫完了允许的页数仍未找到目标。
//
// **它不等于"不存在"**：已终结的 activation 本来就不在 active 列表里。
// 把它当成不存在，会让一次本可以导入的号码被判成"上游没有这笔"，
// 而那正是人工核对最需要准确的时刻。
var ErrLookupIncomplete = fmt.Errorf("herosms: 扫描范围内未找到，且分页未穷尽")

// LookupActivation 按 ID 反查。用于 unknown 的只读导入。
func (c *Client) LookupActivation(ctx context.Context, activationID string) (Activation, error) {
	id := strings.TrimSpace(activationID)
	if id == "" {
		return Activation{}, connector.NewError(connector.KindRejected, "Hero-SMS activation ID 为空", nil)
	}
	for page := 1; page <= lookupMaxPages; page++ {
		activations, more, err := c.ListActivations(ctx, page, lookupPageSize)
		if err != nil {
			return Activation{}, err
		}
		for _, a := range activations {
			if a.ID == id {
				return a, nil
			}
		}
		if !more {
			// 翻到不满的一页还没找到——这才是"确实不在 active 列表里"。
			return Activation{}, connector.NewError(connector.KindRejected,
				"Hero-SMS 未找到该 activation（可能已终结，不在 active 列表）", nil)
		}
	}
	return Activation{}, ErrLookupIncomplete
}

// OTP 是一条收到的验证码。
type OTP struct {
	// ID 是上游的 OTP id（官方 ActivationOtpSchema.id）；只在列表接口里有意义。
	ID         string
	Code       string
	Sender     string
	Text       string
	ReceivedAt string
	// Type 是 sms / call（官方 VerificationType）。call 的那条码为空是正常的。
	Type string
	// Service 是这条码对应的服务代号。
	Service string
}

// GetLastOTP 取最近一条验证码。
func (c *Client) GetLastOTP(ctx context.Context, activationID string) (OTP, bool, error) {
	id := strings.TrimSpace(activationID)
	if id == "" {
		return OTP{}, false, connector.NewError(connector.KindRejected, "Hero-SMS activation ID 为空", nil)
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/activations/"+url.PathEscape(id)+"/otp/last", nil, &resp)
	if err != nil {
		var rejected *RejectedError
		// 404 = 还没有码，是这条链路上的**正常状态**，不是失败：
		// 人刚点了注册，码要几秒到几十秒才来。
		if errors.As(err, &rejected) && rejected.Status == http.StatusNotFound {
			return OTP{}, false, nil
		}
		return OTP{}, false, err
	}
	if len(resp.Data) == 0 {
		return OTP{}, false, nil
	}
	// 上游字段名是 smsCode / smsText / phoneFrom / receivedAt，
	// **不是** code / text / sender——按后者解会得到一条空码，
	// 而空码在页面上看起来就是「还没到」，人会一直等下去。
	code := scalarString(resp.Data, "smsCode")
	if code == "" {
		return OTP{}, false, nil
	}
	return OTP{
		Code:       code,
		Sender:     sanitizeText(scalarString(resp.Data, "phoneFrom"), 128),
		Text:       scalarString(resp.Data, "smsText"),
		ReceivedAt: scalarString(resp.Data, "receivedAt"),
	}, true, nil
}

func parseActivations(raw json.RawMessage) ([]Activation, error) {
	var values []map[string]any
	if err := decodeOneJSON(raw, &values); err != nil {
		return nil, &ProtocolError{Kind: "activation 集合"}
	}
	out := make([]Activation, 0, len(values))
	for _, o := range values {
		id := scalarString(o, "id")
		if id == "" {
			return nil, &ProtocolError{Kind: "activation 缺少 ID"}
		}
		out = append(out, Activation{
			ID:        id,
			Phone:     scalarString(o, "phone"),
			Service:   scalarString(o, "service"),
			Country:   scalarInt(o, "country"),
			Status:    sanitizeText(scalarString(o, "status"), 64),
			Price:     scalarString(o, "price", "cost"),
			CreatedAt: scalarString(o, "createdAt"),
			// 上游字段名是 expiredAt（不是 expiresAt）。
			ExpiresAt:        scalarString(o, "expiredAt"),
			Operator:         sanitizeText(scalarString(o, "operator"), 32),
			CountryPhoneCode: scalarInt(o, "countryPhoneCode"),
			VerificationType: sanitizeText(scalarString(o, "verificationType"), 8),
			Subtype:          scalarInt(o, "subtype"),
		})
	}
	return out, nil
}

func scalarString(o map[string]any, keys ...string) string {
	for _, key := range keys {
		switch typed := o[key].(type) {
		case string:
			return strings.TrimSpace(typed)
		case json.Number:
			return typed.String()
		}
	}
	return ""
}

func scalarInt(o map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch typed := o[key].(type) {
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return n
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}
