package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// XM-ASSURE1-glue：证明 XM-ASSURE1-ui（前端）与 XM-ASSURE1-core（后端）
// 两片彼此兼容——用真实 assurance.Action Handler（经 action.Kernel 完整
// 走一遍 Schema 校验/权限/环境检查）+ 真实 assurance.Store（真库）+ 真实
// AssuranceProbeWorker（fake 探测客户端），declare → run → 执行批次 →
// Query，全程零 mock。
//
// 两处专门验证的兼容性假设（见 docs/handoffs/slices/XM-ASSURE1-ui.md 偏离
// #6、docs/handoffs/slices/XM-ASSURE1-core.md types.go 的 Target 文档注释）：
//
//  1. targets.channel_id 的 ID 空间：前端 web/apps/admin-web/src/api/
//     assuranceProbes.ts 的 encodeTargets() 把渠道目录（PlatformChannelRef）
//     的 externalChannelId 同时塞进 channel_id 与 external_channel_id 两个
//     字段。本测试用与 internal/platform/httpapi/platform_channels.go
//     的 catalogRowsForService/inventoryForService 完全相同的数据源
//     （<platform>.channels.status 的 ops 观测，按 row["channel_id"] 键控）
//     去种子渠道目录，再用同一个字符串同时填 channel_id/external_channel_id
//     构造声明请求——如果这两片对"哪个 ID 空间"的理解不一致，declare@1
//     会在这里报"渠道目录里查无此渠道"，而不是要等到真实环境才发现。
//  2. 自由文本模型名：前端不提供模型下拉（渠道目录今天只有 models.count，
//     没有具体模型名清单，见 assuranceProbes.ts 顶部注释），"目标模型"是
//     运营手填的任意字符串。本测试用一个刻意"看起来不像任何已知厂商模型名"
//     的字符串，验证 declare@1 的 Schema/Store 接受它、探测结果行原样
//     回显它（不被规整、截断或拒绝）。

// uiProbeTarget/uiExpectedShapeWire/uiDeclareParams 逐字段照抄
// web/apps/admin-web/src/api/assuranceProbes.ts 的 encodeTargets()/
// PROBE_DEFAULT_EXPECTED_SHAPE/declareProbe() 的 JSON 键名与结构——这是本
// 测试"请求体按 UI 实际拼法构造"的字面依据，不是照着后端契约反推的。
type uiProbeTarget struct {
	ChannelID         string `json:"channel_id"`
	ExternalChannelID string `json:"external_channel_id"`
	Model             string `json:"model"`
}

type uiExpectedShapeWire struct {
	MinLength   int      `json:"min_length"`
	MaxLength   int      `json:"max_length"`
	MustContain []string `json:"must_contain"`
	TimeoutMS   int      `json:"timeout_ms"`
}

type uiDeclareParamsWire struct {
	Platform          string `json:"platform"`
	Name              string `json:"name"`
	PromptTemplateKey string `json:"prompt_template_key"`
	TargetHost        string `json:"target_host"`
	Targets           string `json:"targets"`
	MaxTokens         int    `json:"max_tokens"`
	ExpectedShape     string `json:"expected_shape"`
}

// uiDeclareActionParams 把 uiDeclareParamsWire 编码成一次 JSON 请求体、再
// 解回 map[string]any——精确复现"浏览器发出的 HTTP 请求体经 encoding/json
// 解码"这一步的类型转换（数字变 float64），而不是直接手搓 Go 原生类型的
// map，确保 action.Schema.Validate/IntParam 走的是真实的 JSON 解码路径。
func uiDeclareActionParams(t *testing.T, platform, name, templateKey, targetHost string, targets []uiProbeTarget, maxTokens int) map[string]any {
	t.Helper()
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		t.Fatalf("encode targets: %v", err)
	}
	shapeJSON, err := json.Marshal(uiExpectedShapeWire{MinLength: 1, MaxLength: 2000, MustContain: []string{}, TimeoutMS: 30000})
	if err != nil {
		t.Fatalf("encode expected_shape: %v", err)
	}
	wire := uiDeclareParamsWire{
		Platform: platform, Name: name, PromptTemplateKey: templateKey, TargetHost: targetHost,
		Targets: string(targetsJSON), MaxTokens: maxTokens, ExpectedShape: string(shapeJSON),
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("encode declare body: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(body, &params); err != nil {
		t.Fatalf("decode declare body: %v", err)
	}
	return params
}

// recordingProbeEnqueuer 记录 InsertTx 调用而不接触真实 river_job 表——与
// internal/platform/assurance 包自己的集成测试（store_integration_test.go
// 的 fakeJobEnqueuer）同一条纪律：Store 的入队耦合只需要满足
// assurance.JobEnqueuer 接口的"某个东西"，这个测试库从不跑 River 自己的
// 迁移。
type recordingProbeEnqueuer struct {
	calls []assurance.ProbeArgs
}

func (r *recordingProbeEnqueuer) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	probeArgs, ok := args.(assurance.ProbeArgs)
	if !ok {
		return nil, nil
	}
	r.calls = append(r.calls, probeArgs)
	return &rivertype.JobInsertResult{}, nil
}

// fakeActionRunStore 是 action.Kernel 需要的最小 RunStore：只记录，不断言
// ——审计链路本身不是本测试要验证的东西（那是 action 包自己的职责）。
type fakeActionRunStore struct{ runs []action.Run }

func (f *fakeActionRunStore) InsertRun(_ context.Context, r action.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

// seedUICompatChannelCatalog 写一条 <platform>.channels.status 观测——与
// internal/platform/httpapi/platform_channels.go 的 catalogRowsForService/
// inventoryForService 读的是同一个 metric_key、同一个 row["channel_id"]
// 键，确保本测试种子的目录与真实"渠道管理"页读到的是同一份数据源。
func seedUICompatChannelCatalog(t *testing.T, pool *pgxpool.Pool, platform, environment, externalChannelID string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := ops.NewStore(pool).UpsertWithSample(context.Background(), ops.Observation{
		MetricKey: platform + ".channels.status", Source: platform + "-ui-compat-test", Environment: environment,
		SyncedAt: now, ObservedAt: &now, LastSuccess: &now, Status: ops.SyncOK,
		StalenessThresholdSeconds: 1800,
		Value: map[string]any{"channels": []any{
			map[string]any{"channel_id": externalChannelID, "channel_name": "渠道 " + externalChannelID},
		}},
	})
	if err != nil {
		t.Fatalf("seed channel catalog: %v", err)
	}
}

// uiCompatCleanup 镜像 internal/platform/assurance 包自己的
// store_integration_test.go assurancePool() 清空的三张表/范围——尤其是
// core.connector_config：platform+environment 键空间只有 2x3=6 种取值，
// internal/platform/credentials 包（TestConnectorConfigSetAndList 等）与
// internal/platform/jobs 包自己的 connector_config_integration_test.go 都会
// 写 ('sub2api'/'newapi', 'staging') 这两行且不保证在它们之后清干净（那边的
// 注释也点明了这一点）。本测试起初漏了这一条，在单独跑（-run UICompat）时
// 因为共享测试库里恰好没有别人留下的行而蒙混过关，但跟其它包一起跑
// `go test -p 1 ./...`（同一个共享 xm_test 库）时，会读到别的包留下的
// mode=real 行，把 fake 模式该绕过的全部闸误判成 real 模式的四层闸，
// 导致 run@1 被 global_kill_switch_off 拒绝——本片自己撞过这个坑，
// 修法是跟 assurancePool 一样两头（开跑前 + t.Cleanup）都清干净，
// 不依赖"这张表本来就是空的"这个假设。
func uiCompatCleanup(t *testing.T, pool *pgxpool.Pool, ctx context.Context, environment string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `TRUNCATE assurance.probe_result, assurance.probe_run, assurance.probe_declaration`); err != nil {
		t.Fatalf("清空 assurance 表: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM core.connector_config WHERE platform IN ('sub2api','newapi') AND environment = $1`,
		environment); err != nil {
		t.Fatalf("清空接入配置: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM ops.metric_observation WHERE metric_key IN ('sub2api.channels.status','newapi.channels.status') AND environment = $1`,
		environment); err != nil {
		t.Fatalf("清空渠道目录观测: %v", err)
	}
}

// asMap 把 Action 结果断言成 map[string]any，失败时给出可读的错误。
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result value = %#v, want map[string]any", v)
	}
	return m
}

// TestUICompatDeclareRunQueryEndToEnd 是团队交接消息要求的"declare → run →
// query the run"兼容性证据：请求体按 web/apps/admin-web/src/api/
// assuranceProbes.ts 的实际拼法构造，跑真实 Action Handler（经
// action.Kernel）、真实 assurance.Store（真库）、真实 AssuranceProbeWorker
// （fake 探测客户端），最后用真实 assurance.Service 的两个 Query 方法
// （httpapi 层直接调用的同一批方法）核对结果，全程零 mock。
func TestUICompatDeclareRunQueryEndToEnd(t *testing.T) {
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set XM_TEST_DATABASE_URL (CI) or XM_RUN_INTEGRATION=1 with a local DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup（不是 defer）且先于下面 uiCompatCleanup 的 t.Cleanup 注册——
	// t.Cleanup 按后注册先执行（LIFO），这样收尾时会先清表、再关连接池；
	// 反过来（defer pool.Close() 在函数体内先于 t.Cleanup 注册）会导致
	// 收尾清表时连接池已经关闭。
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	const environment = "staging"
	const platform = "sub2api"
	// 渠道目录唯一标识——刻意不用 "chn-1" 这类明显是测试夹具的字符串，
	// 贴近真实上游账号 ID 的形状（数字/连字符），与 XM-ASSURE1-ui 偏离 #6
	// 描述的真实场景一致。
	const externalChannelID = "acc-77042"
	// 刻意选一个不像任何已知厂商模型名的自由文本，证明 declare@1/探测客户端
	// 不对模型名做任何厂商枚举限制（渠道目录今天没有模型名清单，"目标模型"
	// 是运营手填的任意字符串，见 assuranceProbes.ts 顶部注释）。
	const freeTextModel = "Codestral-Ultra-9 (internal-eval, 2026-08)"

	uiCompatCleanup(t, pool, ctx, environment)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		uiCompatCleanup(t, pool, cleanupCtx, environment)
	})
	seedUICompatChannelCatalog(t, pool, platform, environment, externalChannelID)

	enq := &recordingProbeEnqueuer{}
	store, err := assurance.NewStore(pool, enq, ops.NewStore(pool))
	if err != nil {
		t.Fatalf("assurance.NewStore() = %v, want nil", err)
	}
	switcher := credentials.NewStore(pool, t.TempDir())

	reg := action.NewRegistry()
	if err := assurance.RegisterActions(reg, store, switcher); err != nil {
		t.Fatalf("RegisterActions() = %v, want nil", err)
	}
	kernel := action.NewKernel(reg, &fakeActionRunStore{})

	callerCtx := principal.WithPrincipal(ctx, principal.Principal{
		ID: "staff:ui-compat-tester", Type: principal.TypeHuman, Environment: environment,
		Issuer: "xingmang://ui-compat-integration-test",
		Scopes: []string{assurance.ScopeManage, assurance.ScopeRun},
	})

	// --- declare@1：请求体与 assuranceProbes.ts declareProbe()/encodeTargets() 逐字段一致 ---
	declareParams := uiDeclareActionParams(t, platform, "UI 兼容集成测试", assurance.TemplateModelFingerprint,
		"api.example.test", []uiProbeTarget{{ChannelID: externalChannelID, ExternalChannelID: externalChannelID, Model: freeTextModel}}, 64)
	declareRes, err := kernel.Execute(callerCtx, action.Request{
		ActionID: assurance.ActionDeclare, ActionVersion: "1", RequestID: "req-declare-1", Params: declareParams,
	})
	if err != nil {
		t.Fatalf("declare@1 执行失败（前后端不兼容？）: %v", err)
	}
	declareBody := asMap(t, declareRes.Value)
	declarationID, _ := declareBody["declaration_id"].(string)
	if declarationID == "" {
		t.Fatalf("declare 结果缺少 declaration_id: %#v", declareBody)
	}

	// --- run@1：请求体与 assuranceProbes.ts runProbe() 一致（不带 client_run_key）---
	runRes, err := kernel.Execute(callerCtx, action.Request{
		ActionID: assurance.ActionRun, ActionVersion: "1", RequestID: "req-run-1",
		Params: map[string]any{"declaration_id": declarationID},
	})
	if err != nil {
		t.Fatalf("run@1 执行失败: %v", err)
	}
	runBody := asMap(t, runRes.Value)
	runID, _ := runBody["run_id"].(string)
	if runID == "" {
		t.Fatalf("run 结果缺少 run_id: %#v", runBody)
	}
	if status, _ := runBody["status"].(string); status != string(assurance.RunStatusPending) {
		t.Fatalf("run 结果 status = %q, want pending（fake 模式应绕过全部闸）: %#v", status, runBody)
	}
	if len(enq.calls) != 1 || enq.calls[0].RunID != runID {
		t.Fatalf("enqueue calls = %+v, want exactly one matching RunID %s", enq.calls, runID)
	}

	// --- 执行一次批次：真实 AssuranceProbeWorker + 真实 Store（fake 探测客户端）---
	worker := NewAssuranceProbeWorker(AssuranceProbeOptions{
		Environment: environment, Store: store, GlobalKillSwitchEnabled: true,
		Now: func() time.Time { return time.Now().UTC() },
	})
	if err := worker.Work(ctx, assuranceProbeTestJob(runID)); err != nil {
		t.Fatalf("Work() = %v, want nil", err)
	}

	// --- Query：assurance.Service 的两个只读方法，httpapi 层直接调用同一批 ---
	svc, err := assurance.NewService(store)
	if err != nil {
		t.Fatalf("assurance.NewService() = %v, want nil", err)
	}
	listResult, err := svc.ProbeList(ctx, platform, environment)
	if err != nil {
		t.Fatalf("ProbeList() = %v, want nil", err)
	}
	if len(listResult.Items) != 1 {
		t.Fatalf("ProbeList items = %d, want 1", len(listResult.Items))
	}
	item := listResult.Items[0]
	if len(item.ChannelIDs) != 1 || item.ChannelIDs[0] != externalChannelID {
		t.Fatalf("ChannelIDs = %v, want [%q]（渠道目录 external_channel_id 必须原样成为 channel_id）", item.ChannelIDs, externalChannelID)
	}
	if len(item.TargetModels) != 1 || item.TargetModels[0] != freeTextModel {
		t.Fatalf("TargetModels = %v, want [%q]（自由文本模型名必须原样回显）", item.TargetModels, freeTextModel)
	}
	if item.LastRunStatus != assurance.ResultStatusOK {
		t.Fatalf("LastRunStatus = %q, want %q（model_fingerprint 模板 + fake 客户端应恒 ok）", item.LastRunStatus, assurance.ResultStatusOK)
	}

	entries, err := svc.ProbeHistory(ctx, platform, environment, nil, 50)
	if err != nil {
		t.Fatalf("ProbeHistory() = %v, want nil", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ProbeHistory entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ChannelID != externalChannelID || entry.ExternalChannelID != externalChannelID {
		t.Fatalf("entry channel ids = (%q, %q), want both %q", entry.ChannelID, entry.ExternalChannelID, externalChannelID)
	}
	if entry.Model != freeTextModel {
		t.Fatalf("entry.Model = %q, want %q（探测结果行必须原样回显自由文本模型名，不规整/不截断）", entry.Model, freeTextModel)
	}
	if entry.Status != assurance.ResultStatusOK {
		t.Fatalf("entry.Status = %q, want %q", entry.Status, assurance.ResultStatusOK)
	}
}

// TestUICompatDeclareRejectsUnknownChannelID 证明 declare@1 的存在性校验
// 认的是渠道目录的 external_channel_id 这同一个 ID 空间：用一个渠道目录里
// 确实没有的 ID（而不是恰好打错的目录里已有的 ID）声明，必须以稳定的
// INVALID_PARAMS 拒绝，而不是静默通过或用另一个错误码掩盖——前端
// AssuranceProbeDeclareDialog 需要能把这个拒绝原因显示给运营，而不是让
// declare@1 表现成一次"随便什么原因"的失败。
func TestUICompatDeclareRejectsUnknownChannelID(t *testing.T) {
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set XM_TEST_DATABASE_URL (CI) or XM_RUN_INTEGRATION=1 with a local DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup（不是 defer）且先于下面 uiCompatCleanup 的 t.Cleanup 注册——
	// t.Cleanup 按后注册先执行（LIFO），这样收尾时会先清表、再关连接池；
	// 反过来（defer pool.Close() 在函数体内先于 t.Cleanup 注册）会导致
	// 收尾清表时连接池已经关闭。
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	const environment = "staging"
	const platform = "sub2api"
	const knownChannelID = "acc-known-1"
	const unknownChannelID = "acc-does-not-exist-9"

	uiCompatCleanup(t, pool, ctx, environment)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		uiCompatCleanup(t, pool, cleanupCtx, environment)
	})
	// 渠道目录确实存在，只是不包含 unknownChannelID——排除"目录整体没采集"
	// 这个已经被另一条测试覆盖的分支（assurance 包 store_integration_test.go
	// 的 TestStoreDeclareRejectsWhenCatalogNeverCollected）。
	seedUICompatChannelCatalog(t, pool, platform, environment, knownChannelID)

	store, err := assurance.NewStore(pool, &recordingProbeEnqueuer{}, ops.NewStore(pool))
	if err != nil {
		t.Fatalf("assurance.NewStore() = %v, want nil", err)
	}
	switcher := credentials.NewStore(pool, t.TempDir())
	reg := action.NewRegistry()
	if err := assurance.RegisterActions(reg, store, switcher); err != nil {
		t.Fatalf("RegisterActions() = %v, want nil", err)
	}
	kernel := action.NewKernel(reg, &fakeActionRunStore{})
	callerCtx := principal.WithPrincipal(ctx, principal.Principal{
		ID: "staff:ui-compat-tester", Type: principal.TypeHuman, Environment: environment,
		Issuer: "xingmang://ui-compat-integration-test",
		Scopes: []string{assurance.ScopeManage},
	})

	declareParams := uiDeclareActionParams(t, platform, "UI 兼容集成测试（未知渠道）", assurance.TemplateModelFingerprint,
		"api.example.test", []uiProbeTarget{{ChannelID: unknownChannelID, ExternalChannelID: unknownChannelID, Model: "gpt-4o"}}, 64)
	_, err = kernel.Execute(callerCtx, action.Request{
		ActionID: assurance.ActionDeclare, ActionVersion: "1", RequestID: "req-declare-unknown-1", Params: declareParams,
	})
	if err == nil {
		t.Fatal("declare@1 对渠道目录里查无此渠道的声明返回了 nil error，want 非 nil")
	}
	// 此前 internal/platform/action/kernel.go 的 Execute() 会把 Handler
	// 自己算出的错误码一律吞成 CodeExecutionFailed（XM-ASSURE1-glue 发现、
	// 已拆成独立切片 XM-KERNEL-ERRCODE0 修复，见该切片的 handoff 与
	// internal/platform/action/kernel_test.go 的
	// TestKernelPreservesHandlerActionErrorCode）——现在 Kernel 会用
	// errors.As 识别出 Handler 返回的就是 *action.Error，原样透传它的
	// Code/Message，因此这里断言的是修复后的正确行为：
	// assurance.domainError 在 Handler 内部算出来的 CodeInvalidParams 能
	// 一路走到调用方，不再变成通用的 CodeExecutionFailed。
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("ErrorCode(err) = %q, want INVALID_PARAMS（XM-KERNEL-ERRCODE0 修复后 Handler 的 Code 应原样透传）; err = %v",
			action.ErrorCode(err), err)
	}
	// Store/Handler 层自己算出的具体原因通过 errors.Is 可达（Unwrap 链条
	// 完整）——证明"存在性校验认的是渠道目录的 external_channel_id 这同一个
	// ID 空间"这条业务逻辑本身是对的，不只是外层 Code 碰巧对了。
	if !errors.Is(err, assurance.ErrInvalidInput) {
		t.Fatalf("errors.Is(err, assurance.ErrInvalidInput) = false，want true（具体拒绝原因应仍在 Unwrap 链里）: %v", err)
	}
	// 修复后 Kernel 把 Handler 的 Message 原样带到最外层（不再是一句通用的
	// "执行失败"），err.Error() 本身就应该直接提到具体的未知渠道 ID，不需要
	// 再 Unwrap 一层才能看到——这一点本身也是 XM-KERNEL-ERRCODE0 修复的一部分，
	// 值得单独断言。
	if !strings.Contains(err.Error(), unknownChannelID) {
		t.Fatalf("err.Error() = %q，want 提及具体的未知渠道 ID %q", err.Error(), unknownChannelID)
	}
}
