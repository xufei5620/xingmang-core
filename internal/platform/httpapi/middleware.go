package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

// safeRequestID：调用方提供的值会进日志与响应头，因此限字符集与长度，
// 防止日志注入（换行伪造日志行）与响应头注入。
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// RequestID 读取或生成请求 ID，写入响应头与上下文（规格 §5.8）。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// statusRecorder 记录写出的状态码供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// principalRecorder 是身份中间件写给访问日志的单向信道。
//
// 为什么需要它：RequirePrincipal 用 `r.WithContext(...)` 把 Principal 放进一个
// **派生**上下文，那个上下文只对内层可见；AccessLog 包在外面，手里的 `r` 永远
// 是解析之前的那个，读不到身份。要么把身份解析挪到最外层（那样 panic 恢复与
// 请求 ID 就排在鉴权之后，顺序更糟），要么留一个显式的可变槽——选后者。
//
// 带锁不是过度设计：Timeout 中间件用 http.TimeoutHandler，它在**另一个
// goroutine** 里跑内层链，超时后自己先返回。于是 AccessLog 读 id 时内层可能
// 还在写。没有锁这就是一个真实的数据竞争，而且只在超时路径上偶发。
type principalRecorder struct {
	mu sync.Mutex
	id string
}

func (p *principalRecorder) set(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id = id
}

func (p *principalRecorder) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.id
}

type principalRecorderKey struct{}

// recordPrincipalID 把已解析的身份告知访问日志。
// 没有槽（例如单测直接调 handler）时静默返回——日志字段缺失不该让请求失败。
func recordPrincipalID(ctx context.Context, id string) {
	if rec, ok := ctx.Value(principalRecorderKey{}).(*principalRecorder); ok {
		rec.set(id)
	}
}

// AccessLog 按规格 §18.8 的字段记录访问日志。
//
// 记录方法、路径、状态、耗时、request_id 与 principal_id ——
// **不记录任何 Header**，因为 Authorization / Cookie 会带 Token（宪法 7 条）。
//
// principal_id 是 XM-0031 补的（回归 Codex 冷审 PR #47 第 4 条 / PR #43 head
// `0a0642c` 第 4 条）：「高敏读取不可归责……事后无法回答谁拉取过审计数据」。
// /api/v1/audit/events 会返回操作前后镜像，/api/v1/metrics 会返回收入与余额；
// 这类读取出了事必须能回答「是谁在什么时候拉走的」。只记 method/path/status
// 时，日志能证明「有人拉过 100 条审计」，却证明不了是谁。
//
// 身份未解析时（探针、403 的无身份请求、身份解析本身失败）记空串而不是省略
// 字段：字段恒在，日志检索不必区分「没有这个字段」与「值为空」；而空串本身
// 就是一个事实——这次访问没有可归责的身份。
//
// **只记 principal_id，不记 scopes / issuer / 身份类型**：ID 足以归责，其余是
// 授权决策的输入，进日志只会扩大留存面。需要复原授权判定时看审计链。
// Logging 把进程的结构化 logger 放进请求上下文，供 WriteError 等取用。
//
// 与 AccessLog 分开是因为两件事的时机不同：AccessLog 在 next 返回**之后**
// 才记一条访问日志，而 handler 在执行**期间**就需要那个 logger。把注入折进
// AccessLog 也做得到，但那会让「取 logger」隐式依赖「有没有开访问日志」——
// 一个独立的、只做一件事的中间件更难被误关。
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithLogger(r.Context(), logger)))
		})
	}
}

func AccessLog(logger *slog.Logger, service, environment string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			who := &principalRecorder{}
			r = r.WithContext(context.WithValue(r.Context(), principalRecorderKey{}, who))
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http_request",
				slog.String("service", service),
				slog.String("module", "httpapi"),
				slog.String("environment", environment),
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("principal_id", who.get()),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}

// NoStore 给响应打上 `Cache-Control: no-store`。
//
// 为什么是路由中间件而不是塞进 WriteJSON（XM-0031，回归 Codex 冷审 PR #47
// 第 8 条 / PR #43 head `0a0642c` 第 5 条）：
//
//  1. **这是策略，不是编码细节。** WriteJSON 回答「怎么把值变成字节」，
//     缓存语义回答「谁可以把这些字节存下来」。把后者藏进序列化 helper，
//     路由表就不再是「哪个端点受什么约束」的完整清单——而 RequireScope 已经
//     立了「约束写在路由上」这个规矩，两套地方声明会分叉。
//  2. **作用范围要能被看见。** 装在 /api/v1 整组上，新加的端点自动继承，
//     不依赖作者记得调用某个特定的写出函数；而 /healthz、/readyz 保持可缓存
//     ——反代与看门狗对探针做几秒微缓存是合理的，不该被顺手波及。
//  3. **错误响应也要覆盖。** 中间件在写响应头之前就设好，无论 handler 走的是
//     WriteJSON、WriteError 还是 TimeoutHandler 的固定错误体。
//
// 指令只写 `no-store`，不写 `private, no-store`：no-store 禁止**任何**缓存
// （共享的与私有的）把响应落到存储，严格强于只约束共享缓存的 private。两个
// 并列会让人以为它们互补，将来有人「优化」成只留 private 时也看不出退化。
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// Recover 把 panic 转成 500，细节只进日志不进响应。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.ErrorContext(r.Context(), "http_panic",
						slog.String("module", "httpapi"),
						slog.String("request_id", RequestIDFrom(r.Context())),
						slog.String("path", r.URL.Path),
						slog.String("error_code", "panic"),
						slog.Any("panic", v),
					)
					WriteJSON(w, http.StatusInternalServerError, ErrorResponse{Error: ErrorBody{
						Code:      "INTERNAL",
						Message:   "服务内部错误",
						RequestID: RequestIDFrom(r.Context()),
					}})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout 给请求上下文加期限（规格 §18.1-4：所有外部 I/O 必须有超时）。
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, `{"error":{"code":"TIMEOUT","message":"请求超时"}}`)
	}
}
