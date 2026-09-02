# XM-INV-KEYCLOAK-RETIRE 设计：开票系统退役 Keycloak

> 状态：**设计阶段，不是实施授权**。依据 `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md` g 条第二阶段、`docs/roadmap/CR-0006-console-auth-slices.md`、`docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md`。配套实施计划见同目录 `2026-09-03-xm-inv-keycloak-retire.md`。开票仓库路径以 `K:/发票/wt-XM-INV-AUTOLOGIN`（分支 `ai/claude/XM-INV-AUTOLOGIN`）为准，只读核对，本设计不改开票仓库任何文件。

## 0. 范围边界：与 phase2-rollout 的分工

CR-0006 正文 g 条把"`OIDC_ADMIN_LOGIN_ENABLED=false` 与身份迁移"写在本切片范围内；但更晚成文、由验收线执笔的 `2026-09-03-cr0006-phase2-rollout.md` 步骤 4／5 已把身份迁移、关闭 OIDC、≥3 天观察窗口收进**它自己**的范围，并明确"下一阶段 XM-INV-KEYCLOAK-RETIRE（Keycloak 实际停用，另立、需产品负责人另行放行）"。本设计按后者处理：**身份迁移与关闭 OIDC 是本片开工前必须已具备的前提，不是本片任务**；本片任务严格从"Keycloak 容器可以安全拆除"这一时间点开始，覆盖发布门禁、部署编排、备份恢复、nginx、文档五个面。此处两份文档字面冲突，按更晚且更具体的 phase2-rollout 解读，随实现同步记入变更单。

## 1. 现状（2026-09-03）

RC74（`e38d7c0`，tag `v0.1.0-rc74-signed`）已部署：`CONSOLE_ASSERTION_KEYRING_FILE` 只读挂载与五个断言相关环境变量已接线，但 `CONSOLE_ASSERTION_ENABLED=false`、`OIDC_ADMIN_LOGIN_ENABLED=true`——**断言模式仍是暗发布**。phase2-rollout 步骤 1（生成真实密钥）与 3～6（启用顺序、身份迁移、关 OIDC、观察窗口、下一阶段授权）均未开始（`docs/handoffs/ACCEPTANCE-LOG.md:231`）。本片的四条前提——密钥已双侧同步、身份迁移已 `APPLIED`、`OIDC_ADMIN_LOGIN_ENABLED=false` 满 3 天、一次无 Keycloak 恢复演练 PASS——**今天全部尚未满足**。

## 2. Keycloak 当前提供什么：依赖清单（开票仓库，file:line）

| 类别 | 文件:行 | 作用 |
|---|---|---|
| 编排 | `deploy/docker-compose.idp.yml`（93 行） | compose 项目 `invoice-keycloak-prod`，服务 `keycloak-postgres`+`keycloak` |
| 编排 | `deploy/docker-compose.idp.bootstrap.yml`（11 行） | 一次性 bootstrap admin 覆盖层 |
| 编排 | `deploy/docker-compose.prod.yml:220-234` | `OIDC_*` 十余个变量，供 Keycloak OIDC 客户端 |
| 编排 | `deploy/roll-forward.sh:10,40-42,74,88-89` | "nine images" 注释；镜像存在性循环含 `invoice-keycloak`；`[1/6] keycloak project`；断言恰好 18 容器 |
| 备份 | `deploy/backup/backup.sh:103,110,416-426` | `BACKUP_LOCAL_KEYCLOAK` 分支，产出 `*.keycloak.dump.age` |
| 恢复 | `deploy/backup/restore-drill.sh:82-84,113,388-396` | `KEYCLOAK_BACKUP` 可选分支，恢复进 `keycloak_restore` 库并断言表数 >20 |
| 门禁 | `scripts/release-image-gate-lib.ps1:837-879` `Get-IdpGateDecision` | `ValidateSet('keycloak','none','external-managed')` 三态判定，`none` today 语义是"未含 IdP，阻断生产"（非"无需 IdP"） |
| 门禁 | `scripts/release-image-gate-lib.ps1:881-914` `Get-CommonReleaseImageTag` | 仅当 `IdPMode -ceq 'keycloak'`（897 行）才把 `keycloak` 追加进 8 个固定仓库；非 keycloak 模式已天然只产出 8 项 |
| 门禁 | `scripts/release-image-gate-lib.ps1:158-183` `Assert-KeycloakDockerfileLiteralBasePins` | 钉死 Keycloak 26.7.2 digest |
| 门禁 | `scripts/release-image-gate-lib.ps1:319-350` `Assert-KeycloakVendorRejectedCveScope` | `CVE-2026-22020` 例外，审阅窗口到期 `2026-09-30T00:00:00Z` |
| 门禁 | `scripts/release-image-gate-lib.ps1:710-806` `Assert-TransferReadyManifest` | 整函数硬编码 RC74 与 9 镜像（738 行含 `keycloak`），由 `scripts/verify-release-image-artifacts.ps1:23,68` 调用，是真实生产门禁的一部分 |
| 门禁 | `scripts/release-image-gate.ps1:13` | `$IdPMode` 参数默认值 `'keycloak'` |
| 门禁 | `scripts/release-image-gate.ps1:256-258,343,467-490,575-579,631,635` | 六处按 `$IdPMode -ceq 'keycloak'` 分支（构建、CVE 例外核对、隔离运行时冒烟、`Get-IdpGateDecision` 调用、manifest 字段） |
| 门禁 | `scripts/verify-keycloak-runtime.ps1`（136 行） | 被 `release-image-gate.ps1:477` 调用，隔离 Keycloak+PostgreSQL 健康与 OIDC discovery 冒烟 |
| 门禁 | `scripts/verify-keycloak-provisioning.ps1`（409 行）、`verify-keycloak-permanent-master-admin.ps1`（111 行）、`create-keycloak-offsite-ack.ps1`（56 行）+ 两个测试 | 一次性运维校验工具，**不在自动门禁默认路径**上 |
| nginx | `deploy/nginx/auth.solov.cc.conf.template`（69 行）、`auth-admin.solov.cc.conf.template`（39 行） | 两个整域名，专服务 Keycloak 公共 realm 路径与 Admin REST |
| nginx | `deploy/nginx/keycloak-proxy-headers.conf`（19 行） | 上述两域名共用 include，无其他引用者 |
| nginx | `deploy/nginx/auth-admin.solov.cc.allow.conf.example` + `deploy/validate-keycloak-admin-allowlist.sh` | Keycloak Admin 网段白名单示例与语法校验器 |
| 运维脚本 | `deploy/keycloak/entrypoint.sh`（31 行）、`validate-proxy-trust.sh`（37 行） | 容器入口/运行时校验，容器不起来自然不执行 |
| 运维脚本 | `deploy/keycloak/010-keycloak-app-role.sh`（35 行） | postgres 初始化脚本，由 idp.yml 挂载进 `docker-entrypoint-initdb.d` |
| 运维脚本 | `deploy/keycloak/provision-solov-realm.sh`（793 行）、`invite-permanent-master-admin.sh`（807 行） | 一次性 realm/管理员配置，**已执行过**，留档不再运行 |
| 运维脚本 | `deploy/keycloak/run-permanent-master-admin-maintenance.sh`（370 行）+`verify-permanent-master-admin-maintenance.sh`（322 行） | 维护窗口 wrapper 及其测试 |
| 运维脚本 | `deploy/keycloak/attach-invoice-basic-scope.sh`（258 行） | RC22 一次性历史脚本（硬编码 `/incoming/rc22/` 路径），已执行完毕 |
| 密钥权限 | `deploy/preflight-secret-permissions.sh:55,85` 无条件、`:86-87` 已带 `if -e` | `keycloak_owner_db_password`/`keycloak_app_db_password` 两行**无条件必需**（与本片"保留 secrets 供回滚"一致，暂不用改）；`keycloak_bootstrap_admin_password` 已是条件检查 |
| 后端 | `backend/internal/httpapi/server.go:120-141` | `AuthMode` 只有 `"mock"`/`"oidc"` 两态；`"oidc"` 是生产伞形名（内部同时含 OIDC 与断言路径），**不是**"仅 OIDC"字面意思，本片不改名、不新增枚举值 |
| 后端 | `backend/internal/httpapi/production_auth.go`、`backend/internal/auth/console_assertion.go`/`postgres.go`/`identity_migrate.go` | 替代路径已实现并暗发布（XM-INV-CONSOLE-ASSERT／XM-INV-IDENTITY-MIGRATE／XM-INV-CONSOLE-ASSERT-DEPLOY），本片不改代码 |
| 文档 | `docs/PRODUCTION-RUNBOOK.md` §5（952-1314 行，363 行）"Central Keycloak reference deployment" | 整节将转历史 |
| 文档 | 同文档 §4.1（886-951）、§11 备份（1956-2089）、§12 回滚（2164-2202，2196-2201 已有 console-assertion 回滚条目） | 需更新为稳态描述、补 Keycloak 拆除后的回滚条目 |
| 文档 | `docs/IMAGE-SCAN-REVIEW.md:100-133` | Keycloak vendor-rejected-CVE 例外表，到期 `2026-09-30T00:00:00Z` |
| 文档 | `RELEASE-READINESS.md:52-57` | "nine images" 表述 |
| 文档 | `docs/SECURITY-ARCHITECTURE.md:297-309` | OIDC 弹窗 step-up 段落——**已被断言机制取代却从未更新**（XM-INV-CONSOLE-ASSERT 交接文档 Follow-up 5 已指出此缺口） |
| 文档 | 同文档 `:311-318` | "Keycloak administration is separately isolated" 整段过时 |
| 文档 | `docs/CONFIGURATION.md:137-155,202` | Keycloak OIDC 配置说明 |
| 数据 | Keycloak 专属 Postgres 库（`keycloak-postgres` 容器） | realm 配置、管理员账号与少量历史会话，**不含开票业务数据** |

## 3. 什么替代它；用户侧登录是否受影响

替代机制（控制台签名断言）已实现并暗发布：`XM-INV-CONSOLE-ASSERT`（`058fb13` 等六提交，RC72 暗上线）、`XM-INV-IDENTITY-MIGRATE`（`0ff6e9a`，工具已交付，尚未执行）、`XM-INV-CONSOLE-ASSERT-DEPLOY`（`4683ff4`，RC74 已把 compose 接线补齐，暗）。

**明确结论：只有开票管理员登录依赖 Keycloak。** 证据：①`deploy/docker-compose.prod.yml:249-254` 注释明确"User-facing login (CR-0004/XM-INV-LOGIN): the invoice-system backend forwards submitted credentials to the platform's own login endpoint"（转发到 Sub2API/NewAPI，非 Keycloak）；②`docs/handoffs/ACCEPTANCE-LOG.md:188` "业务用户已按 CR-0003 走平台密码登录"；③对开票仓库 `web/` 全目录 `grep -rniI keycloak` **零命中**（已核实）。业务/用户前端登录本片零风险，不涉及任何改动。

独立入口 `invoice.solov.cc/admin`：验收线 2026-09-02 裁定③已明确"第二阶段后独立入口也改走控制台断言，不再有独立登录，应急走开票侧既有 break-glass 网段"，且 XM-INV-CONSOLE-ASSERT 已交付的 `web/src/App.tsx`（"LoginPage hides the OIDC entry when disabled and shows a console pointer on the standalone /admin path"）**已经满足**这条裁定——`OIDC_ADMIN_LOGIN_ENABLED=false` 生效后该页面自动退化为"请到控制台登录"的静态指引，无需本片新工作。

## 4. 目标终态

- **compose**：生产三个 compose 项目（prod/idp/sources）变两个（prod/sources）；`docker-compose.idp.yml`/`.idp.bootstrap.yml` **保留在仓库不删**，只是 `roll-forward.sh` 不再引用（供回滚，见第 6 节）。
- **roll-forward.sh**：删去 `[1/6] keycloak project` 步骤并重新编号；镜像存在性循环（40-42 行）去掉 `invoice-keycloak`；容器数断言（88-89 行）从 18 改 16（6 个 prod 常驻服务 + 10 个 source-agent，已用 `docker-compose.sources.yml` 服务清单核实）；10 行注释同步改。
- **镜像**：9→8（`invoice-system-{api,pdf-scanner,tools,web}`、`invoice-source-agent`、`invoice-{postgres,clamav,ingest-proxy}`）；`release-image-gate.ps1` 默认 `$IdPMode` 从 `'keycloak'` 改 `'console-assertion'`（新增取值，不复用 `none`——`none` 语义是"阻断生产"，与"断言已生产可用"矛盾）；`Get-IdpGateDecision` 新增对应分支，参照 `'external-managed'` 的"pending canary"形状返回一个新 `BlockReason`（如 `console_assertion_pending_canary`），直到生产断言金丝雀证据补齐才放行；`'keycloak'` 分支保留、`Get-CommonReleaseImageTag`/`Assert-KeycloakVendorRejectedCveScope`/`Assert-KeycloakDockerfileLiteralBasePins` 均不删代码，只是默认路径不再选中它们。
- **备份恢复**：脚本不改代码（`BACKUP_LOCAL_KEYCLOAK`/`KEYCLOAK_BACKUP` 本来就是可选开关）；运维惯例改为默认不传，执行前留一次含 Keycloak 的备份作保留基准（第 5 节）。
- **nginx**：`auth.solov.cc`/`auth-admin.solov.cc` 两个 vhost 从宿主宝塔面板摘除（运维操作，不在本仓库 diff）；仓库内模板文件保留、顶部加注释"Keycloak 已退役，此模板不再部署，仅供回滚参考"；`keycloak-proxy-headers.conf`/`validate-keycloak-admin-allowlist.sh` 同样保留不改。
- **校验脚本**：`verify-keycloak-runtime/-provisioning/-permanent-master-admin/create-offsite-ack` 及测试保留，不再默认触发（仅 `-IdPMode keycloak` 回滚演练时使用）。
- **env 默认值收紧**：`docker-compose.prod.yml:244` 的 `${OIDC_ADMIN_LOGIN_ENABLED:-true}` 改 `${OIDC_ADMIN_LOGIN_ENABLED:-false}`（防止漏配 `.env.production` 时悄悄回到需要 Keycloak 的状态；仍可显式设 `true` 用于回滚，不做代码级抹除）。**待确认**：`CONSOLE_ASSERTION_ENABLED` 是否同步把默认值改 `:-true`——若产品负责人希望默认值本身保持保守（显式配置优先于默认值），可以不改，两种取法均不影响已显式设置的生产 `.env.production`。

## 5. 数据保留

Keycloak 专属 Postgres 库仅含 realm 配置、管理员账号与少量历史会话（不含开票业务数据）。保留办法：执行拆除前最后跑一次 `BACKUP_LOCAL_KEYCLOAK=true bash deploy/backup/backup.sh`，产出的 `*.keycloak.dump.age` 与其余备份组件同置一处（`$BACKUP_DIR`，age 加密、SSH 签名），作为保留基准，不做单独抽取。**待确认（产品负责人）**：保留期限；建议不少于 90 天（覆盖至少一个完整发布/事故复盘周期），到期后另立变更单物理删除卷（`keycloak_postgres_data`）、加密备份与 `deploy/keycloak/` 一次性 provisioning 脚本，不在本片范围。

## 6. 回滚（本片自身：容器已停用之后）

与 phase2-rollout 的"翻转开关"回滚不同，本片回滚针对"容器已经拆除"之后：①`docker compose -f deploy/docker-compose.idp.yml up -d --no-build`（卷 `keycloak_postgres_data` 未删，数据原样）；②若 bootstrap admin 也已停用，需重新叠 `docker-compose.idp.bootstrap.yml`；③`OIDC_ADMIN_LOGIN_ENABLED` 改回 `true` 并重启 `api`；④`release-image-gate.ps1 -IdPMode keycloak` 走回 9 镜像门禁；⑤nginx 两域名 vhost 需要重新在宿主启用。**残余风险**：第④步依赖 Keycloak 26.7.2 的 CVE 例外仍在审阅窗口内（`2026-09-30T00:00:00Z` 前）；若回滚发生在到期之后，例外失效，`release-image-gate.ps1` 会直接拒绝 9 镜像模式，回滚路径需要先重新走一次镜像审查——见第 7 节安全复核第 3 条。

## 7. 安全复核要点

1. **console.solov.cc 单点**：Keycloak 容器停用后，开票管理员登录唯一依赖控制台可用性与断言签名密钥清单；即便走 break-glass CIDR，仍需先拿到一枚有效断言或已存在会话，没有独立于控制台的登录后备。这是产品负责人 2026-09-02 口头裁定已接受的取舍，本片只是把它从"可回退状态"变成"默认状态"，建议在变更单中重申一次供确认。
2. `docs/SECURITY-ARCHITECTURE.md` 两段过时表述（297-309、311-318 行）若不更新，日后审计/新人据此复核会得到错误的现状描述，必须随本片一起修正。
3. **CVE 例外到期与执行时点的先后关系**：若本片在 `2026-09-30T00:00:00Z` 前执行，例外条目应随镜像清单一起标记历史移除；若晚于到期日执行，第 6 节回滚路径已经受影响，需要产品负责人知悉这个日期窗口，必要时提前顺序。
4. **两处 IP 允许清单**：`XM_CONSOLE_ADMIN_IP_ALLOWLIST`（控制台侧）与开票侧 `AdminCIDRs`/`BreakGlassCIDRs` 在 Keycloak 两域名摘除后，成为管理员网络层唯一两处校验来源，建议核对两者当前取值仍然一致（CR-0006 c 条既有纪律）。
5. `preflight-secret-permissions.sh:85` 对 `keycloak_app_db_password` 的检查目前无条件必需；本片建议保留该 secret 文件（配合回滚）故暂不用改；若未来物理清理阶段真删除该文件，必须同步把该行改成 `keycloak_bootstrap_admin_password`（86-87 行）已有的 `if -e` 条件写法，否则该脚本会对不存在的文件报错退出。

## 8. 待核实清单

- `scripts/verify-nginx-configs.ps1` 是否断言 `auth.solov.cc`/`auth-admin.solov.cc` 两个模板文件必须存在——若是，需要改为可选，本片未读到其判定逻辑全文，需实现前核实。
- `scripts/verify-release-image-artifacts.ps1` 调用的 `Assert-TransferReadyManifest`（硬编码 RC74/9 镜像）是否每个 RC 都手工更新字面量——本设计假设是，实现时需核对最近几个 RC 的历史 diff 确认模式。
- `scripts/verify.ps1` 的 `$productionEnv` 夹具（XM-INV-CONSOLE-ASSERT-DEPLOY 已改过一次）是否需要因 `OIDC_ADMIN_LOGIN_ENABLED`/`CONSOLE_ASSERTION_ENABLED` 默认值改动而同步调整。
- 数据保留期限、`CONSOLE_ASSERTION_ENABLED` 默认值是否现在翻转、何时物理删除卷与 provisioning 脚本——均待产品负责人裁定，详见配套实施计划的"前提门禁"与"待裁定事项"。
