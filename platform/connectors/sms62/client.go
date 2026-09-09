// Package sms62 是 62-US 接码供应商的客户端（XM-SMS0）。
//
// 协议形状取自 SoloAI 的冻结实现（2026-09-05 交接包），**其与当前官方服务的
// 一致性未在本仓实测**——所有"上游会怎样"的说法都以那份交接为准，不是我们
// 验证过的事实。已知有据的部分逐条写在下面各处注释里。
//
// 与 connectors/infini 同一条纪律：凭据只经 CredentialRef，明文只在调用栈上
// 存在；写通道带主机 allowlist、拒绝重定向；**写路径不做应用层重试**——
// 重试可能真的再买一次号。
package sms62

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// ProductionBaseURL 是固定主机。
//
// **不做成可配置**：这个客户端会拿着密钥往它发请求，让端点可配等于让一次
// 配置错误把密钥送到别处去。沙箱与生产同一个主机（交接包里没有沙箱端点）。
const ProductionBaseURL = "https://api.62-us.com"

// purposeAPIKey 进凭据审计：每次解析都留一条「谁、为什么取了这个密钥」。
const purposeAPIKey = "sms62.api_key"

const (
	// maxResponseBytes 是响应体上限。超了当协议错误，而不是截断后照常解析——
	// 截断的 JSON 解出来可能"成功"，那比报错糟得多。
	maxResponseBytes = 1 << 20
	// maxProviderMessage 是上游文案带进错误的长度上限（ADR-004：上游原文
	// 只进服务端日志与 cause 链，不外传）。
	maxProviderMessage = 500
	defaultTimeout     = 15 * time.Second
)

// Client 是 62-US 的客户端。
type Client struct {
	baseURL  string
	provider secrets.SecretProvider
	keyRef   secrets.CredentialRef
	httpc    *http.Client

	// apiKey 只在 fake/测试装配时直接给；生产走 provider + keyRef。
	apiKey string
}

// NewClient 组装真实客户端。
//
// 凭据**不在这里解析**：只存 CredentialRef，每次调用时经 SecretProvider 取，
// 这样密钥轮换不必重启进程（ADR-014）。
func NewClient(provider secrets.SecretProvider, keyRef secrets.CredentialRef, allowlist []string) *Client {
	return &Client{
		baseURL:  ProductionBaseURL,
		provider: provider,
		keyRef:   keyRef,
		httpc:    connector.NewVendorWriteClient(allowlist, defaultTimeout),
	}
}

// apiKeyValue 取出密钥明文。
//
// 明文只在调用栈上存在：不进结构体字段、不进日志、不进错误文本。取不到一律
// 归到 auth——那正是「凭据没配好」该落的地方，而不是被当成网络问题排查。
//
// **必须用 Reveal()**：SecretValue.String() 恒返回 "[REDACTED]"，用它算出来
// 的请求会一路 401，而诊断会指向「密钥不对」。这个坑在 XM-CARD4 上花过几小时。
func (c *Client) apiKeyValue(ctx context.Context) (string, error) {
	if c.provider == nil {
		return c.apiKey, nil
	}
	value, err := c.provider.Resolve(ctx, c.keyRef, purposeAPIKey)
	if err != nil {
		return "", connector.NewError(connector.KindAuth, "62-US 密钥未配置或读取失败", err)
	}
	key := strings.TrimSpace(value.Reveal())
	if key == "" {
		return "", connector.NewError(connector.KindAuth, "62-US 密钥为空", nil)
	}
	return key, nil
}

// providerEnvelope 是 62 的统一响应信封。
type providerEnvelope struct {
	Code      json.RawMessage `json:"code"`
	Message   string          `json:"msg"`
	RequestID string          `json:"request_id"`
	Time      json.RawMessage `json:"time"`
	Data      json.RawMessage `json:"data"`
}

// ProviderMeta 是每次成功调用带回的上游元信息，进操作台账便于对账。
type ProviderMeta struct {
	RequestID    string
	ProviderTime int64
}

// do 发一次请求并解开信封。
//
// **成功要同时满足两件事**：HTTP 2xx 且业务 code==1。只看其中一个都会判错：
// 上游把业务拒绝配成 HTTP 200 是这家的常态，而把成功配成非 2xx 也出现过
// （交接包的 do() 同样两个都查）。
func (c *Client) do(ctx context.Context, method, requestURI string, body []byte, contentType string, out any) (ProviderMeta, error) {
	if err := validateRequestURI(requestURI); err != nil {
		return ProviderMeta{}, err
	}
	key, err := c.apiKeyValue(ctx)
	if err != nil {
		return ProviderMeta{}, err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestURI, bytes.NewReader(body))
	if err != nil {
		return ProviderMeta{}, connector.NewError(connector.KindRejected, "构造 62-US 请求失败", err)
	}
	// 密钥进 Header，绝不进 query——URL 会进代理日志、浏览器历史与错误上报。
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		// 传输失败：写操作到这里是**不确定**的，钱可能已经花了。
		// 调用方必须落 unknown 而不是重试。
		return ProviderMeta{}, connector.NewError(connector.KindUnavailable, "62-US 请求失败", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil || len(raw) > maxResponseBytes {
		return ProviderMeta{}, &ProtocolError{Kind: "响应体", Status: resp.StatusCode}
	}

	var envelope providerEnvelope
	if decodeErr := decodeOneJSON(raw, &envelope); decodeErr != nil || len(envelope.Code) == 0 {
		return ProviderMeta{}, &ProtocolError{Kind: "响应信封", Status: resp.StatusCode}
	}
	code, codeErr := decodeJSONInteger(envelope.Code)
	if codeErr != nil {
		return ProviderMeta{}, &ProtocolError{Kind: "业务码", Status: resp.StatusCode}
	}

	message := sanitizeText(envelope.Message, maxProviderMessage)
	if code != 1 {
		return ProviderMeta{}, &BusinessError{
			HTTPStatus: resp.StatusCode,
			Code:       code,
			Message:    message,
			RequestID:  sanitizeText(envelope.RequestID, 128),
		}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ProviderMeta{}, &ProtocolError{Kind: "业务码为 1 但 HTTP 非 2xx", Status: resp.StatusCode}
	}

	// data 缺失或为 null 是协议失败，**不是空结果**：伪装成空集合会让
	// 「上游改了响应形状」悄悄变成「今天没有商品」。
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return ProviderMeta{}, &ProtocolError{Kind: "响应 data", Status: resp.StatusCode}
	}
	if out != nil {
		if err := decodeOneJSON(envelope.Data, out); err != nil {
			return ProviderMeta{}, &ProtocolError{Kind: "响应 data 结构", Status: resp.StatusCode}
		}
	}

	providerTime, _ := decodeJSONInteger(envelope.Time)
	return ProviderMeta{
		RequestID:    sanitizeText(envelope.RequestID, 128),
		ProviderTime: providerTime,
	}, nil
}

// Info 是连接测试的返回：供应商观察到的我方出口 IP，以及 Key 的状态。
//
// 它的用处不是展示，是**排查**：上游若做 IP 白名单，这个值与白名单里那个
// 对不上就是全部 403 的原因，而那种失败从错误码上看只是「没权限」。
// 官方文档原话：「如果白名单不通过，优先调 /api/v1/info，查看当前服务端
// 识别到的 client_ip」。
type Info struct {
	// ClientIP 官方字段名是 client_ip。
	//
	// 此前按冻结源码读的是 ip——真上游会回一个空的出口 IP，而页面上空 IP
	// 看起来只是「这家没给」，没人会怀疑是我们读错了字段。保留 ip 作回退，
	// 是给旧版服务端留的余地，不是两个都对。
	ClientIP string
	// KeyStatus 是 Key 状态（官方：status=1 为启用）。响应结构未在文档里
	// 钉死，取不到就是 0，页面据此不显示而不是显示「已停用」。
	KeyStatus int64
}

// GetInfo 是只读的连接测试。
func (c *Client) GetInfo(ctx context.Context) (Info, error) {
	var raw map[string]any
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/info", nil, "", &raw); err != nil {
		return Info{}, err
	}
	return Info{
		ClientIP:  sanitizeText(scalarString(raw, "client_ip", "ip"), 64),
		KeyStatus: scalarInt(raw, "status", "key_status"),
	}, nil
}

// validateRequestURI 挡住把绝对 URL 或换行塞进路径的调用。
//
// 换行能拆出额外的 HTTP 头，绝对 URL 能把带密钥的请求发到别的主机上去。
func validateRequestURI(requestURI string) error {
	if !strings.HasPrefix(requestURI, "/api/v1/") || strings.ContainsAny(requestURI, "\r\n") {
		return connector.NewError(connector.KindRejected, "62-US 请求路径非法", nil)
	}
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return connector.NewError(connector.KindRejected, "62-US 请求路径非法", nil)
	}
	return nil
}

// decodeOneJSON 解一个 JSON 值，**拒绝尾随内容**。
//
// 尾随值意味着响应不是我们以为的那个形状；容忍它等于容忍一半解析成功。
func decodeOneJSON(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("尾随 JSON 值")
		}
		return err
	}
	return nil
}

// decodeJSONInteger 解一个 JSON 数字，**拒绝带引号的**。
//
// "1" 与 1 在这份协议里不是一回事：接受字符串会让一个把 code 写成字符串的
// 上游改动静默通过，而那正是需要被看见的时刻。
func decodeJSONInteger(raw json.RawMessage) (int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '"' {
		return 0, errors.New("JSON 整数缺失或被引号包裹")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	return number.Int64()
}

// sanitizeText 限长并去掉控制字符。
func sanitizeText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

// BusinessError 是上游明确的业务拒绝（HTTP 2xx 但 code != 1）。
//
// 与传输失败分开是要紧的：业务拒绝意味着**钱没花出去**，可以放心让人改参数
// 重来；传输失败意味着不确定，必须落 unknown。
type BusinessError struct {
	HTTPStatus int
	Code       int64
	Message    string
	RequestID  string
}

func (e *BusinessError) Error() string {
	if hint := e.Hint(); hint != "" {
		return fmt.Sprintf("62-US 业务错误 code=%d（%s）: %s", e.Code, hint, e.Message)
	}
	return fmt.Sprintf("62-US 业务错误 code=%d: %s", e.Code, e.Message)
}

// Hint 把官方错误码翻成**能指路**的中文。
//
// 40304 与 42204 的下一步完全不同（去加 IP 白名单 vs 去充值），而上游的 msg
// 是英文短语；页面上只显示「业务错误 code=40304」等于什么都没说。
// 表来自官方文档「常见错误码」一节（2026-09-06）。没列的码返回空串，
// **不猜**：一个猜出来的提示会把人带到错的地方。
func (e *BusinessError) Hint() string {
	return officialErrorHints[e.Code]
}

var officialErrorHints = map[int64]string{
	40101: "缺少 API Key——密钥引用未填或读取失败",
	40102: "API Key 无效——密钥填错了",
	40301: "API Key 已停用——去 62 后台启用它",
	40302: "API Key 已过期——去 62 后台续期或换新",
	40304: "请求 IP 不在白名单——把连接测试显示的出口 IP 加进 62 后台的白名单",
	40401: "Host 不允许——请求打到了错误的主机",
	42201: "goods/detail 参数错误——商品 ID 应为两段（平台-国家）",
	42202: "get 参数错误——商品 ID 应为三段（平台-国家-天数），数量 1–200",
	42203: "商品配置不存在——这个商品 ID 在 62 侧没有对应配置",
	42204: "余额不足——去 62 后台充值",
	42205: "库存不足——换商品或稍后再试",
	42206: "缺少 token——取码请求没带订单 token",
	42207: "order/tokens 参数错误——订单 ID 不对",
	40411: "token 不存在或无权限——号码不属于当前 Key 的账号",
	40412: "订单不存在或无权限——订单 ID 不属于当前 Key 的账号",
}

// ProtocolError 是响应不符合协议：解不开、形状不对、data 为空。
//
// 归为不确定而不是失败：一个解不开的响应说明不了上游做了什么。
type ProtocolError struct {
	Kind   string
	Status int
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("62-US 协议错误（%s，HTTP %d）", e.Kind, e.Status)
}
