# CR-0006 第二阶段上线计划

> **For agentic workers：** 本计划横跨两个仓库/两套生产环境，多数步骤是需要人工审批与生产凭据的运维操作（密钥入库、身份数据迁移的 `--apply`、关闭 `OIDC_ADMIN_LOGIN_ENABLED`），不是可全自动执行的编码任务。仅其中的纯代码/文档步骤（RC74 的部署文件改动本身）适合用 superpowers:executing-plans 逐步实现并走常规门禁；涉及生产凭据、真实密钥生成、`--apply`、关闭开关的步骤必须由验收线人工在获得产品负责人批准后逐条执行，代理不得自行对生产发起这些调用。

**Goal：** 在 CR-0006 第一阶段（XM-AUTH-TOTP0、XM-INVCON1、XM-INV-CONSOLE-ASSERT、XM-INV-IDENTITY-MIGRATE 均已合入生产，断言能力已装配但默认关闭）之上，把控制台断言登录从"能力具备"推进到"生产唯一管理员登录路径"：生成并双侧同步首把 Ed25519 签名密钥、完成开票侧部署文件改动（RC74）、按验收线既定顺序启用断言并执行身份迁移、关闭 OIDC 登录、跑完一个满足既定量化门槛的观察窗口，为下一阶段 XM-INV-KEYCLOAK-RETIRE（Keycloak 实际停用，另立、需产品负责人另行放行）扫清前置条件。

**Architecture：** 平台侧（xingmang-platform，console.solov.cc）持有签名私钥并签发断言；开票侧（invoice-system，invoice.solov.cc）已有校验端点与迁移工具，但公钥清单尚未接入部署文件、功能尚未真正启用。本计划第一次让两侧用同一把真实密钥完整走通"签发→postMessage→兑换→身份迁移→停用旧路径"链路；除 RC74 一处必要的部署文件改动外，不新增功能代码，其余全部是配置、数据与审批操作。

**Tech Stack：** Go（`cmd/console-assertion-keygen`、`backend/cmd/identity-migrate`）、Docker Compose、bash 生产运维脚本（`roll-forward.sh`、`backup.sh`、`restore-drill.sh`）、PostgreSQL。

**Spec：** `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`；`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`；`docs/roadmap/CR-0006-console-auth-slices.md`；`docs/handoffs/slices/XM-AUTH-TOTP0.md`、`XM-INVCON1.md`、`XM-INVCON1-FALLBACK.md`；开票仓库 `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`、`XM-INV-IDENTITY-MIGRATE.md`；`docs/handoffs/ACCEPTANCE-LOG.md` 2026-09-02/03 相关条目。

## 前提（已完成，不在本计划范围）

- 平台侧 XM-AUTH-TOTP0（`3e653d7`）、XM-INVCON1（`c1d9e2e`）已上生产，功能默认关闭（`XM_INVOICE_CONSOLE_ASSERTION_ENABLED=false`）。
- 开票侧 RC72（`cadf009`，tag `v0.1.0-rc72-signed`）已上生产，含 XM-INV-CONSOLE-ASSERT（暗发布）与 XM-INV-IDENTITY-MIGRATE 工具镜像；`docker-compose.prod.yml` 尚未包含任何 `CONSOLE_ASSERTION_*`/`OIDC_ADMIN_LOGIN_ENABLED` 键（代码侧默认 `OIDC_ADMIN_LOGIN_ENABLED=true`/`CONSOLE_ASSERTION_ENABLED=false`，只是尚未在部署文件里显式声明）。
- 两仓库 `contracts/auth/console-assertion-keyring.v1.json` 均为空数组占位，尚未生成真实密钥，两侧从未做过真实签发/验签联调。
- 验收线已就 CR-0006 遗留的四个开放问题作出裁定（2026-09-02，`ACCEPTANCE-LOG`）：①密钥用静态审阅清单分发；②管理员 IP 允许清单两侧各配一份（纵深防御）；③第二阶段后 `invoice.solov.cc/admin` 独立入口也改走控制台断言，应急走开票侧既有 break-glass 网段（此裁定执行属 XM-INV-KEYCLOAK-RETIRE 范围，本计划不涉及）；④"一个完整发布周期"量化为断言模式作为唯一路径连续 3 天生产使用 + 一次无 Keycloak 的备份/恢复演练通过——本计划步骤 5 即执行这条门槛。

## 步骤 1：生成并双侧同步首把签名密钥

- [ ] （验收线，平台侧受控环境）运行：
  ```
  go run ./cmd/console-assertion-keygen -key-id 2026-09 -validity-days 365 \
    -manifest contracts/auth/console-assertion-keyring.v1.json
  ```
  工具把公钥条目直接写入本地清单文件（按 `key_id` 排序、拒绝重复），私钥一次性明文打印到终端，不落盘、不经密钥库。
- [ ] （验收线）复核终端打印的公钥条目：`purpose=console_admin_assertion_signing`、`protocol=xm-console-assertion-v1`、`algorithm=Ed25519`；独立对打印的 base64 公钥重新计算一次 SHA-256，逐字符核对与条目里的 `fingerprint` 字段相等——不要只信任工具自己打印的值，这是唯一的人工复核点。
- [ ] （持有 `credential.manage` 的操作员）把私钥存入平台密钥库（本工具本身不接触密钥库，属 Platform Lifecycle Operation 的既定纪律）：
  ```
  POST /api/v1/actions/credential.secret.upsert/versions/1/execute
  {"credential_ref": "secret://console-assertion/signing-key-2026-09",
   "secret_value": "<终端打印的 base64 Ed25519 seed>"}
  ```
  私钥自此只存在于平台的文件型密钥库（`XM_SECRET_ROOT`，与 TOTP 密钥同一份目录），终端记录使用后须清除。
- [ ] （验收线）把生成的公钥条目提交进 xingmang-platform 的 `contracts/auth/console-assertion-keyring.v1.json`（评审后合入）；把**同一条** JSON 原样交给开票线，提交进 invoice-system 自己的同名文件——两份须逐字段比对一致（尤其 `public_key`/`fingerprint`/`key_id`），不接受"看起来一样"。
- [ ] （验收线）把评审后的清单文件传到开票生产主机。清单只含公钥、不含机密，按本仓库既有惯例（`source-public-keys`/`backup-allowed-signers` 同类只读参考文件）放进 `/root/invoice-system/config/`，而非 `secrets/`：
  ```
  scp console-assertion-keyring.v1.json root@<host>:/root/invoice-system/config/console-assertion-keyring.json
  install -m 0444 -o root -g root /root/invoice-system/config/console-assertion-keyring.json
  ```
  证据：两仓库对应提交 SHA + 目标主机上 `sha256sum console-assertion-keyring.json` 与仓库文件一致的记录。

## 步骤 2：开票侧部署文件改动（RC74 范围，非纯代码发布）

- [ ] （验收线）`deploy/docker-compose.prod.yml` 的 `api` 服务 `environment:` 块，紧邻现有 `OIDC_*` 变量之后新增：
  ```yaml
  OIDC_ADMIN_LOGIN_ENABLED: ${OIDC_ADMIN_LOGIN_ENABLED:-true}
  CONSOLE_ASSERTION_ENABLED: ${CONSOLE_ASSERTION_ENABLED:-false}
  CONSOLE_ASSERTION_ISSUER: ${CONSOLE_ASSERTION_ISSUER:-}
  CONSOLE_ASSERTION_AUDIENCE: ${CONSOLE_ASSERTION_AUDIENCE:-xingmang-console-assertion-v1}
  CONSOLE_ASSERTION_KEYS_FILE: /config/console-assertion-keyring.json
  ```
  `volumes:` 块新增只读挂载（与 `SOURCE_TRUST_CONFIG_FILE`/`ADMIN_SETTINGS_BOOTSTRAP_FILE` 同一惯例：host 路径用独立变量名，容器内路径是固定字面量）：
  ```yaml
  - ${CONSOLE_ASSERTION_KEYRING_FILE:?set CONSOLE_ASSERTION_KEYRING_FILE}:/config/console-assertion-keyring.json:ro
  ```
- [ ] （验收线）RC74 的 `.env.production` 新增 `CONSOLE_ASSERTION_KEYRING_FILE=/root/invoice-system/config/console-assertion-keyring.json`、`OIDC_ADMIN_LOGIN_ENABLED=true`、`CONSOLE_ASSERTION_ENABLED=false`——本片仍暗发布，不在这次改动里翻开关。
- [ ] （验收线）明确记录：本片是**部署文件改动**而非纯代码发布。`docker-compose.prod.yml` 结构变化意味着步骤 1 落到主机的清单文件是本片能否启动的硬前提——`CONSOLE_ASSERTION_KEYRING_FILE` 未设或挂载源不存在时，`docker compose -f docker-compose.prod.yml up -d` 会拒绝启动 `api` 容器；`deploy/roll-forward.sh` 本身只校验镜像已加载与 `.env.production` 的 tag 格式，不校验 compose 内容，这条前置纯靠人工核对，建议写进 RC74 发布检查清单。
- [ ] 走既有 RC 三段流程（源码门禁→镜像证据→生产，同 RC72/RC73 模板）完成 RC74 构建、签名、部署；`roll-forward.sh` 顺序不变；本片金丝雀 = 确认现有 OIDC 登录不受影响（断言仍暗发布）。
- 证据：发布记录目录（同 `rc72-deploy-*` 惯例）+ 在 `api` 容器内确认 `/config/console-assertion-keyring.json` 存在且内容与主机/仓库文件一致。

## 步骤 3：启用顺序（按验收线 2026-09-03 裁定）

- [ ] （验收线，平台侧）设置四个变量并重启 `platform-api`：
  ```
  XM_INVOICE_CONSOLE_ASSERTION_ENABLED=true
  XM_INVOICE_CONSOLE_ASSERTION_ISSUER=https://console.solov.cc
  XM_INVOICE_CONSOLE_ASSERTION_AUDIENCE=xingmang-console-assertion-v1
  XM_INVOICE_CONSOLE_ASSERTION_KEY_REF=secret://console-assertion/signing-key-2026-09
  ```
  确认启动日志出现 `console_assertion_signer_loaded`，其 `key_id`/`fingerprint` 与清单一致。
- [ ] （验收线，开票侧）同一维护窗口或稍晚，设置 `CONSOLE_ASSERTION_ENABLED=true`、`CONSOLE_ASSERTION_ISSUER=https://console.solov.cc`（`CONSOLE_ASSERTION_AUDIENCE` 留默认；`OIDC_ADMIN_LOGIN_ENABLED` 保持 `true`），重启 `api`。
- [ ] （产品负责人）真实浏览器完成金丝雀：控制台登录（密码 + TOTP）→打开 Sub2API"支付与财务→开票"页签，确认无弹窗、无跳转、直接呈现已登录界面；NewAPI、治理"开票集成"各再验证一次。
- [ ] （验收线）记录证据：平台侧查询 `audit.audit_event`（`ResourceType='console_assertion'`，事件 `staff.console_assertion.issue`）本次签发行的截断 nonce 与时刻；开票侧查询 `audit_events` 表 `actor_type='console_assertion'` 对应兑换成功行与其 `request_id`；核对两侧 nonce/时刻可交叉对应。
- [ ] 回滚：金丝雀失败则任一侧把 `*_ENABLED` 改回 `false` 并重启，操作员回退弹窗 OIDC，Keycloak 全程未受影响。

## 步骤 4：身份迁移（不可逆，需批准）

- [ ] （验收线）平台库查询将成为开票管理员身份来源的员工账号（用户名由产品负责人指定，见文末决策清单第 1 项）：
  ```sql
  SELECT id, username, roles FROM core.staff_account WHERE username = '<产品负责人指定的控制台用户名>';
  ```
  `id` 即 `--to-subject`。
- [ ] （验收线）开票库重新查询现状——**不得**沿用本计划或任何历史文档记录的旧值（CR-0006 正文自己也这样要求）：
  ```sql
  SELECT id, oidc_issuer, oidc_subject, status FROM invoice_users WHERE status = 'active';
  ```
  取得 `--from-issuer`/`--from-subject`。
- [ ] （验收线）`--dry-run`（默认，工具镜像内跑，网络/密钥挂载同 `eligibility-repair` 先例）：
  ```
  docker run --rm --pull=never --user 10001:10001 \
    --network invoice-system-prod_invoice_db --read-only --cap-drop ALL \
    --security-opt no-new-privileges:true \
    -v "$SECRETS_DIR/invoice_owner_database_url:/run/secrets/invoice_owner_database_url:ro" \
    -v "$SECRETS_DIR/invoice_field_keyring.json:/run/secrets/invoice_field_keyring:ro" \
    "invoice-system-tools:$INVOICE_IMAGE_TAG" \
    /usr/local/bin/invoice-identity-migrate \
    --database-url-file /run/secrets/invoice_owner_database_url \
    --field-keyring-file /run/secrets/invoice_field_keyring \
    --migrations-dir /app/migrations \
    --from-issuer "<上一步 oidc_issuer>" --from-subject "<上一步 oidc_subject>" \
    --to-issuer "https://console.solov.cc" --to-subject "<步骤4.1 的 core.staff_account.id>"
  ```
  核对打印摘要：`user_id`/`auth_sessions_total`/`auth_sessions_live`/`audit_rows` 与预期一致。
- [ ] （产品负责人）复核 dry-run 输出，书面/聊天批准 `--apply`，并指定 `--operator-id`（批准人自己的开票管理员 UUID，见决策清单第 2 项）。
- [ ] （验收线）追加 `--apply --operator-id=<批准人 UUID>` 重跑同一条命令；确认输出 `APPLIED` 且会话失效计数与 dry-run 一致；再跑一次 dry-run 确认返回 `ALREADY MIGRATED`。
- 证据：三次命令（dry-run、apply、复核 dry-run）的完整终端输出存档；开票侧 `identity.oidc_binding.migrated` 审计行的 `before_hash`/`after_hash`。
- 回滚/不可逆性：`apply` 是单事务原子写，没有"撤销"命令——回滚 = 用对换后的 `--from`/`--to` 再跑一次 identity-migrate 把两列改回 Keycloak 原值；这本身也是一次需要重新批准的 `--apply`，不是免费操作，请产品负责人在批准本步骤前知悉这一点。

## 步骤 5：关闭 OIDC 管理员登录 + 观察窗口

- [ ] （产品负责人）批准关闭 `OIDC_ADMIN_LOGIN_ENABLED`（决策清单第 3 项）。
- [ ] （验收线）设置开票侧 `OIDC_ADMIN_LOGIN_ENABLED=false`，重启 `api`；确认启动日志不再进行 OIDC discovery，OIDC 跳转/回调/后台步进/back-channel-logout 路由不再注册；Keycloak 容器（`keycloak`/`keycloak-postgres`）继续运行、不停用。
- [ ] 观察窗口 ≥3 天（验收线既定量化门槛），逐日检查：
  - [ ] 开票侧 `audit_events`：窗口内无任何 `actor_type='oidc'` 的新增管理员登录行。
  - [ ] 两侧断言审计计数吻合（`audit.audit_event` 的 `staff.console_assertion.issue` 成功数 vs `audit_events` 的 `console_assertion` 兑换成功数），且无非预期的 `ASSERTION_INVALID`/`ADMIN_STEP_UP_REQUIRED`/`ADMIN_NETWORK_DENIED` 堆积。
  - [ ] 窗口内完成一次"无 Keycloak"恢复演练——不改动常规每次都含 Keycloak 的备份，单独为演练打一份：
    ```
    BACKUP_LOCAL_KEYCLOAK=false bash deploy/backup/backup.sh   # 其余变量同现有惯例
    ```
    随后运行 `deploy/backup/restore-drill.sh`，**不传** `KEYCLOAK_BACKUP`，确认 `PASS`（该脚本在隔离 tmpfs 库中恢复，不触及生产）。
  - [ ] 窗口结束，验收线在 `ACCEPTANCE-LOG` 记录门槛是否达成（3 天洁净 + 演练 PASS）。
- 回滚：`OIDC_ADMIN_LOGIN_ENABLED` 改回 `true`，重启 `api`；Keycloak 全程在线，零停机代价。

## 步骤 6：验收记录与下一阶段授权

- [ ] （验收线）本计划的完成标志：断言模式是唯一生产管理员登录路径、身份迁移已 `APPLIED`、OIDC 已关闭且观察窗口门槛达成，记入 `ACCEPTANCE-LOG`。这**不等于** CR-0006 整体交付——按路线图"断言模式合入 ≠ Keycloak 停用"，CR-0006 要到 XM-INV-KEYCLOAK-RETIRE（`OIDC_ADMIN_LOGIN_ENABLED` 已为 `false` 且 Keycloak 容器停用不删除、发布门禁镜像清单改八个、`RELEASE-READINESS.md`/`IMAGE-SCAN-REVIEW.md` 更新、`invoice.solov.cc/admin` 独立入口按裁定③改走断言）合入部署后才算交付。
- [ ] （产品负责人）门槛达成不代表下一阶段自动开始——按 CR-0006 路线图明确要求，需另行点头授权 XM-INV-KEYCLOAK-RETIRE 立项开工（决策清单第 4 项）。

## 需要产品负责人决策/输入的事项

1. 指定哪个 `core.staff_account`（用户名）成为开票管理员的新身份来源（步骤 4 的 `--to-subject` 依据）。
2. 批准 identity-migrate 的 `--apply` 执行，并指定 `--operator-id`（批准人自己的开票管理员 UUID）。
3. 批准关闭 `OIDC_ADMIN_LOGIN_ENABLED`（步骤 5）。
4. 观察窗口结束后，确认门槛证据（3 天洁净 + 恢复演练 PASS）充分，并明确点头授权 XM-INV-KEYCLOAK-RETIRE 立项开工。
