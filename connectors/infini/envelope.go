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
// 错误一律归到 connector.ErrorKind；上游的**状态码、业务码与脱敏后的
// message** 进 connector.Error.Detail（因而进对外错误文本与日志），
// **原始响应体只进 Unwrap 链**。
//
// 这是 XM-CARD-VISIBILITY 对既有写法的一次有意放宽：ADR-004 的铁律是
// 「第三方错误不得原样透传给**用户**」，而本文件此前把它执行成了
// 「不透传给**调用方**」——严过 ADR 一档。代价是 2026-09-08 的告警风暴里，
// 一条打了 1724 次的失败在日志与后台上都只写着
// 「rejected: infini POST /v2/cards/status/batch」，三天答不出为什么。
// 给用户看的那一层仍然是领域层翻好的中文指引（cards/upstream_error.go），
// 一个字的上游原文都不到那里。
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
		// 状态码 + 脱敏后的体开头进 Detail（对外文本与日志都看得到），
		// 原始体进 cause。少了体开头，一个 404 就只剩「bad_response」
		// 这五个字，运维完全无从下手——那等于没有错误分类。
		detail := fmt.Sprintf("http %d: %s", resp.StatusCode, bodyPrefix(raw))
		return connector.NewErrorWithDetail(kind, op, detail,
			fmt.Errorf("http %d: %s", resp.StatusCode, rawBodyPrefix(raw)))
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return connector.NewErrorWithDetail(connector.KindBadResponse, op,
			fmt.Sprintf("body=%s", bodyPrefix(raw)),
			fmt.Errorf("%w: body=%s", err, rawBodyPrefix(raw)))
	}

	if env.Code != successCode {
		// **本片最要紧的一行**。HTTP 200 + 非零 code 是卡端点业务失败的
		// 常规形状（见 kindForBusinessCode），而卡 API 没有错误码表——
		// message 的文字是此刻唯一存在的证据。把它丢掉就等于把诊断丢掉。
		return connector.NewErrorWithDetail(kindForBusinessCode(env.Code), op,
			fmt.Sprintf("upstream code %d: %s", env.Code, safeUpstreamText(env.Message)),
			fmt.Errorf("upstream code %d: %s", env.Code, env.Message))
	}

	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		// data 的形状与我们的结构体对不上时，也要看得到上游给的是什么——
		// 这是「契约漂移」最常见的暴露方式。data 里最可能装着卡面字段，
		// 所以进对外文本的那一份必须过脱敏。
		return connector.NewErrorWithDetail(connector.KindBadResponse, op,
			fmt.Sprintf("data=%s", bodyPrefix(env.Data)),
			fmt.Errorf("%w: data=%s", err, rawBodyPrefix(env.Data)))
	}
	return nil
}

// bodyPrefixLimit 是进错误链的响应体截断长度。
//
// 取 300 字节：够看清一段错误 JSON 或一个网关错误页的开头，
// 又不至于把一整页 HTML 灌进日志。
const bodyPrefixLimit = 300

// bodyPrefix 脱敏、截断响应体并压平换行，供**对外可见**的 Detail 使用。
//
// 压平与脱敏的先后顺序在 redactUpstreamText 里（它自己压平），这里不能
// 先压平再交给它：那样看起来一样，但三个 message 入口不经过本函数，
// 顺序保证就只覆盖了一半的路。要原样的那一份用 rawBodyPrefix。
func bodyPrefix(raw []byte) string {
	return safeUpstreamText(string(raw))
}

// rawBodyPrefix 只截断不脱敏，**仅供 Unwrap 链**。
//
// 保留它是因为 ADR-004 禁的是原样透传给用户，不是禁止运维在服务端日志里
// 看见原文；而对外那一份已经由 bodyPrefix 兜住。两个函数刻意同名同形，
// 调用点上一眼能看出选的是哪一份。
func rawBodyPrefix(raw []byte) string {
	return truncateFlat(strings.TrimSpace(string(raw)))
}

func truncateFlat(text string) string {
	if len(text) > bodyPrefixLimit {
		// 按字节切可能切断一个多字节字符；这里只是给人看的诊断串，
		// 用 ToValidUTF8 把切出来的半个字符抹掉，不让日志里出现乱码。
		text = strings.ToValidUTF8(text[:bodyPrefixLimit], "") + "…(截断)"
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

// kindForBusinessCode 把应用层信封里的非零 code 分成「怎么办」。
//
// 为什么要分：一律归 rejected 时，台账上只写着「上游拒绝」，而余额不足、
// 参数写错、IP 没放行、上游内部错这四种的处置完全不同——前三种改了条件
// 就能重来，最后一种**可能已经生效了**，重试就是第二次扣钱。
//
// **部分业务错误以 HTTP 200 + 非零 code 返回**（官方错误码页明写），所以
// 这一层不能只看 HTTP 状态码。
//
// 依据是 docs/en/8-errorcodes 与四份 OpenAPI 的错误示例（已入库
// contracts/connectors/infini/openapi/）。注意**卡 API 没有专属错误码表**：
// 它的端点在 OpenAPI 里只定义了 200 响应。所以这里只对确实有据的通用码与
// 资金码分档，卡端点的业务失败落到 rejected 的默认分支——按猜测给一张卡
// 端点的错误码表，只会在猜错时把一次「可能已生效」说成「确定没生效」。
func kindForBusinessCode(code int) connector.ErrorKind {
	switch code {
	case 401:
		return connector.KindAuth
	case 403:
		// 权限不足或 IP 未放行。与网关那两条同类：修的是配置不是请求。
		return connector.KindIPNotAllowed
	case 500:
		// 上游内部错误：请求可能已经在那边生效了。归到 unavailable 这一档，
		// 让领域层的 stateForUpstreamError 落 unknown 而不是 failed。
		return connector.KindUnavailable
	default:
		// 其余（含 30003 余额不足、30005 超限、30013 参数错等）都是上游在
		// 处理业务之前就明确拒绝了，钱确定没花出去。
		return connector.KindRejected
	}
}
