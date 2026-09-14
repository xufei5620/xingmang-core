# 统一进程切换结果：A11D

> 2026-09-14 后续交接：本页记录原发布窗口结果；随后确认的用户读取超时、死信冻结和来源识别问题尚未修复。接手版本与诊断范围见 [代码收敛复审交接](CODE-REVIEW-HANDOFF-20260914.md)。

**统一进程生产切换已完成：正式服务器 E 为 COMMITTED/0，辅助清理完整；原30分钟观察为 OBSERVATION_COMPLETE/0。** 观察自2026-09-14 05:09:53.648231至05:39:56.129700 UTC，实际1802.481483534秒、31样本，非绿色/未知检查0、日志测量不完整0。独立服务器D、fresh本地完整D和真正故意失败/自动回滚也已通过。[资格与生产证据总收据](G:/xingmang/logs/unified-server-execute-20260912/QUALIFICATION-AND-PRODUCTION-A11D.json)

CODE_HEAD 是本轮实际构建/部署代码；原部署交接文档提交为 `35753ae247b4bf5f0f8171f3e5d3e137389241a4`。后续文档更新不表示生产代码重新部署。原push/query结果以仓库外 `G:/xingmang/logs/unified-server-execute-20260912/FINAL-DOCS-PUBLICATION-A11D.json` 为准。仓库入口：[切换手册](../runbooks/UNIFIED-CUTOVER.md)、[就绪阶段](../../deploy/rehearsal-unified/READINESS-PHASES.md)、[部署验收脚本](../../deploy/rehearsal-unified/preview_smoke.py)。下表均为UTC；原件若含时区偏移，只换算展示，原字节不改。

## 代码与交接版本

| 项目 | 已核实值 |
|---|---|
| CODE_HEAD | `a11d0b768119c7ae1c12e5dfc0c0e6953af005d7`；本轮构建、门禁、签名制品和服务器输入共同绑定 |
| 代码发布时 GitHub main | 03:36 真实 push/query 后为上述 CODE_HEAD；前一远端 HEAD 为 `73fed94ce7f86f2124e6bdbedd99a2dbefae0dd6`；最终文档发布后主分支值由外部发布收据记录 |
| 执行源码 | `G:/xingmang/09-wt/unified-server-final-20260912`，实际 HEAD=a11d 且 clean；从该 stable 工作树发布 |
| 最终文档范围（5文件） | 本文件 `docs/handoffs/UNIFIED-CUTOVER-RESULT.md`、[READINESS历史指针](UNIFIED-CUTOVER-READINESS.md)，以及 `platform/AGENTS.md`、`platform/PROJECT-CONSTITUTION.md`、`platform/docs/runbooks/GIT-WORKFLOW.md`；实际提交文件集以发布收据为准 |
| 原发布 DOCS_HEAD | `35753ae247b4bf5f0f8171f3e5d3e137389241a4`；原同步凭证为仓库外 `FINAL-DOCS-PUBLICATION-A11D.json`，不把文档提交冒充 CODE_HEAD |
| manifest SHA-256 | `def6efe3b8adc2fb102fca4daf36fede490d31116f02546b58bb7b02cad0c171` |
| source / incoming 叶名 | `final-a11d0b768119-20260913-r1` |
| 生产配置 / owner | `server-config/v13-a11d0b76` / `e4f59192cc0c48269d95e1d906f68c2d` |
| OPS | `/var/log/xingmang-ops/unified-a11d0b76-20260913` |
| v13 workflow PLAN SHA | `ec01f92a3a42b665efd01b5d11fa2d668eab5bd701fcd3a4abddf7e48973484e` |

[GitHub 同步原件](G:/xingmang/logs/unified-server-execute-20260912/FINAL-GITHUB-SYNC-PROOF-A11D.json) 绑定实际 push、query 和本轮七门禁；没有以同 HEAD 的旧查询代替本次同步。

## 本轮修复及已完成的代码资格

a11d 只修复来源恢复期间的部署 smoke 比较：实际 Go DTO oracle 证明三个受限投影字段可随来源状态合法变化，其余 13 个稳定字段仍严格相同；最终 GET 后增加原始财务快照复核，仍要求拒写返回 HTTP503 和 `SOURCE_SYNC_UNAVAILABLE`。没有修改业务 API、金额规则、schema、依赖或基础镜像。前一服务器 cf03 未留存完整实际 DTO，因此不声称复原了那次响应的具体字段值。[修复与独立复审原件](G:/xingmang/logs/expired-smoke-fix-20260914/RESULT.md)

真实 Python **3.10.12** 完整 operator 306 项于 2026-09-13 17:15:21.339646 → 17:15:34.085987 UTC 原生0；五个隔离变异均被检出，原生1；提交区间 gitleaks 于 17:19:01.439451 → 17:19:01.796285 UTC 原生0。Go oracle 首次无效测试数据失败与修正后真实通过的原件均保留，不把准备或语法检查称为服务器执行。[306 项](G:/xingmang/logs/expired-smoke-fix-20260914/09-python310-full-operator.json)、[变异证明](G:/xingmang/logs/expired-smoke-fix-20260914/MUTATIONS.json)

### 最终七门禁

[总记录](G:/xingmang/logs/unified-server-execute-20260912/gates-final-a11d-01/RESULT.json)：`sevenComplete=true`，总退出码、`platformWrapper` 和 `detachedWrapperExitCode` 均0；运行时前后为同一 clean a11d。invoice 使用原完整 verify，随后平台六项，没有局部测试替代。

| 门禁 | 开始 UTC（2026-09-13） | 结束 UTC | 原生码 |
|---|---|---|---:|
| invoice-full-verify | 17:24:49.785673 | 17:39:04.670839 | 0 |
| platform-go-test | 17:39:06.647208 | 17:43:02.554819 | 0 |
| platform-go-vet | 17:43:02.831690 | 17:43:11.276949 | 0 |
| platform-pnpm-install | 17:43:11.553711 | 17:43:11.908315 | 0 |
| platform-typecheck | 17:43:12.168809 | 17:43:23.121989 | 0 |
| platform-test | 17:43:23.379989 | 17:44:13.153757 | 0 |
| platform-governance | 17:44:13.438321 | 17:44:22.103817 | 0 |

### 构建与本地原正向结果

| 步骤 | UTC 起止（2026-09-13） | 实际结论 |
|---|---|---|
| 11 镜像 build + verify | 17:20:04.890962 → 17:22:53.645709 | PASS / 0，前后源码 clean 同 HEAD |
| 原本地 D | 17:28:10.990448 → 17:31:08.991105 | PASS / 0 |
| 原本地 E | 17:32:31.909312 → 17:38:12.380797 | COMMITTED / 0 |
| 原本地显式 rollback | 17:39:10.943797 → 17:41:04.130952 | ROLLED_BACK / 0 |
| 制品签名后的原验签 | 17:37:52.738356 → 17:37:52.757133 | 原生0 |

原件：[build](G:/xingmang/logs/unified-server-execute-20260912/final-build-a11d-cn-01/RESULT.json)、[本地 D](G:/xingmang/logs/unified-server-execute-20260912/local-e-plan/runs/D-a11d-01/RESULT.json)、[本地 E](G:/xingmang/logs/unified-server-execute-20260912/local-e-plan/runs/E-a11d-01/RESULT.json)、[显式回滚](G:/xingmang/logs/unified-server-execute-20260912/local-e-plan/runs/rollback-a11d-01/RESULT.json)、[验签](G:/xingmang/logs/unified-server-execute-20260912/final-transfer-plan/prepared-a11d0b76-01/sign-results/verify.json)。这些正向收据保留原命名空间/配置/快照，没有改名重绑为 fresh 环境结果。

原 `negative-a11d-01` 的 FAIL/FileNotFoundError（native1、外 wrapper2）及 fresh D01–D05 失败全部保留；其中 D04 于 2026-09-14 04:20:24.147887 → 04:21:10.520685 UTC 原生1。它们没有被新结果覆盖或改名为 PASS。[旧负向原件](G:/xingmang/logs/unified-server-execute-20260912/local-e-plan/negative-a11d-01/execution/RESULT.json)、[fresh D04](G:/xingmang/logs/unified-server-execute-20260912/fresh-local-old-a11d-01/runs/D-a11d-fresh-04/RESULT.json)

全新 `20260914b` 环境最终使用 `fresh-local-old-a11d-01/local-e-bound-04` 与实际 `capture-02` 备份：完整 **D06 在 04:41:14.597509 → 04:44:02.692429 UTC PASS/0**；随后真正负向 wrapper 在 **04:45:47.999027 → 04:53:00.929072 UTC PASS/0**。04:51:11.098746 → 04:51:13.197592 UTC 于切流后的 public smoke 注入 `HTTP_TRANSPORT_FAILED`，原 E native1 后自动 `ROLLED_BACK`，回滚使用真实配置与原 public probes，没有手工救援。阶段证明 D BASE35.213s、E BASE39.946s、source freshness40.518s 均通过；这次是实际故障与回滚结果，不是生成器0。[fresh D06](G:/xingmang/logs/unified-server-execute-20260912/fresh-local-old-a11d-01/runs/D-a11d-fresh-06/RESULT.json)、[真正负向及阶段证明](G:/xingmang/logs/unified-server-execute-20260912/fresh-local-old-a11d-01/negative-a11d-fresh-01/execution/RESULT.json)、[wrapper原生记录](G:/xingmang/logs/unified-server-execute-20260912/fresh-local-old-a11d-01/negative-a11d-fresh-01/wrapper.json)

## 已完成的服务器发布前置

| 步骤 | UTC 起止（2026-09-14） | 已完成结果 |
|---|---|---|
| GitHub push | 03:36:42.499394 → 03:36:46.057530 | 原生0，推送 a11d |
| GitHub main 只读 query | 03:36:55.484603 → 03:36:57.187309 | 原生0，远端确认为 a11d |
| fresh C1 platform SQL | 03:43:27.499482 → 03:43:27.657398 | 原生0，finance.read/TOTP=1/1，admin交集/login-ready=1/1 |
| fresh C2 invoice SQL | 03:43:27.657817 → 03:43:27.958899 | 原生0；原 unresolved actor blocker 保留 |
| owner exact-event effective 验证 | 03:44:42.112130 → 03:44:42.198248 | 原生0，effective blockers为空 |
| server stage/load | 03:49:51.567462 → 03:50:16.684550 | 原生0，11 镜像及完整 source 绑定；没有启动生产切换 |
| general public | 03:51:41.714532 → 03:51:45.748694 | 原生0，confirmed |
| census 证据传输 | 03:53:29.434336 → 03:53:33.287172 | 原生0，confirmed |
| native nginx / operator | 03:53:33.363902 → 03:53:39.365542 | 两步原生0，confirmed |
| 原只读 baseline | 04:05:49.329571 → 04:05:50.046748 | 原生0，两域ready200、dead0/0、ledger0032、网络不重叠 |
| 最终 E 前 C1 原 SQL | 04:54:50.831312 → 04:54:51.077061 | PASS/0；finance.read/TOTP、admin交集/login-ready均1/1，数据库身份与原public输入未变 |
| E 前双备份重验 | 04:54:50.621047 → 04:54:55.516145 | 原生0，两域Good；platform18.155h、invoice18.257h，capture时间未改 |
| E 前状态核对 | 04:55:13.439605 → 04:55:13.989440 | 原生0，28网络与baseline相同、两域ready200、dead0/0、ledger0032 |

完整 source/context 从实际 stable 重新渲染：3071 条 inventory、3059 个 context 成员、41538247 字节逐 Git blob 核对；34 个公开操作文件中的全部21个 Python 模块纳入 stage。签名/逐对象验证先于 load，manifest 的 `productionReady=false` 原值保留。[制品与绑定](G:/xingmang/logs/unified-server-execute-20260912/final-transfer-plan/README-A11D.md)、[stage/load 原件](G:/xingmang/logs/unified-server-execute-20260912/server/stage-load-a11d-01/RESULT.json)

### 历史审计说明

C2 仅使用已批准的完整事件 `9e0c885d-d2f6-43c2-afa9-c7f49d17627b`、actor `deployment-bootstrap`、action `admin_settings.initialize`、UTC `2026-08-21T08:21:04.813206Z`。负责人已核定它是首次部署的引导作业，属于系统操作，并非人员身份；原事件未修改、未删除，没有新增通用 system/bootstrap 豁免。raw STOP/SQL/native时间戳不改，新 sidecar 绑定本次实际 a11d capture 与原授权；C1 时效仍按原24小时、inventory→capture按原15分钟。[C1/C2 effective 原件](G:/xingmang/logs/unified-server-execute-20260912/final-census-a11d0b76-01/owner-effective/RECEIPT.json)

v13 继承已修复的完整 PUBLIC_ROOT/TLS/OPS renderer；本轮全生成文本/配置路径及 Cloudflare 原时效检查已完成原生0，实际 workflow 后按 general-public→census→native→baseline→独立 D 顺序执行。baseline 原件保留两域 /readyz、开票队列与账本、磁盘及网络的切换前对照；旧栈与 nginx 的输入绑定见 native operator 原件。[native operator](G:/xingmang/logs/unified-server-execute-20260912/server/native-operator-a11d-01/RESULT.json)、[baseline](G:/xingmang/logs/unified-server-execute-20260912/server/observation-baseline-a11d-01/RESULT.json)

## 最终运行结果

| 最终项 | 结果 | 实际内容 |
|---|---|---|
| 独立服务器 D | **PASS / 0** | 04:10:55.807072 → 04:22:16.490063 UTC；BASE36.922s、唯一预期source_watermark_expired、cleanup=true |
| fresh 本地完整 D06 | **PASS / 0** | 04:41:14.597509 → 04:44:02.692429 UTC；实际bound04/capture02 |
| fresh 本地故意失败/自动 rollback | **PASS / verifier 0** | 原native1、HTTP_TRANSPORT_FAILED真实注入，自动ROLLED_BACK；wrapper04:45:47.999027 → 04:53:00.929072 UTC |
| 正式服务器 E | **COMMITTED / 0** | 04:56:11.943328 → 05:09:01.512559 UTC；auxiliary cleanup=true，cleanup_failures/audit_errors均空 |
| E 后30分钟观察 | **OBSERVATION_COMPLETE / 0** | 05:09:53.648231 → 05:39:56.129700 UTC，1802.481483534秒、31样本；非绿色/未知0、日志测量不完整0 |
| DOCS_HEAD / 文档发布 | 以文件提交历史和外部发布收据为准 | 具体SHA与实际push/query原件写仓库外收据；本文不预先声明文档发布成功 |

本轮服务器 D 输出为 `/var/log/xingmang-ops/unified-a11d0b76-20260913/server-D-20260914T041055Z-3588250`，最终 PASS/0、cleanup=true。BASE 自 04:20:32.025753 到 04:21:08.947906 UTC，实测36.922156930秒；唯一允许的过期分类为 `source_watermark_expired`。04:32 的 E-prerequisite-D 只读核对绑定真实 outer SHA `b4724800fb916920bb0942746f1c177ffc3317e4b83921e19f0f1abd930d6d54`、canonical SHA `468890253039b7aa02c240e5a6513f8dabc8102a1ba470f7924ebeee251dcc7a`，不是用派发0或早期 STARTING 摘要代替最终 D。[D最终原件](G:/xingmang/logs/unified-server-execute-20260912/server/D-observe-a11d-04/RESULT.json)、[E前D原件绑定](G:/xingmang/logs/unified-server-execute-20260912/server/E-prerequisite-D-a11d-01/RESULT.json)

正式 E 的 canonical 与外层结果均为 COMMITTED/0。内部 D 实际 PASS、cleanup=true，BASE31.715s；E BASE28.976s，source freshness 在 **05:08:39.021625 → 05:08:56.639658 UTC READY，17.618034154s**；public smoke 在05:08:56.641107 → 05:08:58.034284 UTC PASS/0。外层于05:09:01.512559 UTC结束，辅助清理完整，`cleanup_failures=[]`、`audit_errors=[]`。[E最终原件](G:/xingmang/logs/unified-server-execute-20260912/server/E-observe-a11d-05/RESULT.json)、[E与内部D阶段原件](G:/xingmang/logs/unified-server-execute-20260912/server/E-canonical-progress-a11d-01/RESULT.json)

最终 C1 原 SQL 已实际 PASS，所有 admin/finance-read/TOTP/login-ready 计数均为1，数据库身份与原 public SHA 保持；双备份签名Good、全部cipher hash/原capture时效通过，E前两域ready与网络/ledger核对通过。[最终C1](G:/xingmang/logs/unified-server-execute-20260912/server/precutover-C1-a11d-01/RESULT.json)、[备份重验](G:/xingmang/logs/unified-server-execute-20260912/server/preE-backup-a11d-01/RESULT.json)、[E前状态](G:/xingmang/logs/unified-server-execute-20260912/server/preE-state-a11d-01/RESULT.json)。原 watcher 在 E COMMITTED 后单次派发，job=`/var/log/xingmang-ops/unified-a11d0b76-20260913/observation-watch-01`、PID4036007；最终 supervisor 为 `CHILD_COMPLETED`、native0，观察结果为 `OBSERVATION_COMPLETE`、exit0。完整31样本耗时1802.481483534秒，非绿色/未知0、日志测量不完整0；最终sample-summary23与观察原件同SHA `346d40e26487b7c9715ad4ce9b6575b218c6287688c852928efd3bfc6b207e1d`。API的ID/StartedAt稳定，重启计数0；日志ERROR latest/max均0，累计窗口不求和，这些计数是信息记录，不新增验收门槛。[观察完成原件](G:/xingmang/logs/unified-server-execute-20260912/server/observation-watch-observe-a11d-02/RESULT.json)、[最终样本摘要](G:/xingmang/logs/unified-server-execute-20260912/server/watch-sample-summary-a11d-23/RESULT.json)

## 已知辅助问题与保留边界

| 分类 | 实际处理与当前边界 |
|---|---|
| 先前版本的代码 smoke 缺陷 | 0D86 的 preview mode 与 e824 的 Python3.10 timestamp 问题由后续提交处理；73fed/cf03 的来源恢复 DTO 比较由 a11d 修复。旧 STOP 原件保留，不冒称旧版本当时成功 |
| 服务器配置生成辅助错误 | 旧 v10 Cloudflare review 过期、v11 TLS PID/OPS 路径遗漏保留；后续完整renderer及全 `.conf`/workflow检查修正，v13使用修正机制。没有通过改时钟、延长时效或放宽PID检查过关 |
| fresh 本地传输与参数绑定 | 修正 helper层 Docker DNS目标、同image的provider引用及原每阶段必填env列表；属于本地执行辅助输入问题，没有改业务逻辑或新增产品漏洞结论 |
| fresh 候选 DB/卷权限输入 | 修正candidate新DSN目标/权限和空document卷owner，保留旧文件及全部失败；最终bound04/capture02的完整D06和真正负向回滚均以新的实际原件通过，没有靠修改财务/恢复校验过关 |

[辅助修复及失败归档摘要](G:/xingmang/logs/unified-server-execute-20260912/PROGRESS.md)、[候选DSN修正原件](G:/xingmang/logs/unified-server-execute-20260912/fresh-local-old-a11d-01/application-execution/unified-dsn-inputs-01.json)。初始生成器/参数/测试环境错误与非零收据均保留；不列临时文件长清单，也不借后续成功抹去此前失败。

公共 smoke 使用真实域名和TLS，并通过 host连接 `127.0.0.1:443` 验证服务器本机入口；未独立验证CDN外部链路，也未进行真人登录/业务验收。自动化验收仅使用合成身份，没有取得或使用真人凭据；不新增验收项。范围限本轮统一部署及专属synthetic/frozen资源，不操作其它项目；其它网络只作既有只读冲突检查。私钥、DSN、密码、TOTP只由既定工具按路径消费，不读取或输出正文。固定PG镜像/既有CVE不在本轮变更范围，不追加PG补丁或新增CVE阻断项；原部署门禁、财务约束和失败停止边界保留。
