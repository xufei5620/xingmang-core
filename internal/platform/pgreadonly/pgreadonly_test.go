package pgreadonly

import (
	"errors"
	"strings"
	"testing"
)

// TestAssertSelectOnly 钉住只读语句判据。
//
// 白名单（只放行 SELECT）而不是黑名单：黑名单要枚举 INSERT/UPDATE/DELETE/
// TRUNCATE/COPY/CREATE/DROP/ALTER/GRANT/DO/CALL/MERGE…，漏一个就是一个写口子。
// 下面这串正是「黑名单会漏掉哪些」的清单——它们**全部**必须被拒。
func TestAssertSelectOnly(t *testing.T) {
	for _, ok := range []string{
		"SELECT 1",
		"select value from options",
		"  \n SELECT COALESCE(SUM(quota),0)::bigint\n FROM quota_data",
		"-- 注释在前\nSELECT 1",
	} {
		if err := AssertSelectOnly(ok); err != nil {
			t.Fatalf("AssertSelectOnly(%q) 应放行, got %v", ok, err)
		}
	}

	for _, bad := range []string{
		"",
		"   ",
		"INSERT INTO quota_data VALUES (1)",
		"UPDATE options SET value = '1'",
		"DELETE FROM quota_data",
		"TRUNCATE quota_data",
		"DROP TABLE quota_data",
		"CREATE TABLE t (id int)",
		"ALTER TABLE quota_data ADD COLUMN x int",
		"GRANT ALL ON quota_data TO public",
		"COPY quota_data FROM '/tmp/x'",
		"DO $$ BEGIN PERFORM 1; END $$",
		"CALL some_proc()",
		"MERGE INTO t USING s ON true",
		"WITH x AS (DELETE FROM quota_data RETURNING *) SELECT * FROM x",
		// 多语句拼接是最经典的绕过：首词是 SELECT，第二条才是写。
		"SELECT 1; DROP TABLE quota_data",
		"SELECT 1;",
		// 注释掩护：真正的首词是 UPDATE
		"-- SELECT 1\nUPDATE options SET value='1'",
	} {
		err := AssertSelectOnly(bad)
		if err == nil {
			t.Fatalf("AssertSelectOnly(%q) 必须被拒", bad)
		}
		if !errors.Is(err, ErrWriteAttempt) {
			t.Fatalf("AssertSelectOnly(%q) 的根因应可被 errors.Is 认出: %v", bad, err)
		}
	}
}

// TestRevenueDBQueriesAreSelectOnly：本通道会发的每一条 SQL 都必须是只读的。
// ---------------------------------------------------------------------------
// 闸 1 / 闸 2：连接配置
// ---------------------------------------------------------------------------

// TestConfigForcesReadOnly：闸 2 的启动包参数必须被写进去。
func TestConfigForcesReadOnly(t *testing.T) {
	cfg, err := Config("postgres://reader:pw@db.example.test:5432/newapi")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.RuntimeParams[ReadOnlyParam]; got != "on" {
		t.Fatalf("%s = %q, want on（闸 2：启动包强制只读）", ReadOnlyParam, got)
	}
	// 闸 3 必须挂上：池是懒建连接的，只在开池时查一次会漏掉后续新建的连接。
	if cfg.AfterConnect == nil {
		t.Fatal("AfterConnect 为空——每条新连接的只读复核（闸 3）没挂上")
	}
	// 别人家的生产库，连接数保守到底。
	if cfg.MaxConns != PoolMaxConns {
		t.Fatalf("MaxConns = %d, want %d", cfg.MaxConns, PoolMaxConns)
	}
}

// TestConfigRejectsExplicitReadWrite：显式要求可写的连接串必须被拒，
// **而不是被静默改成只读**。
//
// 配置者是带着意图写下 off 的。静默改写会让那个意图（以及它背后的误解——
// 比如「我以为这个通道也要写」）永远没有机会被发现。
func TestConfigRejectsExplicitReadWrite(t *testing.T) {
	for _, dsn := range []string{
		"postgres://reader:pw@db.example.test/newapi?" + ReadOnlyParam + "=off",
		"postgres://reader:pw@db.example.test/newapi?" + ReadOnlyParam + "=false",
		// 塞在 options 里的那种也要认：pgx 把 options 当不透明字符串原样发给
		// 服务端，我们在 RuntimeParams 上写的 on 并不保证能覆盖它。
		"postgres://reader:pw@db.example.test/newapi?options=-c%20" + ReadOnlyParam + "%3Doff",
	} {
		_, err := Config(dsn)
		if !errors.Is(err, ErrReadWriteRequested) {
			t.Fatalf("显式可写的连接串必须被拒（dsn=%s）, got %v", dsn, err)
		}
	}

	// 显式写 on 是可以的：它与我们要做的事一致，没有需要暴露的误解。
	if _, err := Config(
		"postgres://reader:pw@db.example.test/newapi?" + ReadOnlyParam + "=on"); err != nil {
		t.Fatalf("显式 on 不该被拒: %v", err)
	}
}

func TestRequestsReadWrite(t *testing.T) {
	cases := []struct {
		params map[string]string
		want   bool
	}{
		{map[string]string{}, false},
		{map[string]string{ReadOnlyParam: "on"}, false},
		{map[string]string{ReadOnlyParam: "off"}, true},
		{map[string]string{ReadOnlyParam: "0"}, true},
		{map[string]string{ReadOnlyParam: "false"}, true},
		{map[string]string{"options": "-c " + ReadOnlyParam + "=off"}, true},
		{map[string]string{"options": "-c " + ReadOnlyParam + "=on"}, false},
		{map[string]string{"options": "-c statement_timeout=5s"}, false},
	}
	for _, tc := range cases {
		if got := RequestsReadWrite(tc.params); got != tc.want {
			t.Fatalf("RequestsReadWrite(%v) = %v, want %v", tc.params, got, tc.want)
		}
	}
}

// TestOpenNewAPIRevenueDBNotConfigured：没配 DSN = 功能未启用，**不是错误**。
//
// 这条锁住 XM-0044 最重要的兼容性承诺：没配的部署行为与本任务之前逐字相同
// （收入侧 not_supported、台账写 NULL）。
// ---------------------------------------------------------------------------
// 凭据遮罩
// ---------------------------------------------------------------------------

// TestScrubError：pgx 的连接错误会带整条连接串，而这些错误会进结构化日志。
func TestScrubError(t *testing.T) {
	if got := ScrubError(nil); got != "" {
		t.Fatalf("nil 应回空串, got %q", got)
	}
	cases := []struct {
		in       string
		mustHide string
		mustKeep string
	}{
		{
			in:       `failed to connect to postgres://reader:hunter2@db.example.test:5432/newapi`,
			mustHide: "hunter2",
			// 主机与库名要留着——整条抹掉等于把一个可修的配置错误变成黑箱。
			mustKeep: "db.example.test",
		},
		{
			in:       `cannot connect: host=db.example.test password=hunter2 dbname=newapi`,
			mustHide: "hunter2",
			mustKeep: "dbname=newapi",
		},
	}
	for _, tc := range cases {
		got := ScrubError(errors.New(tc.in))
		if strings.Contains(got, tc.mustHide) {
			t.Fatalf("口令泄漏: %s", got)
		}
		if !strings.Contains(got, tc.mustKeep) {
			t.Fatalf("排障信息被抹掉了（应保留 %q）: %s", tc.mustKeep, got)
		}
	}
}
