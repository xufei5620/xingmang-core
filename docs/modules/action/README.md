# Action 模块

所有业务与平台配置写操作的唯一入口（ADR-003）。本模块是 Foundation-A 级的
**Action Core Lite**：只提供 L0/L1 快速路径。

## 执行顺序

```text
查注册表 → Principal 存在 → Principal 类型允许 → 风险等级 Foundation 边界
→ Environment 匹配 → 权限 → 参数 Schema → Handler
```

除「未注册」与「无身份」外，每一步失败都会写入 ActionRun。这两种例外是因为
此时无法确定 `risk_level` / `principal_id` 等非空字段，写入会污染审计口径。

## Foundation-A 的边界

L2/L3/L4 的 Action **可以注册，但执行时一律被拒绝**（`ADVANCED_CONTROLS_REQUIRED`），
Handler 不会被调用。这是 ADR-003 的刻意设计：先让高风险动作在注册表里可见、
可审计、可被前端灰显，而不是等 Foundation-B 才存在。

XM-0030（Action Advanced Controls）补齐后，这些 Action 才会真正可执行：
幂等键、写后读取确认、人工审批、Step-up MFA、冷却期、Kill Switch、补偿。

## 不做

- 不认识任何具体业务：业务通过 `Register` 反向注册，本包不 import 任何业务包；
- 不做身份认证（那是 Principal 来源方 XM-0008 的职责），只做**授权判定**；
- 不提供 HTTP 层。

## 接入方式

```go
reg := action.NewRegistry()
_ = registry.RegisterActions(reg, registryStore)
kernel := action.NewKernel(reg, action.NewPgRunStore(pool, logger),
    action.WithAuditSink(audit.NewActionSink(audit.NewStore(pool))),
    action.WithLogger(logger))

res, err := kernel.Execute(ctx, action.Request{
    ActionID: "registry.service.create", ActionVersion: "1",
    RequestID: requestID, Params: params,
})
```

`ctx` 必须携带 Principal（`principal.WithPrincipal`），否则一律拒绝。

## 审计接线

每次执行——**包括被拒绝的尝试**——都会往哈希链写一条审计事件（规格 §4.4）。
被拒的尝试同样进链：只审计成功的动作，等于把「谁在试探权限边界」整条线索丢掉。

内核不 import `audit` 包。它只声明 `action.AuditSink` 接口，适配器
`audit.NewActionSink` 住在 audit 包里——「怎么变成一条审计事件」属于审计设施
自己的知识，两个核心包因此保持互不依赖。

### Handler 贡献业务信息

`resource_type` / `resource_id` / 前后摘要只有 Handler 知道，内核无从得知。
Handler 通过 ctx 可选地贡献：

```go
func createService(ctx context.Context, p map[string]any) (any, error) {
    action.RecordResource(ctx, "core.service", id)
    action.RecordBefore(ctx, map[string]any{"exists": false})
    action.RecordAfter(ctx, map[string]any{"exists": true})
    return result, nil
}
```

不调用也能正常工作（事件里这些字段为空），Handler 因此可以脱离 Action 上下文
单独测试。**摘要的脱敏由调用方负责**——`Record*` 不判断什么是敏感的，宪法 7 条
的责任落在写 Handler 的人身上。

摘要**不是行的副本**：只挑「改了会影响运营判断」的字段。全量复制会让审计链变成
一个影子数据库，还会在字段变敏感时（比如将来加了内部凭据路径）悄悄把它带进链里。
CredentialRef 是个例外——它是引用不是凭据（ADR-014），「这条连接绑了哪个凭据」
正是事故复盘要问的第一个问题，必须进链。

### 验证 Handler 记了什么

```go
value, contrib, err := action.CaptureAudit(ctx, handler, params)
// contrib.ResourceType / ResourceID / Before / After
```

走内核当然更真实，但内核会先拦掉 L2 以上的动作（Foundation-A 没有 Advanced
Controls），而恰恰是 Kill Switch 这类高风险动作最需要验证审计内容。`CaptureAudit`
**不做**任何权限、环境、风险等级判定——它是测试接缝，不是执行通道（宪法 2 条）。

### 已知缺口：审计写与业务写不在同一事务

`Handler` 自己管理事务，内核拿不到它；审计写发生在 Handler 返回**之后**，
是一次独立的数据库写入。因此存在一个窗口：业务变更已提交，审计写失败。

这种情况下内核**照常返回成功**，不把动作报成失败——业务写已经生效，回滚不了，
报失败只会让调用方重试从而制造重复变更。缺口以 `error` 级日志
（`error_code=audit_write_failed`）记录，运维按事故处理，见
`docs/modules/audit/RUNBOOK.md`。

补法留给 Foundation-B：事务型 outbox——Handler 的事务里插一条 outbox 记录，
由后台任务搬进审计链，业务写与审计意图从此原子。Foundation-A 不做，
因为它要求 Handler 交出事务控制权，是一次跨所有 Action 的接口变更。

## 相关

- ADR-003（Action 唯一写入口）、规格 §4.1/§4.4/§18.4/§18.5/§19.5
- 契约：`contracts/actions/*.json`
- 后续：XM-0030 Action Advanced Controls；Foundation-B 事务型 outbox（见上）
