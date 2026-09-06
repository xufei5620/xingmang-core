# XM-INV-CREDIT-EVENTS-DELETE-GRANT —— 授权策略收错了一项，投影的软失败路径被废掉

- status: ready-for-review（**下一版 RC 必带**；不带的话每次部署都会重新踩）
- branch: `ai/claude/XM-INV-CREDIT-EVENTS-DELETE-GRANT`
- base: `a2c53f0`（RC102 = `v0.1.0-rc102-signed`，已在生产）

## summary

2026-09-06 RC102 的 roll-forward 在最后一关失败：`readyz not 200 within 600s`。
部署本身是完整的（18 个容器在 rc102、迁移 0030 已应用），失败的是就绪探针。

根因不在 RC102，而在**同一天早些时候我为修另一个事故做的授权重放**：

1. 11:44Z：为修「迁移新建表没授权」（XM-INV-ROLLFORWARD-PERMISSIONS），我在生产
   重放了 `deploy/postgres/harden-runtime-role.sql`。
2. 那份策略把 `source_credit_events` 的 `UPDATE, DELETE, TRUNCATE` **一起**收掉。
3. `evaluatePendingBalanceEvidenceTx`（`consumption.go`，XM-INV-BLIP-SOFTFAIL）
   在确认一笔试探性信用其实不是简单的余额抖动时，要 `DELETE` 掉自己刚写下的那行
   `UNKNOWN_POSITIVE`。那篇交接文档写得很清楚：**返回错误会让该账号的投影任务在
   每次重试时失败、永远失败**——软失败路径就是为此而写。
4. 17:42Z 账号 `40bd883d` 走到这条路径，`permission denied for table
   source_credit_events (SQLSTATE 42501)`；18:16Z 任务判死。
5. `eligibilityProjectionReady` 见 `Dead > 0` 即返回未就绪 → readyz 503，持续
   约一个半小时（19:46Z 补权限后恢复）。**用户侧全程 200**，受影响的只有就绪
   探针与依赖它的部署判定。

## 为什么补 DELETE 不放大风险

真正的管控在库里的触发器，不在表级权限：

```
source_credit_events_immutable  BEFORE DELETE OR UPDATE ... EXECUTE enforce_source_credit_event_mutation()
```

```sql
IF TG_OP='DELETE' AND OLD.credit_kind='UNKNOWN_POSITIVE'
   AND COALESCE(current_setting('invoice.balance_blip_repair_delete', true),'')='on' THEN
    RETURN OLD;
END IF;
RAISE EXCEPTION 'source eligibility facts are immutable';
```

只有那一种行、且只有带上专用会话标志时才删得掉，其余一律拒绝。表级 DELETE 比
它宽得多，收掉并不增加安全性，只能让设计好的路径失效。

`UPDATE` 与 `TRUNCATE` 仍然收掉——事实本身不可改，而且 `cmd/api/runtime.go` 的
启动自检**明确要求**这张表的 `UPDATE` 为 false（它没有、也不该有对 `DELETE` 的
要求）。`scripts/verify-postgres.ps1` 钉住的权限向量同样只覆盖 `UPDATE`，本片
不影响它。

## 改动

`deploy/postgres/harden-runtime-role.sql`：把 `source_credit_events` 从
「REVOKE UPDATE, DELETE, TRUNCATE」那一组里拆出来，单独写成

```sql
REVOKE UPDATE, TRUNCATE ON TABLE source_credit_events FROM invoice_app;
GRANT SELECT, INSERT, DELETE ON TABLE source_credit_events TO invoice_app;
```

并把上面这段理由写在注释里——否则下一个人看到「事实表居然能删」会顺手收回去。

## files_changed

- `deploy/postgres/harden-runtime-role.sql`
- `docs/handoffs/XM-INV-CREDIT-EVENTS-DELETE-GRANT.md`（本文）

## tests_run

- `scripts/verify-postgres.ps1`：在隔离容器里对迁移后的库执行本策略，并以
  `invoice_app` 身份跑一组允许/拒绝断言，含那条钉死的权限向量。

## not_run

- 未跑 `verify.ps1` 全量（本片只改一份 SQL 策略，PostgreSQL 门禁是它的直接覆盖面）；
  合入发布线后随下一版 RC 一起跑。

## risks

- **生产当前是手工补的权限**（`GRANT DELETE ON source_credit_events TO invoice_app`，
  2026-09-06 19:46Z 由脚本 `incoming/rc102/fix-credit-events-delete.sh` 执行）。
  在本片上线之前，**任何一次 roll-forward 的 `[0b/6]` 都会再次收掉它**，readyz 会
  再次转 503。所以：本片之前不要重跑 roll-forward；真要跑，跑完立刻重新补权限。
- 反过来说，这也正是 `[0b/6]` 的价值：策略里任何一处过时都会立刻兑现，而不是
  藏到某个账号恰好走到那条路径的那天。代价是策略文件必须一直是对的。

## follow_ups

- **这份策略里还有多少条与代码不符？** 它是 RC39 时代写的，之后代码涨了很多。
  本次是被动发现的第二处（第一处是新建表根本没授权）。值得做一次系统性核对：
  把策略里每一条 REVOKE 与代码里对该表的实际写操作对照一遍，而不是等下一次
  投影在生产上判死。已登记，未开工。
- 那条判死的任务重排后进入 `BALANCE_PROOF_PENDING`（正常等待态，非错误）。
  账号 `40bd883d` 的可开票额需要在下一轮证据到齐后复核。
