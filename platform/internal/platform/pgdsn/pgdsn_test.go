package pgdsn_test

import (
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
)

const cleanDSN = "postgres://xingmang@db.internal:5432/xingmang?sslmode=disable"

func TestValidateAcceptsCleanDSN(t *testing.T) {
	for _, dsn := range []string{
		cleanDSN,
		"postgresql://u@127.0.0.1:5432/db",
		"postgres://u:pw@localhost/db?sslmode=require&connect_timeout=5",
		"postgres://u@db/x?pool_max_conns=8&application_name=platform-worker",
	} {
		if err := pgdsn.Validate(dsn, false); err != nil {
			t.Errorf("合法 DSN 被拒绝 %q: %v", dsn, err)
		}
	}
}

func TestValidateRejectsCredentialCarryingQueryParams(t *testing.T) {
	// 这是本包存在的理由：pgx 把 query 当连接设置读，且**在**填完
	// host/user **之后**覆盖它们。自己解析 URL 判断「有没有内联密码」
	// 会被这些参数整个绕过。
	for name, dsn := range map[string]string{
		"query 里的密码":  "postgres://u@prod-db/db?password=hunter2",
		"query 覆盖主机":  "postgres://u@127.0.0.1:5432/db?host=prod.example.com",
		"hostaddr 覆盖": "postgres://u@127.0.0.1:5432/db?hostaddr=10.0.0.9",
		"passfile":    "postgres://u@db/x?passfile=/tmp/pw",
		"service 文件":  "postgres://u@db/x?servicefile=/tmp/svc",
		"改用户":         "postgres://u@db/x?user=postgres",
	} {
		if err := pgdsn.Validate(dsn, false); err == nil {
			t.Errorf("%s 应被拒绝: %s", name, dsn)
		}
	}
}

func TestValidateErrorNamesTheOffendingParams(t *testing.T) {
	// 错误信息要能直接告诉运维「是哪个参数」，否则排查只能靠猜
	err := pgdsn.Validate("postgres://u@db/x?password=p&host=h&sslmode=disable", false)
	if err == nil {
		t.Fatal("应被拒绝")
	}
	if !strings.Contains(err.Error(), "host") || !strings.Contains(err.Error(), "password") {
		t.Errorf("错误信息未点名违规参数: %v", err)
	}
	if strings.Contains(err.Error(), "hunter") || strings.Contains(err.Error(), "=p") {
		t.Errorf("错误信息不得回显参数值: %v", err)
	}
}

func TestValidateRejectsNonPostgresAndMalformed(t *testing.T) {
	for _, dsn := range []string{
		"mysql://u@db/x",
		"postgres:///x",
		"://",
		"",
	} {
		if err := pgdsn.Validate(dsn, false); err == nil {
			t.Errorf("非法 DSN 应被拒绝: %q", dsn)
		}
	}
}

func TestRequireManagedPasswordRejectsEveryPasswordSource(t *testing.T) {
	// 宪法 7 条：非开发环境的数据库密码只能来自 CredentialRef
	if err := pgdsn.Validate("postgres://u:inline@db/x", true); err == nil {
		t.Error("内联密码在托管模式下应被拒绝")
	}
	// query 形态在白名单那关就被拦下，这里确认它同样不通过
	if err := pgdsn.Validate("postgres://u@db/x?password=p", true); err == nil {
		t.Error("query 密码在托管模式下应被拒绝")
	}
	// 干净的 DSN 通过
	if err := pgdsn.Validate(cleanDSN, true); err != nil {
		t.Errorf("无密码 DSN 在托管模式下应通过: %v", err)
	}
}

func TestRequireLoopbackUsesEffectiveHosts(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u@127.0.0.1:5432/db",
		"postgres://u@localhost:5432/db",
		"postgres://u@[::1]:5432/db",
	} {
		if err := pgdsn.RequireLoopback(dsn); err != nil {
			t.Errorf("loopback DSN 被拒绝 %q: %v", dsn, err)
		}
	}

	for name, dsn := range map[string]string{
		"外部主机":        "postgres://u@prod.example.com:5432/db",
		"query 伪装成本机": "postgres://u@127.0.0.1:5432/db?host=prod.example.com",
	} {
		if err := pgdsn.RequireLoopback(dsn); err == nil {
			t.Errorf("%s 应被拒绝: %s", name, dsn)
		}
	}
}

func TestRequireLoopbackDoesNotTrustDNS(t *testing.T) {
	// 一个解析到 127.0.0.1 的域名不算 loopback：把判定交给 DNS 等于
	// 把安全边界交给被测环境的配置
	if err := pgdsn.RequireLoopback("postgres://u@localtest.me:5432/db"); err == nil {
		t.Error("非字面量 IP 的主机不应被当作 loopback")
	}
}

func TestWithPassword(t *testing.T) {
	out, err := pgdsn.WithPassword(cleanDSN, "s3cr3t")
	if err != nil {
		t.Fatalf("WithPassword: %v", err)
	}
	if !strings.Contains(out, "xingmang:s3cr3t@") {
		t.Errorf("密码未装入 userinfo: %s", out)
	}
	// 装完仍应是合法 DSN，且 pgx 会真的用上这个密码
	if err := pgdsn.Validate(out, false); err != nil {
		t.Errorf("装入密码后 DSN 不合法: %v", err)
	}

	if _, err := pgdsn.WithPassword("postgres://db/x", "p"); err == nil {
		t.Error("缺用户名时应报错——没有用户名的密码装不进去")
	}
}
