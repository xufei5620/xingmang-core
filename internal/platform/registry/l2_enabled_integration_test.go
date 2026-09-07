package registry_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// 这个文件钉住**启用审批中心到底改变了什么**（XM-0030-ENABLE）。
//
// `registry.connector.create` 与 `registry.connection.set_status` 声明了 L2，
// 而 Foundation-A 的内核对 L2 及以上一律 `ADVANCED_CONTROLS_REQUIRED`（501）
// ——两个 Action 登记在册却**根本执行不了**，这是盘点时记录在案的缺陷。
//
// 接上审批中心之后它们变成「可执行，但要先被人批」。下面两条用同一次调用
// 分别在「没接」与「接了」两种装配下断言，差别就是这一片的全部价值。

func enablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	t.Cleanup(pool.Close)
	cleanup := func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM core.approval_request WHERE requester_id = 'staff_alice'`); err != nil {
			t.Errorf("清理审批单: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	return pool
}

type enableRunStore struct{ runs []action.Run }

func (s *enableRunStore) InsertRun(_ context.Context, r action.Run) error {
	s.runs = append(s.runs, r)
	return nil
}

func l2AdminCtx(scopes ...string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman,
		IdentityZone: "staff", Issuer: "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: "staging", Scopes: scopes,
	})
}

// connectorCreateRequest 是一次形态完全合法的 L2 调用。
//
// 参数要真的过 Schema——否则「被拒」可能是因为参数不合法，而不是因为等级，
// 那样两条用例都会以错误的理由绿。
func connectorCreateRequest() action.Request {
	return action.Request{
		ActionID:      "registry.connector.create",
		ActionVersion: "1",
		RequestID:     "req-l2-enable-1",
		Reason:        "上游换了域名，需要登记新的连接器",
		// 逐字对着 connectorCreateDef() 的 Schema 填五个必填项。
		// 第一版我凭印象写了 connector_type/name，被 INVALID_PARAMS 挡在
		// 风险闸之前——那样两条用例都会以错误的理由绿（也以错误的理由红）。
		Params: map[string]any{
			"key":                         "sub2api",
			"version":                     "1",
			"contract_version":            "v1",
			"connection_schema_path":      "contracts/connectors/sub2api.connection.schema.json",
			"target_allowlist":            []string{"api.example.com"},
			"supported_upstream_versions": []string{"0.2"},
		},
	}
}

func newRegistryKernel(t *testing.T, gateway action.ApprovalGateway) (*action.Kernel, *enableRunStore) {
	t.Helper()
	reg := action.NewRegistry()
	if err := registry.RegisterActions(reg, nil); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	store := &enableRunStore{}
	opts := []action.KernelOption{}
	if gateway != nil {
		opts = append(opts, action.WithApprovalGateway(gateway))
	}
	return action.NewKernel(reg, store, opts...), store
}

// TestL2WasUnreachableBeforeEnabling 是**对照组**：没接审批中心时，这次调用
// 拿到 501 ADVANCED_CONTROLS_REQUIRED——也就是启用之前生产的样子。
//
// 有它在，下面那条「接了之后拿 202」才说明问题：否则「拿到 APPROVAL_REQUIRED」
// 有可能只是因为参数或权限碰巧对上了别的分支。
func TestL2WasUnreachableBeforeEnabling(t *testing.T) {
	kernel, _ := newRegistryKernel(t, nil)

	_, err := kernel.Execute(l2AdminCtx("registry.connector.manage"), connectorCreateRequest())
	if code := action.ErrorCode(err); code != action.CodeAdvancedControlsRequired {
		t.Fatalf("没接审批中心时应当 fail closed，got %q（%v）", code, err)
	}
}

// TestL2BecomesApprovalRequiredAfterEnabling 是启用的**主张**：同一次调用，
// 接上审批中心之后被受理成一张待批单，而不是被拒。
func TestL2BecomesApprovalRequiredAfterEnabling(t *testing.T) {
	pool := enablePool(t)
	svc := approval.NewService(
		approval.NewPgStore(pool, "staging", nil), approval.DefaultPolicy(), nil)
	kernel, store := newRegistryKernel(t, svc)

	_, err := kernel.Execute(l2AdminCtx("registry.connector.manage"), connectorCreateRequest())
	if code := action.ErrorCode(err); code != action.CodeApprovalRequired {
		t.Fatalf("接上审批中心后应当受理成审批单，got %q（%v）", code, err)
	}
	var ae *action.Error
	if !asActionErr(err, &ae) || ae.ApprovalRequestID == "" {
		t.Fatalf("错误里必须带单号供调用方跟进：%+v", ae)
	}
	// 单要真的落库，而且冻结的是这次调用的参数与理由。
	req, getErr := svc.Get(context.Background(), ae.ApprovalRequestID)
	if getErr != nil {
		t.Fatalf("单没落库: %v", getErr)
	}
	if req.ActionID != "registry.connector.create" || req.RiskLevel != "L2" ||
		req.Reason != "上游换了域名，需要登记新的连接器" ||
		req.Params["key"] != "sub2api" || req.Params["contract_version"] != "v1" {
		t.Fatalf("单上冻结的内容不对：%+v", req)
	}
	// 哈希非空且与参数自洽——执行时内核靠它比对「批准的是这一份参数」。
	if req.ParamsHash == "" {
		t.Fatalf("参数哈希没冻结：%+v", req)
	}
	if req.RequesterID != "staff_alice" || req.Status != approval.StatusPending {
		t.Fatalf("提交人或状态不对：%+v", req)
	}
	// 落单也要留痕（谁在什么时候请求做什么）。
	if len(store.runs) != 1 || store.runs[0].ErrorCode != action.CodeApprovalRequired {
		t.Fatalf("落单必须留痕：%+v", store.runs)
	}
}

// TestEnablingDoesNotWeakenPermissionCheck：启用**不能**顺带放宽权限。
// 没有 registry.connector.manage 的人，接了审批中心之后仍然拿 PERMISSION_DENIED
// ——而不是「单已建立」。这正是 XM-0030a-wire 把风险闸挪到权限之后的理由。
func TestEnablingDoesNotWeakenPermissionCheck(t *testing.T) {
	pool := enablePool(t)
	svc := approval.NewService(
		approval.NewPgStore(pool, "staging", nil), approval.DefaultPolicy(), nil)
	kernel, _ := newRegistryKernel(t, svc)

	_, err := kernel.Execute(l2AdminCtx("some.other.scope"), connectorCreateRequest())
	if code := action.ErrorCode(err); code != action.CodePermissionDenied {
		t.Fatalf("没权限的人不该能刷审批单，got %q", code)
	}
	// 队列里不该多出任何东西。
	reqs, listErr := svc.List(context.Background(), approval.StatusPending, 50)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	for _, r := range reqs {
		if r.RequesterID == "staff_alice" {
			t.Fatalf("没权限的调用却落了单：%+v", r)
		}
	}
}

func asActionErr(err error, target **action.Error) bool {
	ae, ok := err.(*action.Error)
	if ok {
		*target = ae
	}
	return ok
}
