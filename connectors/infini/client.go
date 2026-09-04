package infini

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Client 是 Infini 卡服务的 HTTP 客户端。
//
// 凭据以值的形式驻留在结构体里，只在签名时使用；任何路径上都不会把它写进
// 请求头、URL、日志或错误文本（宪法条款 7）。解析来源是 CredentialRef，
// 见 connection 配置装配处。
type Client struct {
	baseURL string
	// keyID/secret 只在测试与 fake 装配里直接赋值；生产路径经 provider 解析。
	keyID  string
	secret string
	// provider 非 nil 时每次调用解析凭据，支持轮换而不必重启进程。
	provider  secrets.SecretProvider
	keyIDRef  secrets.CredentialRef
	secretRef secrets.CredentialRef
	httpc     *http.Client

	// now 可注入，让 Date 头在测试里可预测。生产用 time.Now。
	now func() time.Time
}

// newRequest 组装一个已签名的请求。
//
// 三个头的关系（文档第 4 章）：
//
//	Date          RFC1123 GMT，服务端只容忍 ±300 秒偏差
//	Digest        仅在有请求体时出现，Base64(SHA-256(body))，**不参与签名**
//	Authorization Signature 方案，签的是 keyId + 方法 + 含查询串的路径 + date
//
// 签名与 Date 头取自同一个时刻：分两次取 time.Now() 会在跨秒的请求上偶发 401，
// 而那种失败极难复现。
func (c *Client) newRequest(ctx context.Context, method, pathWithQuery string, body []byte) (*http.Request, error) {
	keyID, secret, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}

	date := c.now().UTC().Format(http.TimeFormat)

	var reader *bytes.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathWithQuery, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Date", date)
	if d := digestHeader(body); d != "" {
		req.Header.Set("Digest", d)
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", authorizationHeader(
		keyID,
		signature(secret, signingString(keyID, method, pathWithQuery, date)),
	))

	return req, nil
}

// 凭据用途与调用方身份，进凭据审计（ADR-014 / 规格 §4.5），不影响解析结果。
const (
	credentialCaller     = "connector:" + ConnectorKey
	purposeKeyID         = "infini card api key id"
	purposeSecret        = "infini card api secret"
	defaultClientTimeout = 30 * time.Second
)

// NewClient 组装真实客户端。
//
// 凭据以 CredentialRef 传入并**每次调用时解析**，不在构造期取一次就长期
// 持有：密钥轮换后进程不必重启（ADR-014 的轮换要求），而 SecretProvider
// 那一层本来就负责缓存与审计。
//
// allowlist 通常只有 base URL 的那一个主机——写通道 fail closed，
// 放宽到多个主机没有业务理由。
func NewClient(
	baseURL string,
	provider secrets.SecretProvider,
	keyIDRef, secretRef secrets.CredentialRef,
	allowlist []string,
) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		provider:  provider,
		keyIDRef:  keyIDRef,
		secretRef: secretRef,
		httpc:     connector.NewVendorWriteClient(allowlist, defaultClientTimeout),
		now:       time.Now,
	}
}

// credentials 解析出 keyId 与 secret。
//
// 明文只在调用栈上存在：不进结构体字段、不进日志、不进错误文本。
// 解析失败一律 auth 类错误——那正是「凭据没配好」该被归到的地方，
// 而不是被当成网络问题排查。
func (c *Client) credentials(ctx context.Context) (string, string, error) {
	if c.provider == nil {
		// fake 模式或测试装配：keyID/secret 直接给在结构体上。
		return c.keyID, c.secret, nil
	}

	ctx = secrets.WithCaller(ctx, credentialCaller)
	keyID, err := c.provider.Resolve(ctx, c.keyIDRef, purposeKeyID)
	if err != nil {
		return "", "", connector.NewError(connector.KindAuth, "infini credentials", err)
	}
	secret, err := c.provider.Resolve(ctx, c.secretRef, purposeSecret)
	if err != nil {
		return "", "", connector.NewError(connector.KindAuth, "infini credentials", err)
	}
	return keyID.Reveal(), secret.Reveal(), nil
}
