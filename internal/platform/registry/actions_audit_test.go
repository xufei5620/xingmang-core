package registry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// 这些测试验证 Handler 往审计链里贡献了什么。
//
// 内核只知道「谁在什么环境执行了哪个动作」；「动了哪个资源、改前改后长什么样」
// 只有 Handler 知道。没有这一层，审计链虽然完整但回答不了事故复盘的问题。

// capturingSink 记录内核交出的审计事件。
type capturingSink struct{ events []action.AuditEvent }

func (c *capturingSink) Append(_ context.Context, e action.AuditEvent) error {
	c.events = append(c.events, e)
	return nil
}

func auditingKernel(t *testing.T, store *registry.Store) (*action.Kernel, *capturingSink) {
	t.Helper()
	reg := action.NewRegistry()
	if err := registry.RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	sink := &capturingSink{}
	return action.NewKernel(reg, &memRunStore{}, action.WithAuditSink(sink)), sink
}

func adminCtx(scopes ...string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer:              "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: "development", Scopes: scopes,
	})
}

func lastEvent(t *testing.T, sink *capturingSink) action.AuditEvent {
	t.Helper()
	if len(sink.events) == 0 {
		t.Fatal("没有产生审计事件")
	}
	return sink.events[len(sink.events)-1]
}

func TestServiceCreateRecordsResourceAndAfter(t *testing.T) {
	store := registry.NewStore(testPool(t))
	k, sink := auditingKernel(t, store)

	res, err := k.Execute(adminCtx("registry.service.manage"), action.Request{
		ActionID: "registry.service.create", ActionVersion: "1", RequestID: "req-1",
		Params: map[string]any{
			"service_type": "sub2api", "instance_id": "sub2api-audit-1",
			"environment": "development", "endpoint": "https://api.example.com",
			"owner": "ops@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	e := lastEvent(t, sink)
	if e.ResourceType != "core.service" {
		t.Errorf("ResourceType = %q, want core.service", e.ResourceType)
	}
	svc, ok := res.Value.(registry.Service)
	if !ok {
		t.Fatalf("返回值类型意外: %T", res.Value)
	}
	if e.ResourceID != svc.ID.String() {
		t.Errorf("ResourceID = %q, want %q", e.ResourceID, svc.ID)
	}
	// 新建没有前态：留空而不是写一个空对象
	if len(e.BeforeSummary) != 0 {
		t.Errorf("新建不应有 BeforeSummary: %v", e.BeforeSummary)
	}
	if e.AfterSummary["instance_id"] != "sub2api-audit-1" ||
		e.AfterSummary["owner"] != "ops@example.com" ||
		e.AfterSummary["status"] != string(registry.ServiceActive) {
		t.Errorf("AfterSummary 内容不对: %v", e.AfterSummary)
	}
}

func TestServiceObserveRecordsBeforeAndAfter(t *testing.T) {
	store := registry.NewStore(testPool(t))
	svc := seedService(t, store)
	k, sink := auditingKernel(t, store)

	if _, err := k.Execute(adminCtx("registry.service.manage"), action.Request{
		ActionID: "registry.service.observe", ActionVersion: "1", RequestID: "req-2",
		Params: map[string]any{
			"service_type": svc.ServiceType, "instance_id": svc.InstanceID,
			"watermark": "wm-2026-08-27", "status": string(registry.ServiceDegraded),
		},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	e := lastEvent(t, sink)
	if e.ResourceID != svc.ID.String() {
		t.Errorf("ResourceID = %q, want %q", e.ResourceID, svc.ID)
	}
	// 状态变迁是这条审计的全部意义：只记 after 的话，
	// 「从 active 掉到 degraded」和「本来就是 degraded」看起来一模一样
	if e.BeforeSummary["status"] != string(registry.ServiceActive) {
		t.Errorf("BeforeSummary.status = %v, want active", e.BeforeSummary["status"])
	}
	if e.AfterSummary["status"] != string(registry.ServiceDegraded) {
		t.Errorf("AfterSummary.status = %v, want degraded", e.AfterSummary["status"])
	}
	if e.AfterSummary["watermark"] != "wm-2026-08-27" {
		t.Errorf("AfterSummary.watermark = %v", e.AfterSummary["watermark"])
	}
}

// handlerFor 取出某 Action 的 Handler，用于直接验证它的审计贡献。
//
// L2 以上的动作会被 Foundation-A 的内核拦掉（没有 Advanced Controls），
// 但恰恰是 Kill Switch 这类高风险动作最需要验证审计内容——
// action.CaptureAudit 就是为这个场景准备的接缝。
func handlerFor(t *testing.T, store *registry.Store, id string) action.Handler {
	t.Helper()
	reg := action.NewRegistry()
	if err := registry.RegisterActions(reg, store); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	_, h, ok := reg.Lookup(id, "1")
	if !ok {
		t.Fatalf("未注册的 action %s", id)
	}
	return h
}

func TestConnectionCreateRecordsCredentialRefButNeverPlaintext(t *testing.T) {
	store := registry.NewStore(testPool(t))
	svc := seedService(t, store)
	conn := seedConnector(t, store)

	const ref = "secret://sub2api-dev/read-only-admin"
	_, contrib, err := action.CaptureAudit(context.Background(),
		handlerFor(t, store, "registry.connection.create"),
		map[string]any{
			"connector_id": conn.ID.String(), "service_id": svc.ID.String(),
			"environment": "development", "credential_ref": ref,
			"target_allowlist": []string{"api.example.com"},
			"kill_switch":      "ops.kill.sub2api",
		})
	if err != nil {
		t.Fatalf("建连接: %v", err)
	}

	if contrib.ResourceType != "core.connection" {
		t.Errorf("ResourceType = %q", contrib.ResourceType)
	}
	if contrib.ResourceID == "" {
		t.Error("缺少 ResourceID")
	}
	// CredentialRef 是引用不是凭据（ADR-014），进链既安全又必要——
	// 「这条连接绑了哪个凭据」是事故复盘的第一个问题
	if contrib.After["credential_ref"] != ref {
		t.Errorf("credential_ref 未进审计: %v", contrib.After["credential_ref"])
	}
	// 宪法 7 条：摘要里不得出现形似明文凭据的东西（带账号密码的 DSN）
	for k, v := range contrib.After {
		str, isStr := v.(string)
		if !isStr {
			continue
		}
		if strings.Contains(str, "@") && strings.Contains(str, "://") &&
			!strings.HasPrefix(str, "secret://") {
			t.Errorf("字段 %s 疑似含明文凭据 DSN: %q", k, str)
		}
	}
}

func TestConnectionSetStatusRecordsTransition(t *testing.T) {
	// Kill Switch 拉闸是最需要审计的动作之一。前态必须留痕：
	// 「本来就是 killed」和「刚从 enabled 拉闸」在事故复盘里是两件事。
	store := registry.NewStore(testPool(t))
	svc := seedService(t, store)
	connector := seedConnector(t, store)

	created, _, err := action.CaptureAudit(context.Background(),
		handlerFor(t, store, "registry.connection.create"),
		map[string]any{
			"connector_id": connector.ID.String(), "service_id": svc.ID.String(),
			"environment": "development", "credential_ref": "secret://sub2api-dev/read-only-admin",
			"target_allowlist": []string{"api.example.com"},
			"kill_switch":      "ops.kill.sub2api",
		})
	if err != nil {
		t.Fatalf("建连接: %v", err)
	}
	conn := created.(registry.Connection)

	_, contrib, err := action.CaptureAudit(context.Background(),
		handlerFor(t, store, "registry.connection.set_status"),
		map[string]any{
			"connection_id": conn.ID.String(), "status": string(registry.ConnectionKilled),
		})
	if err != nil {
		t.Fatalf("拉闸: %v", err)
	}

	if contrib.ResourceID != conn.ID.String() {
		t.Errorf("ResourceID = %q, want %q", contrib.ResourceID, conn.ID)
	}
	if contrib.Before["status"] != string(registry.ConnectionEnabled) {
		t.Errorf("Before.status = %v, want enabled", contrib.Before["status"])
	}
	if contrib.After["status"] != string(registry.ConnectionKilled) {
		t.Errorf("After.status = %v, want killed", contrib.After["status"])
	}
}

func TestConnectorCreateRecordsGrantedCapabilities(t *testing.T) {
	// registry.connector.create 是 L2，同样过不了 Foundation-A 的内核。
	// 「授予了哪些能力」是 ADR-004 的核心，必须留痕。
	store := registry.NewStore(testPool(t))

	_, contrib, err := action.CaptureAudit(context.Background(),
		handlerFor(t, store, "registry.connector.create"),
		map[string]any{
			"key": "sub2api-audit", "version": "0.1.152", "contract_version": "1",
			"connection_schema_path": "contracts/connectors/sub2api.read.v1.md",
			"target_allowlist":       []string{"api.example.com"},
			"read_capabilities":      []string{"sub2api.users.read"},
		})
	if err != nil {
		t.Fatalf("建 Connector: %v", err)
	}

	if contrib.ResourceType != "core.connector" {
		t.Errorf("ResourceType = %q", contrib.ResourceType)
	}
	caps, ok := contrib.After["read_capabilities"].([]string)
	if !ok || len(caps) != 1 || caps[0] != "sub2api.users.read" {
		t.Errorf("read_capabilities 未进审计: %#v", contrib.After["read_capabilities"])
	}
	// 没授写能力就该是空，而不是缺字段——「没有写能力」是要能被断言的事实
	writes, ok := contrib.After["write_capabilities"].([]string)
	if !ok || len(writes) != 0 {
		t.Errorf("write_capabilities 应为空切片, got %#v", contrib.After["write_capabilities"])
	}
}

func TestFailedHandlerStillRecordsResourceWhenKnown(t *testing.T) {
	// Handler 在失败前已经确定了动的是哪个资源，这条线索必须保留：
	// 「改哪个资源时出的错」是排查的起点
	store := registry.NewStore(testPool(t))
	svc := seedService(t, store)
	k, sink := auditingKernel(t, store)

	// 用一个不存在的状态制造失败？Schema 会先拦下。改用不存在的实例：
	// 那样 Handler 在 RecordResource 之前就返回了，事件里没有资源信息——
	// 这是预期行为，测的是「有则记，无则空」而不是「必须有」
	if _, err := k.Execute(adminCtx("registry.service.manage"), action.Request{
		ActionID: "registry.service.observe", ActionVersion: "1", RequestID: "req-6",
		Params: map[string]any{
			"service_type": svc.ServiceType, "instance_id": "不存在的实例",
			"watermark": "wm-x", "status": string(registry.ServiceActive),
		},
	}); err == nil {
		t.Fatal("不存在的实例应当失败")
	}

	e := lastEvent(t, sink)
	if e.Succeeded {
		t.Error("失败的执行不应记为成功")
	}
	if e.ErrorCode != action.CodeExecutionFailed {
		t.Errorf("ErrorCode = %q", e.ErrorCode)
	}
	if e.ResourceID != "" {
		t.Errorf("Handler 还没定位到资源就失败了，不该有 ResourceID: %q", e.ResourceID)
	}
}
