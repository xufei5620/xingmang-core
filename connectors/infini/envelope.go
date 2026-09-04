package infini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// maxResponseBytes 限制单次响应体大小。
//
// 上游返回一个超大 JSON（分页被忽略、或上游出 bug）时，同步作业不该把
// worker 的内存吃光——超限归 bad_response。
const maxResponseBytes = 4 << 20

// successCode 是信封里表示成功的 code 值（文档：code === 0）。
const successCode = 0

// envelope 是上游统一的响应信封。
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// do 发一次已签名的请求，校验信封，把 data 解进 out（out 为 nil 时丢弃）。
//
// 错误一律归到 connector.ErrorKind，供应商原始文本只进 Unwrap 链
// （ADR-004：不把供应商错误透传给调用方）。
func (c *Client) do(ctx context.Context, method, pathWithQuery string, body []byte, out any) error {
	op := "infini " + method + " " + pathOnly(pathWithQuery)

	req, err := c.newRequest(ctx, method, pathWithQuery, body)
	if err != nil {
		return connector.NewError(connector.KindInternal, op, err)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		// 传输层自己的拒绝（方法不允许、目标不在 allowlist、重定向）已经
		// 是 connector.Error，原样返回，不要盖成 unavailable——
		// 那会把配置错误伪装成网络抖动。
		var ce *connector.Error
		if errors.As(err, &ce) {
			return err
		}
		return connector.NewError(connector.KindUnavailable, op, err)
	}
	defer resp.Body.Close()

	if kind, bad := kindForStatus(resp.StatusCode); bad {
		return connector.NewError(kind, op, fmt.Errorf("http %d", resp.StatusCode))
	}

	limited, err := connector.LimitResponseBody(resp, maxResponseBytes)
	if err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}

	if env.Code != successCode {
		// 上游的 message 只进 Unwrap 链（服务端日志看得到），不进 Error() 文本。
		return connector.NewError(connector.KindRejected, op,
			fmt.Errorf("upstream code %d: %s", env.Code, env.Message))
	}

	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	return nil
}

// kindForStatus 把 HTTP 状态码映射成错误分类。
//
// 401/403 单独成类的理由很实际：这条通道上 401 最可能的成因是 IP 白名单
// 没生效或本机时钟偏差超过 ±300 秒，两者的排查方向与「网络不通」完全不同，
// 吞成 unavailable 会让人往错误的方向查半天。
func kindForStatus(status int) (connector.ErrorKind, bool) {
	switch {
	case status >= 200 && status < 300:
		return "", false
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return connector.KindAuth, true
	case status == http.StatusTooManyRequests:
		return connector.KindRateLimited, true
	case status >= 500:
		return connector.KindUnavailable, true
	default:
		return connector.KindBadResponse, true
	}
}

// pathOnly 去掉查询串：错误文本会进日志与审计，查询串里可能带卡片 id。
func pathOnly(pathWithQuery string) string {
	if i := strings.IndexByte(pathWithQuery, '?'); i >= 0 {
		return pathWithQuery[:i]
	}
	return pathWithQuery
}
