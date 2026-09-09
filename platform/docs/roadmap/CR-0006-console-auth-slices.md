# CR-0006 切片路线：控制台代认证与 Keycloak 退役

本文件是 `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md` 的执行排序索引，不重复该 CR 的设计内容——细节以 CR 本体与配套技术规格 `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md` 为准。仓库目前没有独立的"路线图"文档类别（`PROJECT-CONSTITUTION.md` 第二节的文档治理只定义 ADR/Contract/Runbook/Inventory/Evidence/CR 六类），本文件是为本 CR 新开的 `docs/roadmap/` 目录下第一篇，供后续跨多个切片的变更单参照复用这一形式。

## 切片一览

| 切片 | 所属线 | 依赖 | 交付内容 |
|---|---|---|---|
| [XM-AUTH-TOTP0](#xm-auth-totp0) | 平台线 | 无 | 控制台 TOTP 二因素、恢复码、`core.staff_session.mfa_at`、管理员 IP 名单配置 |
| [XM-INVCON1](#xm-invcon1) | 平台线 | XM-AUTH-TOTP0 | 断言签发端点、Ed25519 签名密钥托管与轮换、`EmbeddedConsoleFrame` 断言转交 |
| [XM-INV-CONSOLE-ASSERT](#xm-inv-console-assert) | 开票线 | 与 XM-INVCON1 共享契约草案，建议并行开发 | 断言兑换端点、重放防护、`invoice_users` 身份迁移工具、iframe 侧接收逻辑 |
| [XM-INV-KEYCLOAK-RETIRE](#xm-inv-keycloak-retire) | 开票线 | XM-INV-CONSOLE-ASSERT 已生产运行满一个发布周期，且产品负责人明确放行 | 停用 Keycloak 容器、发布门禁改八镜像、备份/恢复演练脚本收尾 |

责任人标注沿用 CR-0005 先例："平台线"/"开票线"当前均由验收线执行（不预设某个具体代理工具）。

## 发布排序

```text
XM-AUTH-TOTP0 ──▶ XM-INVCON1 ──┐
                                  ├──▶ [断言模式可用，Keycloak 仍是生产默认] ──▶ 一个完整发布周期的生产验证窗口 ──▶ XM-INV-KEYCLOAK-RETIRE
              XM-INV-CONSOLE-ASSERT ┘
```

- **XM-AUTH-TOTP0 必须先于 XM-INVCON1**：断言签发端点要求 `mfa_at` 新鲜度，没有 TOTP 就没有 `mfa_at` 可判定。
- **XM-INVCON1 与 XM-INV-CONSOLE-ASSERT 建议并行**：两者的边界是断言契约本身（技术规格第 3 节），只要契约在两侧动工前已冻结，两个团队/代理可以各自独立实现，最后各自走完门禁后再对齐联调——这是 CR-0005 里 XM-INVCON0 与 XM-INV-ADMIN-EMBED 已经验证过可行的协作模式。
- **断言模式合入 ≠ Keycloak 停用**：三个切片合入后，生产环境的默认登录路径仍是 Keycloak OIDC（`OIDC_ADMIN_LOGIN_ENABLED=true`），断言路径是新增的、经过验证的备选路径。这条"先并存、后切换"的排序是 CR-0006 正文明确要求的，不是本文件自行加码。
- **"一个完整发布周期"不是一个可交付的切片**，是一个时间/证据门槛：断言登录在生产实际承担真实管理员登录、至少完整经历一次发布节奏、双侧审计计数吻合、无未解决的金丝雀异常。达到门槛与否由验收线在 `docs/handoffs/ACCEPTANCE-LOG.md` 记录判断依据。
- **XM-INV-KEYCLOAK-RETIRE 的开工前提是产品负责人明确放行**，不因为前置条件满足就自动开始——这与 CR-0005 第二阶段"另行授权"的既定纪律一致，也是 CR-0006 正文"状态"行特别强调的一点。

## 各切片摘要

### XM-AUTH-TOTP0
- **前提**：无。
- **范围**：`internal/platform/localauth` 扩展（TOTP 登记/确认/重置三个 L1 Action、恢复码、两步登录）、`core.staff_account`/`core.staff_session` 表结构扩展、`XM_CONSOLE_ADMIN_IP_ALLOWLIST` 配置项与断言签发端点尚不存在前提下的独立可验证性（本切片自身即可通过"控制台登录要求 TOTP"这条验收线，不依赖后续切片）。
- **不做**：不涉及开票系统任何代码；不涉及断言签发/兑换。

### XM-INVCON1
- **前提**：XM-AUTH-TOTP0 已合入（需要 `mfa_at` 与 TOTP 登记状态）。
- **范围**：新包 `internal/platform/consoleassertion`（签发端点、`finance.read`/IP 名单/新鲜度三重校验）、Ed25519 签名密钥生成与 `secret://console-assertion/*` 托管、`contracts/auth/console-assertion-keyring.v1.json` 首版发布、`EmbeddedConsoleFrame` 的 `kind:"admin-assertion"` postMessage 扩展。
- **不做**：不涉及开票系统任何代码（开票侧如何消费断言是 XM-INV-CONSOLE-ASSERT 的范围）；不停用弹窗 OIDC 路径（那是 XM-INV-KEYCLOAK-RETIRE 的范围，且在另一个仓库）。

### XM-INV-CONSOLE-ASSERT
- **前提**：与 XM-INVCON1 共享的断言契约（技术规格第 3 节）已冻结；不要求 XM-INVCON1 先合入生产，但建议开发期对齐同一份契约草稿。
- **范围**：`ProductionAuth` 新增兑换端点、`console_assertion_nonces` 表与保留期任务、开票 iframe 前端新增入站 `postMessage` 监听与"等待控制台登录"过渡态、`invoice_users` 身份迁移工具（工具本身随本切片交付，**执行**该工具是独立的、需要变更单批准的 Platform Lifecycle Operation，不在本切片"合入即完成"的范围内）。
- **不做**：不停用 OIDC 登录路径；不改发布门禁脚本；不执行数据迁移（只交付工具）。

### XM-INV-KEYCLOAK-RETIRE
- **前提**：XM-INV-CONSOLE-ASSERT 已在生产运行满一个完整发布周期，金丝雀干净；产品负责人明确放行本切片开工。
- **范围**：`OIDC_ADMIN_LOGIN_ENABLED=false`；`docker-compose.idp.yml` 从生产 compose 组合摘除（容器停止、卷保留、不删除）；`scripts/release-image-gate-lib.ps1` 新增 `IdPMode=console-assertion` 取值与对应八镜像清单；`docs/IMAGE-SCAN-REVIEW.md`/`RELEASE-READINESS.md` 更新为八镜像表述，移除即将于 2026-09-30 到期的 Keycloak CVE 例外条目（标记历史，不删除）；`backup.sh`/`restore-drill.sh` 的 Keycloak 分支停用（脚本代码保留）；执行 `invoice_users` 身份迁移（使用上一切片交付的工具，走独立批准）。
- **不做**：不物理删除 `oidc_authorization_flows`/`oidc_backchannel_logout_events` 表结构与 `internal/oidcretention` 代码——这是至少再晚一个发布周期的独立变更，明确排除在本切片之外（见 CR-0006 正文对应段落）。

## 相关文档
- `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md` —— 本路线所服务的变更单。
- `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md` —— 断言契约、时序图、错误码、威胁模型、测试矩阵。
- `docs/handoffs/slices/XM-CR0006-console-auth.md` —— 本次设计交付的交接记录。
