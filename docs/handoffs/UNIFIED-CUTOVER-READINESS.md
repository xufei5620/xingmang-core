# UNIFIED-CUTOVER-READINESS

本交接单按 [06-统一进程-可部署终点.md](G:/xingmang/07-docs/prompts/06-统一进程-可部署终点.md) 的闭合 A–F 清单记录已经发生的本地实测。**B1/B2 r5完整门禁及D/E r3整包、真切换、显式回滚、故意失败自动回滚均已完成；最终六主题冻结与最终包资格须由下列固定证据索引闭合后才能宣告交付完成。** 这里没有生产部署结论。

所有时间为2026-09-11本机实测UTC；“✓”仅证明该行写明的源码/镜像/本地范围。本轮未连接服务器执行部署、备份、恢复、重启或环境修改；未推GitHub、动main/tag、读密钥正文、改上游/CPA、升级依赖或修基础镜像CVE。测试中的**1元人民币仅为本地合成输入，产品默认200元未变**；资金规则、金融迁移和其他业务源码未随统一进程改动。

## 源码、镜像与交付索引

- 基线`d0adca1acf56ac717e0a4803652fe94e4ba4b267`，父点为main `9d430fb284e5e9b91327089ae39cef207c42f482`与完整audit `d407875429a7ff9c3b0b5017a4b632db6324eacf`。前置audit实际测试`fb2853740a77dfc74262355554f5f057a2ff8d8b`，七gate及26份raw logs核验保留。
- 最新完整B1/B2测试对象：clean `1dd40b4814096f2b0c4378ab66b676eef8aa03c8`，树`893ce22786f1e8e000515dd7e1cffcf6eca2c974`，前后未变。
- r3的11镜像构建与D/E运行镜像来自`6f40cb2758fc7cdfa8b5976968bdf36303ec6ae4`；manifest SHA256 `5452175c73b461ca5825f9ad6f8336e01fe57f1d4dfce4e652392044a230c553`。镜像与操作脚本分别绑定，不能统称为同一个最终HEAD。
- D/E运行当时操作脚本为`d2834dcd87ac11148eb663d1461e4f231f31fd6d`加已记录的未提交文件，运行前后字节稳定。其中restore SHA256 `7c1c7490c839dab3068b007c91f23fc79086202be7f0a177db9be6e26f11368e`，lifecycle SHA256 `015e1cffb8a6edb5ccb686f94392b381e71e8bfef1ecf0e23a4bae3ad57205bb`。所有操作结束后才提交为`53d44209e365ed5d1852d45df72b024d2790bc48`；[原字节映射](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/source-completion-53d44209.json)中的11文件已实际与canonical1dd40b48复核相等，不倒写旧日志HEAD。
- 最终六组提交、message的红绿变异映射及与受验树相等的证明固定在[commits/final-six-proof.json](G:/xingmang/logs/unified-deploy-endpoint-20260912/commits/final-six-proof.json)。该记录须由主线程实际生成核验，当前不猜未来提交号。
- 最终交付索引固定为[FINAL-DELIVERY.json](G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL-DELIVERY.json)：记录六主题冻结后的完整11镜像包、真实B3/B4及最后D/必要E的HEAD、manifest、退出码和原始证据。**索引尚未实际完成时不宣称整件交付完成；历史r3通过不能自动给未来包贴PASS。** 仓库F记录归档前事实与这个固定路径，后续资格证据写外部sidecar，不回填仓库F制造新的受验HEAD。

## A–E逐项核对

| 项 | 状态 | 实际范围 / 边界 | 证据 |
|---|---|---|---|
| A1 main + 完整audit | ✓ | 原main与完整审计提交保持；从d0adca1a合并基线重整。此前七gate属于审计证据，未替代本轮完整B1/B2。 | [本轮记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/PROGRESS.md) / [审计原证据](G:/xingmang/logs/full-audit-exact-head-20260912/EVIDENCE-VERIFIED.json) |
| A2 六主题提交 | 冻结证明待生成 | 运行时抽取、员工身份解析、原生管理页、统一构建compose、旧登录链删除、测试证据六组；最终树相等证明与commit列表由固定外部记录交付，不预填未知SHA。 | [最终六组证明（交付前生成）](G:/xingmang/logs/unified-deploy-endpoint-20260912/commits/final-six-proof.json) |
| A3 提交说明和红绿变异 | 冻结证明待生成 | 已有各组行为失败测试/有效变异/恢复绿；六组最终message逐组引用实际证据，须与A2同一记录核对。 | [六组message与证据映射](G:/xingmang/logs/unified-deploy-endpoint-20260912/commits/final-six-proof.json) / [runtime](G:/xingmang/logs/unified-deploy-endpoint-20260912/runtime/mutations/results.json) / [身份](G:/xingmang/logs/unified-deploy-endpoint-20260912/identity/HANDOFF.md) / [原生页](G:/xingmang/logs/unified-deploy-endpoint-20260912/frontend/REPORT.md) |
| A4 不相关业务留原分支 | ✓ 已核范围 | 旧card/funding/eligibility/notification/PG补丁排除；12 scopes、472路径的mode/blob与d4078754完全相等。1元仅本地合成测试输入，产品默认200元与资金业务源码不改。最终冻结关系由A2证明闭合。 | [排除清单](G:/xingmang/logs/unified-deploy-endpoint-20260912/inventory/cr0010-reconstruction.md) / [472文件证明](G:/xingmang/logs/unified-deploy-endpoint-20260912/inventory/financial-baseline-proof-r3.json) |
| A5 原官方PG digest | ✓ | 两PG角色保持原官方digest，无补丁镜像；原22CVE表原字节留待负责人，不新扫描、不新增零漏洞门槛。 | [实际库存](G:/xingmang/logs/unified-deploy-endpoint-20260912/images-r3/INVENTORY.md) / [原表](G:/xingmang/logs/unified-deploy-endpoint-20260912/inventory/pg-owner-table-unchanged.md) |
| B1 platform六完整门禁 | ✓ r5 | 六项及wrapper/detached均0，前后同clean1dd40b48；12份原日志SHA实际吻合。 | [完整suite](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) / [原日志复核](G:/xingmang/logs/unified-deploy-endpoint-20260912/f-final-review/RAW-GATE-VERIFICATION.json) |
| B2 invoice完整verify | ✓ r5 | run-detached实际执行整条verify，903.853秒，verify/wrapper/detached均0；前后同clean1dd40b48，两份原日志SHA实际吻合。原完整检查保留，无Skip/waiver。 | [完整verify](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/invoice/verify.json) / [旧→新逐项映射](G:/xingmang/logs/unified-deploy-endpoint-20260912/verify-adaptation/REPORT.md) / [原日志与detached](G:/xingmang/logs/unified-deploy-endpoint-20260912/f-final-review/RAW-GATE-VERIFICATION.json) |
| B3 全部11镜像构建/校验 | ✓ r3；最终包见索引 | r3 build/verify均0，11镜像名/reference/imageId/base digest/archive绑定6f40。六主题冻结后的最终全包身份与真实验证由FINAL-DELIVERY记录，不借用旧manifest身份。 | [r3 manifest](G:/xingmang/logs/unified-deploy-endpoint-20260912/images-r3/manifest.json) / [11镜像库存](G:/xingmang/logs/unified-deploy-endpoint-20260912/images-r3/INVENTORY.md) / [最终包索引](G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL-DELIVERY.json) |
| B4 main..HEAD gitleaks | ✓ 已执行区间；最终见索引 | 6f40区间实际0；精确公开tag/hash fingerprint审查保留，没有宽泛路径忽略。冻结后真实main..最终HEAD结果单独记录。 | [原命令](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r2/gitleaks-command.json) / [最终区间索引](G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL-DELIVERY.json) |
| B5 原26项逐项处置 | ✓ | 24项实际PASS；真实Infini未测，always-skip无业务体占位单列。1409选定源码绑定995d1447，0差异；原26行见后文。 | [24项实际结果](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/SUMMARY.json) / [Git内容绑定](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/checkpoint-995d1447-binding.json) |
| C1 MFA覆盖 | ✓ 本地；生产需负责人 | 实际只读finance.read总数1、TOTP登记1；另有4/2及enabled3/1负向合成。未登记者先登记或明确临时锁定；统计不代表OTP/引用文件/当前step-up已核。 | [只读SQL](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/deploy/unified/audit/staff-mfa.sql) / [实际1/1](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/mfa-query-record.json) / [负向与变异](G:/xingmang/logs/unified-deploy-endpoint-20260912/identity-audit/RESULT.md) |
| C2 员工历史身份 | ✓ 工具与规则 | 精确旧issuer/subject→既有invoice UUID→已存在staff UUID，负责人证据存外部受控crosswalk；runtime不读crosswalk，不按邮箱猜合、不改FK/AAD。未映射/冲突/仅归属报不连续，生产需核对。 | [已集成工具](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/deploy/unified/audit/README.md) / [真实合成SQL结果](G:/xingmang/logs/unified-deploy-endpoint-20260912/identity-audit/RESULT.md) |
| C3 readyz三模块/11闩 | ✓ | platform决定控制台HTTP就绪；invoice_sources/invoice_projection报告真实状态，未执行为not_evaluated；原11闩和严格开票探针不变。D/E要求三模块及invoice_ready全部真。 | [RED](G:/xingmang/logs/unified-deploy-endpoint-20260912/runtime/c3-red.json) / [GREEN](G:/xingmang/logs/unified-deploy-endpoint-20260912/runtime/c3-green.json) / [5个有效变异](G:/xingmang/logs/unified-deploy-endpoint-20260912/runtime/mutations/results.json) / [真实HTTP](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.md) |
| C4 单进程影响面 | ✓ | 两端API、会话和进程内开票任务共用Go进程，崩溃同时中断；platform worker、十路采集、PDF scanner/ClamAV及库仍是独立进程，业务依赖不会因此消失。 | [手册风险段](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/docs/runbooks/UNIFIED-CUTOVER.md) |
| D1 两库签名备份恢复 | ✓ r3本地 | 两域签名/age→冻结双库及metadata/document/source-state真实验证；十路source-agent-prod check-state全0。未读密钥正文，临时身份仅路径交工具。 | [原receipt](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/D-r3-11/rehearsal-pass.json) / [逐步事件索引](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.json) |
| D2 独立项目与端口 | ✓ r3本地 | 独立frozen项目和owner资源、隔离端口及精确运行镜像清单完成；原输入只读、ClamAV用owner副本。不能把loopback合成前置当服务器NTP/CF事实。 | [runtime_inventory/host_preflight](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/D-r3-11/rehearsal-pass.json) |
| D3 真实HTTP冒烟 | ✓ r3 10/10 | SUB登录提交、NEW登录非空隔离、员工真实TOTP及原生管理页/审批/扫描上传下载、三模块readyz、旧OIDC/断言拒绝均由完整smoke实际通过。 | [HTTP10项及原smoke引用](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.md) |
| D4 冻结资源与临时身份清理 | ✓ r3本地 | 原入口完成8冻结卷清理与2暂存身份GNUshred，原身份/输入保留；原旧22与relay为后续最终资格验收保留，不宣称全部环境已销毁。 | [cleanup/identity_cleanup](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/D-r3-11/rehearsal-pass.json) / [清理事件与保留资源](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.json) |
| D5 合成整包实跑 | ✓ r3；最终绑定见索引 | D11从入口到清理真实PASS，外层OS0/receipt0；不是dry-run或局部单测。最后冻结包的同身份实际D留在外部索引；服务器D负责人执行。 | [原OS结果](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/D-r3-11/RESULT.json) / [最终资格索引](G:/xingmang/logs/unified-deploy-endpoint-20260912/FINAL-DELIVERY.json) |
| E脚本真切换/回滚/故障/dry-run | ✓ r3本地 | 真实COMMITTED0、显式ROLLED_BACK0；故意invoice-migrate73触发自动回滚，外层与记录保持1、旧4+18精确验证及双ready成功；两dry-run原0。 | [全部实际结果](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.md) |
| E1 fail-closed前置 | ✓ 本地与手册 | D标记、C1结果、两库签名备份新鲜度、磁盘/env/网络等由实际consumer执行；服务器D/C1/主机事实仍需负责人另跑。 | [前置命令](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/docs/runbooks/UNIFIED-CUTOVER.md) / [实际preflight与故障路径](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.json) |
| E2 顺序/验证/停机估计 | ✓ | 正式手册给出顺序、每步验证及窗口估计；本地切换/显式回滚总窗分别237.656/123.848秒。生产实际停写时长未测，负责人安排低峰与回滚预算。 | [手册](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/docs/runbooks/UNIFIED-CUTOVER.md) / [切换](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-cutover-01/RESULT.json) / [回滚](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-rollback-01/RESULT.json) |
| E3 原4+18回滚/0032/权限 | ✓ 本地 | 原平台4+开票18的精确镜像/env/compose/卷核验及两ready通过；0032和原32迁移保持，invoice_app权限作业重放，不删ledger、不将旧三binary替代完整拓扑。 | [oldSnapshot/rollbackEvidence](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.json) / [迁移与财务基线](G:/xingmang/logs/unified-deploy-endpoint-20260912/inventory/financial-baseline-proof-r3.json) |
| E4 Keycloak退役/7天清理 | ✓ 手册；生产未执行 | 代码登录链已删；当天停止流量/配置/容器，数据和相关secret仅回滚保留7天，第7天精确对象清理及库存划除模板已集成。当天即删需负责人另选，手册注明重装恢复估计。 | [第7天命令与风险](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/docs/runbooks/UNIFIED-CUTOVER.md) / [旧入口退役验证](G:/xingmang/logs/unified-deploy-endpoint-20260912/identity/HANDOFF.md) |
| E5 旧发布链退役/新签名交付 | ✓ 手册和本地consumer | d2834dcd手册已正式集成；新11镜像签名、原字节核验、传输/stage/load步骤及两旧链退役明确。服务器外传与load未执行。两域备份签名锚须独立治理。 | [正式手册](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/docs/runbooks/UNIFIED-CUTOVER.md) / [实际顺序/原字节/错误传播](G:/xingmang/logs/unified-deploy-endpoint-20260912/runbook-consumer-adaptation/RESULT.md) |
| E6 风险与影响面 | ✓ | C4共命运、身份/MFA、备份/回滚、停机估计及Keycloak选择已在手册；本地真实失败及修复、生产未验证边界列于本交接单。 | [风险段](G:/xingmang/09-wt/unified-deploy-endpoint-20260912/docs/runbooks/UNIFIED-CUTOVER.md) / [实际D/E](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.md) |

## 门禁与操作实测

以下按实际外层命令计时；D receipt及故障自动回滚内层另列，避免混淆。B1/B2原14份日志SHA及两个detached退出码已核对。

| 命令 / 实际操作 | UTC开始 | UTC结束 | 耗时 | 退出码 | 证据 |
|---|---|---|---:|---:|---|
| `G:/cache/go-mod/golang.org/toolchain@v0.0.1-go1.27.0.windows-amd64/bin/go.exe test -race -p 1 ./...` | 2026-09-11T21:25:33.071553+00:00 | 2026-09-11T21:25:34.833261+00:00 | 1.762s | 0 | [platform-go-test](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| `G:/cache/go-mod/golang.org/toolchain@v0.0.1-go1.27.0.windows-amd64/bin/go.exe vet ./...` | 2026-09-11T21:25:35.115704+00:00 | 2026-09-11T21:25:36.225411+00:00 | 1.110s | 0 | [platform-go-vet](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| `pnpm.cmd install --config.verify-deps-before-run=false --frozen-lockfile` | 2026-09-11T21:25:36.605702+00:00 | 2026-09-11T21:25:37.004404+00:00 | 0.399s | 0 | [platform-pnpm-install](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| `pnpm.cmd -r run typecheck` | 2026-09-11T21:25:37.436273+00:00 | 2026-09-11T21:25:52.053763+00:00 | 14.617s | 0 | [platform-typecheck](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| `pnpm.cmd -r run test` | 2026-09-11T21:25:52.325298+00:00 | 2026-09-11T21:26:29.210104+00:00 | 36.885s | 0 | [platform-test](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| `D:/Git/bin/bash.exe scripts/check-governance.sh` | 2026-09-11T21:26:29.546899+00:00 | 2026-09-11T21:26:38.543045+00:00 | 8.996s | 0 | [platform-governance](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| B1六项wrapper | 2026-09-11T21:25:32.811340+00:00 | 2026-09-11T21:26:38.944432+00:00 | 66.133s | 0 | [suite](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/platform/suite.json) |
| B2 `run-detached.ps1` → `scripts/verify.ps1`完整执行 | 2026-09-11T21:25:31.893475+00:00 | 2026-09-11T21:40:35.643114+00:00 | 903.853s | 0（verify/wrapper/detached） | [verify](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r5/invoice/verify.json) |
| 11镜像build | 2026-09-11T19:33:22.787744+00:00 | 2026-09-11T19:37:05.761398+00:00 | 222.974s | 0 | [原命令](G:/xingmang/logs/unified-deploy-endpoint-20260912/build/images-r3-command.json) |
| 11镜像verify | 2026-09-11T19:37:58.608415+00:00 | 2026-09-11T19:38:02.120675+00:00 | 3.512s | 0 | [原命令](G:/xingmang/logs/unified-deploy-endpoint-20260912/build/images-r3-verify.json) |
| gitleaks `main..HEAD` @6f40 | 2026-09-11T19:34:06.555965+00:00 | 2026-09-11T19:34:07.469318+00:00 | 0.913s | 0 | [原命令](G:/xingmang/logs/unified-deploy-endpoint-20260912/gates-r2/gitleaks-command.json) |
| B5原24项本地补验 | 2026-09-11T19:24:04.195271+00:00 | 2026-09-11T19:25:14.244085+00:00 | 70.049s | 0 | [SUMMARY](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/SUMMARY.json) |
| D完整包外层 | 2026-09-11T21:18:22.533512+00:00 | 2026-09-11T21:22:14.128477+00:00 | 231.595s | 0 | [原OS记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/D-r3-11/RESULT.json) |
| E切换dry-run | 2026-09-11T21:23:25.067756+00:00 | 2026-09-11T21:23:25.262915+00:00 | 0.195s | 0 | [原OS记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-dryrun-01/RESULT.json) |
| E真实切换 | 2026-09-11T21:24:11.179965+00:00 | 2026-09-11T21:28:08.835915+00:00 | 237.656s | 0 | [原OS记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-cutover-01/RESULT.json) |
| E显式回滚 | 2026-09-11T21:29:43.618786+00:00 | 2026-09-11T21:31:47.466964+00:00 | 123.848s | 0 | [原OS记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-rollback-01/RESULT.json) |
| E回滚dry-run | 2026-09-11T21:32:58.268063+00:00 | 2026-09-11T21:32:58.468772+00:00 | 0.201s | 0 | [原OS记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-rollback-dryrun-01/RESULT.json) |
| E故障切换及自动回滚 | 2026-09-11T21:32:03.331978+00:00 | 2026-09-11T21:35:43.914898+00:00 | 220.583s | 1 | [原OS记录](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-fault-01/RESULT.json) |
| D11内层receipt（PASS） | 2026-09-11T21:18:23.328115+00:00 | 2026-09-11T21:22:13.994915+00:00 | 230.667s | 0 | [原receipt](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/D-r3-11/rehearsal-pass.json) |
| D11真实HTTP 10/10 | 2026-09-11T21:20:15.357246+00:00 | 2026-09-11T21:21:30.261880+00:00 | 74.905s | 0 | [原smoke证据索引](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.md) |
| 故障输入：真实invoice-migrate | 2026-09-11T21:33:42.802807+00:00 | 2026-09-11T21:33:43.879943+00:00 | 1.077s | **73** | [faultEvent](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.json) |
| 故障后的原自动回滚记录 | 2026-09-11T21:33:43.941559+00:00 | 2026-09-11T21:35:43.766878+00:00 | 119.825s | **1（ROLLED_BACK）** | [deployment-record](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/E-r3-fault-01/deployment-record.json) |

E故障路径的验收事实是“实际73导致停止切换并自动恢复旧22”，故外层与部署记录保留非零1，不能把回滚成功改写成切换成功。显式回滚内层为21:29:43.830260→21:31:47.329683，ROLLED_BACK/0；上表记录其外层原进程时间。旧22精确快照及双ready由被哈希绑定的原check_old必经路径、实际事件和终态证明，不虚构另存的HTTP响应正文。

D11 receipt原字节SHA256：`256ce615a32ae3cb4ebc5ffd37476844dd3526492e54a8a23f5221cbcae9cd51`。D/E逐事件、原命令、输入和清理证据索引：[完整JSON](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.json) / [可读汇总](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/DE-r3-SUMMARY.md)。原D01–D10失败记录保留；当前旧22及relay仍为最终资格运行保留，此项与D11完成自身8冻结卷清理不矛盾。

## 已关闭的必要适配与失败记录

- B2按统一架构适配实际消费者：保留backend race/vet、web test/typecheck/build/audit、agents race/vet、源契约、PG15/18真实隔离、四上游pin、安全和脚本守卫。旧Keycloak/独立发布链检查改为旧入口真实拒绝与统一拓扑/签名/路径/生命周期消费者；没有Skip、exit64替代完整verify，也没有audit waiver。数据库隔离逻辑与金融守卫未放宽。[完整旧→新映射](G:/xingmang/logs/unified-deploy-endpoint-20260912/verify-adaptation/REPORT.md)、[三runbook消费者](G:/xingmang/logs/unified-deploy-endpoint-20260912/runbook-consumer-adaptation/RESULT.md)、[十路状态顺序](G:/xingmang/logs/unified-deploy-endpoint-20260912/restore-source-state-consumer/RESULT.md)、[create顺序及失败阻断](G:/xingmang/logs/unified-deploy-endpoint-20260912/restore-network-consumer/RESULT.md)。
- 生成物曾在r2因工作树LF失败。原eol规则已存在，使用原生成器-update重生CRLF；新旧归一化SHA均`e51f92b17b061f08f8b4f8b38a3324bf692c6bdd8321635359addff444bf68e5`，新raw SHA`4fc601116f4d167dc83dbd71753cd6fd1814bc786ffb85459fb24405c29be732`；契约内容、Git blob和全局设置未改。LF-renderer变异红、恢复golden绿，其后r3及r5完整verify均实际重跑0。[生成物原证据](G:/xingmang/logs/unified-deploy-endpoint-20260912/generated-endings/RESULT.json)。
- D04 BusyBox tar不接受GNU式恢复flags，真实消费者exit1；最小兼容argv保持numeric owner与权限。公开合成tar实测保留UID/GID/0700/0600/0644；故意丢权限的实际变异红、恢复绿。D05只读metadata 0600在capDropAll root下读取失败；仅加DAC_READ_SEARCH恢复读取，RO写仍拒绝，归档权限/数据不改。原失败均清理冻结资源、shred临时身份，保留原资产。[tar实证](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/archive-diagnostic/COMPATIBLE-RESULT.json)、[权限变异](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/archive-diagnostic/MODE-MUTATION-RESULT.json)、[metadata读取](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/archive-diagnostic/METADATA-READ-RESULT.json)。
- D10白名单只读观察捕获ClamAV在原签名RO卷上restart/unhealthy，固定归类READ_ONLY_FILESYSTEM；原错误日志仅内存分类与hash，不输出正文。观察器旧Health模板缺失单独保留为工具错误，修复后真实无Health对象exit0，不把它当业务故障。[真实根因](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/startup-diagnostic-r3/REPORT.md)。
- 修复在启动前将三个公开签名家族从原RO卷复制到本次owner可写副本，逐文件比SHA/UID/GID/mode/mtime/size，原entrypoint、健康检查和freshness规则保持。真实原函数21:19:44.688893→46.566160 exit0；只把cp -p改cp的副本21:19:48.107935→49.106710因owner变化exit1；还原21:19:50.399238→52.218343 exit0。原输入hash/属性不变，3探针副本已owner核验清理。D11随后整包通过，此探针没有替代整D。[复制与有效变异](G:/xingmang/logs/unified-deploy-endpoint-20260912/rehearsal/scanner-clone-diagnostic/REPORT.md)。

## 原26项逐项处置

以下24项 **仅**引用本轮 `B5/run-20260911T192404Z`，不沿用旧22d84082或18:08轮当成当前最终证据。`sourceContentSha256=83b4f96ef3bcc88b9f5f0c33bacd698ed2d649da3847ee1b28ae830bdcd6fc4a`；1409白名单源码文件与995d1447的Git绑定0差异。后续6f40仅三项invoice脚本变化的关系见前述内容绑定；最终六组HEAD由root再明确绑定，不改原报告的身份。

| # | 原测试名 | 本轮结果 | 原始证据/剩余边界 |
|---:|---|---|---|
| 1 | `TestWriteOnePermissionBits` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-0.stdout.log) |
| 2 | `TestAUD2ArchiveSegmentAcceptsCommittedRow` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 3 | `TestAUD2ArchiveSegmentRejectsInvalidRangeAndHash` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 4 | `TestAUD2ArchiveSegmentDuplicateRangeIsRejected` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 5 | `TestAUD2ArchiveSegmentIsAppendOnly` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 6 | `TestAUD2ArchiveSchemaMissingIsAVisibleRedFailure` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 7 | `TestAUD2ContractDSNRejectsRemoteHost` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 8 | `TestAUD2FilesystemRejectsSymlinkedObjectPrefix` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 9 | `TestAUD2PostgresStoresRoundTrip` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 10 | `TestAUD2PostgresReceiptRejectsCompressedIncompleteOrdinals` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 11 | `TestAUD2OperationIntentIsAppendOnlyAndByteBound` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 12 | `TestAUD2PutReceiptUniqueByOrdinalAndTerminalAppendOnly` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 13 | `TestAUD2ReceiptTablesRejectTruncate` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-1.stdout.log) |
| 14 | `TestChannelBindingStoreCreateIdempotentRebindAndHistory` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-2.stdout.log) |
| 15 | `TestRunwayThresholdStoreIsolatedHarnessIsOptIn` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-2.stdout.log) |
| 16 | `TestPolicyFixturesAreStructurallyValidWithRealQPDF` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-3.stdout.log) |
| 17 | `TestCheckClamAVDatabaseFreshnessRejectsSymlink` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-4.stdout.log) |
| 18 | `TestCheckClamAVDatabaseFreshnessRejectsUnsafeAlternateDailyFile` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-4.stdout.log) |
| 19 | `TestQPDFPolicyRealBinaryWhenAvailable` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-4.stdout.log) |
| 20 | `TestSidecarStreamsBytesAndCleansPrivateTemporaryFile` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-5.stdout.log) |
| 21 | `TestSidecarRejectsUnsafePDFAndWrongCapability` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-5.stdout.log) |
| 22 | `TestSidecarFailsClosedOnOversizedAndUnavailablePolicy` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-5.stdout.log) |
| 23 | `TestCapabilityLoaderRejectsSymlinkAndWritableFile` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/09-events-5.stdout.log) |
| 24 | `TestLiveReadOnlyProbe` | 未运行：真实 Infini | 缺获授权真实凭据/出口；执行环境待负责人指定，不能凭此称“只能服务器” |
| 25 | `TestS3LiveQualificationDisposableMinIO` | ✓ 本轮本地 PASS | [本轮原始 Go 事件](G:/xingmang/logs/unified-deploy-endpoint-20260912/B5/run-20260911T192404Z/11-minio.stdout.log) |
| 26 | `TestS3LiveQualificationProviderQualificationMissing` | 诊断 always-skip，占位 | 没有独立可变为 PASS 的测试体；真实 MinIO 用例见 #25，不归入服务器测试 |

24项为23个Linux环境用例 + 1个真实一次性MinIO用例；不把包级PASS、helper或子用例凑成26。原26集合没有被证明“只能服务器运行”的测试。Infini可在具备授权凭据和合法出口的本地或服务器运行；本任务没有使用真实凭据，也没有授权真实外部调用。always-skip占位不能列成“服务器以后能过”的测试。

## 需服务器（负责人执行）

1. 在目标主机用最终包、最终配置和两库近期签名备份跑 D；两域分别核定独立已审的allowed-signers公钥路径：invoice-backup / solov-invoice-backup-v1 与 platform-backup / solov-platform-backup-v1。平台锚是本轮新约定，必须由负责人明确选定授权公钥，不能称旧平台已有签名链；私钥/age identity只由工具接收路径，不读取正文。真实两库/文档/source-state恢复与解密、主机磁盘余量、NTP、四真实来源版本、Cloudflare全集/real-IP、网络/精确信任与端口、实际secret挂载权限均需目标主机证据。本地合成证明不代表这些生产条件。
   服务器D的ClamAV签名输入同样必须是明确name/owner的原只读卷；启动前复制到本次owner新建的clamav_database可写副本，并逐文件核对公开签名家族的SHA、UID/GID、mode、mtime、size。ClamAV仅写自身副本，保持原entrypoint/healthcheck与原新鲜度规则；不得通过使原卷可写、修改mtime、跳过freshness来通过。D cleanup只移除本次副本，保留原只读输入。此要求属于原D隔离恢复，不是新增扫描或CVE门槛。
2. 用已集成只读 `deploy/unified/audit/staff-mfa.sql` 捕获生产有效 finance.read 角色总数/TOTP登记数、启用子集与followup。登记字段证明不等于真实OTP、文件可读或当次session已step-up；未登记员工先完成原有流程，或负责人明确接受临时锁定并安排恢复联系人。
3. 用只读身份审计确认生产旧Keycloak issuer/subject→既有invoice UUID→平台staff UUID及历史actor；未映射、冲突、仅归属不能冒充登录连续，也不能擅自按邮箱合并或改FK/AAD。只读capture的生产连接/权限尚未实测。
4. 负责人在低峰实施E。签名传输/stage/load、实际停写/就绪/回滚时间、目标主机完整旧4+18回归均未执行。当前所有毫秒或秒数仅本机工具/合成环境时间。
5. Infini如由负责人指定只能使用服务器上的真实provider环境，再在该环境进行获授权只读probe；目前它是“真实provider未验证”，不是自动增加的D阻断或已核server-only条件。

## 需负责人拍板

| 事项 | 待决定内容 | 当前边界 |
|---|---|---|
| PG官方镜像既有22 CVE | 记录当前原digest的已知风险；处置保持等待官方 postgres:18 重建后由负责人换 digest，不作为本轮阻塞，不引入补丁镜像或零漏洞门槛 | 原表逐行如下，来自旧已核快照，不代表本轮更新查询 |
| 两域备份签名信任锚 | 独立保留并审核各域allowed-signers公钥路径；invoice-backup / solov-invoice-backup-v1保留原锚，platform-backup / solov-platform-backup-v1为本轮新约定，需负责人明确授权签名公钥 | 不信任备份包自带公钥，不把平台新锚冒充既有链；这里只列路径/公钥治理，不查看任何私钥或age identity正文 |
| 低峰停机窗口 | 实施时间、负责人、允许停写时长、超时回滚阈值、现场恢复联系人 | 本地D整包231.595秒、E切换237.656秒、显式回滚123.848秒；仅为本机合成操作总窗，不能据此承诺服务器停机时间 |
| Keycloak保留/删除 | 默认切换当天停流量/容器，旧数据与相关secret仅供回滚保留7天；确定第7天清理负责人和准确对象 | 当天即删是另一个明确选择，会使回滚需要重装同版本并恢复签名数据；实际恢复时长未测，须预算 |
| 未登记MFA员工 | 完成登记或接受当日暂时锁定；给出恢复路径 | 本地1/1不能替代生产总数 |
| 历史身份映射异常 | 精确归属来源、冲突裁定、未连续身份如何处理 | 审计crosswalk只为证据，不自动写库/运行时映射 |
| 真实Infini测试 | 是否提供获授权真实provider环境/执行人 | 当前未测且不会读其key；不把诊断占位当真实测试 |

统一发布包另使用手册规定的`invoice-release@solov.cc / solov-invoice-release-v1`签名域；备份两个域分别授权，不能由包内自带公钥替代独立已审allowed-signers。只传私钥/age identity路径，不查看正文。正式手册给出的10–20分钟切换、10–20分钟回滚与30–45分钟排期是容量/环境未量测前的安排估计，非本机秒数的生产保证。

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


## 明确未验证与交付停止点

1. 六主题最终提交及与已验树的相等证明尚由主线程生成，路径固定为`commits/final-six-proof.json`；最后全11镜像包、冻结后gitleaks区间及同身份最终D/必要E记录由`FINAL-DELIVERY.json`索引。当前记录不为未发生的最终包或未来SHA预写PASS。索引与六组证明闭合后停止，不再追加A–F以外验收。
2. 生产C1 MFA覆盖、C2身份归属和读取权限、目标服务器磁盘/NTP/端口/CF信任/四来源版本/env和secret挂载、真实备份数据规模、实际停写/恢复耗时均未验证。原因是本轮明确仅本地准备与合成实跑，服务器D由负责人执行。
3. 真实Infini provider API未运行：没有获授权的真实凭据/出口；它不是被证明只能服务器运行的用例，也不增加为新的D阻断。always-skip占位没有可独立执行的业务体，已与24项实际PASS分开。
4. 服务器签名包传输/stage/load、生产切换回滚与Keycloak当天/第7天对象清理未执行；这里只交正式命令和本地完整成功/失败闭环证据。
5. PG CVE修复、依赖升级、资金策略/通知/CPA/上游改动不属于06；本轮未将这些工作混入统一进程交付。原22CVE表是此前已核快照，不冒充本次最新扫描或修复结论。

F的五项内容已按06逐一列入：A–E证据表、实际命令UTC/耗时/退出码、服务器清单、负责人清单、未验证及原因。完成条件仍包括外部最终冻结/包资格索引的真实闭合；不是自动批准生产部署。
