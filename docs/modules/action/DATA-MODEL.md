# Action 数据模型

Schema：`action`。迁移：`db/migrations/000002_init_action.up.sql`（forward-only）。

## action.action_run

一次 Action 执行的记录。**append-only**（规格 §4.4）。

| 列 | 约束 |
|---|---|
| action_id | `^[a-z0-9]+(\.[a-z0-9_]+){2,3}$` |
| principal_type | HUMAN / SERVICE / AI / SERVER_AGENT |
| environment | FK → core.environment |
| risk_level | L0~L4 |
| status | succeeded / failed |
| error_code | 失败必非空、成功必为空（`(status='failed') = (error_code<>'')`） |
| duration_ms | ≥ 0 |

索引：`(action_id, started_at DESC)`、`(principal_id, started_at DESC)`、`(request_id)`。

## append-only 的实现与断言方式

```sql
CREATE RULE action_run_no_delete AS ON DELETE TO action.action_run DO INSTEAD NOTHING;
CREATE RULE action_run_no_update AS ON UPDATE TO action.action_run DO INSTEAD NOTHING;
```

PostgreSQL 规则让 DELETE/UPDATE 成为**静默空操作**而不是报错（实测返回
`DELETE 0` / `UPDATE 0`）。因此代码里不要用「语句报错」判断 append-only 是否
生效，要用「执行后值没变」来断言——集成测试即如此写。

部署时还应对应用账号 `REVOKE DELETE, UPDATE ON action.action_run`；规则是第二道防线。

## Foundation-A 未落库的字段

规格 §4.4 的完整审计字段中，以下尚未落库，在 XM-0011/XM-0030 补齐：
`approval_id`、`trace_id`、`source_ip`、`before_summary`、`after_summary`、
`connector_request_summary`、`connector_response_summary`、`compensation_result`。

## 关于 gen/ 中的重复 model

`action.action_run` 通过 FK 引用 `core.environment`，因此两个 sqlc 配置项共享
同一个 `db/migrations` schema 目录。sqlc 会为 schema 中**所有**表生成 model，
于是 `registry/gen/models.go` 里会出现未被使用的 `ActionActionRun`，
`action/gen/models.go` 里也会出现 `Core*` 系列。这是 sqlc 的正常行为，
生成结果确定可复现，CI 的一致性检查照常通过。
