# UNIFIED-CUTOVER-READINESS — R2 自主收尾

> 历史记录（2026-09-14 更新）：以下保留 R2 当时的范围和停止条件。负责人随后授权了签名备份、调整 D/E 新鲜度判据及正式切换，实际生产结果见 [统一进程切换结果](UNIFIED-CUTOVER-RESULT.md)。本页不作为当前部署状态或后续授权的依据。

本轮依据 [R2 复审](G:/xingmang/logs/unified-review-r2-20260912/REPORT.md) 与负责人最新授权修 N-1～N-5。执行顺序是：五项修复和本地相关验证/E → 合入 main → 七道完整门禁 → gitleaks → 推 GitHub main → fiberstate 服务器 D。生产 E 不在本轮授权内。

实际 HEAD、逐项提交、UTC/退出码、制品和 GitHub 同步证明统一记录于 [R2 最终索引](G:/xingmang/logs/unified-review-fixes-r2-20260912/FINAL-DELIVERY.md)。服务器停止条件触发时以 [STOPPED.md](G:/xingmang/logs/unified-review-fixes-r2-20260912/STOPPED.md) 为结论；没有真实记录不能宣称 D 或任何后续步骤通过。R1 的 6f229b35 记录在 [历史交接](archive/UNIFIED-CUTOVER-READINESS-R1-6f229b35.md)，不作为新 HEAD 的验证替代。

## 本轮变更

| 项 | 行为与验证 |
|---|---|
| N-1 | D/独立预检恢复提交、审批、开具、真实扫描上传与双方下载 SHA；写能力须核实际冻结资源与独立监听。E/直接 smoke CLI 仍只读，会话必须撤销。 |
| N-2 | 根因是本地嵌套 nc 不传播 HTTP/1.0 close-delimited 响应的 EOF。将已实证的双向 CloseWrite 修复和真实网络回归纳入版本控制；增加隐私安全的传输阶段记录，保持30秒单次超时、无重试。不是未经证明的数据库查询修补。 |
| N-3 | 默认角色/scope 由实际 Go DefaultRoleScopeMap 导出并钉来源，显式映射不能漏角色或默认 scope；保留额外角色和原 C1 联合条件。 |
| N-4 | 目标 vhost 在暂存布局先经离线 nginx -T，再复核输入、原子安装、安装后检查与 reload；原备份恢复不依赖候选仍存在。 |
| N-5 | 按真实短路路径解释 not_evaluated；启动至第一次观察原11闩全绿使用单一300秒硬预算，保存实际响应/UTC/monotonic。超时D仍清理并写STOPPED，E仍自动回滚。 |

## 门禁与制品口径

开票完整 verify 经 run-detached，平台六项串行，Go test 强制 -count=1，平台总包装必须0。合 main 后 gitleaks 使用保留的合并前 main SHA 到交付 main 的实际区间；不能把 main..HEAD 的空区间当有效覆盖。GitHub 仅推 main，不运行旧平台脚本的全 refs --mirror。合入/推送本次已有负责人授权，其他分支及签名 tag 保持。

11 镜像 manifest 与实际操作源码绑定本次交付 HEAD；不手改 manifest 的提交号。官方 PostgreSQL 原 digest、依赖、上游与 CPA 保持。N-1 的额外写验证仅发生在新冻结卷，生产数据、原密钥与旧栈均不写。

## 服务器 D 的必停条件

1. 先实际运行 [staff-mfa.sql](../../deploy/unified/audit/staff-mfa.sql) 和 [invoice-actors.sql](../../deploy/unified/audit/invoice-actors.sql)，使用 canonical capture/identity 审计规则；原 SQL 字节与输出有SHA和UTC。C1 任一所需 finance.read 员工未注册 TOTP，或联合角色无可登录管理员，即停止，不豁免覆盖率。
2. C2 必须证明员工 tuple、原 invoice_users UUID 与历史 actor 连续；crosswalk 仅归属说明不能冒充已迁移。任何不连续、冲突或缺少必要事实均 STOP；本轮不执行 identity-migrate apply。精确 tuple 补充 SQL 在 [身份迁移只读核查](../../deploy/unified/audit/identity-migrate-check.sql)。
3. 新栈第一次运行服务启动前开始计时，原11闩首次全绿须在300秒内。保留 candidate-readiness.json 与 .response；超时停止并走D清理。没有启动则明确写 NOT_RUN，不能填写虚构耗时。
4. 两域必须已有新鲜完整签名备份、独立信任锚、原identity路径；缺必要材料则保留缺口并停止，不新做生产备份、不重签过期数据。传输前实际核剩余磁盘和接收/stage路径归属。

STOPPED.md 记录：main/GitHub/manifest、阶段、原退出码、实际C1/C2计数或阻塞、11闩耗时或未执行原因、清理状态与需负责人处理事项。服务器 D 仅在独立项目/端口/新卷中运行，不改生产 nginx、.env.production 或生产写者。D通过后同样停下，生产切换另行授权。

## 尚待服务器事实及负责人决定

本地合成结果不证明生产 MFA/身份连续、真实数据规模恢复耗时、11闩追数时间或生产网络/磁盘条件。它们只在本轮服务器真实记录中关闭。完整步骤和范围见 [服务器 D 计划](G:/xingmang/logs/unified-review-fixes-r2-20260912/server-plan/PLAN.md) 与 [正式手册](../runbooks/UNIFIED-CUTOVER.md)。

PG CVE 仍只是负责人记录，不阻塞本轮，不补基础镜像；后续官方重建换digest、Keycloak旧数据保留/删除时点、生产维护窗口与回滚联系人仍由负责人决定。本轮不删除旧生产数据。

### PG CVE 原表（原字节保留，不新增扫描）

| CVE | 来源包 | 上游修复状态（已核快照） | 处置 |
|---|---|---|---|
| [CVE-2026-13221](https://security-tracker.debian.org/tracker/CVE-2026-13221) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-42496](https://security-tracker.debian.org/tracker/CVE-2026-42496) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-42497](https://security-tracker.debian.org/tracker/CVE-2026-42497) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-48962](https://security-tracker.debian.org/tracker/CVE-2026-48962) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-57432](https://security-tracker.debian.org/tracker/CVE-2026-57432) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-57433](https://security-tracker.debian.org/tracker/CVE-2026-57433) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-8376](https://security-tracker.debian.org/tracker/CVE-2026-8376) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-9538](https://security-tracker.debian.org/tracker/CVE-2026-9538) | perl | 已修：Perl 5.40.5（包含对应模块修复及维护后继） | 等官方镜像重建后换 digest |
| [CVE-2026-76642](https://security-tracker.debian.org/tracker/CVE-2026-76642) | util-linux | 已修：2.41.6 / 2.42.3 | 等官方镜像重建后换 digest |
| [CVE-2026-78408](https://security-tracker.debian.org/tracker/CVE-2026-78408) | util-linux | 已修：2.41.6 / 2.42.3 | 等官方镜像重建后换 digest |
| [CVE-2026-78409](https://security-tracker.debian.org/tracker/CVE-2026-78409) | util-linux | 已修：2.41.6 / 2.42.3 | 等官方镜像重建后换 digest |
| [CVE-2026-78410](https://security-tracker.debian.org/tracker/CVE-2026-78410) | util-linux | 已修：2.41.6 / 2.42.3 | 等官方镜像重建后换 digest |
| [CVE-2026-6653](https://security-tracker.debian.org/tracker/CVE-2026-6653) | libxml2 | 已有修复：2.11.0 | 等官方镜像重建后换 digest |
| [CVE-2026-74860](https://security-tracker.debian.org/tracker/CVE-2026-74860) | libxml2 | 已有修复：2.15.3 | 等官方镜像重建后换 digest |
| [CVE-2026-86140](https://security-tracker.debian.org/tracker/CVE-2026-86140) | libxml2 | 已有修复：2.15.4 | 等官方镜像重建后换 digest |
| [CVE-2026-11822](https://security-tracker.debian.org/tracker/CVE-2026-11822) | sqlite3 | 已有修复：3.53.2 | 等官方镜像重建后换 digest |
| [CVE-2026-11824](https://security-tracker.debian.org/tracker/CVE-2026-11824) | sqlite3 | 已有修复：3.53.2 | 等官方镜像重建后换 digest |
| [CVE-2026-24882](https://security-tracker.debian.org/tracker/CVE-2026-24882) | gnupg2 | 已有修复：2.5.17（安全修复，另需后续RSA回归修复） | 等官方镜像重建后换 digest |
| [CVE-2025-69720](https://security-tracker.debian.org/tracker/CVE-2025-69720) | ncurses | 已有修复：6.5-20251213 | 等官方镜像重建后换 digest |
| [CVE-2026-16742](https://security-tracker.debian.org/tracker/CVE-2026-16742) | systemd | 已有修复：258.10 / 259.8 / 260.4 / 261.2 / 262 | 等官方镜像重建后换 digest |
| [CVE-2026-41992](https://security-tracker.debian.org/tracker/CVE-2026-41992) | gzip | 已有修复：1.14 | 等官方镜像重建后换 digest |
| [CVE-2026-54369](https://security-tracker.debian.org/tracker/CVE-2026-54369) | acl | 已有修复：2.4.0 | 等官方镜像重建后换 digest |
