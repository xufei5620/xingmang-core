package httpapi

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// XM-R011 读端点限流（Codex 冷审 #47/#48，Issue #75）。
//
// ── 修的是什么 ────────────────────────────────────────────────────────────
// /api/v1/metrics/history 与 /api/v1/audit/events 可以低成本反复拉取:
// `limit` 是**行数**上限,不是字节上限,而审计事件带操作前后镜像、指标历史带
// jsonb 值。一个有合法 audit.read 的身份反复翻页就能把库和进程内存拖住,
// 而访问日志里它每一条都是 200——看起来完全正常。
//
// ── 为什么按 (Principal, 路由) 分桶 ───────────────────────────────────────
// 只按 Principal:一个身份拉审计会顺带堵死自己的看板刷新,而两者的代价差着
// 一个数量级。只按路由:一个身份就能把所有人的额度吃光,限流反而成了
// 拒绝服务的工具。两者组合才让「谁在拉什么」各自计费。
//
// 路由用 chi 的**模板**（`/api/v1/platforms/{platform}/requests`)而不是实际
// 路径:按实际路径分桶的话,换一个 platform 就是一个新桶,限流形同虚设。
//
// ── 为什么是内存令牌桶 ────────────────────────────────────────────────────
// 单副本部署下够用,且不引入 Redis 依赖(VERSIONS.lock 里没有它)。
// **多副本时每个副本各算各的**,实际额度是 N 倍——这是已知的、写在文档里的
// 限制,不是遗漏。真要跨副本一致,那是另一件事(共享计数器 + 它自己的可用性
// 问题),不该顺手塞进这次加固。
//
// ── 探针为什么天然豁免 ────────────────────────────────────────────────────
// /healthz 与 /readyz 挂在路由根上,不在 /api/v1 组里,而本中间件只装在
// /api/v1 上。反代与看门狗每几秒探一次,给它们限流等于自己造一次假故障。
// 这条靠**装配位置**保证,不靠一份要维护的豁免名单——名单会漏,位置不会。

const (
	// DefaultRateLimitPerMinute 是每 (Principal, 路由) 每分钟的默认配额。
	//
	// 120 是「人正常用不到、脚本一跑就撞上」的量级：看板一屏最多几个请求，
	// 手动翻页也到不了每秒两次。定得再低会误伤自动刷新的看板，
	// 再高就挡不住反复拉取。
	DefaultRateLimitPerMinute = 120

	// DefaultRateLimitBurst 是瞬时可用的令牌数。
	//
	// 允许一屏之内并发发几个请求（看板打开时会同时要指标、告警、服务清单），
	// 但不允许把一分钟的额度一次性花光。
	DefaultRateLimitBurst = 20

	// rateLimitSweepInterval 是清理闲置桶的间隔。
	//
	// 不清理的话，map 会随「见过多少个 (身份, 路由) 组合」单调增长——
	// 一个被撤销的身份留下的桶永远不会再被用到，却一直占着内存。
	rateLimitSweepInterval = 10 * time.Minute
	// rateLimitIdleTTL 是一个桶多久没被碰过就可以丢掉。
	// 必须显著长于恢复满桶所需的时间，否则清理会变相重置额度。
	rateLimitIdleTTL = 15 * time.Minute
)

// RateLimitConfig 是限流的可配参数。
type RateLimitConfig struct {
	// PerMinute 是每 (Principal, 路由) 每分钟允许的请求数；<=0 用默认值。
	PerMinute int
	// Burst 是瞬时并发额度；<=0 用默认值。
	Burst int
	// Now 可注入时钟（测试用）。
	Now func() time.Time
}

func (c RateLimitConfig) normalized() RateLimitConfig {
	if c.PerMinute <= 0 {
		c.PerMinute = DefaultRateLimitPerMinute
	}
	if c.Burst <= 0 {
		c.Burst = DefaultRateLimitBurst
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// bucket 是一个令牌桶。
//
// 存 tokens（浮点）+ 上次补充时刻，而不是「窗口内计数」：固定窗口会在窗口
// 边界上放行两倍流量（窗口末尾打满 + 新窗口开头再打满），而令牌桶天然平滑。
//
// tokens 用 float64 是这里少数几个浮点合理的地方之一——它不是金额，是一个
// 会被连续补充的配额，取整会让低速率下的补充永远凑不满一个令牌。
type bucket struct {
	tokens   float64
	lastFill time.Time
	lastSeen time.Time
}

// RateLimiter 是按 (Principal, 路由) 分桶的内存令牌桶。
type RateLimiter struct {
	cfg RateLimitConfig
	// refillPerSecond 是每秒补充的令牌数。
	refillPerSecond float64

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// NewRateLimiter 构造限流器。
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	c := cfg.normalized()
	return &RateLimiter{
		cfg:             c,
		refillPerSecond: float64(c.PerMinute) / 60,
		buckets:         map[string]*bucket{},
		lastSweep:       c.Now(),
	}
}

// Allow 取一个令牌。
//
// 第二个返回值是建议的重试等待时长：被拒时它是「攒够一个令牌还要多久」，
// 直接进 Retry-After。给一个**算出来的**值而不是固定常数：固定值要么太短
// （客户端立刻重试、继续被拒、双方都在空转），要么太长（额度早就恢复了还在等）。
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	now := l.cfg.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		// 新桶从**满**开始：一个刚出现的身份不该因为「桶还没攒起来」被拒。
		b = &bucket{tokens: float64(l.cfg.Burst), lastFill: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastFill).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(float64(l.cfg.Burst), b.tokens+elapsed*l.refillPerSecond)
			b.lastFill = now
		}
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// 还差多少令牌 ÷ 补充速率 = 还要等多久。向上取整到秒：Retry-After 的单位
	// 就是秒，返回 0 会让客户端立刻重试并再次被拒。
	need := 1 - b.tokens
	wait := time.Duration(math.Ceil(need/l.refillPerSecond)) * time.Second
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// sweepLocked 丢掉长期没被碰过的桶。调用方必须持锁。
//
// 顺带在写路径上做而不是起一个 goroutine：限流器的生命周期与路由一样长，
// 一个后台 goroutine 要么泄漏、要么需要一套关闭接线，而清理本身很廉价。
func (l *RateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < rateLimitSweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > rateLimitIdleTTL {
			delete(l.buckets, k)
		}
	}
}

// RateLimit 是限流中间件。
//
// **必须装在 RequirePrincipal 之后**：桶键要用已解析的身份，而不是调用方
// 声称的那个。装反了等于让伪造者换个 Header 就换一个新桶。
//
// 身份缺失时（理论上走不到，那说明装配顺序错了）用一个固定的兜底键限流，
// 而不是放行：放行会让「装配顺序写错」这种失误静默地把限流整个关掉。
func RateLimit(limiter *RateLimiter, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rateLimitKey(r)
			allowed, retryAfter := limiter.Allow(key)
			if allowed {
				next.ServeHTTP(w, r)
				return
			}

			// 被限的请求要能被查出来：一个只在客户端看得见的 429，
			// 运维在服务端翻日志时完全不知道发生过。
			// **不记桶键**——它含 principal_id，而 AccessLog 已经记了那个；
			// 这里再记一遍只是把同一个标识多留一份。
			logger.WarnContext(r.Context(), "请求被限流",
				slog.String("module", "httpapi"),
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("path", r.URL.Path),
				slog.String("route", routePattern(r)),
				slog.String("error_code", "rate_limited"),
				slog.Int("retry_after_seconds", int(retryAfter.Seconds())),
			)

			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			WriteJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: ErrorBody{
				Code: "RATE_LIMITED",
				// 文案说清「等多久」而不是只说「太频繁了」：后者不给人下一步。
				Message:   "请求过于频繁，请在 " + strconv.Itoa(int(retryAfter.Seconds())) + " 秒后重试",
				RequestID: RequestIDFrom(r.Context()),
			}})
		})
	}
}

// rateLimitKey 是桶键：已解析身份 + 路由模板。
func rateLimitKey(r *http.Request) string {
	id := "anonymous"
	if p, ok := principal.FromContext(r.Context()); ok && p.ID != "" {
		id = p.ID
	}
	// 用 \x00 连接而不是冒号：principal_id 里可能含冒号（`staff:operator-01`），
	// 用冒号会让两个不同的 (身份, 路由) 组合拼出同一个键。
	// 这与 XM-R009 修的是同一类错，只是这里的后果轻得多。
	return id + "\x00" + routePattern(r)
}

// routePattern 返回 chi 匹配到的**路由模板**（`/api/v1/platforms/{platform}/requests`）。
//
// 用模板而不是 r.URL.Path：按实际路径分桶的话，换一个 {platform} 就是一个新桶，
// 限流形同虚设——攻击者只要遍历路径参数就能拿到无限额度。
//
// 取不到模板时（中间件在路由匹配之前跑、或不是 chi 路由）回落到方法 + 路径。
// 回落比返回空串好：空串会把所有端点并进同一个桶，一个身份刷审计会连带
// 限住它自己的看板刷新。
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return r.Method + " " + r.URL.Path
}
