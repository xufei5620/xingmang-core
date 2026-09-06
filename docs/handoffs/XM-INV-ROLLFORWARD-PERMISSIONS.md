# XM-INV-ROLLFORWARD-PERMISSIONS —— roll-forward 迁移后自动重放运行时角色授权

- status: ready-for-review（未合入发布线；下一版 RC 必须带上）
- branch: `ai/claude/XM-INV-ROLLFORWARD-PERMISSIONS`
- base: `558fe5f`（`ai/claude/XM-INV-AUTOLOGIN`，即 RC101 = `f2f67e1` 之后的发布线）
- commit: 见分支尖端

## summary

RC101 上线（2026-09-06 11:29Z）后 15 分钟内，**用户提交开票全部失败**（SQLSTATE
42501）。原因链：

1. 迁移 0027/0028 新建了 `invoice_notice_outbox`、`notice_webhook_setting`。
2. 生产库 `pg_default_acl` 为空——`deploy/postgres/010-invoice-roles.sh` 里的
   `ALTER DEFAULT PRIVILEGES` 从未在这个库上生效过；`invoice_app` 对每张表的
   权限**只**来自 `deploy/postgres/harden-runtime-role.sql`（先
   `GRANT ... ON ALL TABLES` 再逐表收窄），它通过 compose 的 `permissions`
   一次性作业（`tools` profile）执行。
3. `deploy/roll-forward.sh` 跑 `migrate` 但从不跑 `permissions`——runbook 第
   998 行一直明说这一点，要求每次"迁移新建表"后手动重放；RC101 的发版材料没把
   这条列进去。
4. `EnqueueInvoiceNoticeTx`（`store.go:531`）故意放在申请创建事务里（通知行存在
   当且仅当申请提交成功），于是 outbox 没权限 = 提交事务整体失败。

生产修复（11:44Z）：在 RC101 发布目录执行
`docker compose --env-file … -f deploy/docker-compose.prod.yml run --rm --pull never permissions`
（退出 0；12 REVOKE / 12 GRANT / 4 ALTER ROLE），worker 报错即刻归零。
过程记录：`/root/invoice-system/incoming/rc101/`。

本片让这类事故不再依赖人记得 runbook 第 998 行：

- `deploy/roll-forward.sh`：`[0/6] migrate` 之后新增 `[0b/6]`，无条件
  `run --rm --pull never permissions`（单事务、幂等），失败即退出 1，不进入
  容器切换。
- `deploy/postgres/harden-runtime-role.sql`：`invoice_notice_outbox` 加入
  "REVOKE DELETE, TRUNCATE" 组（与 `email_outbox` 同款：worker 只 UPDATE，
  提交只 INSERT，从不删）；`notice_webhook_setting` 保留默认
  SELECT/INSERT/UPDATE/DELETE（`ClearNoticeWebhook` 会删单例行）并写明理由。
  **当前生产上 outbox 仍有 DELETE**（本次手动重放用的是 RC101 树里的旧策略
  文件）；下一版 roll-forward 的 `[0b/6]` 会把它收掉。
- `docs/PRODUCTION-RUNBOOK.md`：第 998 段改写为新事实，并把 RC101 这次事故
  写进去当例子。

## files_changed

- `deploy/roll-forward.sh`
- `deploy/postgres/harden-runtime-role.sql`
- `docs/PRODUCTION-RUNBOOK.md`
- `docs/handoffs/XM-INV-ROLLFORWARD-PERMISSIONS.md`（本文）

## tests_run

- `bash -n deploy/roll-forward.sh`
- `scripts/verify-postgres.ps1` 会在隔离容器里对迁移后的库执行
  `harden-runtime-role.sql` 并以 `invoice_app` 做写入断言（第 157–166 行），
  新增的 `REVOKE ... invoice_notice_outbox` 作用于 0027 建好的表——见 not_run。

## not_run

- `scripts/verify.ps1` 全量源门禁（含 verify-postgres.ps1）：等本片合入发布线、
  与下一版一起跑；单独跑一次 `verify-postgres.ps1` 前先确认共享测试库空闲。
- roll-forward 新步骤没有在真实主机上演练过（它就是 11:44Z 那条手动命令的
  原样脚本化，且 `permissions` 作业 RC101 已经在生产上跑过一次退出 0）。

## risks

- `permissions` 作业需要 `/run/secrets/invoice_owner_database_url` 以 UID
  10001 可读（runbook 第 934 行既有约束）；若某次发布把该密文权限改坏，
  `[0b/6]` 会先于容器切换失败——这是想要的行为（fail closed），但意味着
  roll-forward 多了一个前置依赖。
- `harden-runtime-role.sql` 是 `deployment` 上下文指纹的一部分：改它必然是
  新 RC，不存在"只热修生产文件"的路径。

## follow_ups

- 发版材料模板（平台仓库 `docs/handoffs/RELEASE-*.md`）加一条固定检查：
  "本次迁移是否 CREATE TABLE → 若是，写明 `permissions` 重放"。本片合入后这条
  由脚本兜底，但材料里仍应写明，方便审读。
- `010-invoice-roles.sh` 的默认权限为何在生产上不存在（可能是库早于该脚本
  初始化）——值得查一次，但**不要**为了"修"它去生产上加默认权限：逐表最小化
  的现状比 blanket 默认更严。
- 盯守脚本（`rc101-watch.sh`）的 `error_lines` 只认 `level=error`，api 的实际
  格式是 `2026/09/06 11:32:32 ERROR …`——这次是靠额外加的 `notice_error_lines`
  才抓到。下次盯守脚本的通用错误计数要同时匹配 ` ERROR ` 前缀。
