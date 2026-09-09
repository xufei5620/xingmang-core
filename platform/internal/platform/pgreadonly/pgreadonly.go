// Package pgreadonly 把一条 PostgreSQL 连接串变成一个「只可能只读」的连接池。
//
// 平台有两处要只读直连**别人家的库**：
//
//	connectors/metering  自营 new-api 库的收入取数（XM-0044，设计稿 §3.2）
//	internal/platform/shadow  SoloAI 库的影子对比基准（XM-0037e，设计稿 §9）
//
// 两处的四道闸必须是**同一份实现**。各写一份的话，两份迟早走偏，而走偏的那一份
// 守的是一条别人以为还在的防线——这正是本包被从 metering 里提出来的理由。
//
// ── ADR-018 四道只读闸在数据库通道上的落点 ────────────────────────────────
// 闸 2/3 的原文本来就是针对数据库通道写的（HTTP 通道只能「部分满足」）：
//
//	闸 1 拒绝可写连接配置 → Config：显式写 default_transaction_read_only=off
//	                        的连接串直接拒（调用方另需用 pgdsn.Validate 挡掉
//	                        ?password=/?host= 这类能覆盖 DSN 表面声明的参数）；
//	闸 2 强制只读         → 启动包参数 default_transaction_read_only=on
//	                        （走 pgx 的 RuntimeParams，不拼字符串）；
//	闸 3 复核服务端只读   → AfterConnect 在**每条新连接**上查两个 GUC，
//	                        不通过就拒用该连接（池是懒建连接的，只在开池时
//	                        查一次会漏掉后续新建的连接）；
//	闸 4 无写路径         → AssertSelectOnly：调用方把每条 SQL 过一遍白名单。
//
// 实现依据是 SoloAI 的 internal/platformdb/readonly.go（它在生产上跑过，
// 实测 superuser 会话下 CREATE TABLE 得到
// `ERROR: cannot execute CREATE TABLE in a read-only transaction`）。
//
// ── 凭据纪律 ──────────────────────────────────────────────────────────────
// 连接串含口令，只在内存里活。pgx 的连接错误里会带连接串片段，而这些错误会进
// 结构化日志，所以**任何**往外走的错误都必须先过 ScrubError。
package pgreadonly

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ReadOnlyParam 是 PostgreSQL 的 GUC 名。它可以在启动包里设置（USERSET），
	// 因此 pgx 的 RuntimeParams 与连接串里的 options=-c <name>=on 完全等价。
	ReadOnlyParam = "default_transaction_read_only"

	// 连接池参数。**别人家的生产库**——保守到底。
	// 取值对齐 SoloAI platformdb/readonly.go（那套值在生产上稳定跑过）。
	PoolMaxConns    = 3
	PoolMinConns    = 0
	PoolMaxConnLife = 30 * time.Minute
	PoolMaxConnIdle = 5 * time.Minute
	// ConnectTimeout 是建连接（含只读复核）的超时。
	ConnectTimeout = 8 * time.Second
)

// ErrReadWriteRequested 表示连接串**显式**要求了可写连接。
//
// 这种连接串不会被静默改成只读：配置者是带着意图写下 off 的，静默改写会让
// 那个意图（以及它背后的误解）永远没有机会被发现。
var ErrReadWriteRequested = errors.New(
	"连接串显式要求可写（" + ReadOnlyParam + "=off）；只允许只读接入，请删掉该参数")

// ErrWriteAttempt 表示有人往只读通道上塞了一条非 SELECT 语句。
//
// 这是闸 4 的**机械**表现：它不该在运行期发生（调用方的 SQL 都是常量），
// 存在的意义是让「将来有人加了一条 UPDATE」在测试里当场变红，
// 而不是等到某次取数把别人的生产数据改掉。
var ErrWriteAttempt = errors.New("只读通道上出现非 SELECT 语句")

// Config 把连接串解析成一个「只可能只读」的连接池配置（闸 1 + 闸 2 + 挂上闸 3）。
//
// 连接串本身绝不进日志/报错——它带口令。所有 error 只描述形态，不回显原文。
func Config(dsn string) (*pgxpool.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("连接串为空")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// err 可能带连接串片段（pgx 会回显它解析不了的部分），**完全不透出**。
		// 这里刻意不用 ScrubError：解析失败时原文形态未知，遮罩规则可能匹配不上，
		// 而匹配不上就等于原样泄漏。
		return nil, errors.New("连接串解析失败（已隐去内容，请检查格式）")
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if RequestsReadWrite(cfg.ConnConfig.RuntimeParams) {
		return nil, ErrReadWriteRequested
	}

	// 闸 2：启动包参数。等价于连接串上的 options=-c default_transaction_read_only=on，
	// 只是走 pgx 的结构化通道而不是拼字符串（拼字符串要同时处理 URL 式与
	// keyword 式两种语法，还会把口令再抄一遍）。
	cfg.ConnConfig.RuntimeParams[ReadOnlyParam] = "on"

	cfg.MaxConns = PoolMaxConns
	cfg.MinConns = PoolMinConns
	cfg.MaxConnLifetime = PoolMaxConnLife
	cfg.MaxConnIdleTime = PoolMaxConnIdle

	// 闸 3：每条新连接当场复核。挂在 AfterConnect 而不是开池时做一次——
	// 池是懒建连接的，后续新建的连接同样要过这一关。
	cfg.AfterConnect = VerifyReadOnly
	return cfg, nil
}

// Open 用 Config 建池并 Ping 一次。
//
// Ping 会真正建一条连接，从而触发 AfterConnect 的只读复核——复核不过在这里
// 就失败，而不是等到第一次取数。返回的错误已经过 ScrubError。
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := Config(dsn)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(dialCtx, cfg)
	if err != nil {
		return nil, errors.New(ScrubError(err))
	}
	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		return nil, errors.New(ScrubError(err))
	}
	return pool, nil
}

// VerifyReadOnly 在每条新连接上复核服务端确实处于只读（闸 3）。
//
// 挂法：`cfg.AfterConnect = pgreadonly.VerifyReadOnly`（Config 已经挂好）。
// 导出它是给「自己建池」的装配路径用的（集成测试、需要额外 RuntimeParams 的
// 场景）：那些地方必须挂上**同一把**复核锁，而不是各写一份。
//
// 查**两个** GUC：
//
//	transaction_read_only         当前事务的实际只读状态（防「这条连接现在就能写」）
//	default_transaction_read_only 后续事务的默认值（防「下一个事务又变回可写」）
//
// 只查前者会漏掉「当前恰好只读、但默认值被 ALTER ROLE / ALTER DATABASE 设成 off」
// 的库；只查后者会漏掉「默认值对但连接中间件在当前事务上翻了盘」的情形。
// 启动包参数只是「我请求了只读」，服务端怎么答才是证据——中间可能有 pgbouncer
// 吞掉启动参数，那种情况下连接会**看起来正常**而实际可写。
func VerifyReadOnly(ctx context.Context, conn *pgx.Conn) error {
	const sql = "SELECT current_setting('transaction_read_only'), current_setting('" +
		ReadOnlyParam + "')"
	var effective, dflt string
	if err := conn.QueryRow(ctx, sql).Scan(&effective, &dflt); err != nil {
		return fmt.Errorf("只读复核失败: %s", ScrubError(err))
	}
	if !IsOn(effective) {
		return fmt.Errorf("只读复核不通过：服务端 transaction_read_only=%q——"+
			"启动参数未生效（连接池中间件可能吞掉了它），拒绝使用该连接", effective)
	}
	if !IsOn(dflt) {
		return fmt.Errorf("只读复核不通过：服务端 %s=%q——"+
			"当前事务虽是只读，但下一个事务会变回可写，拒绝使用该连接", ReadOnlyParam, dflt)
	}
	return nil
}

// RequestsReadWrite 判断连接串是否**显式**要求可写。
//
// 两种写法都要认：独立参数 default_transaction_read_only=off，
// 以及塞在 options 里的 -c default_transaction_read_only=off。
//
// 为什么 options 那种也要认：pgx 把 options 当成一个不透明字符串原样发给服务端，
// 我们在 RuntimeParams 上写的 on 并不会覆盖它——两者谁生效取决于服务端的解析
// 顺序，是一个我们控制不了的行为。与其赌，不如拒绝。
func RequestsReadWrite(params map[string]string) bool {
	if v, ok := params[ReadOnlyParam]; ok && !IsOn(v) {
		return true
	}
	opts := params["options"]
	if opts == "" {
		return false
	}
	lower := strings.ToLower(opts)
	idx := strings.Index(lower, ReadOnlyParam+"=")
	if idx < 0 {
		return false
	}
	rest := lower[idx+len(ReadOnlyParam)+1:]
	if end := strings.IndexAny(rest, " '\"\t"); end >= 0 {
		rest = rest[:end]
	}
	return !IsOn(rest)
}

// IsOn 按 PostgreSQL 的布尔字面量判定。off / false / 0 / no 都是「可写」。
func IsOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(v, "'\""))) {
	case "on", "true", "1", "yes", "t", "y":
		return true
	default:
		return false
	}
}

// AssertSelectOnly 是闸 4 的机械判据：只放行 SELECT 打头的语句。
//
// 白名单而不是黑名单：黑名单要枚举 INSERT/UPDATE/DELETE/TRUNCATE/COPY/
// CREATE/DROP/ALTER/GRANT/DO/CALL/MERGE…，漏一个就是一个写口子；
// 白名单只放行一个词，漏不掉。
//
// 同时挡掉分号：一条语句里塞第二条（`SELECT 1; DROP TABLE x`）是最经典的
// 绕过。pgx 的扩展协议本来就不允许多语句，但这里不依赖那个实现细节——
// 换个驱动或改用 SimpleProtocol 时这道闸仍然在。
//
// 行注释会被跳过再看首词：`-- SELECT 1` 后面跟一条 UPDATE 是骗过「首词是
// SELECT」这类朴素判据的标准手法。
func AssertSelectOnly(sql string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return fmt.Errorf("空语句: %w", ErrWriteAttempt)
	}
	var head string
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		head = line
		break
	}
	fields := strings.Fields(head)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "SELECT") {
		return fmt.Errorf("语句必须以 SELECT 开头: %w", ErrWriteAttempt)
	}
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("语句含分号（可能是多语句拼接）: %w", ErrWriteAttempt)
	}
	return nil
}

var (
	// postgres://user:pass@host…  /  postgresql://user:pass@host…
	dsnURLPasswordRe = regexp.MustCompile(`(postgres(?:ql)?://[^:/@\s]+):[^@\s]*@`)
	// keyword/value 形式：password=xxx 或 password='x y'
	dsnKVPasswordRe = regexp.MustCompile(`(?i)\bpassword\s*=\s*('[^']*'|"[^"]*"|[^\s]+)`)
)

// ScrubError 抹掉错误文本里的数据库口令。
//
// pgx 的连接错误里会带整条连接串，而这些错误会进结构化日志与报告。
// 只抹口令、不抹主机/库名——后者是排障必需的：整条抹成「连接失败」
// 等于把一个可修的配置错误变成一个查不出原因的黑箱。
func ScrubError(err error) string {
	if err == nil {
		return ""
	}
	out := dsnURLPasswordRe.ReplaceAllString(err.Error(), "$1:***@")
	return dsnKVPasswordRe.ReplaceAllString(out, "password=***")
}
