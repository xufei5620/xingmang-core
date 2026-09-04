package infini

import (
	"bytes"
	"context"
	"net/http"
	"time"
)

// Client 是 Infini 卡服务的 HTTP 客户端。
//
// 凭据以值的形式驻留在结构体里，只在签名时使用；任何路径上都不会把它写进
// 请求头、URL、日志或错误文本（宪法条款 7）。解析来源是 CredentialRef，
// 见 connection 配置装配处。
type Client struct {
	baseURL string
	keyID   string
	secret  string
	httpc   *http.Client

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
		c.keyID,
		signature(c.secret, signingString(c.keyID, method, pathWithQuery, date)),
	))

	return req, nil
}
