package assurance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// chatCompletionsPath 是被管平台自己面向付费用户暴露的 OpenAI 兼容推理端点
// （ADR-019 决策·一）。探测从不直接联系上游厂商，永远只打这条路径。
const chatCompletionsPath = "/v1/chat/completions"

// RealClientConfig 是构造 real 模式探测客户端所需的全部输入。
//
// **不含 CredentialRef 本身**：探测专用凭据的解析（secret:// 引用 →
// 明文）由调用方（internal/platform/jobs/assurance_probe.go 的 Work()）
// 经既有的 secrets.SecretProvider 完成，本类型只接收解析后的 Token——与
// internal/platform/connector 的既有纪律一致：Connector 内部从不认识
// CredentialRef 语法本身，只拿到已经解出的值（ADR-014）。
type RealClientConfig struct {
	// TargetHost 是主机名（不含 scheme），已经过调用方对
	// target_allowlist 的校验（本类型自己不重复校验白名单——那是 Handler/
	// Job 执行时刻复检的职责，见 store.go EvaluateRun 与
	// jobs/assurance_probe.go 的注释）。
	TargetHost string
	// Token 是已解析的 Bearer 凭据值。
	Token string
	// HTTPClient 可选；为 nil 时用 Timeout 构造一个默认客户端。
	HTTPClient *http.Client
	// Timeout 只在 HTTPClient 为 nil 时生效。
	Timeout time.Duration
}

// RealClient 是 real 模式探测客户端：对 TargetHost 的 OpenAI 兼容
// chat/completions 端点发一次真实请求。
type RealClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewRealClient 构造客户端。
func NewRealClient(cfg RealClientConfig) (*RealClient, error) {
	host := strings.TrimSpace(cfg.TargetHost)
	if host == "" {
		return nil, fmt.Errorf("target_host 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("probe token 为空: %w", ErrInvalidInput)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &RealClient{
		baseURL: "https://" + host,
		token:   cfg.Token,
		http:    httpClient,
	}, nil
}

type chatCompletionRequestBody struct {
	Model     string            `json:"model"`
	Messages  []chatMessageWire `json:"messages"`
	MaxTokens int               `json:"max_tokens"`
	Stream    bool              `json:"stream"`
}

type chatMessageWire struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponseBody struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete 实现 ProbeClient：POST 一次非流式 chat/completions 请求。
//
// 错误一律映射为 connector.ErrorKind（复用既有的分类习惯，设计稿 §3.2 步骤
// 5）：从不把上游原始响应体/错误文本透传给调用方（威胁模型 §7.1）。
// TokensUsed/首字节延迟：本客户端用非流式请求（Stream=false），因此永远
// 测不到首字节——Measured 恒为 false，与 ASSURE0 的 MeasuredTTFB 纪律一致，
// 不用总耗时冒充首字节。
func (c *RealClient) Complete(ctx context.Context, req ProbeRequest) (ProbeResponse, error) {
	messages := make([]chatMessageWire, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, chatMessageWire{Role: m.Role, Content: m.Content})
	}
	body, err := json.Marshal(chatCompletionRequestBody{
		Model: req.Model, Messages: messages, MaxTokens: req.MaxTokens, Stream: false,
	})
	if err != nil {
		return ProbeResponse{}, connector.NewError(connector.KindInternal, "assurance.real_probe.marshal", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return ProbeResponse{}, connector.NewError(connector.KindInternal, "assurance.real_probe.build_request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProbeResponse{}, ctxErr
		}
		return ProbeResponse{}, connector.NewError(connector.KindUnavailable, "assurance.real_probe.do", err)
	}
	defer resp.Body.Close()

	const maxBodyBytes = 1 << 20 // 1MiB：探测响应应当很小；给一个硬顶防止异常大响应吃满内存
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ProbeResponse{}, connector.NewError(connector.KindBadResponse, "assurance.real_probe.read_body", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ProbeResponse{HTTPStatus: resp.StatusCode},
			connector.NewError(connector.KindAuth, "assurance.real_probe.auth", fmt.Errorf("http %d", resp.StatusCode))
	case resp.StatusCode == http.StatusTooManyRequests:
		return ProbeResponse{HTTPStatus: resp.StatusCode},
			connector.NewError(connector.KindRateLimited, "assurance.real_probe.rate_limited", fmt.Errorf("http %d", resp.StatusCode))
	case resp.StatusCode >= 500:
		return ProbeResponse{HTTPStatus: resp.StatusCode},
			connector.NewError(connector.KindUnavailable, "assurance.real_probe.upstream_5xx", fmt.Errorf("http %d", resp.StatusCode))
	case resp.StatusCode >= 400:
		return ProbeResponse{HTTPStatus: resp.StatusCode},
			connector.NewError(connector.KindBadResponse, "assurance.real_probe.upstream_4xx", fmt.Errorf("http %d", resp.StatusCode))
	}

	var parsed chatCompletionResponseBody
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ProbeResponse{HTTPStatus: resp.StatusCode},
			connector.NewError(connector.KindBadResponse, "assurance.real_probe.decode", err)
	}
	if len(parsed.Choices) == 0 {
		return ProbeResponse{HTTPStatus: resp.StatusCode},
			connector.NewError(connector.KindBadResponse, "assurance.real_probe.empty_choices", fmt.Errorf("choices 为空"))
	}
	return ProbeResponse{
		Text:       parsed.Choices[0].Message.Content,
		HTTPStatus: resp.StatusCode,
		TokensUsed: parsed.Usage.CompletionTokens,
		LatencyMS:  latency,
		Measured:   false,
	}, nil
}
