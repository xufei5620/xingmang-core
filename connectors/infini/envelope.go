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

	// 先读体再判状态码：状态码之外还需要「上游到底回了什么」才能排查。
	// 体是限长的，所以先读不会有内存风险。
	limited, err := connector.LimitResponseBody(resp, maxResponseBytes)
	if err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}

	if kind, bad := kindForStatus(resp.StatusCode); bad {
		if kind == connector.KindAuth {
			// 401/403 再细分一次：签名问题与 IP 白名单问题的修法完全不同。
			kind = kindForUnauthorized(string(raw))
		}
		// 状态码 + 体开头都进 cause（只进 Unwrap 链与服务端日志，
		// 不进对外错误文本）。少了体开头，一个 404 就只剩「bad_response」
		// 这五个字，运维完全无从下手——那等于没有错误分类。
		return connector.NewError(kind, op,
			fmt.Errorf("http %d: %s", resp.StatusCode, bodyPrefix(raw)))
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("%w: body=%s", err, bodyPrefix(raw)))
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
		// data 的形状与我们的结构体对不上时，也要看得到上游给的是什么——
		// 这是「契约漂移」最常见的暴露方式。
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("%w: data=%s", err, bodyPrefix(env.Data)))
	}
	return nil
}

// bodyPrefixLimit 是进错误链的响应体截断长度。
//
// 取 300 字节：够看清一段错误 JSON 或一个网关错误页的开头，
// 又不至于把一整页 HTML 灌进日志。
const bodyPrefixLimit = 300

// bodyPrefix 截断响应体并压平换行，供错误链使用。
//
// 只进 Unwrap 链（服务端日志看得到），不进 Error() 文本——ADR-004 禁的是
// 把供应商原文透传给**调用方**，不是禁止运维看见它。
func bodyPrefix(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > bodyPrefixLimit {
		text = text[:bodyPrefixLimit] + "…(截断)"
	}
	return strings.Join(strings.Fields(text), " ")
}

// kindForUnauthorized 在 401/403 里进一步区分「签名不对」与「IP 不在白名单」。
//
// 依据是官方参考实现列出的两条网关文案（infini-skill/references/
// ERROR_HANDLING.md）：
//
//	{"message":"ip not in whitelist"}            → 源 IP 没放行
//	{"message":"client request can't be validated"} → HMAC/头/Digest/密钥不匹配
//
// 认不出的文案保守归到 auth：把一个未知的认证失败说成「IP 问题」，
// 会让人跑去改白名单而真正的原因没人碰。
func kindForUnauthorized(body string) connector.ErrorKind {
	if strings.Contains(strings.ToLower(body), "ip not in whitelist") {
		return connector.KindIPNotAllowed
	}
	return connector.KindAuth
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
		// 具体是签名还是 IP，由 kindForUnauthorized 看响应体决定。
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
