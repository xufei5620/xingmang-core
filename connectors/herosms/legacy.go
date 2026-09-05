package herosms

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// SMS-Activate 兼容层（官方 OpenAPI 第二个 server：/stubs/handler_api.php）。
//
// 只接**现代层没有的**九个：余额、国家、服务、运营商、价格、Top 国家、
// 租用报价（按服务 / 按国家）、租用下单。与现代层重复的十二个不接——
// 同一功能两套实现只会多一处漂移。
//
// 这一层有两处与现代层不同，都写在 doLegacy 里：
//   - 密钥走 **query 的 api_key**（官方 apiKeyQuery）。这意味着密钥会进上游
//     访问日志；我们这边的日志与错误文本一律不带 URL。
//   - 部分响应是**裸字符串**（`ACCESS_BALANCE:12.50`），不是 JSON。

// maxLegacyResponseBytes 是兼容层响应体上限。
//
// 现代层用 1 MiB 够了，兼容层不够：不带筛选的 getPrices 是全部国家 × 全部
// 服务的价格表，生产实测超过 1 MiB，被 1 MiB 的上限拒成「协议错误（兼容层
// 响应体）」——页面上看到的是「服务内部错误」，而上游其实什么都没做错。
// 16 MiB 仍然是一个上限（防止无限响应体把进程吃满），只是给目录类接口留够。
const maxLegacyResponseBytes = 16 << 20

// doLegacy 发一次兼容层请求。out 为 nil 时把原始文本交给调用方自己解析。
func (c *Client) doLegacy(ctx context.Context, method, action string, params url.Values, out any) (string, error) {
	if action == "" || strings.ContainsAny(action, "\r\n&=?/") {
		return "", connector.NewError(connector.KindRejected, "Hero-SMS 兼容层动作名非法", nil)
	}
	key, err := c.apiKeyValue(ctx)
	if err != nil {
		return "", err
	}
	query := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			query.Add(k, v)
		}
	}
	query.Set("action", action)
	query.Set("api_key", key)

	base := c.legacyBaseURL
	if base == "" {
		base = LegacyBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"?"+query.Encode(), nil)
	if err != nil {
		return "", connector.NewError(connector.KindRejected, "构造 Hero-SMS 兼容层请求失败", err)
	}
	req.Header.Set("Accept", "application/json, text/plain")
	resp, err := c.httpc.Do(req)
	if err != nil {
		// 传输失败对写操作（租用下单）是不确定的：钱可能已经花了。
		return "", connector.NewError(connector.KindUnavailable, "Hero-SMS 兼容层请求失败", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxLegacyResponseBytes+1))
	if readErr != nil || len(raw) > maxLegacyResponseBytes {
		return "", &ProtocolError{Kind: "兼容层响应体超过 16 MiB", Status: resp.StatusCode}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", decodeHTTPError(resp.StatusCode, raw)
	}
	text := strings.TrimSpace(string(raw))
	if out == nil {
		return text, nil
	}
	if err := decodeOneJSON(bytes.TrimSpace(raw), out); err != nil {
		return "", &ProtocolError{Kind: "兼容层响应结构", Status: resp.StatusCode}
	}
	return text, nil
}

// GetBalance 读余额（getBalance → `ACCESS_BALANCE:<amount>`）。
//
// 返回十进制文本，不转 float。任何别的形态（BAD_KEY、ERROR_SQL…）都报错
// 而不是解出一个空余额——空余额在页面上看起来像「没钱了」。
func (c *Client) GetBalance(ctx context.Context) (string, error) {
	text, err := c.doLegacy(ctx, http.MethodGet, "getBalance", nil, nil)
	if err != nil {
		return "", err
	}
	amount, ok := strings.CutPrefix(text, "ACCESS_BALANCE:")
	if !ok {
		return "", legacyTextError(text)
	}
	amount = strings.TrimSpace(amount)
	if _, err := strconv.ParseFloat(amount, 64); err != nil {
		// 只用来验证它是个数，**不用这个 float 做任何计算**。
		return "", &ProtocolError{Kind: "余额不是数字"}
	}
	return amount, nil
}

// legacyTextError 把兼容层的文本错误（BAD_KEY、NO_BALANCE…）翻成拒绝。
//
// 这些都是 SMS-Activate 协议里定义好的**明确拒绝**，不是不确定态。
func legacyTextError(text string) error {
	return &RejectedError{Status: http.StatusOK, Message: sanitizeText("兼容层返回 "+text, maxProviderText)}
}

// Country 是国家目录里的一条（getCountries）。
type Country struct {
	ID      int64
	NameRU  string
	NameEN  string
	NameCN  string
	Visible bool
	Retry   bool
}

// GetCountries 读国家目录。
func (c *Client) GetCountries(ctx context.Context) ([]Country, error) {
	var raw []map[string]any
	if _, err := c.doLegacy(ctx, http.MethodGet, "getCountries", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Country, 0, len(raw))
	for _, o := range raw {
		out = append(out, Country{
			ID:      scalarInt(o, "id"),
			NameRU:  sanitizeText(scalarString(o, "rus"), 64),
			NameEN:  sanitizeText(scalarString(o, "eng"), 64),
			NameCN:  sanitizeText(scalarString(o, "chn"), 64),
			Visible: scalarInt(o, "visible") == 1,
			Retry:   scalarInt(o, "retry") == 1,
		})
	}
	return out, nil
}

// ServiceEntry 是服务目录里的一条（getServicesList）。
type ServiceEntry struct {
	Code string
	Name string
}

// GetServicesList 读服务目录。country 为 0 表示不筛；lang 是官方枚举
// （en/cn/es/de/fr/pt/ru/id/vi/tr），空表示上游默认。
func (c *Client) GetServicesList(ctx context.Context, country int64, lang string) ([]ServiceEntry, error) {
	params := url.Values{}
	if country > 0 {
		params.Set("country", strconv.FormatInt(country, 10))
	}
	if lang = strings.ToLower(strings.TrimSpace(lang)); lang != "" {
		switch lang {
		case "en", "cn", "es", "de", "fr", "pt", "ru", "id", "vi", "tr":
			params.Set("lang", lang)
		default:
			return nil, connector.NewError(connector.KindRejected, "Hero-SMS 服务目录的语言不在官方枚举内", nil)
		}
	}
	var raw []map[string]any
	if _, err := c.doLegacy(ctx, http.MethodGet, "getServicesList", params, &raw); err != nil {
		return nil, err
	}
	out := make([]ServiceEntry, 0, len(raw))
	for _, o := range raw {
		out = append(out, ServiceEntry{
			Code: sanitizeText(scalarString(o, "code"), 8),
			Name: sanitizeText(scalarString(o, "name"), 64),
		})
	}
	return out, nil
}

// GetOperators 读运营商目录：国家 ID → 运营商代号列表。
func (c *Client) GetOperators(ctx context.Context, country int64) (map[string][]string, error) {
	params := url.Values{}
	if country > 0 {
		params.Set("country", strconv.FormatInt(country, 10))
	}
	var raw struct {
		Status           string              `json:"status"`
		CountryOperators map[string][]string `json:"countryOperators"`
	}
	if _, err := c.doLegacy(ctx, http.MethodGet, "getOperators", params, &raw); err != nil {
		return nil, err
	}
	if raw.CountryOperators == nil {
		return nil, &ProtocolError{Kind: "运营商目录缺失"}
	}
	return raw.CountryOperators, nil
}

// PriceCell 是价格表里的一格：某国家某服务的价格与库存。
type PriceCell struct {
	Cost          string
	Count         int64
	PhysicalCount int64
}

// GetPrices 读当前价格：国家 → 服务 → 价格。service / country 为空表示不筛。
func (c *Client) GetPrices(ctx context.Context, service string, country int64) (map[string]map[string]PriceCell, error) {
	params := url.Values{}
	if service = strings.ToLower(strings.TrimSpace(service)); service != "" {
		if !servicePattern.MatchString(service) {
			return nil, connector.NewError(connector.KindRejected, "Hero-SMS 服务代号必须是 2–4 位小写字母或数字", nil)
		}
		params.Set("service", service)
	}
	if country > 0 {
		params.Set("country", strconv.FormatInt(country, 10))
	}
	var raw map[string]map[string]map[string]any
	if _, err := c.doLegacy(ctx, http.MethodGet, "getPrices", params, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]PriceCell, len(raw))
	for countryID, byService := range raw {
		m := make(map[string]PriceCell, len(byService))
		for svc, cell := range byService {
			m[svc] = PriceCell{
				Cost:          scalarString(cell, "cost"),
				Count:         scalarInt(cell, "count"),
				PhysicalCount: scalarInt(cell, "physicalCount"),
			}
		}
		out[countryID] = m
	}
	return out, nil
}

// TopCountry 是某服务的一个推荐国家。
type TopCountry struct {
	Country     int64
	Price       string
	RetailPrice string
	Count       int64
}

// GetTopCountriesByService 读某服务的 Top 国家。byRank 为真走
// getTopCountriesByServiceRank（按用户等级）。
//
// 官方响应是 map<oneOf(map|object)>——键是序号，值可能是对象也可能再嵌一层；
// 这里只认带 country 的对象，认不出来的跳过而不是报错：这是个推荐列表，
// 少一条无害。
func (c *Client) GetTopCountriesByService(ctx context.Context, service string, freePrice, byRank bool) ([]TopCountry, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if !servicePattern.MatchString(service) {
		return nil, connector.NewError(connector.KindRejected, "Hero-SMS 服务代号必须是 2–4 位小写字母或数字", nil)
	}
	params := url.Values{"service": {service}}
	if freePrice {
		params.Set("freePrice", "true")
	}
	action := "getTopCountriesByService"
	if byRank {
		action = "getTopCountriesByServiceRank"
	}
	var raw map[string]any
	if _, err := c.doLegacy(ctx, http.MethodGet, action, params, &raw); err != nil {
		return nil, err
	}
	var out []TopCountry
	for _, v := range raw {
		o, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if _, has := o["country"]; !has {
			continue
		}
		out = append(out, TopCountry{
			Country:     scalarInt(o, "country"),
			Price:       scalarString(o, "price"),
			RetailPrice: scalarString(o, "retail_price"),
			Count:       scalarInt(o, "count"),
		})
	}
	return out, nil
}

// RentOffer 是租用报价里的一格。
type RentOffer struct {
	Service     string
	Quantity    int64
	Price       string
	RetailPrice string
}

// RentOffers 是按国家 + 时长的租用报价（getRentServicesAndCountries）。
type RentOffers struct {
	Operators map[string]string
	Services  map[string]RentOffer
}

// GetRentOffers 读某国家、某时长（小时）的租用报价。两者官方必填。
func (c *Client) GetRentOffers(ctx context.Context, country, durationHours int64) (RentOffers, error) {
	if country < 0 || country > maxCountry || durationHours <= 0 {
		return RentOffers{}, connector.NewError(connector.KindRejected, "Hero-SMS 租用报价需要国家与正的小时数", nil)
	}
	params := url.Values{
		"country":  {strconv.FormatInt(country, 10)},
		"duration": {strconv.FormatInt(durationHours, 10)},
	}
	var raw struct {
		Operators map[string]string         `json:"operators"`
		Services  map[string]map[string]any `json:"services"`
	}
	if _, err := c.doLegacy(ctx, http.MethodGet, "getRentServicesAndCountries", params, &raw); err != nil {
		return RentOffers{}, err
	}
	out := RentOffers{Operators: raw.Operators, Services: map[string]RentOffer{}}
	for svc, cell := range raw.Services {
		out.Services[svc] = RentOffer{
			Service:     svc,
			Quantity:    scalarInt(cell, "quantity"),
			Price:       scalarString(cell, "price"),
			RetailPrice: scalarString(cell, "retail_price"),
		}
	}
	return out, nil
}

// ServiceCountRent 读某服务在各国家的租用价格与数量（serviceCountRent）。
// 形状是国家 → 时长档 → {price,count}。
func (c *Client) ServiceCountRent(ctx context.Context, service string, country int64, operator string) (map[string]map[string]PriceCell, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if !servicePattern.MatchString(service) {
		return nil, connector.NewError(connector.KindRejected, "Hero-SMS 服务代号必须是 2–4 位小写字母或数字", nil)
	}
	params := url.Values{"service": {service}}
	if country > 0 {
		params.Set("country", strconv.FormatInt(country, 10))
	}
	if operator = strings.TrimSpace(operator); operator != "" {
		params.Set("operator", operator)
	}
	var raw map[string]map[string]map[string]any
	if _, err := c.doLegacy(ctx, http.MethodGet, "serviceCountRent", params, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]PriceCell, len(raw))
	for countryID, byDuration := range raw {
		m := make(map[string]PriceCell, len(byDuration))
		for dur, cell := range byDuration {
			m[dur] = PriceCell{Cost: scalarString(cell, "price"), Count: scalarInt(cell, "count")}
		}
		out[countryID] = m
	}
	return out, nil
}

// RentInput 是租用下单的输入。DurationHours 必填（官方 RentDuration：小时）。
type RentInput struct {
	Service       string
	Country       int64
	DurationHours int64
	Operator      string
	// Currency 是官方枚举 643/840/978/156；0 = 上游默认。
	Currency int64
}

// GetRentNumber 租一个号（getRentNumber）。**花钱的写操作，没有任何重试。**
//
// 响应是 successfulNumberv2Response：与现代层的 ActivationSchema 字段名**不同**
// （activationId / phoneNumber / activationCost / activationEndTime…），所以
// 单独一个解析器，不复用 parseActivations。
func (c *Client) GetRentNumber(ctx context.Context, in RentInput) (Activation, error) {
	service := strings.ToLower(strings.TrimSpace(in.Service))
	if !servicePattern.MatchString(service) {
		return Activation{}, connector.NewError(connector.KindRejected, "Hero-SMS 服务代号必须是 2–4 位小写字母或数字", nil)
	}
	if in.Country < 0 || in.Country > maxCountry {
		return Activation{}, connector.NewError(connector.KindRejected, "Hero-SMS 国家代码超出范围", nil)
	}
	if in.DurationHours <= 0 {
		return Activation{}, connector.NewError(connector.KindRejected, "Hero-SMS 租用必须指定正的小时数", nil)
	}
	params := url.Values{
		"service":  {service},
		"country":  {strconv.FormatInt(in.Country, 10)},
		"duration": {strconv.FormatInt(in.DurationHours, 10)},
	}
	if op := strings.TrimSpace(in.Operator); op != "" {
		params.Set("operator", op)
	}
	if in.Currency != 0 {
		switch in.Currency {
		case 643, 840, 978, 156:
			params.Set("currency", strconv.FormatInt(in.Currency, 10))
		default:
			return Activation{}, connector.NewError(connector.KindRejected, "Hero-SMS 币种不在官方枚举内", nil)
		}
	}
	var raw map[string]any
	text, err := c.doLegacy(ctx, http.MethodGet, "getRentNumber", params, &raw)
	if err != nil {
		var protocol *ProtocolError
		// 兼容层在拒绝时可能回裸文本（NO_NUMBERS、NO_BALANCE）。那是明确拒绝，
		// 钱没花；但**只有**在能认出它是协议文本时才这么判。
		if ok := asProtocol(err, &protocol); ok && text != "" && isLegacyStatusWord(text) {
			return Activation{}, legacyTextError(text)
		}
		return Activation{}, err
	}
	act := parseLegacyActivation(raw)
	if act.ID == "" || act.Phone == "" {
		// 租到了但解不出 ID/号码：钱花了、对账无从下手。落 unknown。
		return Activation{}, &ProtocolError{Kind: "租用响应缺少 activationId 或 phoneNumber"}
	}
	return act, nil
}

func parseLegacyActivation(o map[string]any) Activation {
	return Activation{
		ID:               scalarString(o, "activationId"),
		Phone:            scalarString(o, "phoneNumber"),
		Service:          sanitizeText(scalarString(o, "serviceCode"), 8),
		Country:          scalarInt(o, "countryCode"),
		Status:           scalarString(o, "status"),
		Price:            scalarString(o, "activationCost"),
		CreatedAt:        scalarString(o, "activationTime"),
		ExpiresAt:        scalarString(o, "activationEndTime"),
		Operator:         sanitizeText(scalarString(o, "activationOperator"), 32),
		CountryPhoneCode: scalarInt(o, "countryPhoneCode"),
		VerificationType: sanitizeText(scalarString(o, "verificationType"), 8),
		Subtype:          scalarInt(o, "subtype"),
	}
}

func asProtocol(err error, target **ProtocolError) bool {
	p, ok := err.(*ProtocolError)
	if ok {
		*target = p
	}
	return ok
}

// isLegacyStatusWord 认 SMS-Activate 协议里的状态词：全大写字母、数字与下划线。
func isLegacyStatusWord(text string) bool {
	if len(text) == 0 || len(text) > 64 {
		return false
	}
	for _, r := range text {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

var _ = fmt.Sprintf
