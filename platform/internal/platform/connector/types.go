package connector

import (
	"errors"
	"time"
)

// ErrorKind 是第三方交互失败的稳定分类。
//
// 存在的意义是不把供应商原始错误透传出去（ADR-004 铁律）：调用方按 Kind
// 决策（重试？降级？告警？），原始文本只进服务端日志。
type ErrorKind string

const (
	KindUnavailable ErrorKind = "unavailable" // 网络不可达、超时、上游 5xx
	KindAuth        ErrorKind = "auth"        // 凭据无效或权限不足
	// KindIPNotAllowed：请求到达了上游，但来源 IP 不在**上游**的白名单里。
	//
	// 与 KindAuth 分开的理由是修法不同：auth 要去查密钥与签名，
	// 这一类要去上游后台加 IP。两者都是 401/403，归成一类会让台账上只剩
	// 「认证失败」四个字，每次都要人工二选一去试。
	// 注意与 KindForbiddenTarget 的区别：那一个是**我们自己的** allowlist
	// 拦下了出站请求，请求根本没发出去。
	KindIPNotAllowed    ErrorKind = "ip_not_allowed"
	KindRateLimited     ErrorKind = "rate_limited"     // 429 / Retry-After
	KindNotSupported    ErrorKind = "not_supported"    // 上游版本不支持该能力
	KindBadResponse     ErrorKind = "bad_response"     // 响应格式非法、字段缺失、超大
	KindForbiddenTarget ErrorKind = "forbidden_target" // 目标不在 allowlist（闸 4）
	KindWriteAttempt    ErrorKind = "write_attempt"    // 只读通道上出现写请求（闸 4）
	// KindMethodNotAllowed 是供应商写通道上出现允许集之外的 HTTP 方法。
	// 与 KindWriteAttempt 分开：那个表示「这条通道根本不该有写」，
	// 这个表示「这条通道能写，但不该用这个方法」，排查方向完全不同。
	KindMethodNotAllowed ErrorKind = "method_not_allowed"
	// KindRejected 是上游收下了请求但业务上拒绝（余额不足、产品不可用等）。
	// 与 KindUnavailable 分开：那个重试有意义，这个重试没有意义。
	KindRejected ErrorKind = "rejected"
	KindInternal ErrorKind = "internal"
)

// Error 是 Connector 层的统一错误。
type Error struct {
	Kind ErrorKind
	Op   string // 出错的操作，如 "sub2api.users.read"
	// Detail 是**已经脱敏过**的上游简述（业务码 + 处理过的 message、
	// 或本地前置拒绝的原因），会进 Error() 文本。
	//
	// 为什么要有它（XM-CARD-VISIBILITY）：只有 Kind + Op 时，一次失败在日志、
	// 作业错误串与后台上都只剩「rejected: infini POST /v2/cards/status/batch」
	// 这 40 个字符。2026-09-08 的告警风暴里，运维盯着这一句查了三天也答不出
	// 「LINFENG 到底为什么被拒」——分类回答的是「要不要重试」，回答不了
	// 「要改什么」。
	//
	// ADR-004 的铁律是「第三方错误不得原样透传给**用户**」：这里进对外文本的
	// 不是原文，是脱敏后的码 + 摘要，且面向的是管理端运维；真正给用户看的
	// 那一层仍由领域层翻成中文指引（见 cards/upstream_error.go）。
	// **原始响应体永远只进 cause**，不进 Detail。
	Detail string
	cause  error
}

// NewError 构造 Connector 错误。cause 只进 Unwrap 链，不进 Error() 文本。
//
// 行为与 Detail 引入之前逐字节一致（Detail 为空时 Error() 不变），
// 因此其余六个连接器的对外错误文本一个字都没动。
func NewError(kind ErrorKind, op string, cause error) error {
	return &Error{Kind: kind, Op: op, cause: cause}
}

// NewErrorWithDetail 构造带脱敏简述的 Connector 错误。
//
// detail **必须是调用方已经脱敏过的文本**：这个函数不做脱敏，它只负责
// 把简述放进对外文本。原始响应体请放 cause。
func NewErrorWithDetail(kind ErrorKind, op, detail string, cause error) error {
	return &Error{Kind: kind, Op: op, Detail: detail, cause: cause}
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Kind) + ": " + e.Op
	}
	return string(e.Kind) + ": " + e.Op + " (" + e.Detail + ")"
}

// DetailOf 取出错误链里最靠外的那条脱敏简述；没有则返回空串。
//
// 供作业与日志显式取用：调用方不必知道 *Error 的形状，也不会不小心
// 把整条 Unwrap 链（含原始响应体）打进日志。
func DetailOf(err error) string {
	if err == nil {
		return ""
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Detail
	}
	return ""
}

// Unwrap 暴露根因给日志与 errors.Is，不进对外文本。
func (e *Error) Unwrap() error { return e.cause }

// KindOf 提取错误分类；非 Connector 错误归为 internal，nil 返回空串。
func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Kind
	}
	return KindInternal
}

// VersionInfo 是上游版本探测结果（规格 §8.1）。
type VersionInfo struct {
	Detected    string
	Fingerprint string
	// Supported 表示该版本是否在 Connector 的兼容矩阵内。
	// 不支持时返回 Supported=false 而非报错——是否 Fail Closed 由调用方按
	// 场景决定（读取可降级，写入必须停）。
	Supported  bool
	DetectedAt time.Time
}

// HealthResult 是健康检查结果（规格 §8.1）。
type HealthResult struct {
	Healthy   bool
	CheckedAt time.Time
	LatencyMS int64
	ErrorKind ErrorKind
	// Detail 是安全的简短说明，不含供应商原始错误文本。
	Detail string
}
