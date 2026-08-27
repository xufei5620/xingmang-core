package finance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 演示数据种子的三条纪律（XM-0037d）：走 Action 内核、生产硬拒、幂等。
//
// 本文件在包内（而不是 finance_test）是为了能断言 demoSeedPrincipal——
// 那个身份是本片唯一一处需要评审确认的取舍（见它的注释），
// 所以它的形状必须被钉住，而不是靠一句注释约束。

// recordingKernel 记下种子调了哪些 Action、带着什么身份。
type recordingKernel struct {
	requests   []action.Request
	principals []principal.Principal
	failOn     string
}

func (k *recordingKernel) Execute(
	ctx context.Context, req action.Request,
) (action.Result, error) {
	k.requests = append(k.requests, req)
	p, _ := principal.FromContext(ctx)
	k.principals = append(k.principals, p)
	if req.ActionID == k.failOn {
		return action.Result{}, errors.New("内核拒绝")
	}
	// 返回值的形状要与真 Handler 一致——种子靠它取新建资源的 id 去挂子资源。
	switch req.ActionID {
	case ActionAccountSet:
		return action.Result{Value: UpstreamAccount{ID: uuid.New()}}, nil
	case ActionProxyAssetSet:
		return action.Result{Value: ProxyAsset{ID: uuid.New()}}, nil
	case ActionSubscriptionBatchRegister:
		return action.Result{Value: SubscriptionBatch{ID: uuid.New()}}, nil
	default:
		return action.Result{}, nil
	}
}

// memSeedRegistry 是内存登记簿，只回答「有没有」。
type memSeedRegistry struct {
	accounts []UpstreamAccount
	err      error
}

func (m *memSeedRegistry) ListAccountsByEnvironment(
	context.Context, string,
) ([]UpstreamAccount, error) {
	return m.accounts, m.err
}

func quietSeedLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func seedOpts(env string, kernel *recordingKernel, reg *memSeedRegistry) DemoSeedOptions {
	return DemoSeedOptions{
		Environment: env,
		Kernel:      kernel,
		Registry:    reg,
		Logger:      quietSeedLogger(),
		Now:         func() time.Time { return time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) },
	}
}

// TestDemoSeedRefusesProduction 是本文件最要紧的一条。
//
// 演示数据一旦落进生产登记簿，采集就会照着它去打一批 .invalid 域名，
// 而台账里会多出几条永远算不出成本的渠道——而且没人会想到去查登记簿。
// **硬拒而不是静默跳过**：静默跳过会让「生产误开了这个开关」永远不被发现。
func TestDemoSeedRefusesProduction(t *testing.T) {
	kernel := &recordingKernel{}
	_, err := SeedDemoData(context.Background(),
		seedOpts("production", kernel, &memSeedRegistry{}))
	if !errors.Is(err, ErrDemoSeedForbiddenInProduction) {
		t.Fatalf("生产必须硬拒, got %v", err)
	}
	if len(kernel.requests) != 0 {
		t.Fatalf("拒绝时一个 Action 都不该发出, got %d", len(kernel.requests))
	}
}

// TestDemoSeedIsIdempotent：环境里已经有登记簿记录就整个跳过。
//
// 按「有没有记录」而不是「有没有这几条」判断：一个已经在用的环境误开了
// 这个开关，种子什么都不该做——更不该往真实登记簿里塞几条 .invalid 渠道。
func TestDemoSeedIsIdempotent(t *testing.T) {
	kernel := &recordingKernel{}
	reg := &memSeedRegistry{accounts: []UpstreamAccount{{ID: uuid.New()}}}
	got, err := SeedDemoData(context.Background(), seedOpts("staging", kernel, reg))
	if err != nil {
		t.Fatalf("跳过不是错误: %v", err)
	}
	if !got.Skipped {
		t.Fatal("已有记录时必须跳过")
	}
	if len(kernel.requests) != 0 {
		t.Fatalf("跳过时一个 Action 都不该发出, got %d", len(kernel.requests))
	}
}

// TestDemoSeedGoesThroughActionKernel 钉住「不绕库」（宪法 2 条）。
//
// 种子写的每一条记录都必须经过一个 Action——绕过去直接 INSERT 会让它成为
// 唯一一条不受 Action 约束的写路径，而那正是宪法 2 条要挡的通道。
func TestDemoSeedGoesThroughActionKernel(t *testing.T) {
	kernel := &recordingKernel{}
	got, err := SeedDemoData(context.Background(),
		seedOpts("staging", kernel, &memSeedRegistry{}))
	if err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	if got.Skipped {
		t.Fatal("空登记簿不该跳过")
	}
	// 两个计量型 + 一个订阅型
	if got.Accounts != 3 {
		t.Fatalf("账号数 = %d, want 3（计量型 2 + 订阅型 1）", got.Accounts)
	}
	if got.TokenMappings != 5 {
		t.Fatalf("映射数 = %d, want 5", got.TokenMappings)
	}
	if got.ProxyAssets != 1 || got.SubscriptionSet != 1 {
		t.Fatalf("订阅批次与代理各一: %+v", got)
	}

	counts := map[string]int{}
	for _, req := range kernel.requests {
		counts[req.ActionID]++
		if req.ActionVersion != actionVersion {
			t.Fatalf("%s 的版本 = %q", req.ActionID, req.ActionVersion)
		}
		// 内核要求 RequestID 非空；前缀让「这一批是种子写的」可被一次检索捞出来
		if !strings.HasPrefix(req.RequestID, demoSeedRequestPrefix) {
			t.Fatalf("%s 的 request_id = %q，应带种子前缀", req.ActionID, req.RequestID)
		}
	}
	for id, want := range map[string]int{
		ActionAccountSet: 3, ActionTokenMapSet: 5,
		ActionProxyAssetSet: 1, ActionSubscriptionBatchRegister: 1,
	} {
		if counts[id] != want {
			t.Fatalf("%s 调用 %d 次, want %d", id, counts[id], want)
		}
	}
}

// TestDemoSeedPrincipalIsMarkedAsSeed 钉住那个需要评审确认的取舍。
//
// 类型必须是 HUMAN（登记簿那几个 Action 声明的是 humanOnly，ADR-009 红线），
// 但来路必须**一眼可辨**：Issuer 是任何真实登录都产不出的串，
// AuthenticationLevel 留空（这里确实没有发生过任何认证）。
//
// 改掉其中任何一样都该让这条用例红——那意味着审计链上再也认不出
// 「这几条不是人干的」。
func TestDemoSeedPrincipalIsMarkedAsSeed(t *testing.T) {
	kernel := &recordingKernel{}
	if _, err := SeedDemoData(context.Background(),
		seedOpts("staging", kernel, &memSeedRegistry{})); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	if len(kernel.principals) == 0 {
		t.Fatal("没有记录到身份")
	}
	for _, p := range kernel.principals {
		if p.Type != principal.TypeHuman {
			t.Fatalf("身份类型 = %s；登记簿那几个 Action 是 humanOnly", p.Type)
		}
		if p.Issuer != "xingmang://finance-demo-seed" {
			t.Fatalf("签发者 = %q，必须是一眼可辨的种子来路", p.Issuer)
		}
		if p.AuthenticationLevel != "" {
			t.Fatalf("这里没有发生过认证，AuthenticationLevel 应留空, got %q",
				p.AuthenticationLevel)
		}
		if p.Environment != "staging" {
			t.Fatalf("环境 = %q", p.Environment)
		}
	}
}

// TestDemoSeedReportsFirstFailure：某一步失败时整体报错（拒绝启动），
// 但**后续步骤照样发出**——一次启动就能看到全部失败，
// 而不是修一个重启一次再看下一个。
func TestDemoSeedReportsFirstFailure(t *testing.T) {
	kernel := &recordingKernel{failOn: ActionTokenMapSet}
	_, err := SeedDemoData(context.Background(),
		seedOpts("staging", kernel, &memSeedRegistry{}))
	if err == nil {
		t.Fatal("有步骤失败时必须整体报错")
	}
	if !strings.Contains(err.Error(), ActionTokenMapSet) {
		t.Fatalf("错误应指出是哪一步: %v", err)
	}
	// 后续的代理与批次仍然被尝试过
	var sawProxy bool
	for _, req := range kernel.requests {
		if req.ActionID == ActionProxyAssetSet {
			sawProxy = true
		}
	}
	if !sawProxy {
		t.Fatal("一步失败不该让后面的步骤不再尝试——那样只能一次修一个")
	}
}

// TestDemoSeedSkipsChildrenOfFailedParent：父账号没建成时，
// 挂在它下面的映射不该发出一个注定失败的请求。
//
// 发出去只会在日志里多一条误导性的「映射登记失败」，
// 而真正的原因在上一条记录里。
func TestDemoSeedSkipsChildrenOfFailedParent(t *testing.T) {
	kernel := &recordingKernel{failOn: ActionAccountSet}
	if _, err := SeedDemoData(context.Background(),
		seedOpts("staging", kernel, &memSeedRegistry{})); err == nil {
		t.Fatal("账号建不成必须报错")
	}
	for _, req := range kernel.requests {
		if req.ActionID == ActionTokenMapSet {
			t.Fatal("父账号没建成时不该发出映射登记请求")
		}
	}
}
