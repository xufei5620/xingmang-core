# Runbook：Action

## 查某个 Action 的最近执行

```sql
SELECT started_at, principal_id, status, error_code, duration_ms, request_id
FROM action.action_run
WHERE action_id = 'registry.service.create'
ORDER BY started_at DESC
LIMIT 20;
```

## 按 request_id 追一次调用

```sql
SELECT * FROM action.action_run WHERE request_id = '<req-id>';
```

## 新增一个 Action

1. 在业务模块的 `actions.go` 写 `Definition`（ID 形如 `<域>.<资源>.<动作>`）与 Handler；
2. 在 `RegisterActions` 中登记；
3. 在 `contracts/actions/<id>.v<n>.json` 落契约，字段与代码逐一对应；
4. 补测试：至少覆盖「无权限被拒」「参数非法被拒」「happy path 落库」；
5. 若风险等级 ≥ L2，在 Foundation-B 之前它会被内核拒绝——这是预期行为，
   测试要断言 `ADVANCED_CONTROLS_REQUIRED` 而不是绕过它。

## 常见故障

| 现象 | 处置 |
|---|---|
| `ACTION_NOT_REGISTERED` | ID 或版本写错；或业务模块的 `RegisterActions` 没被调用 |
| `PERMISSION_DENIED` 且 ctx 有身份 | Principal.Scopes 里没有 Definition 声明的权限 |
| `PERMISSION_DENIED` 且 ctx 无身份 | 调用方未 `principal.WithPrincipal`；此情形不写 ActionRun |
| `ENVIRONMENT_MISMATCH` | Principal.Environment 不在 Definition.Environments 内 |
| `ADVANCED_CONTROLS_REQUIRED` | 该 Action 是 L2+，需等 XM-0030；不要靠降级风险等级绕过 |
| `INVALID_PARAMS` 且参数看起来对 | Schema 是白名单：多传一个未声明字段就会被拒 |
| ActionRun 写入失败 | 日志中 `error_code=audit_write_failed`；审计缺口，按事故处理 |
