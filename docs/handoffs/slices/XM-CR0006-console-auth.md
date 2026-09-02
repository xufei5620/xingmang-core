# XM-CR0006：CR-0006 控制台代认证设计文档交付

## status

READY（设计文档，待验收线/产品负责人审读；本片不含任何代码改动，不可"合入"运行，只可评审后作为后续四个实现切片的依据）

## branch

`ai/claude/XM-CR0006-console-auth`（base `release/v0.1-launch` @ `2c66228210d0dd51e587cc81235ea3c9da80a58c`），worktree `K:/星芒统一控制平台/wt-xmCR0006`

## commits

三个小提交，逐个独立、纯文档：

- `faddefd` — docs(change-requests): add CR-0006 console auth for invoice admin
- `8a18210` — docs(specs): add CR-0006 console assertion technical design
- `4b2534f` — docs(roadmap): add CR-0006 slice sequencing and index pointer（HEAD）

## summary

依据产品负责人 2026-09-02 15:35 口头裁定（"开票系统去掉 Keycloak；方案 = 控制台代认证：控制台唯一身份 + TOTP + 签名断言 → 开票 API；Keycloak 退役，门禁 9→8 镜像"），为 CR-0006 撰写完整设计文档（不含任何代码实现），交付四份文档：

1. `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`——同 CR-0005 结构（发起方/接收方/目标资源/背景事实/结论/两侧改动清单/明确不变/影响面/验证/回滚/路线/确认/执行记录）：断言契约（Ed25519 签名、claims 表、密钥托管与轮换）、控制台 TOTP 设计（登记/确认/恢复码/强制范围）、IP 名单复用方式、Keycloak 分两阶段退役的数据影响（`invoice_users` 身份迁移、`auth_sessions` 无需改动的理由、`oidc_*` 表与保留期任务的处置、`backup.sh`/`restore-drill.sh` 的 Keycloak 分支收尾）、发布门禁契约变化（`IdPMode` 新增 `console-assertion` 取值、九→八镜像）、回滚方案（断言模式配置开关 + Keycloak 容器停用但不删除一个发布周期）。专门辟出一节论证"CR-0006 不需要 Foundation-B 的任何能力"（逐条核对每个新写操作的 Action 风险等级或 Platform Lifecycle Operation 归类），并单列"关于密钥分发方式，请产品负责人确认"的专项确认段落。
2. `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`——两侧实现要对齐的技术规格：组件与信任边界图、断言契约完整细节（编码/claims/签名与密钥托管/两侧请求体）、4 张 mermaid 时序图（正常登录、TOTP 步进、重放拒绝、越权签发拒绝）、两侧 HTTP 端点与错误码表、数据库改动、审计字段、5 行威胁模型表（断言被窃取/重放/iframe 来源伪造/控制台被攻陷）、28 行测试矩阵、与现有代码的接口点索引。
3. `docs/roadmap/CR-0006-console-auth-slices.md`（新增 `docs/roadmap/` 目录，仓库此前无此文档类别，`PROJECT-CONSTITUTION.md` 文档治理只定义 ADR/Contract/Runbook/Inventory/Evidence/CR 六类，本片按团队负责人指示新开一类，供后续跨切片变更单复用这一形式）——四个切片（XM-AUTH-TOTP0/XM-INVCON1/XM-INV-CONSOLE-ASSERT/XM-INV-KEYCLOAK-RETIRE）的依赖关系、发布排序 ASCII 图、各切片范围/前提/明确不做的边界。`docs/architecture/BASELINE-v2.1.md` 新增一个"跨切片路线索引"小节指向它（该文件本身定位是 ADR 索引，只加了一行索引表，未改动其原有的一句话架构原则/ADR 索引/里程碑主线三节）。
4. 本文件。

## files_changed

新增（4）：
- `docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`
- `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`
- `docs/roadmap/CR-0006-console-auth-slices.md`
- `docs/handoffs/slices/XM-CR0006-console-auth.md`（本文件）

修改（1）：
- `docs/architecture/BASELINE-v2.1.md`——追加"跨切片路线索引"小节（一个二级标题 + 一张两行表格），原有三节内容未改动一字。

未改动 `docs/handoffs/ACCEPTANCE-LOG.md`（按分工，验收线专属文件，任务书明确要求不碰）。未改动任何 `.go`/`.ts`/`.tsx`/契约/迁移文件，未改动 K:/发票 任一文件（任务书明确要求不碰开票侧六个发布身份文件与任何代码/契约）。

## tests_run

本片是纯文档交付，无可运行的代码测试。已执行的检查：

- `bash scripts/check-governance.sh`——PASS（exit 0，无输出），在三个文档提交之间共执行两次（首次全部三文档写完后、以及最终 `git log`/交付前复核一次），结果一致。
- 全仓库范围的"ASCII 标点紧贴中文"自查脚本（scratchpad 一次性 node 脚本，逻辑等价于既有的 `cjk-punct.mjs` 检测规则）——对四份新文档（含本文件写完后）逐一跑过，均为 0 处残留；`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md` 首次写入时有若干处被 `cjk-punct.mjs` 自动改写为全角标点，复查确认改写正确（唯一需要人工介入的是威胁模型表格里 `cjk-punct.mjs` 括号成对规则在同一表格单元格内不同位置产生了"`(1)` 与`（2）`混用"的不一致格式，已手工统一改为 ①②③ 记号，四份文档最终逐一目视复核过一遍）。
- 未运行任何 Go/前端门禁（`go test`/`go vet`/`pnpm typecheck`/`pnpm test`/storybook build）——本片未触碰任何源码文件，这些门禁与本片无关；建议验收线合入前仍按仓库惯例复跑一次作为基线确认（同 XM-INVCON0 先例）。

## not_run

- 未连接任何服务器、未执行任何数据库查询、未触碰任何生产/预发环境——设计阶段任务书明确要求"从不触碰服务器"，逐字遵守。
- 未验证文档中引用的 `invoice_users.id`/Keycloak subject 具体取值——该值来自验收线此前一次修复工具操作的记忆记录，非本片独立查证；文档正文已就地加注"执行数据迁移前必须重新查询数据库现状核对该值，本文档不作为其权威来源"，不作为可执行凭据。
- 未与开票线（Codex 或其他代理）就断言契约草案做实际的跨仓库对齐沟通——路线文档建议 XM-INVCON1 与 XM-INV-CONSOLE-ASSERT 并行开发时共享同一份契约草稿，但这次交付的技术规格本身就是那份共享草稿的起点，尚未经开票线独立评审。
- 未验证 `scripts/release-image-gate-lib.ps1` 之外是否还有其他脚本/CI 配置硬编码了"九镜像"或 `IdPMode` 三值枚举——只读到并引用了 `Get-CommonReleaseImageTag` 这一处，`docs/IMAGE-SCAN-REVIEW.md`/`RELEASE-READINESS.md` 的对应表述位置已定位并在 CR 正文点名，但未做全仓库穷举搜索确认没有第三处硬编码。

## risks

- **密钥分发方式是一个未拍板的设计选择，不是既定事实**：CR-0006 正文推荐"评审过的静态公钥清单文件"而非"实时 JWKS 端点"，并单列了一个专项确认段落陈述取舍（轮换运维成本 vs. 新增运行时依赖）。若产品负责人的判断与本文档推荐相反，`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md` 第 3.3 节需要相应改写为 JWKS 方案，届时 §5/§6/§9/§10 里引用静态清单的措辞也需要跟着调整，改动面不小，建议尽早拍板以免后续返工。
- **管理员 IP 名单在两侧各存一份、需人工保持一致**：本设计承认这是"与 `XM_INVOICE_CONSOLE_ORIGIN`/`frame-ancestors` 同类的既有操作代价"，但如实记录：若运维只改了开票侧 `AdminCIDRs` 忘了同步控制台侧 `XM_CONSOLE_ADMIN_IP_ALLOWLIST`，控制台会对着一个已经不再有效（或过窄）的名单签发/拒签，故障模式是"签发端拒绝了本该允许的 IP"（安全侧失败），不是"放行了不该放行的 IP"，方向上是安全的，但仍会造成运维困扰；建议实现时新增的校验脚本作为强制门禁项，不要停留在"建议"。
- **TOTP 强制范围的措辞依赖"哪个角色对应开票断言权限"这一后续切片才会确定的具体角色名**：本文档写作时 `staff.manage` 是唯一已知的相关 scope 名字，正文用"持有 `staff.manage` 或后续开票断言所需角色"这种留有余地的措辞处理，但如果 XM-INVCON1 实现时引入了一个与 `staff.manage` 不同的新 scope（例如更细粒度的 `invoice.admin.assertion`），CR-0006 正文与技术规格里"仅限持有 `staff.manage` 的账号"这类具体措辞需要跟着更新，本片没有替后续切片预先拍定这个 scope 名字。
- **"两侧改动清单"里引用的具体代码标识符（如 `AdminPolicy.StepUpMaxAge` 默认 10 分钟、`RequiredAMR` 默认 `["otp"]`、`loginRateLimitPerMinute=20`）均来自本片写作时刻对两个仓库源码的实读**，但这两个仓库都在活跃开发中（同一会话内有多个并行代理在改动这两个仓库），实现切片开工前应重新核对这些具体数值/字段名是否仍然成立，不应盲目当作已冻结的事实照抄。

## follow_ups

- **首要**：请产品负责人对"关于密钥分发方式，请产品负责人确认"专项段落给出裁定，以及第二阶段（Keycloak 实际停用）的放行时机与判断依据（本文档建议"一个完整发布周期 + 金丝雀干净 + 双侧审计计数吻合"，但"一个发布周期"没有具体天数，需要产品负责人认为合适即可，或另定量化标准）。
- 按路线开工 XM-AUTH-TOTP0（平台线，无前置依赖）；XM-INVCON1 与 XM-INV-CONSOLE-ASSERT 建议并行但先共享技术规格第 3 节的断言契约草案，正式实现前给开票线一次独立评审机会——本片没有与开票线做过这次对齐，只是单方面依据只读研究写就。
- `docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md` 第 10 节列了一份"与现有代码的接口点"，是为实现者定位用的参考清单，不是穷举，实现时如发现遗漏或已过时（两仓库都在快速变化）应随时更新该节，不必回来找本片修订。
- CR-0006 正文"明确不变"一节留了一个"待确认项"：Keycloak 第二阶段停用后，独立入口 `https://invoice.solov.cc/admin` 的登录方式是否也改走控制台断言（需要跳转）或直接下线——本片刻意不裁定，留给 XM-INV-KEYCLOAK-RETIRE 切片连同产品负责人一起决定。
