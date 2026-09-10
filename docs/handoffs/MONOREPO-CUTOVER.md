# Monorepo 切根执行交接

> **S0–S12 切根与隔离演练已完成。最终源码门禁、引用清扫和补充验证通过；服务器仅加载演练镜像并做隔离干跑，生产切换仍由负责人决定。**

- status：`S0_S12_VERIFIED / HANDOFF_READY / NOT_DEPLOYED`。
- branch：`ai/codex/XM-MONO-CUTOVER`；工作树 `G:/xingmang/09-wt/core-mono-cutover`。
- 起点/main：`9d430fb284e5e9b91327089ae39cef207c42f482`；本任务未合并 main、未推 GitHub。
- 已完成身份撤回的提交：`58fae6b84af4705e7fc6ca46c4ec2669032a5be3`；[最终提交与本地 release 分支回执](G:/xingmang/logs/mono-cutover-20260910-030348/FINAL-DELIVERY.json)（本交接文件不能内嵌其自身提交哈希；精确值在回执中）。
- 计划依据：[04-切根执行计划](G:/xingmang/07-docs/prompts/04-切根执行计划.md)、[原工作区交接](G:/xingmang/07-docs/工作区交接-20260910.md)。
- 证据目录：`G:/xingmang/logs/mono-cutover-20260910-030348`；[完整逐命令时序](G:/xingmang/logs/mono-cutover-20260910-030348/steps.jsonl)；[执行状态（最新节覆盖旧节）](G:/xingmang/logs/mono-cutover-20260910-030348/RUN-STATE.md)。

## summary：已完成内容与边界

开票发布链已识别 monorepo 的 `invoice/` 根；平台部署链保留 Git 顶层并使用 `platform/` 项目资产。本地门禁与服务器隔离演练已覆盖这些路径变化，生产真相源切换仍留负责人决定。
S0 四棵钉版树已一次性只读拷入 `G:/xingmang/06-upstream/pinned/`，保留 `_research/` 层级，逐棵 HEAD/干净状态核验通过；其后未再访问旧盘，未用最新上游镜像替代钉版树。
开票 `INVOICE_UPSTREAM_ROOT` 支持绝对路径覆盖与默认解析，取不到即失败；`gitDirty` 只反映 `invoice/` 子树，`source.gitHeadScope=monorepo` 明确提交范围。
平台 `repo_path` 继续等于 Git 顶层，`project_path=$repo_path/platform`；compose/env 默认路径与显式 override 的受限路径均在项目子目录下，既有根目录、分支和远端限制未放宽。
平台发布常量 `release/v0.1-launch` 保留；平台 Go 测试使用 `-race -p 1`，开票原有 `go test -race ./...` 行为保持。
阻塞期间按用户追加授权修复了测试 fixture、生成文件换行、Keycloak Netty CRITICAL 与构建器 ICU 依赖；旧失败证据与两枚签名 tag 均保留。
S7 仅向演练目录传包、展开源码并加载 9 个新 tag；S10 仅在服务器隔离目录运行新脚本干跑。**未上线；本任务未改动生产配置、真相源、账本或既有容器。**

## files_changed：实现与收尾的净变更

以下路径相对工作树根；实现基线为 `58fae6b...`，S12 增量另列。完整最终 Git 文件清单与提交见交付回执。

| 用途 | 文件 |
| --- | --- |
| 钉版与换行 | `invoice/.gitattributes`、`invoice/scripts/check-upstream-integrity.ps1` |
| 当前路径/发布约定 | `invoice/README.md`、`invoice/UPSTREAM-INTEGRITY.md`、`invoice/docs/CONFIGURATION.md`、`invoice/docs/PRODUCTION-RUNBOOK.md` |
| monorepo 发布元数据与门禁 | `invoice/scripts/release-image-gate-lib.ps1`、`invoice/scripts/release-image-gate.ps1`、`invoice/scripts/verify.ps1` |
| 开票行为/变异断言 | `invoice/scripts/test-monorepo-release-contract.ps1`、`invoice/scripts/test-release-image-gate.ps1`、`invoice/scripts/test-keycloak-source-build.ps1` |
| 仅测试路径适配 | `invoice/backend/internal/testdb/testdb_test.go` |
| Keycloak 构建和证明 | `invoice/deploy/keycloak/Dockerfile`、`invoice/deploy/keycloak/source-build/README.md`、`SourceBuild.java`、`inputs.properties`、`verify-runtime.sh`（后三项同目录） |
| 平台实现与回归 | `platform/deploy/scripts/deploy-local.sh`、`platform/tests/deploy/deploy-local-monorepo.test.sh`、`platform/tests/deploy/deploy-local.test.sh` |

S12 **已应用并验证**：[六文件清扫补丁](G:/xingmang/logs/mono-cutover-20260910-030348/S12-active-reference-cleanup.patch)，只含 launcher 注释、测试路径字符串、部署示例注释和三份历史出处文字；净 +731 字节，不放宽测试或历史排除。
S12 已更新 `docs/MONOREPO-MIGRATION.md` 并写入本交接单。六文件补丁涉及 `invoice/scripts/run-detached-lib.ps1`、`invoice/scripts/test-run-detached.ps1`、平台部署脚本注释及 `platform/docs/{modules/connector/README.md,requirements/ROUND2-INTAKE-2026-08-28.md,superpowers/specs/2026-09-03-xm-inv-keycloak-retire-design.md}`；运行脚本去掉整行注释后，其余字节与 S11 基线相同。
忽略目录中的 `invoice/release/*mono-rehearsal*.ps1` 是本次执行包装；S4–S7 经 `invoice/scripts/run-detached.ps1` 启动，包装前置 System32 并保留 UTC/退出码日志。

## commit 与不可变发布身份

| 对象 | 值/结果 |
| --- | --- |
| 初始切根实现 | `4ad83557902bc811aa9e9e25592a738fa956b77a` |
| 原演练身份提交 | `3ae105bbc4896cee098ba844645aa5bc92d67684`，S11 已撤回 |
| Keycloak 源码修复 | `7f4415d5eb4a64ec4d740bb6cdeadc72ceb3900b`，保留 |
| r2 演练身份提交 | `fce1790c3afac76e13c36bce41e8b2eba10d1030`，S11 先行撤回 |
| 严格源码构建布局门禁 | `c317c6852b30277c5552798ff711e7a583aedb3d`，保留；r2 镜像绑定此提交 |
| S8/S9 平台验收提交 | `4765acb7c629b0784fab6e21dd4c1790886859fe`，S10 bundle 绑定此提交 |
| 原 tag `rehearsal/mono-20260910-signed` | tag object `4be401bef5661ab393b1cd1ec32ed07e403233a0`；peeled `58bcf1bf5315842f00dbbdae856cc72690bc6621` |
| 新 tag `rehearsal/mono-20260910-r2-signed` | tag object `36c26fe1c6089eb0cc0071e5d50ef17e9cbe77f1`；peeled `c317c6852b30277c5552798ff711e7a583aedb3d` |

两次 `git verify-tag` 均 Good，peeled 精确等于对应候选 HEAD；两枚 tag 对象未移动。S11 还原 RC110 发布身份，不把演练身份写入生产真相；[四文件字节证明](G:/xingmang/logs/mono-cutover-20260910-030348/S11-identity-restoration-proof.json)表明其余安全修复/路径适配保留。

## tests_run：逐步实测 UTC 与退出码

下表时间均为 **2026-09-10 UTC**；使用内层命令实测值，跨多条命令的行注明范围。完整精度与每条命令在 `steps.jsonl`；启动包装 exit 0 不等于实际门禁通过。

| 步骤/实际执行 | UTC 起 | UTC 止 | 退出码 |
| --- | --- | --- | --- |
| S0 准备 | 03:03:48.7526112Z | 03:06:37.0523152Z | 0 |
| S1 新断言预期红 | 03:06:37.0932626Z | 03:15:17.8096951Z | 1（预期红） |
| S2 开票链及变异验证 | 03:15:17.9097088Z | 03:39:29.9963052Z | 0 |
| S3 原演练身份 | 03:39:30.0023148Z | 03:42:17.5755226Z | 0 |
| S4 首次：负向 fixture | 03:42:55.1679214Z | 03:43:07.1737634Z | 1 |
| S4 retry1：EOL/根断言 | 04:06:02.4831722Z | 04:06:49.2970487Z | 1 |
| S4 retry2 | 04:17:30.5375059Z | 04:29:36.9049746Z | 0 |
| S4 r2 首次：旧布局门禁 | 12:05:42.3912998Z | 12:06:59.1775370Z | 1 |
| S4 r2 retry1 | 12:20:57.3410352Z | 12:31:13.2722393Z | 0 |
| S5 原签名/验签/剥离 | 04:31:10.7496239Z | 04:31:11.3010384Z | 0/0/0 |
| S5 r2 签名/验签/剥离 | 12:32:28.7623831Z | 12:32:29.2305332Z | 0/0/0 |
| S6 首次包装预检 | 04:33:45.6832133Z | 04:33:46.6344783Z | 1 |
| S6 原版镜像门禁 | 04:43:39.3208211Z | 05:07:50.6847661Z | 1 |
| S6 r2 镜像门禁 | 12:33:39.1550628Z | 12:58:02.7874029Z | 42（预期） |
| S6 普通产物验证 | 12:58:02.7982754Z | 12:58:09.1215885Z | 0 |
| S6 严格 TransferReady | 12:58:09.1267072Z | 12:58:15.7397199Z | 0 |
| S7 本地打包/签名/导出 | 12:59:50.7987627Z | 13:00:22.4413207Z | 全部 0 |
| S7 磁盘只读查询（本地驱动） | 13:01:53.6055245Z | 13:01:56.6834135Z | 0 |
| S7 driver 首次（尚未 SSH） | 13:06:35.4246467Z | 13:06:35.5219512Z | 1 |
| S7 retry1 本地 bundle 包装 | 13:13:16.3210283Z | 13:13:16.4194384Z | 1；原生 Git 为 0 |
| S7 retry2 实际传输/展开/load | 13:17:17.1669128Z | 13:20:55.6068854Z | 0 |
| S8 平台适配/14 案例/17 变异 | 13:21:36.5657295Z | 13:23:43.4727000Z | 全部 0 |
| S9.1 go test -race -p 1 ./... | 13:24:26.8371938Z | 13:30:43.0672997Z | 0 |
| S9.2 go vet ./... | 13:30:43.0773259Z | 13:30:48.0554012Z | 0 |
| S9.3 pnpm install | 13:30:48.0612801Z | 13:31:03.1917612Z | 0 |
| S9.4 pnpm -r run typecheck | 13:31:03.1977230Z | 13:31:19.0489551Z | 0 |
| S9.5 pnpm -r run test | 13:31:19.0561582Z | 13:32:08.7070241Z | 0 |
| S9.6 有效 governance（含基线预检） | 13:33:47.0746643Z | 13:33:54.7768991Z | 0 |
| S10 初次 driver | 13:34:36.3453624Z | 13:34:57.3647706Z | 1 |
| S10 只读探针初次 | 13:36:17.7477420Z | 13:36:21.6251463Z | 1 |
| S10 只读探针修复 | 13:37:16.8035171Z | 13:37:20.5568837Z | 0 |
| S10 隔离目录恢复/真实干跑 | 13:41:19.4270954Z | 13:41:23.1975509Z | 0 |
| S11 两次身份撤回 | 13:41:57.5764489Z | 13:41:58.0420356Z | 0/0 |
| S11 首次证明包装（非撤回失败） | 13:41:58.0799874Z | 13:41:58.3859638Z | 1 |
| S11 四文件字节/其他树/tag 证明 | 13:42:44.7327607Z | 13:42:45.2920979Z | 0 |
| S11 最终源码门禁 | 13:42:46.4549316Z | 13:54:17.5630653Z | 0 |
| S12 引用清扫/定向验证/不变证明 | 13:55:50.9569818Z | 13:57:20.127072Z | 0 |

## 关键正确性与测试证据

- S1 先红；S2 开票 18/18 变异被拒绝、真实四树核验通过；S8 新 14 案例通过、17 变异由预期断言拒绝、旧 `DEPLOY-LOCAL-TEST-OK`。详见 [S2 变异矩阵](G:/xingmang/logs/mono-cutover-20260910-030348/S2-invoice-mutation-matrix-final.json)及 [S8 日志](G:/xingmang/logs/mono-cutover-20260910-030348/detached/s8-platform-root-and-mutations-20260910T132135Z-5d03/transcript.log)。
- 生成器在 `invoice/backend` 执行：`go test -count=1 ./internal/eligibilitywire/... -run ^TestGeneratedTypeScriptMatchesContract$ -update`；未手改生成值。
- TS 旧/新内容按 CRLF→LF 归一化后 SHA256 **均为 `e51f92b17b061f08f8b4f8b38a3324bf692c6bdd8321635359addff444bf68e5`**，逐字等同；73 行 LF→CRLF，增加该文件的 `.gitattributes eol=crlf`。见 [生成证明](G:/xingmang/logs/mono-cutover-20260910-030348/S4-retry2-generated-content-proof.json)。
- `invoice/backend/internal/testdb/testdb.go` 前后 SHA256 **均为 `B21914588ACA080DED2E882A711F5E50DDB662829C8199ED44C4D4F6775D787D`**；只把测试断言换为 `invoice/backend/go.mod`，保留工作树数据库隔离实现；正确测试 exit 0，旧路径 overlay 变异 exit 1。[路径测试证明](G:/xingmang/logs/mono-cutover-20260910-030348/S4-retry2-testdb-summary.json)。
- Keycloak 使用官方 26.7.2 源码提交 `289376b142480b4d600aca7acb1e4651862ed2a1` 和归档 SHA256 `5ab3b50c02ecb70cb2a8aece74617da2e74d753176b50886812abbc316300096`；以 BOM 4.1.137.Final 完整重建，非替换单个 JAR。
- 完整构建/运行证明核对 55 个 managed coordinates、18 个 Netty JAR 条目及最终运行时哈希；原运行时基底与原 HIGH 精确豁免不变。源码预构建 10:25:29.2915402Z→11:48:51.6482218Z，exit 0。
- 本地扫描结果 **0 CRITICAL / 1 HIGH**；唯一 HIGH 为旧豁免 `CVE-2026-22020 / java-21-openjdk-headless / 1:21.0.12.1.1-1.2.el9`，未扩大豁免。见 [扫描 JSON](G:/xingmang/logs/mono-cutover-20260910-030348/keycloak-source-preview-proof/result.json)，这不是生产镜像已升级的证明。
- 上述扫描 JSON 当时保留 `FunctionalGatePending=true`；后续隔离 PostgreSQL/OIDC/admin/login 资源检查在 11:54:01.6704796Z→11:54:31.4674213Z exit 0，以 [实际后续日志](G:/xingmang/logs/mono-cutover-20260910-030348/detached/keycloak-preview-functional-20260910T115401Z-a16f/transcript.log)补足时间顺序。
- Keycloak JS 根因：builder 缺 `libicu`，Kiota 原生进程失败而 npm 包装误报成功，导致 adminClient 缺失，后续 TS2307；加入精确 `libicu-67.1-10.el9_6.x86_64` 和 Maven 前 rpm 检查，无 TypeScript/Node/tsconfig 降级或跳过类型检查。
- 坏 Wireit 缓存的反例被复现；新隔离缓存检查执行 7 条真实 JS 命令、无缓存恢复/跳过、输入哈希不变，10:20:17→10:20:38.0707035Z exit 0。[原错误](G:/xingmang/logs/mono-cutover-20260910-030348/js-diagnostic-expanded-error.log)、[完整 JS 证明](G:/xingmang/logs/mono-cutover-20260910-030348/wireit-fresh-cache-complete-js.log)。
- 新布局门禁保留 FROM 精确 pin、Compose `pull_policy=never`/无 build，逐 runtime stage 校验整根替换、顺序、UID、裁剪与证明链；38 案例含 30 结构变异及 6 个真实 verify 接线案例通过。[完整明细](G:/xingmang/logs/mono-cutover-20260910-030348/source-rebuild-layout-20260910-r2/summary.md)。
- S9 首次 WSL governance 在 13:32:08.7133532Z→13:32:13.9839111Z 返回 0，但未解析 Windows worktree 指针、跳过基线，**不算有效通过**；改用 Git for Windows Bash，`GOVERNANCE_BASE_REF=HEAD` + `GOVERNANCE_REQUIRE_BASE=1` 后才计有效 0。[有效结果](G:/xingmang/logs/mono-cutover-20260910-030348/S9-governance-gitbash-result.json)。

## 保留的失败与恢复

- S4 首次负向 fixture 意外向父 monorepo 发现 Git 根；获准后隔离 Git discovery、逐例还原 ceiling，真实默认 fixture 绿色且对应变异红。随后 retry1 暴露 EOL 与根路径断言；按上述生成器/单行测试修复后通过。
- S6 初次包装仅生成文件 stat/index 不一致：HEAD、index blob、clean-filter blob 相同；获准刷新该文件索引元数据后工作字节/tag/HEAD 不变。[索引证明](G:/xingmang/logs/mono-cutover-20260910-030348/S6-index-refresh-proof.json)。
- S6 原版失败产物仍在 `invoice/release/0.1.0-mono-rehearsal1-exact1`，CRITICAL 为 `CVE-2026-75595 / io.netty:netty-handler / 4.1.136.Final`。官方 26.7.3 实际仍含该版本，未当作已证实修复；另建 r2，未覆盖旧签名/证据。[原发现](G:/xingmang/logs/mono-cutover-20260910-030348/S6-keycloak-blocking-findings.json)。
- 首次完整源码构建 06:53:28→07:50:38 exit 1，以及 JS 诊断下载/CLI/超时/输入绑定失败都保留；不能用诊断包装 exit 0 冒称内部 generator 通过。[诊断恢复记要](G:/xingmang/logs/mono-cutover-20260910-030348/JS-BUILD-RECOVERY.md)。
- S4 r2 首次失败是旧 `verify.ps1` 按两次裁剪命令计数，与完整根替换布局不兼容；修复的是严格布局验证，不是放过失败镜像；之后重跑完整 S4 通过。
- S7 首次 driver 的文化比较 `StartsWith(BOM)` 误判；改为 Ordinal，经 5 案例验证。retry1 原生 Git exit 0，但 awaiter 的 `VoidTaskResult` 污染返回值；仅修复进程封装并用真实本地进程回归验证。两次都发生在 SSH/传输前。[首次日志](G:/xingmang/logs/mono-cutover-20260910-030348/detached/s7-r2-transfer-and-load-20260910T130634Z-1aac/transcript.log)、[retry1 原生证明](G:/xingmang/logs/mono-cutover-20260910-030348/S7-r2-transfer-evidence-retry1/local-bundle-reference.process.json)。
- S10 初次已上传 bundle 并 init/fetch，空 template 未建 `.git/info`，sparse init 原生 exit 128，driver exit 1；没有跑到产品干跑。[首次原生结果](G:/xingmang/logs/mono-cutover-20260910-030348/S10-platform-execution/03-checkout-and-dry-run.process.json)。
- S10 首次只读探针受 Python 3.10 无 `hashlib.file_digest` 影响；改为分块哈希，核对局部状态后只新建缺失 info 目录，复用已传数据，未 re-fetch/re-upload/清理。原 sparse/干跑尾部字节保持，恢复成功。[部分状态](G:/xingmang/logs/mono-cutover-20260910-030348/S10-partial-state-readonly-retry1.json)、[恢复日志](G:/xingmang/logs/mono-cutover-20260910-030348/S10-platform-resume-execution/resume-sparse-and-dry-run.stdout.log)。
- S11 首次证明包装漏列两个大写 r1 负向 fixture；实际两次 revert 已成功，仅在证据 helper 补上原已观测身份映射，再次证明四文件字节正确。最终源码门禁已通过，完整日志见本交接单末尾。

- S12 首轮额外的小写搜索误匹配 Linux bind mount 中 `sock:/`、`work:/` 的末尾字符，检查 exit 1；保留原计划的大写盘符模式，只为新增的小写检查加盘符边界，未改动这些挂载配置。随后活动引用为 0。[初次记录](G:/xingmang/logs/mono-cutover-20260910-030348/S12-pre-handoff-verification.json)、[修正后证明](G:/xingmang/logs/mono-cutover-20260910-030348/S12-pre-handoff-verification-retry1.json)。

## S7：磁盘先报、三包签名与 9 个镜像

- 服务器磁盘实测 13:01:52.896011Z→13:01:53.422222Z；同一文件系统最低余量 **1,344,487,579,648 bytes / 117,271,300 inodes**，保守预算 **23,154,643,741 bytes**；13:04:20.2337541Z 已向负责人报告，早于实际上传。[报告回执](G:/xingmang/logs/mono-cutover-20260910-030348/S7-r2-disk-report-receipt.json)。
- 三包共 826,330,778 bytes，含 `images.tar` 801,667,072 bytes；六个传输文件共 826,337,847 bytes。签名在产物目录外对字节相同的 SHA256SUMS 副本进行，bash 原始重定向验签，不经文本转码。
- 服务器先验证六文件集合与哈希、三包 SHA256、checksum 签名、signed tag/commit、OCI/config/layer 绑定；传输 driver 没有自动重试、既有路径或 tag 覆盖。
- 实际 scp 13:17:27.5040843Z→13:20:03.0868278Z exit 0；stage2 13:20:07.9879140Z→13:20:51.7300701Z exit 0；最终再次比较 exit 0。
- 服务端新目录为 `/root/invoice-system/incoming/rehearsal-mono/` 及 `/root/invoice-system/app/releases/c317c6852b30277c5552798ff711e7a583aedb3d-rehearsal/`，源码根精确落在后者 `source/invoice`。
- api、pdf-scanner、tools、web、source-agent、postgres-runtime、clamav-runtime、ingest-proxy、keycloak 的 r2 tag **9/9 imageId 与 linux/amd64 均与签名清单一致**；完整 ID 见 [S7 最终结果](G:/xingmang/logs/mono-cutover-20260910-030348/S7-r2-transfer-evidence-retry2/S7-transfer-result.json)。
- 这九个镜像仅 `docker load`，未部署。演练目录现已存在，不得直接重跑创建/传输 driver 或覆盖它们；后续清理由负责人另定。

## S10：真实服务器上的验证范围

候选代码来自 `4765acb7...`，bundle SHA256 `8b6bef37899b6a6345e387cb6e0888ca1bc7bc1f8dfef8344c1753800bdaef2c`；脚本 blob `0d8d2ea0b595f371f2ba4995bc6db32c0aaad980`。见 [S10 最终结果](G:/xingmang/logs/mono-cutover-20260910-030348/S10-final-result.json)。
运行位置是 r2 演练 release 下 `s10-platform/xingmang-platform`，采用真正 sparse monorepo 和脚本真实模式（`TestMode=false`），通过 root/basename/origin/branch/精确 SHA/blob/clean 检查。
配置仅为 **三行合成 staging 配置**，与生产配置验证严格区分：`ProductionConfigurationVerified=false`、`ContainersChanged=false`、`ProductionTruthChanged=false`。
这证明新代码可在真实服务器隔离路径执行 `--dry-run`；没有验证生产 env/override 值、生产切换、重建、重启或运行健康，也没有改变服务器裸仓库/现有生产 checkout。

## S12 历史引用排除与正式扫描

正式扫描按原计划覆盖 591 个既有 Git 跟踪 `*.ps1/*.sh/*.md`，另检查 6 个活动 release 包装器及新交接单；保留原计划的大写盘符搜索，并补查独立的小写盘符。下列 13 类历史排除保持，活动引用为 **0**。预备时的 12 行/7 文件活动命中已处理；没有访问旧盘。

| 排除路径 | 命中文件 | 命中行 |
| --- | ---: | ---: |
| `docs/handoffs/` | 0 | 0 |
| `invoice/docs/handoffs/` | 56 | 116 |
| `platform/docs/handoffs/` | 84 | 119 |
| `docs/superpowers/plans/` | 0 | 0 |
| `invoice/docs/superpowers/plans/` | 49 | 51 |
| `platform/docs/superpowers/plans/` | 3 | 8 |
| `platform/docs/evidence/` | 4 | 4 |
| `platform/docs/adr/` | 1 | 1 |
| `platform/docs/architecture/` | 1 | 1 |
| `platform/docs/change-requests/` | 2 | 2 |
| `contracts/connectors/*.md` | 0 | 0 |
| `invoice/contracts/connectors/*.md` | 0 | 0 |
| `platform/contracts/connectors/*.md` | 4 | 6 |

正式证据为 [S12-final-verification.json](G:/xingmang/logs/mono-cutover-20260910-030348/S12-final-verification.json)，包含逐类排除计数、活动引用为 0、生成契约与数据库隔离实现哈希不变、运行脚本仅注释变化的证明。完整 S12 定稿/提交/本地分支 UTC 起止另见交付回执。
清扫保留中文/空格/引号测试边界，历史来源改为原仓库/相对目录/历史分支，明确未验证当前上游副本；当前迁移文档已同步。S12 对平台部署脚本仅改一处示例注释，S10 验证过的执行逻辑保持。

## not_run 与 risks

- 未执行生产 backup、shadow evaluation、roll-forward、容器重启、上线、数据库迁移或 CPA 操作；未更改 `.env.production`、生产账本、生产裸仓库/真相源，未推 GitHub、未合并 main。
- 测试名称或夹具包含 backup/restore/shadow 字样，不等于实施了实际生产备份、恢复或影子评估；本地隔离测试容器与生产容器操作分别记录。
- 未查看任何密钥文件内容；签名配置只写路径，签名/传输工具按授权使用，日志不输出密钥。没有用关闭校验、改豁免或放宽路径守卫换取通过。
- `npm audit` 依赖第三方可用性；旧审计豁免的 tag/路径基线在 monorepo 不可用，遇服务故障应报阻塞，不能冒用旧库基线。
- S9 的 `XM_TEST_DATABASE_URL` 未设置，本次平台测试通过不能声称全部真实数据库集成场景已跑；开票门禁实际包含其独立 PG15/PG18 合约检查。
- Keycloak 仍有已记录的精确 HIGH 豁免；本地修复镜像加载到服务器不代表生产容器已修复。扫描/数据库/源站结论均限定到实测候选和当时数据。
- 本地镜像、三包和服务器演练目录继续占盘；未自动清理。签名 tag 不覆盖，最终源码 HEAD 与已签 r2 镜像 HEAD 的差异已明确，不能混称同一候选。

## 负责人后续服务器真相源步骤（本任务未执行）

1. 先验收最终 S11/S12 证据和精确最终提交；决定 main 集成与本地 `release/v0.1-launch` 验收线。本地 `release/v0.1-launch` 指向最终交接提交，精确 SHA 见交付回执，服务器发布分支并未随本任务切换。
2. 决定 monorepo 如何进入 `/srv/git/xingmang-platform.git` 并衔接现有历史；不自动强推、覆盖分支或重写历史。服务器 `deploy/git-hooks/{pre-receive,post-receive}` 的 `platform/` 适配另审查。
3. 切换前核对精确提交、分支、干净状态与配置映射；本地改动、非快进、配置冲突立即停，由负责人决定，不 stash/reset/clean/删。生产配置留在 Git 外，禁止打印内容。
4. 仅在负责人已经完成并确认现有 checkout 的 monorepo 真相源切换后，才可手动执行下列 sparse 操作；这些命令本身不转换远端历史：

```bash
git -C /srv/deploy/xingmang-platform sparse-checkout init --cone
git -C /srv/deploy/xingmang-platform sparse-checkout set platform
git -C /srv/deploy/xingmang-platform rev-parse --show-toplevel
git -C /srv/deploy/xingmang-platform branch --show-current
git -C /srv/deploy/xingmang-platform rev-parse HEAD
git -C /srv/deploy/xingmang-platform status --porcelain
```

预期顶层仍是 `/srv/deploy/xingmang-platform`，分支 `release/v0.1-launch`，HEAD 为负责人批准的 monorepo SHA，资产在 `platform/`。生产 compose 新目录 `platform/deploy/compose/`；配置迁移方案由负责人确认，不自动复制或覆盖。
5. 上述前置完成后，负责人可用批准 SHA 执行 [S8-server-switch-instructions.md](G:/xingmang/logs/mono-cutover-20260910-030348/S8-server-switch-instructions.md) 的完整带 `--dry-run` 命令；该文档包含缺 SHA 即拒绝的检查。本任务未执行这些生产 checkout 命令，不允许自动去掉 `--dry-run`。
6. 干跑不证明真实上线/迁移/运行健康；生产部署、配置变更、容器操作与 GitHub 推送仍须另行授权。开票未来发布使用最终批准源码与新产物，不能把恢复 RC110 身份等同重新部署 RC110。

## 交付证据入口

- [S11 最终源码门禁完整日志](G:/xingmang/logs/mono-cutover-20260910-030348/detached/s11-restored-production-identity-source-gate-20260910T134245Z-d6ff/transcript.log)
- [S7 实际上传/加载日志](G:/xingmang/logs/mono-cutover-20260910-030348/detached/s7-r2-transfer-and-load-retry2-20260910T131716Z-acdd/transcript.log)；[服务器 stage2 原始日志](G:/xingmang/logs/mono-cutover-20260910-030348/S7-r2-transfer-evidence-retry2/stage2-verify-unpack-load.stdout.log)
- [S10 服务器隔离干跑日志](G:/xingmang/logs/mono-cutover-20260910-030348/S10-platform-resume-execution/resume-sparse-and-dry-run.stdout.log)
- [S12 补充测试日志](G:/xingmang/logs/mono-cutover-20260910-030348/S12-test-run-detached.log)；[最终提交、文件清单与边界回执](G:/xingmang/logs/mono-cutover-20260910-030348/FINAL-DELIVERY.json)
