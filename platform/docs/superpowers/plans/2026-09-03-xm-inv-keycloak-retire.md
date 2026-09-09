# XM-INV-KEYCLOAK-RETIRE 实施计划：拆除 Keycloak

> **For agentic workers：** 本计划的多数步骤是生产运维操作（拆容器、宿主 nginx 摘 vhost、走 RC 发布仪式），需要人工审批与生产凭据，不可全自动执行。只有任务 1／2／4／5／6（发布门禁脚本、compose/roll-forward、nginx 模板注释、文档、release-image-gate 参数）是纯代码/文档改动，适合用 superpowers:executing-plans 在独立分支实现并走常规门禁；任务 3／7／8／9 涉及生产凭据与容器操作，必须由验收线在获得产品负责人批准后逐条人工执行，代理不得自行对生产发起这些调用。

**Goal：** 在 CR-0006 第一、二阶段（断言模式已在生产唯一承担开票管理员登录、OIDC 已关闭且观察窗口达标）之上，把 Keycloak 从"已关闭但仍部署"推进到"容器拆除、发布门禁改八镜像、文档更新为稳态"，完成 CR-0006 整体交付。

**Architecture：** 本计划只涉及开票仓库（`invoice-system`）。控制台侧（xingmang-platform）在 XM-AUTH-TOTP0/XM-INVCON1 已交付，不在本计划范围。除发布门禁脚本与 compose/roll-forward 的必要改动外，不新增业务代码——本片是编排、门禁与文档收尾。

**Tech Stack：** Docker Compose、PowerShell（`release-image-gate*.ps1`）、bash（`roll-forward.sh`/`backup.sh`/`restore-drill.sh`）、Nginx。

**Spec：** `docs/superpowers/specs/2026-09-03-xm-inv-keycloak-retire-design.md`；`docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`；`docs/roadmap/CR-0006-console-auth-slices.md`；`docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md`。

## 前提门禁（全部未满足前不得开工，2026-09-03 状态：全部未满足）

- [ ] phase2-rollout 步骤 6：产品负责人已在 `docs/handoffs/ACCEPTANCE-LOG.md` 明确点头授权本片立项开工（不因前三条满足就自动开始）。
- [ ] phase2-rollout 步骤 5：开票侧 `OIDC_ADMIN_LOGIN_ENABLED=false` 已连续生效 ≥3 天，期间 `audit_events` 无 `actor_type='oidc'` 的新增管理员登录行。
- [ ] 同期一次"无 Keycloak"恢复演练 PASS：`BACKUP_LOCAL_KEYCLOAK=false bash deploy/backup/backup.sh` 产出的备份，`deploy/backup/restore-drill.sh` 不传 `KEYCLOAK_BACKUP` 确认 `PASS`。
- [ ] 观察窗口内两侧断言审计计数吻合，无非预期的 `ASSERTION_INVALID`/`ADMIN_STEP_UP_REQUIRED`/`ADMIN_NETWORK_DENIED` 堆积。

## 待产品负责人裁定的事项（不阻塞本文档定稿，阻塞执行）

1. Keycloak 数据保留期限（设计文档第 5 节建议 ≥90 天）。
2. `CONSOLE_ASSERTION_ENABLED` 默认值是否随本片一起改 `:-true`（`OIDC_ADMIN_LOGIN_ENABLED` 改 `:-false` 已在任务 5 内确定，无需裁定）。
3. 何时另立变更单物理删除 `keycloak_postgres_data` 卷、加密备份与 `deploy/keycloak/` 一次性 provisioning 脚本（本片只停用不删除）。
4. `scripts/verify-nginx-configs.ps1` 若断言两个 Keycloak nginx 模板必须存在，是否批准将其改为可选（任务 4 的一个分支，取决于核实结果）。

## 任务

### 任务 1：发布门禁新增 `IdPMode=console-assertion`
- [ ] `scripts/release-image-gate-lib.ps1`：`Get-IdpGateDecision`（837-879 行）`ValidateSet` 新增 `'console-assertion'`，新增 `switch` 分支，返回形状参照 `'external-managed'`（`ProductionCanary='pending'`，新 `BlockReason` 如 `console_assertion_pending_canary`，直到生产断言金丝雀证据补齐才应放行——由更晚一次变更把这个分支的放行条件坐实，本片先建好骨架）；`Get-CommonReleaseImageTag`（881-914 行）`ValidateSet` 同步新增该取值（函数体本身已对非 `'keycloak'` 值只产出 8 项，无需再改）。
- [ ] `scripts/release-image-gate.ps1`：`$IdPMode` 默认值（13 行）从 `'keycloak'` 改 `'console-assertion'`；六处 `-ceq 'keycloak'` 分支（256-258,343,467-490,575-579,631,635 行）不动（保留 `keycloak` 全部原行为供回滚），只需确认新默认值下这六处均正确跳过。
- [ ] `scripts/test-release-image-gate.ps1`：新增 `console-assertion` 模式离线夹具用例——镜像数 8、不含 `invoice-keycloak`、`Get-IdpGateDecision` 新分支的三个返回字段。
- 文件：`scripts/release-image-gate-lib.ps1`、`scripts/release-image-gate.ps1`、`scripts/test-release-image-gate.ps1`。
- 测试：`pwsh -NoProfile -File scripts/test-release-image-gate.ps1` 全绿（含新增用例）。

### 任务 2：compose/roll-forward 摘除 Keycloak 项目
- [ ] `deploy/roll-forward.sh`：删去 `[1/6] keycloak project` 步骤（74 行）并重新编号后续步骤；镜像存在性循环（40-42 行）去掉 `invoice-keycloak`；容器数断言（88-89 行）18 改 16；10 行 "nine images" 注释同步改为 8。
- [ ] `deploy/docker-compose.idp.yml`、`deploy/docker-compose.idp.bootstrap.yml`：不改动、不删除（供第 6 节回滚）。
- 文件：`deploy/roll-forward.sh`。
- 测试：`bash -n deploy/roll-forward.sh` 语法检查；参照 `XM-INV-CONSOLE-ASSERT-DEPLOY` 交接文档的先例，把改动后的镜像循环与容器计数两段逻辑隔离抽取到 scratchpad 单独验证（本环境无法接触生产 18/16 容器的真实计数）。

### 任务 3：备份/恢复默认不含 Keycloak（运维执行，非代码改动）
- [ ] 执行前最后一次 `BACKUP_LOCAL_KEYCLOAK=true bash deploy/backup/backup.sh`，归档该次 `*.keycloak.dump.age` 路径与 sha256 到变更单（数据保留基准，见设计文档第 5 节）。
- [ ] 此后运维默认不传 `BACKUP_LOCAL_KEYCLOAK`/`KEYCLOAK_BACKUP`（脚本本身不改代码，两开关本来就可选）。
- 文件：无代码改动。
- 测试：直接复用前提门禁里"无 Keycloak 恢复演练 PASS"的既有证据；若执行本任务时距那次演练已久，重新跑一次 `bash deploy/backup/restore-drill.sh`（不传 `KEYCLOAK_BACKUP`）确认仍 `PASS`。

### 任务 4：nginx 摘除两个域名
- [ ] 宿主宝塔面板摘除 `auth.solov.cc`/`auth-admin.solov.cc` 两个 vhost（运维操作，不在本仓库 diff 内）。
- [ ] 仓库内 `deploy/nginx/auth.solov.cc.conf.template`、`auth-admin.solov.cc.conf.template`、`keycloak-proxy-headers.conf`、`auth-admin.solov.cc.allow.conf.example` 顶部各加一行注释："Keycloak 已退役，此模板不再部署，仅供回滚参考"；`deploy/validate-keycloak-admin-allowlist.sh` 不改。
- [ ] 核实 `scripts/verify-nginx-configs.ps1` 是否断言这两个模板必须存在；若是，按裁定事项 4 的结果决定是否改为可选。
- 文件：`deploy/nginx/auth.solov.cc.conf.template`、`deploy/nginx/auth-admin.solov.cc.conf.template`、`deploy/nginx/keycloak-proxy-headers.conf`、`deploy/nginx/auth-admin.solov.cc.allow.conf.example`，视核实结果可能含 `scripts/verify-nginx-configs.ps1`。
- 测试：宿主摘除 vhost 后运维执行 `nginx -t`（不在本地门禁范围）；`scripts/verify-nginx-configs.ps1` 本地跑一遍确认未因新注释行或模板缺失而误报。

### 任务 5：compose env 默认值收紧
- [ ] `deploy/docker-compose.prod.yml:244`：`${OIDC_ADMIN_LOGIN_ENABLED:-true}` 改 `${OIDC_ADMIN_LOGIN_ENABLED:-false}`。
- [ ] 按裁定事项 2 的结果，视情况把 245 行 `${CONSOLE_ASSERTION_ENABLED:-false}` 改 `${CONSOLE_ASSERTION_ENABLED:-true}`。
- 文件：`deploy/docker-compose.prod.yml`。
- 测试：`docker compose --env-file <throwaway 副本> -f deploy/docker-compose.prod.yml config` 渲染确认默认值生效（同 `XM-INV-CONSOLE-ASSERT-DEPLOY` 已验证过的方法）；确认 `scripts/verify.ps1` 的 `$productionEnv` 夹具显式设置了这两个变量、不受默认值改动影响。

### 任务 6：文档更新为稳态
- [ ] `docs/PRODUCTION-RUNBOOK.md`：§5"Central Keycloak reference deployment"（952-1314 行）整节标题前加"（历史，Keycloak 已退役）"并保留内容；§4.1（886-951 行）改写为稳态描述（不再是"OIDC 保持 true 的过渡期"表述）；§11 备份小节与 §12 回滚（2164-2202 行）在 2201 行后补一条"Keycloak 容器拆除后的回滚"指向设计文档第 6 节。
- [ ] `docs/IMAGE-SCAN-REVIEW.md:100-133`：移除到期的 Keycloak vendor-rejected-CVE 例外条目，标记历史（不删除，供追溯）。
- [ ] `RELEASE-READINESS.md:52-57`："nine images" 改"eight images"，去掉 Keycloak 列举项。
- [ ] `docs/SECURITY-ARCHITECTURE.md:297-309`：OIDC 弹窗 step-up 段落改写为控制台断言 `postMessage` 机制描述（可直接引用 CR-0006 配套技术规格 §4 时序图的文字版）；`:311-318`："Keycloak administration is separately isolated" 整段删除或改"（历史）"标注。
- [ ] `docs/CONFIGURATION.md:137-155,202`：Keycloak OIDC 配置说明同步标注历史或删除，视是否仍需描述"外部 IdP 兼容"这一泛化能力而定。
- 文件：见上述五个文档。
- 测试：人工复核（文档类无自动化门禁）；`bash scripts/check-governance.sh` 若含链接/引用完整性检查需通过。

### 任务 7：一次 RC 发布仪式
- [ ] 走既有 RC 三段流程（源码门禁 → 镜像证据（8 镜像，`IdPMode=console-assertion`）→ 生产），任务 1/2/5 的改动随此 RC 一起发布。
- 文件：无新增文件；产出该 RC 的 `release/<name>-exactN/` 证据目录。
- 测试：`scripts/verify.ps1` 全量源码门禁；`scripts/release-image-gate.ps1 -IdPMode console-assertion` 产出 8 镜像 manifest 且不含 `invoice-keycloak`。

### 任务 8：生产拆除 Keycloak 容器（运维操作）
- [ ] 该 RC 的 `roll-forward.sh`（已不再引用 `docker-compose.idp.yml`）实跑后，`keycloak`/`keycloak-postgres` 自然不再被拉起；确认后运维显式执行一次 `docker compose -f deploy/docker-compose.idp.yml stop`（不加 `-v`，卷保留）。
- 文件：无代码改动。
- 测试：`docker ps` 确认容器数为 16 且不含 `keycloak`/`keycloak-postgres`；`healthz`/`readyz` 200。

### 任务 9：变更单与 ACCEPTANCE-LOG 记录
- [ ] 记录本片完成：任务 1-8 的证据链接、Keycloak 数据保留基准位置与 sha256、CVE 例外移除时间与 `2026-09-30T00:00:00Z` 的先后关系（设计文档第 7 节第 3 条）。
- [ ] 明确记录 CR-0006 整体交付完成（呼应 `docs/roadmap/CR-0006-console-auth-slices.md` 的路线终点）。
- 文件：`docs/handoffs/ACCEPTANCE-LOG.md`。
- 测试：不适用（记录性任务）。
