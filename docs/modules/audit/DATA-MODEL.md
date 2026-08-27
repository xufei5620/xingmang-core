# 审计数据模型

Schema：`audit`。迁移：`db/migrations/000003_init_audit.up.sql`（forward-only）。

## audit.audit_event

字段集合逐字对齐规格 §4.4。**append-only**，且参与哈希链。

| 列 | 说明 |
|---|---|
| sequence | 单调递增、无缺口；唯一索引保证链无分叉 |
| occurred_at | 业务发生时刻，**进哈希**（固定微秒精度） |
| recorded_at | 库侧写入时刻，**不进哈希**——不同副本可能不同，进链会导致从备份恢复后校验失败 |
| before/after/connector_*_summary | jsonb，脱敏后的摘要 |
| prev_hash / event_hash | 64 位小写十六进制（CHECK 约束） |

索引：`sequence`（唯一）、`event_hash`（唯一，让重放插入失败）、
`action_run_id`、`(principal_id, occurred_at DESC)`、`occurred_at DESC`。

## audit.chain_root

| 列 | 说明 |
|---|---|
| from_sequence / to_sequence | 本次根覆盖的区间 |
| root_hash | 等于 `to_sequence` 那条的 `event_hash` |
| signature | base64(Ed25519(签名载荷)) |
| key_id | 签名密钥标识，支持轮换后追溯 |
| exported_at / export_target | 导出到库外的时间与位置 |

`chain_root` **不加** append-only 规则：需要回写 `exported_at`。

## append-only 的实现与断言方式

```sql
CREATE RULE audit_event_no_delete AS ON DELETE TO audit.audit_event DO INSTEAD NOTHING;
CREATE RULE audit_event_no_update AS ON UPDATE TO audit.audit_event DO INSTEAD NOTHING;
```

PostgreSQL 规则让 DELETE/UPDATE 成为**静默空操作**而非报错。因此断言方式是
「执行后值没变」，不是「语句报错」。部署时还应对应用账号
`REVOKE DELETE, UPDATE ON audit.audit_event`——规则是第二道防线。

⚠️ 那个 `REVOKE` 今天**没有生效**（XM-R012 查证）：当前部署里应用账号
既是超级用户又是表 owner，超级用户绕过一切权限检查。审计表实际靠的是上面
那两条规则，它们与角色无关，超级用户也拦得住；但 `action.action_run` 与
`ops.metric_observation_sample` 没有规则，对它们来说这句承诺目前是空的。
现状证据查询：`deploy/bootstrap/002_grants_evidence.sql`。要让承诺变真需要把
迁移用的 owner 与应用用的受限角色拆开，属于部署拓扑变更，另立任务。

保留期清理**不动审计**：一条都不删。理由见
`internal/platform/jobs/retention.go` 文件头（宪法 11 条 append-only；
删中间任意一条会断链；而且上面的规则会让 DELETE 静默空转）。

## 并发追加

`Append` 在事务内先取 `pg_advisory_xact_lock(4771001)`，串行化「读链尖 →
算哈希 → 插入」。不加锁会产生两条指向同一 `prev_hash` 的事件（链分叉）。
有 8 并发的测试验证串行化有效。
