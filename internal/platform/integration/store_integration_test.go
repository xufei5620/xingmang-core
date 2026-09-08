package integration_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/integration"
)

// 本文件跑在真库上：两张表的关键不变量有一半只写在 SQL 里
// （credential_ref 的形态 CHECK、状态与触发类别的闭集 CHECK、
// 环境外键、两条唯一键），用内存假货复刻只会测到假货。
//
// 内存假货那一份在 actions_test.go——它验的是另一件事（Handler 的行为与
// 「规则不会被执行」），刻意不依赖数据库，好让那条断言在任何机器上都跑。

const testEnv = "production"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// 两张表互不引用，一条 TRUNCATE 列全即可。
	if _, err := pool.Exec(ctx,
		"TRUNCATE core.api_client, core.automation_rule"); err != nil {
		t.Fatalf("清空登记簿失败: %v", err)
	}
	return pool
}

func testStore(t *testing.T) *integration.Store {
	t.Helper()
	return integration.NewStore(testPool(t), testEnv, func() time.Time {
		return time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	})
}

func sampleClient() integration.APIClient {
	return integration.APIClient{
		PrincipalID:    "svc-billing-sync",
		PrincipalType:  "SERVICE",
		DisplayName:    "对账同步",
		Purpose:        "每日拉取上游账单",
		Owner:          "财务运营",
		ExpectedScopes: []string{"finance.read"},
		CredentialRef:  "secret://integration/billing-sync",
		Status:         integration.ClientActive,
		CreatedBy:      "staff_alice",
		UpdatedBy:      "staff_alice",
	}
}

func TestAPIClientRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	created, err := s.CreateAPIClient(ctx, sampleClient())
	if err != nil {
		t.Fatalf("CreateAPIClient: %v", err)
	}
	if created.Environment != testEnv {
		t.Fatalf("环境应由仓储决定，实际 %q", created.Environment)
	}
	if created.CredentialRef != "secret://integration/billing-sync" {
		t.Fatalf("凭据引用应原样存回，实际 %q", created.CredentialRef)
	}

	got, err := s.GetAPIClient(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAPIClient: %v", err)
	}
	if got.DisplayName != "对账同步" || len(got.ExpectedScopes) != 1 {
		t.Fatalf("读回的行与写入不一致: %+v", got)
	}

	list, err := s.ListAPIClients(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAPIClients 应得 1 行，实际 %d err=%v", len(list), err)
	}
}

// TestAPIClientEnvironmentIsScoped 钉住跨环境读写的边界：仓储绑定的是
// 自己那个环境，另一个环境的仓储既读不到也改不了这一行（宪法 15 条）。
func TestAPIClientEnvironmentIsScoped(t *testing.T) {
	pool := testPool(t)
	prod := integration.NewStore(pool, "production", nil)
	staging := integration.NewStore(pool, "staging", nil)
	ctx := context.Background()

	created, err := prod.CreateAPIClient(ctx, sampleClient())
	if err != nil {
		t.Fatalf("CreateAPIClient: %v", err)
	}
	// 正向锚点：本环境读得到。没有它，下面那条「另一个环境读不到」也可能
	// 只是因为这一行压根没写进去。
	if _, err := prod.GetAPIClient(ctx, created.ID); err != nil {
		t.Fatalf("锚点：本环境应读得到: %v", err)
	}
	if _, err := staging.GetAPIClient(ctx, created.ID); !errors.Is(err, integration.ErrNotFound) {
		t.Fatalf("另一个环境不该读到这一行，实际 err=%v", err)
	}
	if _, err := staging.SetAPIClientStatus(ctx, created.ID,
		integration.ClientDisabled, "staff_bob"); !errors.Is(err, integration.ErrNotFound) {
		t.Fatalf("另一个环境不该改得动这一行，实际 err=%v", err)
	}
	// 且本环境的那一行没有被改动。
	after, err := prod.GetAPIClient(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAPIClient: %v", err)
	}
	if after.Status != integration.ClientActive {
		t.Fatalf("跨环境写不该生效，实际状态 %q", after.Status)
	}
}

func TestAPIClientDuplicatePrincipalRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateAPIClient(ctx, sampleClient()); err != nil {
		t.Fatalf("第一条应成功: %v", err)
	}
	second := sampleClient()
	second.DisplayName = "另一个名字"
	_, err := s.CreateAPIClient(ctx, second)
	if !errors.Is(err, integration.ErrDuplicate) {
		t.Fatalf("同环境同 principal_id 应被唯一键挡住，实际 err=%v", err)
	}
}

// TestAPIClientCredentialRefCheckIsEnforcedInDatabase 钉的是**库层**那道
// CHECK，不是 Go 侧的 Validate。
//
// 两道都要有：Go 侧那道给出可读的错误，库层那道保证即便有人绕过服务层
// （将来的批量导入、修数脚本）也塞不进一把明文凭据。这里直接用 SQL 写，
// 正是为了跳过 Go 的校验、真的去撞那条 CHECK。
func TestAPIClientCredentialRefCheckIsEnforcedInDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO core.api_client(id, principal_id, principal_type, display_name,
			credential_ref, status, environment, created_by, updated_by)
		VALUES(gen_random_uuid(), 'svc-x', 'SERVICE', 'X',
			'sk-live-0123456789', 'active', $1, 'staff_alice', 'staff_alice')`, testEnv)
	if err == nil {
		t.Fatal("库层应拒绝非 secret:// 形态的 credential_ref（宪法 7 条）")
	}
	if !strings.Contains(err.Error(), "api_client_credential_ref_check") {
		t.Fatalf("应撞上 credential_ref 的 CHECK，实际 %v", err)
	}
	// 对照组：换成合法引用同一条语句必须成功——否则上面那条红可能来自
	// 别的原因（列名写错、环境不存在），什么也没证明。
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.api_client(id, principal_id, principal_type, display_name,
			credential_ref, status, environment, created_by, updated_by)
		VALUES(gen_random_uuid(), 'svc-x', 'SERVICE', 'X',
			'secret://integration/x', 'active', $1, 'staff_alice', 'staff_alice')`, testEnv); err != nil {
		t.Fatalf("对照组：合法引用应写得进去，实际 %v", err)
	}
}

func sampleRule() integration.AutomationRule {
	return integration.AutomationRule{
		Name:                "余额低于阈值时补充",
		Description:         "上游余额跌破阈值时补一次充值登记",
		TriggerKind:         integration.TriggerEvent,
		TriggerDetail:       "alerts.alert.opened(rule_key=upstream_balance_low)",
		TargetActionID:      "finance.upstream_account.set",
		TargetActionVersion: "1",
		Status:              integration.RuleDraft,
		CreatedBy:           "staff_alice",
		UpdatedBy:           "staff_alice",
	}
}

func TestAutomationRuleRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	created, err := s.CreateAutomationRule(ctx, sampleRule())
	if err != nil {
		t.Fatalf("CreateAutomationRule: %v", err)
	}
	updated, err := s.SetAutomationRuleStatus(ctx, created.ID, integration.RuleRegistered, "staff_bob")
	if err != nil {
		t.Fatalf("SetAutomationRuleStatus: %v", err)
	}
	if updated.Status != integration.RuleRegistered {
		t.Fatalf("状态应变成 registered，实际 %q", updated.Status)
	}
	list, err := s.ListAutomationRules(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAutomationRules 应得 1 行，实际 %d err=%v", len(list), err)
	}
}

// TestAutomationRuleStatusCheckIsEnforcedInDatabase 钉住库层的状态闭集。
//
// 特意用 'enabled' 去撞：那正是最可能被将来某个人塞进来的词，而它一旦存在，
// 页面上就会出现一个看起来像运行开关的状态——而这张表没有执行器。
func TestAutomationRuleStatusCheckIsEnforcedInDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO core.automation_rule(id, name, trigger_kind,
			target_action_id, target_action_version, status, environment,
			created_by, updated_by)
		VALUES(gen_random_uuid(), 'r1', 'event', 'a.b.c', '1', 'enabled', $1,
			'staff_alice', 'staff_alice')`, testEnv)
	if err == nil {
		t.Fatal("库层应拒绝 status='enabled'：这张表没有执行器，" +
			"一个叫 enabled 的状态会被读成运行开关")
	}
	if !strings.Contains(err.Error(), "automation_rule_status_check") {
		t.Fatalf("应撞上 status 的 CHECK，实际 %v", err)
	}
	// 对照组：闭集内的取值必须写得进去。
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.automation_rule(id, name, trigger_kind,
			target_action_id, target_action_version, status, environment,
			created_by, updated_by)
		VALUES(gen_random_uuid(), 'r1', 'event', 'a.b.c', '1', 'registered', $1,
			'staff_alice', 'staff_alice')`, testEnv); err != nil {
		t.Fatalf("对照组：registered 应写得进去，实际 %v", err)
	}
}
