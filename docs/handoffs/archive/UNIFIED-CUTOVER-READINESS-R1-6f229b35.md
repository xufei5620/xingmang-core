# UNIFIED-CUTOVER-READINESS — R1 修复交接

本轮按 [R1 报告](G:/xingmang/logs/unified-review-r1-20260912/REPORT.md) 修复 P0-1 至 P1-12。验收范围仍为提示词 06 的 A–F，加上负责人本轮明确要求：生产只读/可撤销冒烟、停旧栈前隔离预检、严格开票 readiness、主机 nginx 切换与恢复、离线 preflight。**服务器 D 与生产 E 均未执行；完成本地证据后停下等第二轮复审。**

## 最终证据入口与提交身份

- 当前交付身份以本文件所在的最终 Git HEAD 为准。最终完整证据统一在 [FINAL-DELIVERY.json](G:/xingmang/logs/unified-review-fixes-r1-20260912/FINAL-DELIVERY.json)；[可读实测表](G:/xingmang/logs/unified-review-fixes-r1-20260912/FINAL-DELIVERY.md) 列命令、UTC 起止、耗时、退出码、原始日志 SHA。
- 索引必须同时绑定最终 clean HEAD、七道完整门禁、11 镜像 manifest、`main..HEAD` 扫描、D 真跑、E 真切换和故意失败自动回滚。缺项、异 HEAD、平台总包装非 0 或只有定向补验，均不能宣称通过。
- D/E 内 `actual_operator_source` 与 `operator-source.json` 对实际执行的 Python/shell/审计 SQL 逐文件比 Git blob，兼容仅 CRLF 检出差异；不依赖服务器上存在 `.git`。只填配置中的 HEAD 不构成代码来源证明。
- [本轮修复清单及红绿变异](G:/xingmang/logs/unified-review-fixes-r1-20260912/REPAIRS.json) 按 finding 对应一个提交，保留最初失败、修复后通过、有效变异和还原通过。整理提交后的 SHA 不倒写历史运行记录。
- 前轮 [FINAL-DELIVERY.json](G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL-DELIVERY.json) 与 [原交接单快照](G:/xingmang/logs/unified-review-fixes-r1-20260912/F-before-r1.md) 仅为历史；本轮不能引用它们代替最终门禁或重建。最终索引在真实运行后落盘，本文不预写未来时间与 PASS。
- 受保护 main `9d430fb284e5e9b91327089ae39cef207c42f482`、完整审计 `d407875429a7ff9c3b0b5017a4b632db6324eacf`、签名 tag 保持。基线六主题提交不重写，本轮追加按 finding 整理。

## A–F 核对表

| 项 | 交付要求及证据 |
|---|---|
| A | 保留 main + 完整 audit 基线；本轮只做 R1 列出的部署、安全边界与证据修复。每个 finding 一个提交，映射见 REPAIRS.json。PG 两个官方原 digest 保持，其他业务分支冻结。 |
| B1 / B2 | 最终 HEAD 的开票完整 verify 经 run-detached 启动；平台六条完整串行，总包装 exit 0；所有 Go test 强制 -count=1。七行实测表和 wrapper 原日志见最终索引，不用定向补验替代。 |
| B3 | 最终 HEAD 重建全部 11 镜像，原 builder verify 核 manifest、archive、imageId、构建标签及基础 digest；本地构建不等于生产批准。 |
| B4 | 最终 HEAD 上 gitleaks detect --log-opts="main..HEAD" 无发现；脱敏原始报告及命令退出码见最终索引。 |
| B5 | 原 26 项逐项历史处置保留于原交接单快照：24 项已有本地实跑；Infini 真实 provider 未授权未跑，另 1 项是 always-skip 诊断占位。恢复的 identity-migrate PG 测试由本轮完整 invoice verify 的真实隔离 auth 包执行，不能把 Windows 跳过当通过。 |
| C1 | ADMIN_ROLE 与 XM_AUTH_ROLE_SCOPES 在有效 platform-api Compose 上联合校验，C1 v2 SQL 同时统计原角色与 finance.read；需至少一名两闩与 TOTP 均满足的可登录员工。本地合成 SQL 示例由最终索引记录。 |
| C2 | 恢复离线 identity-migrate 工具；运行时不恢复 Keycloak 登录。提供 production invoice_users 只读核查 SQL，迁移不按邮箱猜身份、不换发票 UUID，不自动执行 apply。见 audit/IDENTITY-MIGRATE.md。 |
| C3 | 外部 /readyz 和 API 容器 wget healthcheck 都反映开票原 11 闩，任一未就绪返回 503。源/投影模块及原始检查仍分别报告；平台业务路由保持原行为。此处遵从 R1 明确要求，替代提示词原 C3 的平台-only 200。 |
| C4 | API 单进程崩溃同时影响两端 HTTP；worker、十个源采集进程、PDF 扫描器、ClamAV、两库仍独立。风险见正式手册。 |
| D | 本地签名备份恢复到 owner 独占冻结副本、隔离网络、只读冒烟、清理副本与 shred 暂存身份真实整包执行；最终索引记录实际步骤和退出。服务器那次需负责人执行。 |
| E | 最终代码真切换与故意失败自动回滚；切换前另跑独立新栈预检且不切旧流量；主机 nginx preflight/切换/快照恢复纳入生命周期。重复运行、旧输入、锁恢复有独立测试和变异。 |
| F | 本文列服务器与负责人清单；最终实测表由唯一同 HEAD 索引闭合。完成即停止，不推送、不部署、不连接生产。 |

## 修复范围

| finding | 最终行为 |
|---|---|
| P0-1 | 候选 PG 密码 secret 使用文件；旧平台唯一既有 environment 型来源只按精确服务/secret/变量允许，原 Compose/env 哈希保全，其他形式仍拒绝。 |
| P0-2 | 来源取显式真实 HTTPS 域名；仅认证/登出可撤销探测和 GET，绝不创建/审核/开具/上传发票。可只将 TCP 接到环回预检入口，Host/SNI/证书验证保留真实 origin。停旧栈前实际跑独立冻结预检；作业只能作用相应候选项目。 |
| P0-3 / P1-4 | 成功恢复快照不被失败重试覆盖；活动进程互斥，能证明过期的锁自动保留并接管，不能证明则拒绝。详见手册。 |
| P1-5 / P1-6 | 原始管理员角色与 scope 联合拒错；容器与外部探针严格反映开票 11 闩，启动先创建依赖/源进程，再统一等待健康。 |
| P1-7 / P1-8 | 主机 nginx 公开 vhost 原字节/权限快照、真实 reload 和回滚探针；所有受管路由与 include 安全作用域核对；preflight 不访问公网，CF 用带 hash/审核者/有效期的离线材料。 |
| P1-9 / P1-10 | 恢复身份迁移工具与只读 SQL；先捕获旧原位输入及 Compose labels，再 stage 新目录，保留相对挂载语义。禁止用退役空模板代替旧部署。 |
| P1-11 / P1-12 | 实际操作脚本绑定最终 manifest；全部门禁、镜像及 D/E 在最终 HEAD 重新执行，历史通过不外推。 |
| P2-13 | 仅门禁发现范围补齐链接到 API 的 invoice 环境变量，不改变应用环境/业务配置。 |
| P2-14 / P2-15 / P2-17 | 未改：cookie 名/重复解析、平台外层 IP 白名单默认值、用户域管理员前缀属于行为变化，超出“P2 只做零风险”。 |
| P2-16 | 仅手册澄清：配置按规范化 JSON 哈希绑定（空白/键序无影响），值变更会拒绝回滚；事故时使用原配置与原状态路径，不放宽校验。 |

## 需服务器（本轮未执行）

1. 负责人在生产平台库运行 [staff-mfa.sql](../../deploy/unified/audit/staff-mfa.sql)，传入有效 ADMIN_ROLE 和完整 XM_AUTH_ROLE_SCOPES 映射；核 v2 查询原始输出、源哈希、两闩交集/TOTP覆盖及登录就绪人数。为未登记/被锁定员工完成原登记或安排恢复联系人。本地示例不代表生产覆盖。
2. 在生产 invoice 库运行 [identity-migrate-check.sql](../../deploy/unified/audit/identity-migrate-check.sql) 与 [身份只读审计](../../deploy/unified/audit/README.md)：核 invoice_users 原 tuple、平台 staff UUID、历史 actor、冲突与 FK。结果明确归入本清单；没有生产查询结果，不宣称身份已连续。迁移需按 [IDENTITY-MIGRATE.md](../../deploy/unified/audit/IDENTITY-MIGRATE.md) 先 dry-run，由负责人决定 apply，保留原 UUID/FK/AAD与审计记录。
3. 在目标主机保全旧 Compose/env 原位输入及 actual labels；新包 stage 到单独目录。两库近期备份签名与独立已审公钥、真实解密/数据规模、磁盘/NTP/端口、来源版本、secret/config 挂载权限、CF 离线材料新鲜度、主机原 vhost/include/TLS 语法均需本机实证。不查看私钥正文。
4. 负责人按 [正式切换手册](../runbooks/UNIFIED-CUTOVER.md) 跑服务器 D。冒烟使用生产真实 origin 与审核过的账户，预检连接独立新栈副本，不切在线流量、不写发票。复杂 nginx regex/不支持的 handler 配置拒绝后由负责人审定，不能删安全规则来过检。
5. 生产 E、rollback、签名包传输/stage/load、真实停写和恢复耗时尚未执行；必须另行授权。本地 D/E 秒数不能作为生产停机保证。Infini 真实 provider 未授权未测，不额外添加为 D 阻断。

## 需负责人拍板

- PG 官方镜像原 CVE：不阻塞本轮，等待官方 postgres:18 重建后换 digest；不加补丁镜像或兼容验证提交。原清单在下方，未作新的扫描。
- 两域独立备份信任锚、公钥授权与签名链；不信任包自带公钥。
- 低峰维护时间、允许停写上限、超时回滚阈值与现场恢复联系人。排期估计见手册，实际值等待服务器 D。
- MFA 未登记人员的原有登记/解锁安排；历史身份 tuple 冲突的准确映射与是否执行迁移。
- Keycloak 旧数据/secret 默认仅供回滚保留 7 天；当天或第 7 天删除的最终选择及负责人。本轮不删任何生产资源。

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



本地证据闭合后停下，等待第二轮复审；没有生产部署或 GitHub 推送授权。
