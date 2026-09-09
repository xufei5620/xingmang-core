package alerts_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/dbroles"
)

// 这个测试补的是一个**今天不存在的闸**。
//
// contracts/database/role-policy.v1.json 为 alerts.alert 逐列列了它的列名，
// internal/platform/dbroles/policy.go 的 tableColumns() 里又手抄了一份，而
// dbroles 自己的测试只让这两份**手列**互相对账——两边同源，所以无论迁移怎么
// 改，它们都永远绿。真正会红的是对生产跑 cmd/db-role-verify 时的
// CATALOG_OBJECT_SHAPE_DRIFT / CATALOG_UNKNOWN_OBJECT，也就是别人手上。
//
// 一个闸的覆盖范围如果与被校验对象同源手列，它就永远绿（memory：闸的范围
// 要发现不要手列）。所以这里的范围**从库的 information_schema 里发现**：
// alerts schema 下每多一张表、每多一列，这条测试都会自己看见。
//
// 放在 alerts 包（本片拥有）而不是 dbroles 包：本片不改 dbroles 的测试，
// 而这条闸守的正是「alerts 的迁移与策略契约是否还对得上」。

type policyObject struct {
	Kind    string   `json:"kind"`
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

func rolePolicyPath(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "..",
		"contracts", "database", "role-policy.v1.json")
}

func alertsObjectsInPolicy(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(rolePolicyPath(t))
	if err != nil {
		t.Fatalf("读策略契约: %v", err)
	}
	var doc struct {
		Objects []policyObject `json:"objects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析策略契约: %v", err)
	}
	out := map[string][]string{}
	for _, o := range doc.Objects {
		if o.Kind != "table" || o.Schema != "alerts" {
			continue
		}
		cols := append([]string(nil), o.Columns...)
		sort.Strings(cols)
		out[o.Name] = cols
	}
	return out
}

const policyDriftFixHint = `修法（四处必须一起动，缺一处都会在别人手上红）：
  1. internal/platform/dbroles/policy.go 的 tableColumns()
  2. 同文件 defaultObjects() 里对应的 add("table", "alerts", …)
  3. contracts/database/role-policy.v1.json（漂移门禁真正读的那一份）
  4. contracts/database/role-policy-state-events.v1.jsonl 追加一条 policy-update 事件
     （current_policy_sha256 = 新策略文件的 sha256；不追加的话
      dbroles 的 TestCheckedInPolicyContractLoads 会当场红）
第 4 步是审批门控那一步，不能顺手做——见交接文档 XM-OPS-TRUTH-B.md 的 risks。`

// TestAlertsSchemaMatchesRolePolicyInventory：库里的 alerts schema 与策略
// 契约必须**双向**一致。
//
// 单向包含是不够的：只查「契约里的列都在库里」，那么迁移新加的列永远发现
// 不了；只查「库里的列都在契约里」，那么契约里留着一张早已删掉的表也发现
// 不了。两个方向的漏检各自对应一种真实的生产失败。
func TestAlertsSchemaMatchesRolePolicyInventory(t *testing.T) {
	pool := testPool(t) // 缺 XM_TEST_DATABASE_URL 就 Skip
	ctx := context.Background()

	// 范围从库里发现，不手列。
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'alerts' AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
	if err != nil {
		t.Fatalf("列出 alerts 表: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("扫描表名: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表名: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("alerts schema 下一张表都没有——测试库没跑迁移？（否则下面的断言恒真）")
	}

	inPolicy := alertsObjectsInPolicy(t)

	for _, table := range tables {
		colRows, err := pool.Query(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'alerts' AND table_name = $1
			ORDER BY column_name`, table)
		if err != nil {
			t.Fatalf("列出 alerts.%s 的列: %v", table, err)
		}
		var cols []string
		for colRows.Next() {
			var name string
			if err := colRows.Scan(&name); err != nil {
				colRows.Close()
				t.Fatalf("扫描列名: %v", err)
			}
			cols = append(cols, name)
		}
		colRows.Close()
		if err := colRows.Err(); err != nil {
			t.Fatalf("遍历列名: %v", err)
		}

		want, ok := inPolicy[table]
		if !ok {
			t.Errorf("库里有 alerts.%s，策略契约里没有登记它。\n%s", table, policyDriftFixHint)
			continue
		}
		if strings.Join(cols, ",") != strings.Join(want, ",") {
			t.Errorf("alerts.%s 的列与策略契约不一致：\n  库里：%v\n  契约：%v\n%s",
				table, cols, want, policyDriftFixHint)
		}
		delete(inPolicy, table)
	}

	// 反向：契约里留着的表，库里必须存在。
	for name := range inPolicy {
		t.Errorf("策略契约里有 alerts.%s，库里没有这张表（迁移删了却没同步契约？）\n%s",
			name, policyDriftFixHint)
	}
}

// TestAlertsTablePrivilegesAreUnchangedByThisSlice：加列与登记新表都不该
// 顺带扩权。
//
// dbroles 的策略变更是审批门控的（memory）。本片只做了两件事：给
// alerts.alert 加两列、给 alerts.upstream_version_ack 登记它自己的授权。
// 既有两张表的授权一个字都不该动——这条测试把那句话钉住。
func TestAlertsTablePrivilegesAreUnchangedByThisSlice(t *testing.T) {
	data, err := os.ReadFile(rolePolicyPath(t))
	if err != nil {
		t.Fatalf("读策略契约: %v", err)
	}
	var doc struct {
		Objects []struct {
			policyObject
			TablePrivileges map[string][]string `json:"table_privileges"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析策略契约: %v", err)
	}

	want := map[string]map[string][]string{
		"alert": {
			"xm_api_runtime":       {"SELECT", "UPDATE"},
			"xm_worker_runtime":    {"DELETE", "INSERT", "SELECT", "UPDATE"},
			"xm_lifecycle_runtime": {"SELECT"},
			"xm_ops_read":          {"SELECT"},
			"xm_backup_read":       {"SELECT"},
		},
		"alert_silence": {
			"xm_api_runtime":       {"INSERT", "SELECT"},
			"xm_worker_runtime":    {"SELECT"},
			"xm_lifecycle_runtime": {"SELECT"},
			"xm_ops_read":          {"SELECT"},
			"xm_backup_read":       {"SELECT"},
		},
		// 新表：Action 用 ON CONFLICT DO UPDATE 写，所以 api 需要 UPDATE；
		// 审稿追加的撤销 Action 用 DELETE 删行，所以还需要 DELETE。
		// 照抄 alert_silence 的 {SELECT, INSERT} 会在角色分离真正上线那天
		// 才失败——授权要按实际跑的语句给，不按长得最像的那张表抄。
		//
		// 这四个权限对应四条真实语句：SetUpstreamVersionAck（INSERT ... ON
		// CONFLICT DO UPDATE）、GetUpstreamVersionAck / ListUpstreamVersionAcks
		// （SELECT）、DeleteUpstreamVersionAck（DELETE）。多一个少一个都是
		// 「本地全绿、红在别人手上」。
		"upstream_version_ack": {
			"xm_api_runtime":       {"DELETE", "INSERT", "SELECT", "UPDATE"},
			"xm_worker_runtime":    {"SELECT"},
			"xm_lifecycle_runtime": {"SELECT"},
			"xm_ops_read":          {"SELECT"},
			"xm_backup_read":       {"SELECT"},
		},
	}

	seen := map[string]bool{}
	for _, o := range doc.Objects {
		if o.Kind != "table" || o.Schema != "alerts" {
			continue
		}
		expect, ok := want[o.Name]
		if !ok {
			t.Fatalf("策略里多出一张 alerts 表 %q——本片只该新增 upstream_version_ack", o.Name)
		}
		seen[o.Name] = true
		for role, privs := range expect {
			got := append([]string(nil), o.TablePrivileges[role]...)
			sort.Strings(got)
			sorted := append([]string(nil), privs...)
			sort.Strings(sorted)
			if strings.Join(got, ",") != strings.Join(sorted, ",") {
				t.Errorf("alerts.%s 对 %s 的授权 = %v, want %v", o.Name, role, got, sorted)
			}
		}
		if len(o.TablePrivileges) != len(expect) {
			t.Errorf("alerts.%s 的授权角色数 = %d, want %d：%v",
				o.Name, len(o.TablePrivileges), len(expect), o.TablePrivileges)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("策略里缺少 alerts.%s", name)
		}
	}
}

// TestAlertsPolicyGoAndContractAgree：`policy.go` 的 Go 侧默认策略与磁盘上的
// 策略契约必须说同一件事。
//
// 这条是审稿这一轮的变异实测暴露出来的：把 `defaultObjects()` 里
// `alerts.upstream_version_ack` 的 `xm_api_runtime` 授权从
// {SELECT,INSERT,UPDATE,DELETE} 改回 {SELECT,INSERT,UPDATE}，而**不动**
// contracts/database/role-policy.v1.json，`go test ./internal/platform/dbroles`
// 全绿——两份策略之间没有任何测试。
//
// 它们是同一个事实的两份副本：JSON 是漂移门禁（cmd/db-role-verify）真正读的
// 那一份，Go 侧的 `DefaultPolicyV1()` 是代码里那一份。漂开时不报错，只会让
// 其中一份安静地给出旧答案——正是本片立项要治的病。
//
// 范围仍然只覆盖 alerts（本片拥有的那两张表）：这个文件不该替 dbroles 把
// 整份策略的对账补上，那是另一片的事，记在交接文档的 follow_ups 里。
func TestAlertsPolicyGoAndContractAgree(t *testing.T) {
	inContract := map[string]map[string][]string{}
	data, err := os.ReadFile(rolePolicyPath(t))
	if err != nil {
		t.Fatalf("读策略契约: %v", err)
	}
	var doc struct {
		Objects []struct {
			policyObject
			TablePrivileges map[string][]string `json:"table_privileges"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析策略契约: %v", err)
	}
	for _, o := range doc.Objects {
		if o.Kind == "table" && o.Schema == "alerts" {
			inContract[o.Name] = o.TablePrivileges
		}
	}

	inCode := map[string]map[string][]string{}
	for _, g := range dbroles.DefaultPolicyV1().Objects {
		if g.Kind == "table" && g.Schema == "alerts" {
			inCode[g.Name] = g.TablePrivileges
		}
	}

	if len(inContract) == 0 || len(inCode) == 0 {
		t.Fatalf("两侧至少要有一张 alerts 表：contract=%d code=%d", len(inContract), len(inCode))
	}
	// 表名双向相等。
	for name := range inContract {
		if _, ok := inCode[name]; !ok {
			t.Errorf("策略契约里有 alerts.%s，policy.go 的 defaultObjects() 里没有", name)
		}
	}
	for name := range inCode {
		if _, ok := inContract[name]; !ok {
			t.Errorf("policy.go 里有 alerts.%s，策略契约里没有。%s", name, policyDriftFixHint)
		}
	}
	// 逐表逐角色的授权集合相等。
	for name, want := range inContract {
		got, ok := inCode[name]
		if !ok {
			continue
		}
		for role, privs := range want {
			if normalizedPrivs(got[role]) != normalizedPrivs(privs) {
				t.Errorf("alerts.%s 对 %s：policy.go 给 %v，策略契约给 %v。%s",
					name, role, got[role], privs, policyDriftFixHint)
			}
		}
		for role := range got {
			if _, ok := want[role]; !ok {
				t.Errorf("alerts.%s：policy.go 多授权了角色 %s", name, role)
			}
		}
	}
}

func normalizedPrivs(privs []string) string {
	out := append([]string(nil), privs...)
	sort.Strings(out)
	return strings.Join(out, ",")
}
