# XM-CPA-SNAPSHOT · CPA 宿主一致性快照与巡检真实时间

## status

IN_PROGRESS — 本地实现与定向 TDD 已完成；完整门禁、push、生产部署和真实
CPA 验收完成后更新为 READY/DEPLOYED。

## branch / base

- branch: `release/v0.1-launch`（验收线，用户明确要求把 4 个日志提交与修复一起推送）
- base/server: `4c1e7bd669d262f39805aebe3c681806b83648f4`
- pre-change head: `1a448f66045aac3815ff3e141362a02a953dfbfd`

## scope

1. `AccountHealth()` 改读真实 `codex_inspection_runs.id` 与 `started_at_ms`，整数
   关联 results，RunAt 进入 observation/API 类型和两处页面。
2. 新增 Docker-built、宿主运行的 `cpa-snapshot`，用 ncruces online backup、
   metadata、DELETE journal、quick_check、previous hardlink、fsync、atomic rename 发布独立快照。
3. 新增 hardened systemd oneshot/timer；deploy-local 在 production file 模式
   先生成/验证首代再启动 API/worker。
4. server-prod 删除 live cpam-data bind，只把 published 目录长语法只读挂给
   两个消费者，禁止 Docker 自动创建空源目录。
5. worker 对同一轮所有成功结果校验 generation；混代时四项全失败并保留旧值。

## security boundaries

- 不修改 CPA 原库，不执行 checkpoint，不读/挂 `auths` 或 `config.yaml`。
- API/worker 永不接触活动 WAL；不使用 `immutable=1`。
- 服务器不安装 Go/Node/pnpm/gitleaks/sqlite 开发工具；binary 来自 commit-bound
  migrate 镜像。
- 推送以本机 gitleaks 和每项门禁独立 exit 0 为闸。

## files_changed

完成时以 `git diff --name-status 4c1e7bd..HEAD` 的实际清单为准。

## tests_run

- snapshot producer RED/GREEN：live WAL 事务、standalone、quick_check、无 sidecar、
  last-good retention、metadata collision、必需 schema、Step/Close 错误传播。
- connector RED/GREEN：真实整数 schema、started_at 排序、RunAt、snapshot metadata。
- worker RED/GREEN：mixed generation 全轮 fail closed。
- web RED/GREEN：概览与渠道保障显示 run id + UTC time。
- security runtime wiring 与 deploy-local contract：PASS。

## not_run

完整门禁、gitleaks、签名提交、push、生产 installer/systemd/Compose/CPA 页面真实
验证尚未执行；未完成前不得把本片状态写成 READY 或 CPA available。

## risks / rollback

- lifecycle 进程在宿主命名空间需要 SQLite SHM 协调权限；源 DB 自身仍由
  `mode=ro + query_only` 锁死。systemd sandbox 需在 fiberstate 实测。
- 原子持久性保证面是 Linux 同文件系统；Windows 只承担编译与逻辑测试。
- 紧急降级只切 `XM_CPA_MODE=off` 并停 timer；禁止恢复 live WAL bind。

## follow_ups

- 业务日到底按 UTC 还是 Asia/Shanghai、model_prices 是否全为 USD，仍需上游
  正式口径确认，不在本片猜测。
