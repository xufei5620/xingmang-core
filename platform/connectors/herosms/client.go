// Package herosms 是 Hero-SMS 接码供应商的客户端（XM-SMS0）。
//
// 协议形状取自 SoloAI 的冻结实现（2026-09-05 交接包），**其与当前官方服务的
// 一致性未在本仓实测**。
//
// 与 62 最要紧的两处不同，都会影响调用方怎么处置失败：
//
//   - **买号一次调用就拿到号码**（62 要再读一次订单 token）。因此这里没有
//     "买成功了但拿不到号"的那个不确定窗口。
//   - **密钥只在 Header**，请求 URL 里绝不出现（62 的取码 token 按其协议要进
//     query）。所以这一家可以说"URL 不含敏感值"，那一家不能。
package herosms

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

// ProductionBaseURL 是固定主机。理由同 sms62：让端点可配等于让一次配置错误
// 把密钥送到别处去。
const ProductionBaseURL = "https://hero-sms.com/api/v1"

// LegacyBaseURL 是 SMS-Activate 兼容层（官方 OpenAPI 的第二个 server）。
//
// 它存在的理由是给老客户软件用，但有几样东西**只有这一层有**：余额、
// 国家/服务/运营商/价格目录、Top 国家、租用号码。所以两层都要接，
// 而与现代层重复的十二个动作（getNumber、setStatus、getStatus…）刻意不接：
// 同一功能两套实现只会多一处漂移。
const LegacyBaseURL = "https://hero-sms.com/stubs/handler_api.php"

const purposeAPIKey = "herosms.api_key"

const (
	maxResponseBytes = 1 << 20
	maxProviderText  = 500
	defaultTimeout   = 15 * time.Second
)

// Client 是 Hero-SMS 的客户端。
type Client struct {
	baseURL  string
	provider secrets.SecretProvider
	keyRef   secrets.CredentialRef
	httpc    *http.Client
	// legacyBaseURL 是兼容层的完整入口（含 handler_api.php）。
	legacyBaseURL string

	apiKey string // 仅 fake/测试装配
}

// NewClient 组装真实客户端。凭据只存引用，每次调用现取（ADR-014）。
func NewClient(provider secrets.SecretProvider, keyRef secrets.CredentialRef, allowlist []string) *Client {
	return &Client{
		baseURL:       ProductionBaseURL,
		legacyBaseURL: LegacyBaseURL,
		provider:      provider,
		keyRef:        keyRef,
		httpc:         connector.NewVendorWriteClient(allowlist, defaultTimeout),
	}
}

func (c *Client) apiKeyValue(ctx context.Context) (string, error) {
	if c.provider == nil {
		return c.apiKey, nil
	}
	// 必须 Reveal()：String() 恒为 "[REDACTED]"，见 sms62 同名方法的注释。
	value, err := c.provider.Resolve(ctx, c.keyRef, purposeAPIKey)
	if err != nil {
		return "", connector.NewError(connector.KindAuth, "Hero-SMS 密钥未配置或读取失败", err)
	}
	key := strings.TrimSpace(value.Reveal())
	if key == "" {
		return "", connector.NewError(connector.KindAuth, "Hero-SMS 密钥为空", nil)
	}
	return key, nil
}

// do 发一次请求。
//
// 与 62 的信封不同，这家用的是"现代 JSON"：HTTP 状态码就是成功与否的判据。
// 两处特别之处：
//
//   - **204 必须是严格的 204**：body 非空或调用方还等着解结构，都判协议错误。
//     cancel/finish 靠它判成功，含糊一点就会把失败当成功。
//   - 4xx 归"明确拒绝"（钱没花出去），5xx 与传输失败归"不确定"。
//     这个分档是调用方决定"能不能重来"的唯一依据。
func (c *Client) do(ctx context.Context, method, requestURI string, body []byte, out any) error {
	if err := validateRequestURI(requestURI); err != nil {
		return err
	}
	key, err := c.apiKeyValue(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestURI, bytes.NewReader(body))
	if err != nil {
		return connector.NewError(connector.KindRejected, "构造 Hero-SMS 请求失败", err)
	}
	req.Header.Set("Authorization", "ApiKey "+key)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return connector.NewError(connector.KindUnavailable, "Hero-SMS 请求失败", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil || len(raw) > maxResponseBytes {
		return &ProtocolError{Kind: "响应体", Status: resp.StatusCode}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return decodeHTTPError(resp.StatusCode, raw)
	}
	if resp.StatusCode == http.StatusNoContent {
		if len(bytes.TrimSpace(raw)) != 0 || out != nil {
			return &ProtocolError{Kind: "204 却带了响应体", Status: resp.StatusCode}
		}
		return nil
	}
	if out == nil {
		return nil
	}
	if err := decodeOneJSON(raw, out); err != nil {
		return &ProtocolError{Kind: "响应结构", Status: resp.StatusCode}
	}
	return nil
}

// modernPathAllowed 是现代层允许的路径：官方 OpenAPI 里只有这三个前缀。
//
// 白名单而不是黑名单：一个拼错的路径打到上游只会换来含糊的 404，
// 而在这里被挡住会直接说「路径非法」。
func modernPathAllowed(path string) bool {
	for _, prefix := range []string{"/activations", "/classifiers", "/emails"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// validateRequestURI 只允许现代层的三个前缀，且禁止把 api_key 放 query。
//
// 后一条是主动防御：这家的密钥本来就走 Header，若哪天有人"顺手"改成 query，
// 密钥会开始出现在上游访问日志里，而那种泄漏没有任何报错。
func validateRequestURI(requestURI string) error {
	if strings.ContainsAny(requestURI, "\r\n") {
		return connector.NewError(connector.KindRejected, "Hero-SMS 请求路径非法", nil)
	}
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" ||
		!modernPathAllowed(parsed.Path) ||
		parsed.Query().Get("api_key") != "" {
		return connector.NewError(connector.KindRejected, "Hero-SMS 请求路径非法", nil)
	}
	return nil
}

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

// decodeHTTPError 把非 2xx 翻成分档明确的错误。
//
// 4xx = 明确拒绝（钱没花出去，可以改参数重来）；
// 5xx = 不确定（可能已经扣费，必须落 unknown 交人工）。
// 这个分档是调用方唯一能依据的东西，含糊就会导致重复购买。
func decodeHTTPError(status int, raw []byte) error {
	var body struct {
		Title   string          `json:"title"`
		Details string          `json:"details"`
		Errors  json.RawMessage `json:"errors"`
	}
	_ = decodeOneJSON(raw, &body)
	message := sanitizeText(strings.TrimSpace(body.Title+" "+body.Details), maxProviderText)

	if status >= http.StatusInternalServerError {
		return &UnknownOutcomeError{Status: status, Message: message}
	}
	return &RejectedError{Status: status, Message: message}
}

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

// RejectedError 是上游明确拒绝（4xx）：**钱没花出去**。
type RejectedError struct {
	Status  int
	Message string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("Hero-SMS 拒绝（HTTP %d）：%s", e.Status, e.Message)
}

// UnknownOutcomeError 是结果不确定（5xx）：**可能已经扣费**。
//
// 单独成型而不是复用 RejectedError，是为了让调用方在编译期就必须区分这两种
// ——把 5xx 当成拒绝去重试，正是重复买号的那条路。
type UnknownOutcomeError struct {
	Status  int
	Message string
}

func (e *UnknownOutcomeError) Error() string {
	return fmt.Sprintf("Hero-SMS 结果不确定（HTTP %d）：%s", e.Status, e.Message)
}

// ProtocolError 是响应不符合协议。同样归为不确定。
type ProtocolError struct {
	Kind   string
	Status int
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("Hero-SMS 协议错误（%s，HTTP %d）", e.Kind, e.Status)
}
