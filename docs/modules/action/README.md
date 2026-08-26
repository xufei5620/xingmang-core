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
kernel := action.NewKernel(reg, action.NewPgRunStore(pool, logger))

res, err := kernel.Execute(ctx, action.Request{
    ActionID: "registry.service.create", ActionVersion: "1",
    RequestID: requestID, Params: params,
})
```

`ctx` 必须携带 Principal（`principal.WithPrincipal`），否则一律拒绝。

## 相关

- ADR-003（Action 唯一写入口）、规格 §4.1/§4.4/§18.4/§18.5/§19.5
- 契约：`contracts/actions/*.json`
- 后续：XM-0030 Action Advanced Controls；XM-0011 基础审计外部锚点
