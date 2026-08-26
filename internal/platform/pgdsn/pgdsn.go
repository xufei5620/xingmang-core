// Package pgdsn 校验 PostgreSQL 连接串，确保「DSN 看起来连哪里、带不带密码」
// 与 pgx 的实际行为一致。
//
// 存在的理由是一个真实的绕过：pgx 不只读 URL 的 userinfo 与 Host，
// 它把 query 参数也当连接设置读，而且**在**填完 host/user **之后**覆盖它们
// （pgx/v5 pgconn/config.go 的 parseURLSettings）。于是：
//
//	postgres://u@prod-db/db?password=xxx    自己解析 URL 会认为「没有内联密码」
//	postgres://u@127.0.0.1/db?host=prod.example.com   看着是 loopback，实连生产
//
// 结论：任何「先用 net/url 自己解析、再把同一字符串交给 pgx」的校验都是空的。
// 本包不重写 pgx 的解析器，而是问它——用 pgconn.ParseConfig 拿到它**实际**
// 会用的配置，再与 DSN 的表面声明对照。
package pgdsn

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// allowedParams 是 DATABASE_URL 里允许出现的查询参数。
//
// 白名单而非黑名单：pgx 认得几十个连接设置，其中 password / host / hostaddr /
// passfile / service / servicefile 都能改变「连谁、用什么密码」。黑名单漏一个
// 就是一个绕过口子，而白名单漏一个只是让人多写一行 PR。
var allowedParams = map[string]struct{}{
	"sslmode":                  {},
	"sslcert":                  {},
	"sslkey":                   {},
	"sslrootcert":              {},
	"connect_timeout":          {},
	"application_name":         {},
	"target_session_attrs":     {},
	"pool_max_conns":           {},
	"pool_min_conns":           {},
	"pool_max_conn_lifetime":   {},
	"pool_max_conn_idle_time":  {},
	"pool_health_check_period": {},
}

// Validate 校验 DSN。
//
// requireManagedPassword 为真时，密码只能来自 CredentialRef：DSN 里的内联密码、
// query 参数、以及 pgx 会读的环境来源（PGPASSWORD、~/.pgpass）一律拒绝。
// 为假时（开发环境且未配置 CredentialRef）只做结构与参数校验——开发机上有
// ~/.pgpass 很常见，不该因此拦住本地调试。
func Validate(raw string, requireManagedPassword bool) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("DATABASE_URL 不是合法 URL")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return fmt.Errorf("DATABASE_URL 的 scheme 必须是 postgres 或 postgresql")
	}
	if parsed.Host == "" {
		return fmt.Errorf("DATABASE_URL 缺少主机")
	}

	var rejected []string
	for k := range parsed.Query() {
		if _, ok := allowedParams[strings.ToLower(k)]; !ok {
			rejected = append(rejected, k)
		}
	}
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return fmt.Errorf(
			"DATABASE_URL 含未许可的查询参数 %s——pgx 会把 query 当连接设置读，"+
				"其中 password/host/passfile 等能覆盖 DSN 的表面声明；"+
				"如确需新参数请加进 pgdsn 白名单并说明理由",
			strings.Join(rejected, ", "))
	}

	cfg, err := pgconn.ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("DATABASE_URL 无法被 pgx 解析: %w", err)
	}

	// 表面声明的主机必须就是 pgx 实际会连的主机。白名单已经挡掉了 ?host=，
	// 这里是第二道——万一将来往白名单里加错了东西，这条会立刻发现
	declared := parsed.Hostname()
	for _, h := range hosts(cfg) {
		if !strings.EqualFold(h, declared) {
			return fmt.Errorf(
				"DATABASE_URL 声明的主机是 %q，pgx 实际会连 %q", declared, h)
		}
	}

	if !requireManagedPassword {
		return nil
	}
	if cfg.Password != "" {
		return fmt.Errorf(
			"数据库密码必须经 CredentialRef 提供（宪法 7 条）：" +
				"检测到密码来自 DSN 内联、查询参数、PGPASSWORD 或 ~/.pgpass。" +
				"请清除这些来源并只配置 DATABASE_PASSWORD_REF")
	}
	return nil
}

// hosts 返回 pgx 会尝试的全部主机（含多主机 fallback）。
func hosts(cfg *pgconn.Config) []string {
	out := []string{cfg.Host}
	for _, f := range cfg.Fallbacks {
		out = append(out, f.Host)
	}
	return out
}

// EffectiveHosts 返回 pgx 实际会连接的主机列表。
//
// 给「只许连 loopback」这类守卫用：判断依据必须是 pgx 会连哪里，
// 而不是 DSN 字符串看起来像什么。
func EffectiveHosts(raw string) ([]string, error) {
	cfg, err := pgconn.ParseConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL 无法被 pgx 解析: %w", err)
	}
	return hosts(cfg), nil
}

// RequireLoopback 确保 pgx 实际连接的每个主机都是本机。
//
// Unix socket 路径（以 / 开头）视为本机——pgx 用它表示本地套接字。
func RequireLoopback(raw string) error {
	hs, err := EffectiveHosts(raw)
	if err != nil {
		return err
	}
	for _, h := range hs {
		if strings.HasPrefix(h, "/") {
			continue
		}
		if strings.EqualFold(h, "localhost") {
			continue
		}
		// 只认字面量 IP：走 DNS 解析会把判定结果交给 DNS，
		// 而 DNS 是被测环境的一部分，不能拿它当安全边界
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			continue
		}
		return fmt.Errorf("拒绝非 loopback 的 DATABASE_URL 主机 %q", h)
	}
	return nil
}

// WithPassword 把密码装进 DSN 的 userinfo。
//
// 前置条件是 DSN 已经过 Validate——白名单保证了没有 ?password= 之类会在
// 之后把它覆盖掉的参数，否则这里装进去的密码根本不会被用上。
func WithPassword(raw, password string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("DATABASE_URL 不是合法 URL")
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return "", fmt.Errorf("DATABASE_URL 必须包含用户名")
	}
	parsed.User = url.UserPassword(parsed.User.Username(), password)
	return parsed.String(), nil
}
