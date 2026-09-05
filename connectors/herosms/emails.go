package herosms

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

// 邮箱接码（官方 Emails 组，7 个操作）。
//
// 与手机号接码是**两种资源**：邮箱按「站点 + 域名」买，状态只有
// WAIT / CANCEL / SUCCESS 三档，收到的是邮件里的验证内容（官方叫 value）。
// 所以它不复用 Activation / OTP 那套类型，也不进 sms_resource 表——
// 领域层为它单开一张表，见迁移 000040。

// emailIDPattern 是上游的 emailId：正整数。
var emailIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// 批量购买上限（官方 count 1..10）。
const maxEmailBatch = 10

// Email 是一次邮箱接码。
type Email struct {
	ID    string
	Site  string
	Email string
	// Status 是官方枚举 WAIT / CANCEL / SUCCESS。
	Status string
	// Value 是收到的验证内容（码或链接），未收到时空。
	Value string
	// Cost 是十进制文本；Currency 是 ISO 数字币种码（840=USD）。
	Cost     string
	Currency int64
	Date     string
	Message  string
}

// EmailDomain 是可用域名及其价格与库存。
type EmailDomain struct {
	Name  string
	Cost  string
	Count int64
}

// EmailListQuery 是列表筛选。
type EmailListQuery struct {
	Search   string
	Page     int
	Size     int
	SortDesc bool
	// Statuses 是官方 status.id[] 枚举 3/4/5/6/7（数字含义文档未给，原样透传）。
	Statuses []int64
	From, To string // RFC3339
}

// ListEmails 读活跃的邮箱购买（GET /emails）。
func (c *Client) ListEmails(ctx context.Context, q EmailListQuery) ([]Email, error) {
	query := url.Values{}
	if s := strings.TrimSpace(q.Search); s != "" {
		query.Set("search", s)
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
	for _, st := range q.Statuses {
		query.Add("status[id][]", strconv.FormatInt(st, 10))
	}
	if q.From != "" {
		query.Set("from", q.From)
	}
	if q.To != "" {
		query.Set("to", q.To)
	}
	path := "/emails"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Email, 0, len(resp.Data))
	for _, o := range resp.Data {
		out = append(out, parseEmail(o))
	}
	return out, nil
}

// PurchaseEmail 买一个邮箱（POST /emails，成功是 **201**）。**花钱。**
func (c *Client) PurchaseEmail(ctx context.Context, site, domain string) (Email, error) {
	site, domain = strings.TrimSpace(site), strings.TrimSpace(domain)
	if site == "" || domain == "" || !validHostText(site) || !validHostText(domain) {
		return Email{}, connector.NewError(connector.KindRejected, "Hero-SMS 邮箱购买需要合法的站点与域名", nil)
	}
	body, err := json.Marshal(map[string]string{"site": site, "domain": domain})
	if err != nil {
		return Email{}, connector.NewError(connector.KindRejected, "编码 Hero-SMS 请求失败", err)
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/emails", body, &resp); err != nil {
		return Email{}, err
	}
	email := parseEmail(resp.Data)
	if email.ID == "" {
		// 买成功了但 ID 解不出来：钱花了、对账无从下手。判协议错误让它落 unknown。
		return Email{}, &ProtocolError{Kind: "邮箱购买响应缺少 id"}
	}
	return email, nil
}

// EmailBatchItem 是批量购买回的一条（官方 schema 与单买不同：没有 id）。
type EmailBatchItem struct {
	UserID string
	Site   string
	Email  string
	Status string
	Cost   string
	Domain string
}

// PurchaseEmailBatch 批量买邮箱（POST /emails/batch，count 1..10）。**花钱。**
//
// **响应里没有每条的 id**（官方 schema 如此），所以批量买到的邮箱要靠
// ListEmails 再读一次才拿得到 id——与 62 的「买完再读一次」同一个形态，
// 那次补读失败也必须落 unknown。
func (c *Client) PurchaseEmailBatch(ctx context.Context, site, domain string, count int, service string) ([]EmailBatchItem, error) {
	site, domain = strings.TrimSpace(site), strings.TrimSpace(domain)
	if site == "" || domain == "" || !validHostText(site) || !validHostText(domain) {
		return nil, connector.NewError(connector.KindRejected, "Hero-SMS 邮箱购买需要合法的站点与域名", nil)
	}
	if count < 1 || count > maxEmailBatch {
		return nil, connector.NewError(connector.KindRejected,
			fmt.Sprintf("Hero-SMS 邮箱批量购买数量必须在 1–%d 之间", maxEmailBatch), nil)
	}
	req := map[string]any{"site": site, "domain": domain, "count": count}
	if service = strings.ToLower(strings.TrimSpace(service)); service != "" {
		if !servicePattern.MatchString(service) {
			return nil, connector.NewError(connector.KindRejected, "Hero-SMS 服务代号必须是 2–4 位小写字母或数字", nil)
		}
		req["service"] = service
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, connector.NewError(connector.KindRejected, "编码 Hero-SMS 请求失败", err)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
		Meta struct {
			Info  string          `json:"info"`
			Count json.RawMessage `json:"count"`
		} `json:"meta"`
	}
	if err := c.do(ctx, http.MethodPost, "/emails/batch", body, &resp); err != nil {
		return nil, err
	}
	out := make([]EmailBatchItem, 0, len(resp.Data))
	for _, o := range resp.Data {
		out = append(out, EmailBatchItem{
			UserID: scalarString(o, "userId"),
			Site:   sanitizeText(scalarString(o, "site"), 128),
			Email:  sanitizeText(scalarString(o, "email"), 256),
			Status: sanitizeText(scalarString(o, "status"), 16),
			Cost:   scalarString(o, "cost"),
			Domain: sanitizeText(scalarString(o, "domain"), 128),
		})
	}
	// 数量对不上就是不确定：可能买了一部分。
	if len(out) != count {
		return nil, &ProtocolError{Kind: fmt.Sprintf("邮箱批量购买数量不符（要 %d 回 %d）", count, len(out))}
	}
	return out, nil
}

// GetEmail 查一个邮箱购买的状态（GET /emails/{id}）。
func (c *Client) GetEmail(ctx context.Context, emailID string) (Email, error) {
	id, err := safeEmailID(emailID)
	if err != nil {
		return Email{}, err
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/emails/"+id, nil, &resp); err != nil {
		return Email{}, err
	}
	return parseEmail(resp.Data), nil
}

// CancelEmail 取消一次邮箱购买（DELETE，严格 204）。
func (c *Client) CancelEmail(ctx context.Context, emailID string) error {
	id, err := safeEmailID(emailID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/emails/"+id, nil, nil)
}

// ReorderEmail 重新下单（POST /emails/{id}/reorder）。**可能花钱。**
func (c *Client) ReorderEmail(ctx context.Context, emailID string) (Email, error) {
	id, err := safeEmailID(emailID)
	if err != nil {
		return Email{}, err
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/emails/"+id+"/reorder", nil, &resp); err != nil {
		return Email{}, err
	}
	return parseEmail(resp.Data), nil
}

// ListEmailDomains 读某站点可用的域名（GET /emails/domains?site=）。
func (c *Client) ListEmailDomains(ctx context.Context, site string) ([]EmailDomain, error) {
	query := url.Values{}
	if site = strings.TrimSpace(site); site != "" {
		if !validHostText(site) {
			return nil, connector.NewError(connector.KindRejected, "Hero-SMS 站点非法", nil)
		}
		query.Set("site", site)
	}
	path := "/emails/domains"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		Data []struct {
			Name  string          `json:"name"`
			Cost  json.Number     `json:"cost"`
			Count json.RawMessage `json:"count"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]EmailDomain, 0, len(resp.Data))
	for _, d := range resp.Data {
		out = append(out, EmailDomain{
			Name:  sanitizeText(d.Name, 128),
			Cost:  d.Cost.String(),
			Count: jsonInt(d.Count),
		})
	}
	return out, nil
}

func parseEmail(o map[string]any) Email {
	return Email{
		ID:       scalarString(o, "id"),
		Site:     sanitizeText(scalarString(o, "site"), 128),
		Email:    sanitizeText(scalarString(o, "email"), 256),
		Status:   sanitizeText(scalarString(o, "status"), 16),
		Value:    scalarString(o, "value"),
		Cost:     scalarString(o, "cost"),
		Currency: scalarInt(o, "currency"),
		Date:     scalarString(o, "date"),
		Message:  sanitizeText(scalarString(o, "message"), 256),
	}
}

func safeEmailID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if !emailIDPattern.MatchString(id) {
		return "", connector.NewError(connector.KindRejected, "Hero-SMS 邮箱 ID 必须是正整数", nil)
	}
	return id, nil
}

// validHostText 挡住会破坏 JSON / URL 的字符与超长值；不做完整域名校验，
// 站点名到底合不合法由上游说了算（422）。
func validHostText(v string) bool {
	if len(v) > 253 {
		return false
	}
	for _, r := range v {
		if r <= 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' || r == '?' || r == '#' {
			return false
		}
	}
	return true
}
