# Runway 阈值切换到数据库快照

> 本 runbook 是本地/预发布交接材料。它不授权生产执行，也不替代迁移、DB
> 角色拆分、Foundation-B 或发布审批。

## 目的

`finance.runway_threshold_config` 是 API 看板与 worker R5 评估共同读取的
环境级快照；`finance.runway_threshold_history` 保存每次 bootstrap/Action
revision。两者通过 `finance.runway_threshold_current_verified` 只读 JOIN view
提供成对校验的当前行：worker 不需直接读取 history，若 current/history 不匹配则
view 返回零行并 fail closed。环境变量只作为一次性 bootstrap 输入，不能在运行时
偷偷覆盖数据库。

## 切换前置门

1. 核对当前目标分支包含迁移 `000017`，并确认迁移 diff、SQLC 产物和 DB 角色
   拆分都有独立批准记录。
2. 确认 API/worker/lifecycle 使用同一版本化镜像，记录镜像 digest、compose
   文件 checksum、当前 WARN/CRIT 环境值和数据库备份位置。
3. 仅对 disposable 或明确获批的环境执行；连接前通过 `pgdsn.Validate` 与
   loopback/网络边界检查。绝不把 DSN、密码或 CredentialRef 写入日志。

## 执行

```powershell
docker compose -p xingmang-launch -f deploy/compose/launch.yaml `
  --env-file deploy/compose/.env --profile tools `
  run --rm runway-threshold-bootstrap version

docker compose -p xingmang-launch -f deploy/compose/launch.yaml `
  --env-file deploy/compose/.env --profile tools `
  run --rm runway-threshold-bootstrap up
```

命令输出只包含 `environment`、`revision` 和三档整数。首次运行应得到
`revision=1`，并且 current/history 各有一行；重复运行相同值必须幂等。若已有
不同值，命令失败并要求人工核对，不能覆盖。

本地部署脚本必须按 `postgres+migrate → threshold bootstrap → 演示 bootstrap →
API/worker/web` 顺序运行；不要先启动 API/worker 再补阈值。随后查询：

```text
GET /api/v1/finance/runway-thresholds
GET /api/v1/finance/runway-thresholds/history?limit=50
```

要求 API 响应的三档和 revision 与 bootstrap 输出完全一致。启动一份 worker
后，等待一轮评估，核对 R5 detail 中的：

```text
threshold_revision=<revision>; critical_days=<n>; warning_days=<n>; serious_days=<n>
```

若 current 缺行或数据库不可用，API 返回稳定的 503
`RUNWAY_CONFIG_UNAVAILABLE`；worker 整轮 fail closed，不把既有告警恢复成空。

## 回滚演练

回滚是一次原子运维决策，不是只换镜像：

1. 停止升级中的 API/worker，恢复上一版已签名 compose 与镜像 digest。
2. 恢复记录的 `XM_FINANCE_RUNWAY_WARN_DAYS` / `XM_FINANCE_RUNWAY_CRIT_DAYS`
   到 gitignored env 文件，并确认旧 API 与旧 worker 使用同一组值。
3. 先起一份旧 API，确认 summary 回显与记录值一致；再起一份旧 worker，等一轮
   评估并确认其 R5 阈值一致。
4. 证据完整后再滚动其余副本。保留 current/history 表和历史行；不要执行生产
   down migration 或删除 history。

## 当前本地状态

本地分支已经包含迁移、Store、Query、预览与规则 UI，但尚未对 staging/production
应用迁移或执行 bootstrap；C3c 写 Action 仍由 Foundation-B 门禁保护。

`scripts/test-runway-threshold-db.ps1` 当前是**外部 disposable DSN 的安全脚手架**：
它要求 admin/test 两个 loopback DSN 和 `XM_RUNWAY_TEST_ISOLATED=1`，并在检测到
测试被 skip 时失败。它不会自动创建或清理 PostgreSQL，也不证明 DB 角色 ACL、
迁移回滚或触发器 SQLSTATE；`tests/security/runway-*.test.sh` 只做静态 wiring
检查。完整随机数据库/角色生命周期仍是后续获批切片，不能把脚手架输出当成真实
数据库门禁通过。
