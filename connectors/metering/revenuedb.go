package metering

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// NewAPI 收入侧的**只读数据库通道**（XM-0044，设计稿 §3.2）。
//
// ── 为什么收入必须走库而不是 HTTP ──────────────────────────────────────────
// 上游的 HTTP 面**答不出「某个渠道今天赚了多少」**：`/api/log/self/stat` 是
// 用户自助口径（按令牌），`/api/data/` 按 (模型, 小时) 预聚合、没有渠道维度。
// 而 `quota_data` 表里同时有 `channel_id` 与 `quota`，一条 SUM 就够。
// SoloAI 的 `platformdb/channel_revenue.go` 走的正是这条路，本文件按它逐行对齐
// ——影子对比要 0 差异，就必须用**同一个数、同一种运算**。
//
// ── ADR-018 四道只读闸在数据库通道上的落点 ────────────────────────────────
// 闸 2/3 的原文本来就是针对数据库通道写的（HTTP 那侧只能部分满足），这里逐字生效：
//
//	闸 1 拒绝可写连接配置 → newAPIRevenueConfig：pgdsn 白名单挡掉
//	                        ?password=/?host= 之类的覆盖参数，
//	                        显式写 default_transaction_read_only=off 的连接串直接拒；
//	闸 2 强制只读         → 启动包参数 default_transaction_read_only=on
//	                        （走 pgx 的 RuntimeParams，不拼字符串）；
//	闸 3 复核服务端只读   → AfterConnect 在**每条新连接**上查两个 GUC，
//	                        不通过就拒用该连接（池是懒建连接的，只在开池时查一次会漏）；
//	闸 4 包内无写路径     → 本文件只有 SELECT：所有 SQL 走 querySelectOnly，
//	                        它在发语句前先过 assertSelectOnly。
//
// 第五道（NewAPI 特有的伪 GET 写端点黑名单）在这里不适用：那是 HTTP 通道的问题。
//
// ── 凭据纪律 ──────────────────────────────────────────────────────────────
// 连接串**不含密码**，密码只经 CredentialRef 由 SecretProvider 解析，
// 在拼连接串那一瞬才出现（ADR-014、宪法 7 条）。pgx 的连接错误会带连接串片段，
// 所以**任何**往外走的错误都先过 scrubError。

const (
	// readOnlyParam 是 PostgreSQL 的 GUC 名。它可以在启动包里设置（USERSET），
	// 因此 pgx 的 RuntimeParams 与连接串里的 options=-c <name>=on 完全等价。
	readOnlyParam = "default_transaction_read_only"

	// 连接池参数。**别人家的生产库**——保守到底。
	// 取值对齐 SoloAI platformdb/readonly.go（那套值在生产上稳定跑过）。
	revenuePoolMaxConns    = 3
	revenuePoolMinConns    = 0
	revenuePoolMaxConnLife = 30 * time.Minute
	revenuePoolMaxConnIdle = 5 * time.Minute
	revenueConnectTimeout  = 8 * time.Second
	revenueQueryTimeout    = 30 * time.Second

	// revenueCredentialPurpose 进凭据审计（规格 §4.5），不影响解析结果。
	revenueCredentialPurpose = "newapi revenue readonly dsn"
	// revenueCredentialCaller 是审计里的请求方身份。
	revenueCredentialCaller = "connector:" + ConnectorKey + ".revenuedb"
)

// ErrRevenueDBReadWriteRequested 表示连接串**显式**要求了可写连接。
//
// 不静默改成只读：配置者是带着意图写下 off 的，静默改写会让那个意图
// （以及它背后的误解）永远没有机会被发现。这条与 SoloAI 的
// ErrReadWriteRequested 同源。
var ErrRevenueDBReadWriteRequested = errors.New(
	"连接串显式要求可写（" + readOnlyParam + "=off）；自营库只允许只读接入，请删掉该参数")

// ErrRevenueDBWriteAttempt 表示有人往只读通道上塞了一条非 SELECT 语句。
//
// 这是闸 4 的**机械**表现：它不该在运行期发生（本文件只有 SELECT 常量），
// 存在的意义是让「将来有人加了一条 UPDATE」在测试里当场变红，
// 而不是等到某次采集把别人的生产计费数据改掉。
var ErrRevenueDBWriteAttempt = errors.New("只读通道上出现非 SELECT 语句")

// ---------------------------------------------------------------------------
// SQL：全部语句的唯一定义处
// ---------------------------------------------------------------------------

// newapiRevenueQuery 是某个 channel 在某业务日窗口内的使用计费收入，单位 credits。
//
// 对齐 SoloAI 的 newapiChannelRevenueTodayQuery，两处差别都是刻意的：
//
//  1. **多一个上界 `created_at < $2`。** SoloAI 那条只问「今天」，今天没有上界；
//     本契约的 AccountRevenue 收一个**任意业务日**，不封上界会把之后所有天数
//     一起算进来。对「今天」而言上界落在未来，结果与 SoloAI 逐位相同——
//     影子对比不受影响。
//  2. **`::bigint` 而不是 `::float8`。** 金额禁止 float（宪法 13 条）：
//     quota 是 bigint，SUM(bigint) 在 PostgreSQL 里回 numeric，
//     不显式转成 bigint 的话 pgx 会给一个 numeric，再往 int64 上落又是一次转换。
//     直接要 bigint，全程整数。
//
// `channel_id::text` 而不是把参数转成整数：token_map 里存的是字符串，
// 与 SoloAI 保持同一种比较写法，免得两处对同一个键用不同的方式。
const newapiRevenueQuery = `
SELECT COALESCE(SUM(quota), 0)::bigint
  FROM quota_data
 WHERE created_at >= $1
   AND created_at < $2
   AND channel_id::text = $3`

// newapiQuotaPerUnitQuery 读上游此刻在用的 quota → 1 美元刻度。
//
// 表名 options、列名 key/value：上游 model/option.go 的 `Option{Key,Value}`
// 经 GORM 默认复数化建表。它是**运行期可变**的站点配置（root 改选项即刻生效）。
//
// 为什么要读而不是写死 500000：**收入与成本必须用同一个刻度**，否则
// `毛利 = 收入 − 成本` 是两把尺子相减。而成本侧（connectors/metering/newapi.go）
// 已经确立了「读上游此刻真正在用的刻度、读不到才回落到 ★ 常量」这条规则，
// 收入侧照做才对得上。读不到时的回落值 newapiDefaultQuotaPerUnit = 500000
// 正是 SoloAI 写死的那个数，所以未改过刻度的实例上两边逐位相同。
const newapiQuotaPerUnitQuery = `SELECT value FROM options WHERE key = 'QuotaPerUnit'`

// revenueDBQueries 是本通道会发出的**全部** SQL。
//
// 它不是文档，是判据：TestRevenueDBQueriesAreSelectOnly 遍历它逐条断言只读，
// 并断言本文件里再没有别的 SQL 常量漏登记。有人将来加一条语句却忘了登记，
// 那条测试会红。
var revenueDBQueries = []string{newapiRevenueQuery, newapiQuotaPerUnitQuery}

// assertSelectOnly 是闸 4 的机械判据：只放行 SELECT 打头的语句。
//
// 白名单而不是黑名单：黑名单要枚举 INSERT/UPDATE/DELETE/TRUNCATE/COPY/
// CREATE/DROP/ALTER/GRANT/DO/CALL/MERGE…，漏一个就是一个写口子；
// 白名单只放行一个词，漏不掉。
//
// 同时挡掉分号：一条语句里塞第二条（`SELECT 1; DROP TABLE x`）是最经典的
// 绕过。pgx 的扩展协议本来就不允许多语句，但这里不依赖那个实现细节——
// 换个驱动或改用 SimpleProtocol 时这道闸仍然在。
func assertSelectOnly(sql string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return fmt.Errorf("空语句: %w", ErrRevenueDBWriteAttempt)
	}
	// 去掉行注释，免得 `-- x` 之后藏东西影响首词判断
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
		return fmt.Errorf("语句必须以 SELECT 开头: %w", ErrRevenueDBWriteAttempt)
	}
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("语句含分号（可能是多语句拼接）: %w", ErrRevenueDBWriteAttempt)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 连接
// ---------------------------------------------------------------------------

// RevenueDBConfig 是 NewAPI 收入只读直查的连接配置。
type RevenueDBConfig struct {
	// DSN 是自营 new-api 库的连接串，**不含密码**（密码走 PasswordRef）。
	DSN string
	// PasswordRef 是数据库口令的引用（`secret://<scope>/<name>`）。
	PasswordRef string
	// Environment 进凭据审计。
	Environment string
	// Currency 是收入的记账币种；空则用 defaultRevenueCurrency。
	Currency string
	// BusinessDay 是业务日切日时区；nil 则用 CST（+08:00，★口径常量 §4）。
	BusinessDay *time.Location
	// Now 可注入固定时钟（测试用）。
	Now func() time.Time
}

// defaultRevenueCurrency 是收入的记账币种。
//
// NewAPI 的 quota 换算成钱的唯一口径是美元（`美元 = quota / quota_per_unit`），
// 与成本侧同源。写成别的币种会得到一批**看起来完全正常**的错数字。
const defaultRevenueCurrency = "USD"

// cstOffsetSeconds 是业务日的默认时区偏移：CST 固定 +08:00，无夏令时。
//
// ★口径常量（设计稿 §4，与 SoloAI 的 cstNow 一致）。收入与成本**必须共用
// 同一个时间权威**：各切各的会让同一笔请求的收入记在 D 日、成本记在 D+1 日，
// 利润凭空多一天又少一天，而且不报错。
const cstOffsetSeconds = 8 * 3600

// NewAPIRevenueDB 是走只读 DSN 的 NewAPI 收入取数通道。
//
// 它**不实现 ReadClient**：ReadClient 是 HTTP 契约，两条通道的凭据形态与
// 只读保证完全不同（DSN vs 会话；服务端只读事务 vs 方法白名单）。
// 它只满足 RevenueSource——一个单方法接口，由 NewAPI 的 HTTP 客户端在
// AccountRevenue 上委托过来（见 WithRevenueSource）。
type NewAPIRevenueDB struct {
	pool        *pgxpool.Pool
	currency    string
	businessDay *time.Location
	now         func() time.Time

	// mu 保护 quota_per_unit 缓存。
	//
	// 与 HTTP 成本客户端同一条纪律：一轮采集里几十个账号不必各查一次 options。
	// 但本通道的生命周期是**进程级**（连接池由进程持有），所以缓存要有 TTL——
	// 一个跑几天的 worker 不该一直用启动那一刻的刻度。
	mu            sync.Mutex
	quotaPerUnit  int64
	quotaSource   string
	quotaCachedAt time.Time
}

// quotaPerUnitTTL 是刻度缓存的有效期。
//
// 5 分钟：足够让一轮采集（几十个账号）共用一次查询，又短到 root 改了刻度之后
// 最多一个采集周期就跟上。写成永久缓存的话，一个跑几周的 worker 会一直用
// 启动那天的刻度算钱。
const quotaPerUnitTTL = 5 * time.Minute

// RevenueSource 是收入侧的可替换取数通道。
//
// 单方法接口：NewAPI 的收入走库、Sub2API 的走 HTTP，两者唯一的共同点就是
// 「能回答某账号某天的使用计费收入」。接口窄到只有这一件事，
// 才不会让一条通道的形状渗进另一条。
type RevenueSource interface {
	AccountRevenue(ctx context.Context, ownAccountID string, day string) (AccountRevenue, error)
}

// newAPIRevenueConfig 把连接串解析成一个「只可能只读」的连接池配置（闸 1 + 闸 2）。
//
// 连接串本身绝不进日志/报错——它带口令。所有 error 只描述形态，不回显原文。
func newAPIRevenueConfig(dsn string) (*pgxpool.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("连接串为空")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// err 可能带连接串片段（pgx 会回显它解析不了的部分），**完全不透出**。
		// 这里刻意不用 scrubError：解析失败时原文形态未知，遮罩规则可能匹配不上，
		// 而匹配不上就等于原样泄漏。
		return nil, errors.New("连接串解析失败（已隐去内容，请检查格式）")
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if requestsReadWrite(cfg.ConnConfig.RuntimeParams) {
		return nil, ErrRevenueDBReadWriteRequested
	}

	// 闸 2：启动包参数。等价于连接串上的 options=-c default_transaction_read_only=on，
	// 只是走 pgx 的结构化通道而不是拼字符串（拼字符串要同时处理 URL 式与
	// keyword 式两种语法，还会把口令再抄一遍）。
	cfg.ConnConfig.RuntimeParams[readOnlyParam] = "on"

	cfg.MaxConns = revenuePoolMaxConns
	cfg.MinConns = revenuePoolMinConns
	cfg.MaxConnLifetime = revenuePoolMaxConnLife
	cfg.MaxConnIdleTime = revenuePoolMaxConnIdle

	// 闸 3：每条新连接当场复核。挂在 AfterConnect 而不是开池时做一次——
	// 池是懒建连接的，后续新建的连接同样要过这一关。
	cfg.AfterConnect = VerifyReadOnly
	return cfg, nil
}

// VerifyReadOnly 在每条新连接上复核服务端确实处于只读（ADR-018 闸 3）。
//
// 导出它是给「自己建池」的装配路径用的（集成测试、将来可能出现的共享池）：
// 那些地方必须挂上**同一把**复核锁，而不是各写一份——两份实现迟早走偏，
// 而走偏的那一份守的是一条别人以为还在的防线。挂法：
//
//	cfg.AfterConnect = metering.VerifyReadOnly
//
// 查**两个** GUC：
//   - transaction_read_only         当前事务的实际只读状态（防「这条连接现在就能写」）
//   - default_transaction_read_only 后续事务的默认值（防「下一个事务又变回可写」）
//
// 只查前者会漏掉「当前恰好只读、但默认值被 ALTER ROLE / ALTER DATABASE 设成 off」
// 的库；只查后者会漏掉「默认值对但连接中间件在当前事务上翻了盘」的情形。
// 启动包参数只是「我请求了只读」，服务端怎么答才是证据——中间可能有 pgbouncer
// 吞掉启动参数，那种情况下连接会**看起来正常**而实际可写。
func VerifyReadOnly(ctx context.Context, conn *pgx.Conn) error {
	const sql = "SELECT current_setting('transaction_read_only'), current_setting('" +
		readOnlyParam + "')"
	var effective, dflt string
	if err := conn.QueryRow(ctx, sql).Scan(&effective, &dflt); err != nil {
		return fmt.Errorf("只读复核失败: %s", scrubError(err))
	}
	if !isPostgresOn(effective) {
		return fmt.Errorf("只读复核不通过：服务端 transaction_read_only=%q——"+
			"启动参数未生效（连接池中间件可能吞掉了它），拒绝使用该连接", effective)
	}
	if !isPostgresOn(dflt) {
		return fmt.Errorf("只读复核不通过：服务端 %s=%q——"+
			"当前事务虽是只读，但下一个事务会变回可写，拒绝使用该连接", readOnlyParam, dflt)
	}
	return nil
}

// requestsReadWrite 判断连接串是否**显式**要求可写。
//
// 两种写法都要认：独立参数 default_transaction_read_only=off，
// 以及塞在 options 里的 -c default_transaction_read_only=off。
//
// 为什么 options 那种也要认：pgx 把 options 当成一个不透明字符串原样发给服务端，
// 我们在 RuntimeParams 上写的 on 并不会覆盖它——两者谁生效取决于服务端的解析
// 顺序，是一个我们控制不了的行为。与其赌，不如拒绝。
func requestsReadWrite(params map[string]string) bool {
	if v, ok := params[readOnlyParam]; ok && !isPostgresOn(v) {
		return true
	}
	opts := params["options"]
	if opts == "" {
		return false
	}
	lower := strings.ToLower(opts)
	idx := strings.Index(lower, readOnlyParam+"=")
	if idx < 0 {
		return false
	}
	rest := lower[idx+len(readOnlyParam)+1:]
	if end := strings.IndexAny(rest, " '\"\t"); end >= 0 {
		rest = rest[:end]
	}
	return !isPostgresOn(rest)
}

// isPostgresOn 按 PostgreSQL 的布尔字面量判定。off / false / 0 / no 都是「可写」。
func isPostgresOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(v, "'\""))) {
	case "on", "true", "1", "yes", "t", "y":
		return true
	default:
		return false
	}
}

var (
	// postgres://user:pass@host…  /  postgresql://user:pass@host…
	dsnURLPasswordRe = regexp.MustCompile(`(postgres(?:ql)?://[^:/@\s]+):[^@\s]*@`)
	// keyword/value 形式：password=xxx 或 password='x y'
	dsnKVPasswordRe = regexp.MustCompile(`(?i)\bpassword\s*=\s*('[^']*'|"[^"]*"|[^\s]+)`)
)

// scrubError 抹掉错误文本里的数据库口令。
//
// pgx 的连接错误里会带整条连接串，而这些错误会进结构化日志。
// 只抹口令、不抹主机/库名——后者是排障必需的：整条抹成「连接失败」
// 等于把一个可修的配置错误变成一个查不出原因的黑箱。
func scrubError(err error) string {
	if err == nil {
		return ""
	}
	out := dsnURLPasswordRe.ReplaceAllString(err.Error(), "$1:***@")
	return dsnKVPasswordRe.ReplaceAllString(out, "password=***")
}

// OpenNewAPIRevenueDB 建立 NewAPI 收入侧的只读连接池。
//
// **DSN 为空 → (nil, nil)**：「没配」不是故障，是这条能力没启用。调用方判 nil
// 跳过即可，NewAPI 客户端的 AccountRevenue 会继续返回 not_supported——
// 与 XM-0044 之前的行为逐字相同（§3.2 的收入侧在配置到位前本来就是未知）。
//
// 口令只经 CredentialRef：DSN 里不许带内联密码（pgdsn.Validate 的
// requireManagedPassword 会拒），明文由 SecretProvider 解析后在拼连接串
// 那一瞬才出现（ADR-014、宪法 7 条）。
func OpenNewAPIRevenueDB(
	ctx context.Context, cfg RevenueDBConfig, sp secrets.SecretProvider,
) (*NewAPIRevenueDB, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, nil
	}
	// 闸 1 的第一层：pgdsn 的参数白名单挡掉 ?password= / ?host= 这类能覆盖
	// DSN 表面声明的参数，并强制口令走 CredentialRef。
	if err := pgdsn.Validate(dsn, true); err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.revenuedb.config", err)
	}
	if strings.TrimSpace(cfg.PasswordRef) == "" {
		return nil, connector.NewError(connector.KindInternal, "metering.revenuedb.config",
			errors.New("缺少数据库口令的 CredentialRef：连接串里不允许内联密码（宪法 7 条）"))
	}
	ref, err := secrets.ParseCredentialRef(cfg.PasswordRef)
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.revenuedb.config", err)
	}
	if sp == nil {
		return nil, connector.NewError(connector.KindInternal, "metering.revenuedb.config",
			errors.New("secret provider 为空：没有 Provider 就解析不出口令"))
	}

	value, err := sp.Resolve(
		secrets.WithCaller(ctx, revenueCredentialCaller), ref, revenueCredentialPurpose)
	if err != nil {
		return nil, connector.NewError(connector.KindAuth, "metering.revenuedb.config", err)
	}
	// 明文在这一行进入连接串，之后只活在 pgxpool.Config 里；
	// 任何往外走的错误都先过 scrubError。
	withPassword, err := pgdsn.WithPassword(dsn, value.Reveal())
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.revenuedb.config", err)
	}

	poolCfg, err := newAPIRevenueConfig(withPassword)
	if err != nil {
		return nil, connector.NewError(connector.KindInternal, "metering.revenuedb.config", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, revenueConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(dialCtx, poolCfg)
	if err != nil {
		return nil, connector.NewError(connector.KindUnavailable, "metering.revenuedb.open",
			errors.New(scrubError(err)))
	}
	// Ping 会真正建一条连接，从而触发 AfterConnect 的只读复核——
	// 复核不过在这里就失败，而不是等到第一次取数。
	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		return nil, connector.NewError(connector.KindUnavailable, "metering.revenuedb.open",
			errors.New(scrubError(err)))
	}

	loc := cfg.BusinessDay
	if loc == nil {
		loc = time.FixedZone("CST", cstOffsetSeconds)
	}
	currency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if currency == "" {
		currency = defaultRevenueCurrency
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &NewAPIRevenueDB{
		pool: pool, currency: currency, businessDay: loc, now: now,
	}, nil
}

// NewAPIRevenueDBFromPool 用一个**已有**的池构造取数通道（测试与集成装配用）。
//
// 存在的理由是集成测试要连一个测试库，而那条路径上没有 CredentialRef 可解析。
// **护栏不受影响**：只读复核挂在池配置的 AfterConnect 上，谁建的池就由谁负责
// 过闸；而 assertSelectOnly 是本类型自己的，与池无关。
func NewAPIRevenueDBFromPool(pool *pgxpool.Pool, cfg RevenueDBConfig) *NewAPIRevenueDB {
	if pool == nil {
		return nil
	}
	loc := cfg.BusinessDay
	if loc == nil {
		loc = time.FixedZone("CST", cstOffsetSeconds)
	}
	currency := strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if currency == "" {
		currency = defaultRevenueCurrency
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &NewAPIRevenueDB{pool: pool, currency: currency, businessDay: loc, now: now}
}

// Close 关闭连接池。nil 安全。
func (d *NewAPIRevenueDB) Close() {
	if d != nil && d.pool != nil {
		d.pool.Close()
	}
}

// querySelectOnly 是本通道**唯一**的发语句入口（闸 4）。
//
// 每条 SQL 在发出前先过 assertSelectOnly。这不是防御外部输入——本文件里的
// SQL 全是常量；它防的是**将来的改动**：有人往这里加一条 UPDATE，
// 编译得过、评审可能也过，但这一行会让它在第一次运行时就炸。
func (d *NewAPIRevenueDB) querySelectOnly(
	ctx context.Context, op, sql string, args ...any,
) (pgx.Row, error) {
	if err := assertSelectOnly(sql); err != nil {
		return nil, connector.NewError(connector.KindWriteAttempt, op, err)
	}
	return d.pool.QueryRow(ctx, sql, args...), nil
}

// ---------------------------------------------------------------------------
// 取数
// ---------------------------------------------------------------------------

// AccountRevenue 读某个自营 NewAPI 渠道在某业务日的使用计费收入（§3.2）。
//
// ownAccountID 是 `token_map` 里存的值，也就是 new-api 的 `channel_id`。
//
// **返回 0 且 error 为 nil = 已知的 0**（那天这个渠道确实没有消费）；
// 读不到则返回 error。「未知」与「已知 0」在台账里落成不同的东西
// （NULL vs 0，§5.1），把未知写成 0 会让毛利凭空等于成本的负数。
func (d *NewAPIRevenueDB) AccountRevenue(
	ctx context.Context, ownAccountID string, day string,
) (AccountRevenue, error) {
	const op = "metering.account.revenue_read"

	if d == nil || d.pool == nil {
		return AccountRevenue{}, connector.NewError(connector.KindNotSupported, op,
			errors.New("newapi 收入只读通道未配置"))
	}
	if strings.TrimSpace(ownAccountID) == "" {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op,
			errors.New("own_account_id 为空：收入无从归属"))
	}
	if err := ValidateBusinessDay(day); err != nil {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	start, end, err := dayWindowUnix(day, d.businessDay)
	if err != nil {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, revenueQueryTimeout)
	defer cancel()

	// 刻度先取：拿不到刻度就换算不出金额，而**先查了钱再发现换算不了**
	// 会白白让别人的生产库多挨一次 SUM。
	perUnit, source, err := d.quotaPerUnitCached(queryCtx, op)
	if err != nil {
		return AccountRevenue{}, err
	}

	row, err := d.querySelectOnly(queryCtx, op, newapiRevenueQuery, start, end, strings.TrimSpace(ownAccountID))
	if err != nil {
		return AccountRevenue{}, err
	}
	var credits int64
	if err := row.Scan(&credits); err != nil {
		// pgx 的错误可能带连接串片段，遮罩后再进错误链（宪法 7 条）。
		return AccountRevenue{}, connector.NewError(connector.KindUnavailable, op,
			errors.New("newapi 渠道收入查询失败: "+scrubError(err)))
	}

	// 整数换算，全程零 float：credits ÷ quota_per_unit → scale-6 微美元。
	// 与成本侧走的是同一个 money.DivideByUnits，所以毛利相减时两边刻度一致。
	minor, err := money.DivideByUnits(credits, perUnit, UsageScale)
	if err != nil {
		return AccountRevenue{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	observedAt := d.clock()
	return AccountRevenue{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			// 水位带上三件排查时一定会被追问的事：这是哪一天的窗口、
			// 用了哪个刻度、那个刻度是从哪来的（上游选项还是 ★ 回落常量）。
			Watermark: fmt.Sprintf("newapi-db:%s@%d/qpu:%d:%s",
				day, observedAt.Unix(), perUnit, source),
			// 单值原子读：要么拿到要么报错，没有「读到一半」这种状态。
			IsPartial: false,
		},
		OwnAccountID: strings.TrimSpace(ownAccountID),
		Day:          day,

		RevenueMinorUnits: minor,
		Currency:          d.currency,
	}, nil
}

// quotaPerUnitCached 读上游此刻在用的刻度，带 TTL 缓存。
//
// 三条分支**与成本侧（connectors/metering/newapi.go 的 quotaScale）逐条对齐**。
// 必须对齐：`毛利 = 收入 − 成本`，两边刻度的取法不一样就是两把尺子相减。
//
//	options 里有、是正数   → 用它（上游此刻真正在用的刻度）
//	options 里没有这一行   → 回落到 ★ 口径常量 500000
//	                        （等价于成本侧「旧版本没有这个字段」那一支；
//	                        500000 正是 SoloAI 写死的数，影子对比不受影响）
//	查不出来 / 值非正      → **硬报错**，不编刻度
//
// 最后一条最容易被写成「回落一下算了」。不行：读不到刻度还硬算，等于把
// 「读不到」变成「读到了一个可能错的数」，而错的倍数是 50 万量级、
// 看起来却完全正常（宪法 12 条）。**权限不足也走这一支**——只读角色只
// GRANT 了 quota_data 时会落在这里，错误信息点名 options 表，
// 让运维一眼看出该补哪张表的 SELECT（见操作卡的建角色 SQL）。
//
// 第二个返回值说明这个数**从哪来**（`options` / `fallback`），它进水位：
// 运维追问「这天的收入怎么差了一个倍数」时，第一个要看的就是刻度是不是回落了。
func (d *NewAPIRevenueDB) quotaPerUnitCached(ctx context.Context, op string) (int64, string, error) {
	now := d.clock()

	d.mu.Lock()
	if d.quotaPerUnit > 0 && now.Sub(d.quotaCachedAt) < quotaPerUnitTTL {
		perUnit, source := d.quotaPerUnit, d.quotaSource
		d.mu.Unlock()
		return perUnit, source, nil
	}
	d.mu.Unlock()

	row, err := d.querySelectOnly(ctx, op, newapiQuotaPerUnitQuery)
	if err != nil {
		// assertSelectOnly 没过——那是代码问题，不是上游问题，如实往上抛。
		return 0, "", err
	}

	perUnit, source := int64(newapiDefaultQuotaPerUnit), quotaSourceFallback
	var raw string
	switch scanErr := row.Scan(&raw); {
	case scanErr == nil:
		value, parseErr := money.RawAmount(strings.TrimSpace(raw)).Int64()
		if parseErr != nil {
			return 0, "", connector.NewError(connector.KindBadResponse, op,
				fmt.Errorf("上游 options.QuotaPerUnit 解析失败（值的形态不是整数）: %w", parseErr))
		}
		if value <= 0 {
			return 0, "", connector.NewError(connector.KindBadResponse, op,
				fmt.Errorf("上游 options.QuotaPerUnit 是 %d（须为正）：按默认 %d 折算会得到一个"+
					"看起来正常的错数字，故拒绝取数", value, newapiDefaultQuotaPerUnit))
		}
		perUnit, source = value, quotaSourceOptions
	case errors.Is(scanErr, pgx.ErrNoRows):
		// 上游没有这一行（没改过默认值的实例不会写这条 option）：
		// 回落到与 SoloAI 一致的 ★ 口径常量。
	default:
		return 0, "", connector.NewError(connector.KindUnavailable, op,
			errors.New("读 options.QuotaPerUnit 失败（只读角色是否 GRANT 了 options 表的 SELECT？）: "+
				scrubError(scanErr)))
	}

	d.mu.Lock()
	d.quotaPerUnit, d.quotaSource, d.quotaCachedAt = perUnit, source, now
	d.mu.Unlock()
	return perUnit, source, nil
}

// 刻度来源标记，进水位。
const (
	quotaSourceOptions  = "options"
	quotaSourceFallback = "fallback"
)

func (d *NewAPIRevenueDB) clock() time.Time { return d.now().UTC() }

// dayWindowUnix 把业务日换算成 [start, end) 的 unix 秒区间。
//
// 半开区间：`created_at >= start AND created_at < end`。用闭区间的话，
// 次日零点整那一秒会同时落进两天——一个只在极少数秒里发生、几乎不可能被
// 复现的重复计数。
//
// 时区由账号的业务日声明决定（默认 CST +08:00）。**收入与成本必须共用同一个
// 时间权威**（§4 ★）：各切各的会让同一笔请求的收入记在 D 日、成本记在 D+1 日。
func dayWindowUnix(day string, loc *time.Location) (start, end int64, err error) {
	if loc == nil {
		loc = time.FixedZone("CST", cstOffsetSeconds)
	}
	parsed, err := time.ParseInLocation(BusinessDayLayout, day, loc)
	if err != nil {
		return 0, 0, fmt.Errorf("业务日 %q 无法按 %s 解析: %w", day, loc, ErrInvalidDay)
	}
	from := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, loc)
	return from.Unix(), from.AddDate(0, 0, 1).Unix(), nil
}
