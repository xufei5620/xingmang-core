package herosms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 本文件是对照官方 OpenAPI（https://hero-sms.com/docs/v1/openapi.json，
// 2026-09-06 抓取，50 个操作）补齐的现代层接口。字段名**逐字来自 schema**。
//
// 与 endpoints.go / lifecycle.go 的分工：那两个文件是买号与七态推进要用的
// 主链路；这里是围绕它的只读视图（OTP 列表、历史、统计、目录）与两个
// 不花钱的写（收藏）。

// isoDatePattern 是官方 date 格式（YYYY-MM-DD）。
var isoDatePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// ListOTPs 读一个 activation 收到的全部验证码（GET /activations/{id}/otp）。
//
// 与 GetLastOTP 的区别：这里**保留 smsCode 为 null 的条目**——那是一次来电类
// 验证（type=call），码本来就不在文本里。丢掉它会让「打过一个电话」这件事
// 从历史里消失。
func (c *Client) ListOTPs(ctx context.Context, activationID string) ([]OTP, error) {
	id, err := safeActivationID(activationID)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/activations/"+id+"/otp", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]OTP, 0, len(resp.Data))
	for _, o := range resp.Data {
		out = append(out, OTP{
			ID:         scalarString(o, "id"),
			Code:       scalarString(o, "smsCode"),
			Text:       scalarString(o, "smsText"),
			Sender:     sanitizeText(scalarString(o, "phoneFrom"), 128),
			ReceivedAt: scalarString(o, "receivedAt"),
			Type:       sanitizeText(scalarString(o, "type"), 8),
			Service:    sanitizeText(scalarString(o, "service"), 8),
		})
	}
	return out, nil
}

// HistoryQuery 是历史查询的条件。From / To 必填（官方）。
type HistoryQuery struct {
	From, To  string // YYYY-MM-DD
	Services  []string
	Countries []int64
	Page      int
	Size      int
	// SortDesc 为真按 id 倒序（官方 sort[id]=desc）。
	SortDesc bool
	Search   string
}

// HistoryItem 是历史里的一条。
type HistoryItem struct {
	ID         string
	CreateDate string
	Service    string
	Country    int64
	Phone      string
	MoreCodes  string
	// Cost 是十进制文本。
	Cost      string
	Status    int64
	PhoneCode string
	Currency  int64
}

// HistoryPage 是一页历史，带官方给的合计与分页元信息。
type HistoryPage struct {
	Items []HistoryItem
	// TotalSum / SuccessCount 来自官方 totals（**这一页的**合计，不是全量）。
	TotalSum     string
	SuccessCount int64
	Page, Size   int
	Total        int64
	HasMore      bool
}

// ListHistory 读激活历史（GET /activations/history）。
func (c *Client) ListHistory(ctx context.Context, q HistoryQuery) (HistoryPage, error) {
	if !isoDatePattern.MatchString(q.From) || !isoDatePattern.MatchString(q.To) {
		return HistoryPage{}, connector.NewError(connector.KindRejected,
			"Hero-SMS 历史查询的 from/to 必填，格式 YYYY-MM-DD", nil)
	}
	query := url.Values{"from": {q.From}, "to": {q.To}}
	for _, s := range q.Services {
		if s = strings.TrimSpace(s); s != "" {
			query.Add("services[]", s)
		}
	}
	for _, country := range q.Countries {
		query.Add("countries[]", strconv.FormatInt(country, 10))
	}
	if q.Page > 0 {
		query.Set("page", strconv.Itoa(q.Page))
	}
	if q.Size > 0 {
		query.Set("size", strconv.Itoa(q.Size))
	}
	if q.SortDesc {
		query.Set("sort[id]", "desc")
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		query.Set("search", s)
	}

	var resp struct {
		Data   []map[string]any `json:"data"`
		Totals []struct {
			Sum          json.Number     `json:"sum"`
			SuccessCount json.RawMessage `json:"successCount"`
		} `json:"totals"`
		Meta struct {
			Page    json.RawMessage `json:"page"`
			Size    json.RawMessage `json:"size"`
			Total   json.RawMessage `json:"total"`
			HasMore bool            `json:"hasMore"`
		} `json:"meta"`
	}
	if err := c.do(ctx, http.MethodGet, "/activations/history?"+query.Encode(), nil, &resp); err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{
		Page:    int(jsonInt(resp.Meta.Page)),
		Size:    int(jsonInt(resp.Meta.Size)),
		Total:   jsonInt(resp.Meta.Total),
		HasMore: resp.Meta.HasMore,
	}
	if len(resp.Totals) > 0 {
		page.TotalSum = resp.Totals[0].Sum.String()
		page.SuccessCount = jsonInt(resp.Totals[0].SuccessCount)
	}
	for _, o := range resp.Data {
		page.Items = append(page.Items, HistoryItem{
			ID:         scalarString(o, "id"),
			CreateDate: scalarString(o, "createDate"),
			Service:    sanitizeText(scalarString(o, "service"), 8),
			Country:    scalarInt(o, "country"),
			Phone:      scalarString(o, "phone"),
			MoreCodes:  sanitizeText(scalarString(o, "moreCodes"), 256),
			Cost:       scalarString(o, "cost"),
			Status:     scalarInt(o, "status"),
			PhoneCode:  sanitizeText(scalarString(o, "phoneCode"), 8),
			Currency:   scalarInt(o, "currency"),
		})
	}
	return page, nil
}

// ProlongRecord 是一次延长的记录。
type ProlongRecord struct {
	Duration  int64
	Unit      string
	Price     string
	CreatedAt string
}

// GetProlongHistory 读一个号码的延长历史（GET /activations/{id}/prolong/history）。
func (c *Client) GetProlongHistory(ctx context.Context, activationID string) ([]ProlongRecord, error) {
	id, err := safeActivationID(activationID)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			Duration struct {
				Value json.RawMessage `json:"value"`
				Unit  string          `json:"unit"`
			} `json:"duration"`
			Price     json.Number `json:"price"`
			CreatedAt string      `json:"createdAt"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/activations/"+id+"/prolong/history", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]ProlongRecord, 0, len(resp.Data))
	for _, r := range resp.Data {
		out = append(out, ProlongRecord{
			Duration:  jsonInt(r.Duration.Value),
			Unit:      sanitizeText(r.Duration.Unit, 16),
			Price:     r.Price.String(),
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// GetCustomDurations 读非标准激活时长目录（GET /classifiers/activations/custom-durations）。
//
// 形状是 service → country → 小时数（官方 map<map<int>>）。它回答的是
// 「哪些服务在哪些国家的号不是默认 20 分钟」——买之前看一眼，免得以为
// 买到的是短时号。
func (c *Client) GetCustomDurations(ctx context.Context) (map[string]map[string]int64, error) {
	var resp map[string]map[string]json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/classifiers/activations/custom-durations", nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]int64, len(resp))
	for service, byCountry := range resp {
		m := make(map[string]int64, len(byCountry))
		for country, raw := range byCountry {
			m[country] = jsonInt(raw)
		}
		out[service] = m
	}
	return out, nil
}

// StatsEntry 是统计里的一格：某国家某服务当天的数量与金额。
type StatsEntry struct {
	Country string
	Service string
	// 官方 schema 只写了 data.country.service 是对象，没钉字段；
	// 按常见命名取 count / sum，取不到就是零值。**原始对象一并带回**，
	// 页面可以把没认出来的键原样展示。
	Count int64
	Sum   string
	Raw   map[string]any
}

// GetStats 读当天统计（GET /activations/stats?date=）。date 必填。
func (c *Client) GetStats(ctx context.Context, date string) ([]StatsEntry, error) {
	if !isoDatePattern.MatchString(date) {
		return nil, connector.NewError(connector.KindRejected, "Hero-SMS 统计的 date 必填，格式 YYYY-MM-DD", nil)
	}
	var resp struct {
		Data map[string]map[string]map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/activations/stats?date="+url.QueryEscape(date), nil, &resp); err != nil {
		return nil, err
	}
	var out []StatsEntry
	for country, byService := range resp.Data {
		for service, cell := range byService {
			out = append(out, StatsEntry{
				Country: country, Service: service,
				Count: scalarInt(cell, "count", "total"),
				Sum:   scalarString(cell, "sum", "cost", "amount"),
				Raw:   cell,
			})
		}
	}
	return out, nil
}

// Favorite 是一个收藏（服务 × 国家 × 运营商）。
type Favorite struct {
	ID          string
	Service     string
	Country     int64
	CountryName string
	ServiceName string
	Operator    string
	IsFavorite  bool
}

// SetFavorite 新增或编辑收藏（PUT）。operator 必填，官方默认 any。
//
// **不花钱**：收藏只是页面上的快捷入口，不产生任何费用。
func (c *Client) SetFavorite(ctx context.Context, service string, country int64, operator string) (Favorite, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if !servicePattern.MatchString(service) || country < 0 || country > maxCountry {
		return Favorite{}, connector.NewError(connector.KindRejected, "Hero-SMS 收藏的服务代号或国家非法", nil)
	}
	if operator = strings.TrimSpace(operator); operator == "" {
		operator = "any"
	}
	body, err := json.Marshal(map[string]string{"operator": operator})
	if err != nil {
		return Favorite{}, connector.NewError(connector.KindRejected, "编码 Hero-SMS 请求失败", err)
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	path := "/activations/favorites/services/" + url.PathEscape(service) +
		"/countries/" + strconv.FormatInt(country, 10)
	if err := c.do(ctx, http.MethodPut, path, body, &resp); err != nil {
		return Favorite{}, err
	}
	return Favorite{
		ID:          scalarString(resp.Data, "id"),
		Service:     sanitizeText(scalarString(resp.Data, "service"), 8),
		Country:     scalarInt(resp.Data, "country"),
		CountryName: sanitizeText(scalarString(resp.Data, "countryName"), 64),
		ServiceName: sanitizeText(scalarString(resp.Data, "serviceName"), 64),
		Operator:    sanitizeText(scalarString(resp.Data, "operator"), 32),
		IsFavorite:  scalarBool(resp.Data, "isFavorite"),
	}, nil
}

// RemoveFavorite 移除收藏（DELETE，严格 204）。
func (c *Client) RemoveFavorite(ctx context.Context, service string, country int64) error {
	service = strings.ToLower(strings.TrimSpace(service))
	if !servicePattern.MatchString(service) || country < 0 || country > maxCountry {
		return connector.NewError(connector.KindRejected, "Hero-SMS 收藏的服务代号或国家非法", nil)
	}
	path := "/activations/favorites/services/" + url.PathEscape(service) +
		"/countries/" + strconv.FormatInt(country, 10)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func scalarBool(o map[string]any, key string) bool {
	switch v := o[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	case json.Number:
		return v.String() != "0"
	}
	return false
}
