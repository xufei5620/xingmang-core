# 全量审查与修复 — 阶段二交付状态

补充修复快照 UTC `2026-09-10T20:20:03.909146+00:00`。修复分支 `ai/codex/XM-FULL-AUDIT-20260911`，四条补充修复后的代码 HEAD `e14b41f54ea2b757083be25393b60a1fec6adee8`；main `9d430fb284e5e9b91327089ae39cef207c42f482`。最终交付提交另见 [补充交付凭据](<G:/xingmang/logs/full-audit-20260911-followup/FINAL-DELIVERY.json>)。

90 条原始 finding 顺序不变；**90 条均已完成本地修复并提交（24 P0、61 P1、5 P2），未修/待拍板 finding 为 0**。本次按负责人补充授权完成原保留的四条：CPA 专属顺序测试及三条文档/提示问题。原完整门禁 10/10 记录保留；补充修改的定向检查与变异均通过，未把旧完整门禁标作本次重跑。

[逐条提交映射](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/COMMITS.txt>) · [原 86 条命令、UTC 与退出码索引](<G:/xingmang/logs/full-audit-20260910/phase2/final-report-prep/finding-evidence-index.json>)

| 排序 / ID | 问题 | 已修 | 未修 | 待拍板 | 集成提交 |
|---|---|---|---|---|---|
| 1 / PT-03 | P0 — 治理安全测试用 git checkout 还原迁移，会丢弃运行前未提交的用户改动 | 已集成；门禁见下表 | — | — | `f13cccc978427327f8fcbcc4d6d256729bee18bc` |
| 2 / POP-06 | P0 — decrypt-secrets 的 heredoc 抢占 YAML 输入，清空输出后没有生成凭据 | 已集成；门禁见下表 | — | — | `f799a7a8a7249777721f7b4a72e99f54dbfaa651` |
| 3 / IDEP-002 | P0 — backup 接受错误的现存 state generation，且恢复服务时会把该路径交给 Compose | 已集成；门禁见下表 | — | — | `3d420f5cb256fd182412a2800f9bc1548552ae82` |
| 4 / IT-CLI-01 | P0 — Whitespace-only narrowing filters silently become a bulk dead-row repair | 已集成；门禁见下表 | — | — | `49b8ca5a102caf07e09ef23a51c5fec11790b411` |
| 5 / POP-04 | P0 — deploy 按 SHA 加锁允许两次部署同时改同一 checkout | 已集成；门禁见下表 | — | — | `43c789c2348be69c01eb1b472dae6b9cbf582f5a` |
| 6 / POP-05 | P0 — Git 安装器接受解析为根目录的目标并会修改根目录权限 | 已集成；门禁见下表 | — | — | `df9d5284a12ccf4adf6ab6db4c1f71f56d7f4096` |
| 7 / F4 | P0 — F4：roll-forward 与 Keycloak 维护入口仍绑定旧 source 根/清单路径 | 已集成；门禁见下表 | — | — | `4c0ce3742f176753cdf6f3a56f7e6a8d52ae374d` |
| 8 / POP-01 | P0 — 平台服务器/本地工具及对应手册混淆Git根与platform项目根 | 已集成；门禁见下表 | — | — | `cf4f4cc8402e6a8204256129a0b384f2fb83969e` |
| 9 / POP-02 | P0 — pre-receive 清除了新提交所在的 Git quarantine 上下文 | 已集成；门禁见下表 | — | — | `9fec5f15b493e3260e4290b9925251df03e1f96d` |
| 10 / POP-03 | P0 — promote 完成后留下 pid/sha 文件，后续晋级永久锁死 | 已集成；门禁见下表 | — | — | `f3154a2ca641a472b6cfc1e1df94e7c44f39a4f4` |
| 11 / IDEP-003 | P0 — restore-drill 未转发 cutover runtime，源 runtime 升级后的合法备份无法验证 | 已集成；门禁见下表 | — | — | `95825f7a17e66b86d38aa197732a31b2466522d9` |
| 12 / IDEP-004 | P0 — roll-forward 检查 env 文件的 tag，Compose 却可使用外层导出的另一 tag | 已集成；门禁见下表 | — | — | `b0ad5e6afcff0a9d27910de989c5ee42e9edcda7` |
| 13 / IDEP-008 | P0 — pipefail 下用 grep -q 匹配大段 Docker 日志，可把真实启动 marker 判成启动失败 | 已集成；门禁见下表 | — | — | `54f9d045c96c835bfa50526b41d7d4d3acb27347` |
| 14 / INV-AUX-002 | P0 — 投影网络重跑从 network inspect EndpointResource 读取不存在的 Aliases 字段 | 已集成；门禁见下表 | — | — | `acbfe3bf160fbc3d3faafba5780008fd49669435` |
| 15 / INV-DOC-01 | P0 — 资格运维表格列出的两个 repair kind 不被当前工具接受 | 已集成；门禁见下表 | — | — | `c43eb77ca03c1316a1becc11e7be3ecac8e55c30` |
| 16 / OPS-04 | P0 — 两份 real 切换卡直接 up worker 会丢弃当前生产 override | 已集成；门禁见下表 | — | — | `a0b9881cd51436378f56991dd968d575e606a235` |
| 17 / PDOC-03 | P0 — DEPLOY prod 手册/探针要求18089，Compose默认却发布8088 | 已集成；门禁见下表 | — | — | `dfde38bb8179588bc8505c891519ea755d118a72` |
| 18 / POP-07 | P0 — deploy-local 对 local 鉴权配置的识别与 Compose 解析/生产默认不一致 | 已集成；门禁见下表 | — | — | `9385e2043162fa408d73cfdca22ee39b70e2ce29` |
| 19 / POP-08 | P0 — mirror-github 默认 git 参数必定被自己的生产白名单拒绝 | 已集成；门禁见下表 | — | — | `d7b3768b101231a67f1d7045b2587e6b82cf4267` |
| 20 / PT-02 | P0 — 两套测试数据库名称均可能碰撞，跨工作树隔离与删除边界失效 | 已集成；门禁见下表 | — | — | `bf40dc6b25decc195a2211c21461ff28cb4a0dc1` |
| 21 / RUNEARLY-01 | P0 — Artifact signature verification uses unset CMD variables and PowerShell-invalid quote escaping | 已集成；门禁见下表 | — | — | `a045a76243be10c6944008c393bfa445973aa122` |
| 22 / RUNEARLY-02 | P0 — Host command blocks still select deploy/.env.production after the release env moved beside source | 已集成；门禁见下表 | — | — | `b2aca068fa4d64ecaeb9ee2d0dd92df00aec074c` |
| 23 / TRIVY-01 | P0 — 续传分片不绑定 OCI digest，上游变化后反复复用旧分片导致刷新无法恢复 | 已集成；门禁见下表 | — | — | `dba5ea622a389632bfbc121b9d22f8771ca579bf` |
| 24 / TRIVY-04 | P0 — 镜像参数校验拒绝脚本自己的合法 tag+digest 默认值 | 已集成；门禁见下表 | — | — | `5c0e1dd16152a73949672c4b624964de6a73c17d` |
| 25 / AGT-TEST-001 | P1 — Schedule persistence test never seeds or asserts published sequence history | 已集成；门禁见下表 | — | — | `c7b50c60b3b57d8036c18396ee244ef627359d92` |
| 26 / AGT-TEST-002 | P1 — Partial reconciliation test checks absence of tombstone but not unchanged miss counters | 已集成；门禁见下表 | — | — | `31613e09e90f3d6c7aa80b562a12df550c644972` |
| 27 / AGT-TEST-003 | P1 — Invalid keygen KeyID test also supplies aliased outputs, masking removed KeyID guard | 已集成；门禁见下表 | — | — | `2493a55f296ccacf64e8893acd1d469fcb69c9a6` |
| 28 / CONTRACT-AUTH-01 | P1 — 签发端所谓独立验签测试复用生产 wireClaims 与 ACR 常量，单边改字段或域值仍通过 | 已集成；门禁见下表 | — | — | `74d399987668a8cc226494699c6de7ce906e344a` |
| 29 / F-PT-WEB-01 | P1 — 开票断言集成测试只检查 iframe URL，断言投递与重签发接线断开仍全绿 | 已集成；门禁见下表 | — | — | `a6a4407b84a672e1088a811006ab450b464d64a3` |
| 30 / F-PT-WEB-02 | P1 — “不从 Vite 回落开票来源”测试未提供待排除来源，增加违约回落仍通过 | 已集成；门禁见下表 | — | — | `569bfda3e34b02181c28bf611088debf60946cf1` |
| 31 / F1 | P1 — 输入目录并非实际Git仓库根仍通过：钉版树与平台Git操作入口 | 已集成；门禁见下表 | — | — | `761c91f31c3f16e19626d0262c662fe32a70c94f` |
| 32 / F2 | P1 — Git配置隐藏未跟踪文件时，invoice及pinned脏树检查漏判 | 已集成；门禁见下表 | — | — | `2097f84093a4477ca8ae3b51b82fb7aef4880061` |
| 33 / F3 | P1 — 错误分支的dry-run仍成功并输出固定release分支名 | 已集成；门禁见下表 | — | — | `152360f78dbddc2df35c80129f728eea9f5ed48e` |
| 34 / HYG-01 | P1 — 当前入口文档仍给出旧独立仓库路径与已过期的切根状态 | 已集成；门禁见下表 | — | — | `29f72160963fd77ddd02b89a5ee25bab06318c1f` |
| 35 / IDEP-005 | P1 — shadow-eval 完整 ready 报告会覆盖工具非零退出码；静态套件测不到最终退出判据 | 已集成；门禁见下表 | — | — | `4586b954ead888d68c1e6238c9ddd771e14f9197` |
| 36 / IDEP-006 | P1 — cleanup 把可达 Docker daemon 的任何 inspect 错误当资源不存在 | 已集成；门禁见下表 | — | — | `8fb5968ab77e630a029bb56a31f9543369d6274d` |
| 37 / IDEP-007 | P1 — roll-forward 忽略 ingest-proxy 重启失败并继续成功路径 | 已集成；门禁见下表 | — | — | `9ac7d91930d114167476b19f10d8a03371395f12` |
| 38 / INT-TEST-02 | P1 — Keyfile strict-JSON tests pass when unknown-field and trailing-JSON rejection are removed | 已集成；门禁见下表 | — | — | `86279894c7c5deb58b85db98bbbe2e50e726353c` |
| 39 / INT-TEST-03 | P1 — Traversal test passes with all LocalStore.OpenAuthorized path guards removed | 已集成；门禁见下表 | — | — | `22eff546ea17c904fb155863dc5350fa9091b0e3` |
| 40 / INV-AUX-003 | P1 — export/eval掩盖命令替换失败：Keycloak启动与测试DB环境捕获 | 已集成；门禁见下表 | — | — | `ab3332db309642d7bccd2171437a8d6500afb21b` |
| 41 / INV-AUX-004 | P1 — spool 与投影网络成员门禁丢失进程替换生产者退出码 | 已集成；门禁见下表 | — | — | `15f5fbcab0d20dd1d44b1238c04e93ba133bbd03` |
| 42 / INV-AUX-005 | P1 — 文件类型的 AND 列表不 fail-closed：secret 目录及备份/恢复/helper symlink guard 均可继续 | 已集成；门禁见下表 | — | — | `33751067eba2c8c1c8781b000f8730c813cf634d` |
| 43 / INV-AUX-006 | P1 — 管理员邀请的异地 ACK 内容不匹配仍通过，现有测试不覆盖消费端 | 已集成；门禁见下表 | — | — | `ddfd603fb82b89d3142775a20f6afa16e9a253d5` |
| 44 / INV-AUX-007 | P1 — 当前 runbook 要求 RC100 管理员安装，但操作器固定拒绝非 RC38 身份 | 已集成；门禁见下表 | — | — | `61b59850d91421a3c38239849d5e8e4e0e201c52` |
| 45 / INV-DOC-03 | P1 — blocked-cycle 处置段仍引导跳过 acknowledge 直接解冻 | 已集成；门禁见下表 | — | — | `ec2ac2aadb233ae3948cd188418487128e76cb7c` |
| 46 / INV-DOC-04 | P1 — 影子评估计划模板要求先于 tag，与本手册已加载签名候选镜像流程相冲突 | 已集成；门禁见下表 | — | — | `a1b2cb5a25cfb731e69afcfdfcb29847a8463688` |
| 47 / INV-PG-001 | P1 — Concurrent-index finalizer returns success when mandatory evidence writes fail | 已集成；门禁见下表 | — | — | `51c27671fb7b91a8cee58f5e47da2da9b6976756` |
| 48 / INV-PG-002 | P1 — Multi-file bash -n gates parse only the first shell script | 已集成；门禁见下表 | — | — | `27440a650ad59df80131ae772d1cda70652a385e` |
| 49 / INV-PG-003 | P1 — Readiness plan guard can be bypassed while its static contract gate stays green | 已集成；门禁见下表 | — | — | `ab239c57f90c3c337cd7d375f4f12616a3af8ca9` |
| 50 / INV-WIRE-01 | P1 — V3 JSON Schema 契约门禁未验证示例符合 schema，事件名变异后仍通过 | 已集成；门禁见下表 | — | — | `7989ce7ff56de1eaef1b2327f07fb8abf271f569` |
| 51 / INV-WIRE-02 | P1 — 真实批次接收器接受必填 array/boolean 为 null，违反 V3 契约并静默变成空批/false | 已集成；门禁见下表 | — | — | `8e1d335238ddc87ff649832ef2c2ba73016fdc41` |
| 52 / IT-CLI-02 | P1 — Per-account/event repair failures print APPLIED and exit 0 | 已集成；门禁见下表 | — | — | `3ab778d9515341d69988be1b807c668309acd06e` |
| 53 / IT-CLI-03 | P1 — Acknowledge repair account-flag rejection test is masked by the missing-event guard | 已集成；门禁见下表 | — | — | `0d4a021dbd586c2e1e6f00ed4ee9fceff05f7e5d` |
| 54 / ITR-REG-01 | P1 — Unexpected task lookup errors are mistaken for absence before forced registration | 已集成；门禁见下表 | — | — | `704f338cae1318b56a7b6c0b49eb1026b4d608b5` |
| 55 / ITR-REG-02 | P1 — Registration wiring source-text assertions miss branch, WhatIf and refresh-invocation regressions | 已集成；门禁见下表 | — | — | `073e7e08a75f9df880f59e70e0a3203959b166d7` |
| 56 / OPS-02 | P1 — REQLOG 容器验证没有 Compose 文件且固定了错误的项目名 | 已集成；门禁见下表 | — | — | `0343c644de199605063ca7c322f9a30114123336` |
| 57 / OPS-03 | P1 — NewAPI/Sub2API 切换卡仍把 env 缺省当作生效配置 | 已集成；门禁见下表 | — | — | `b43217a4e616afdfc45478bfc685d9edb0d55cc0` |
| 58 / OPS-05 | P1 — SHADOW-COMPARE 用 go run 抹平文档要求的退出码 1 与 2 | 已集成；门禁见下表 | — | — | `7ade292b9c5c6576a8a0caf383bf0c45c5cdfe3c` |
| 59 / OPS-06 | P1 — 审计归档 fixture 的 teardown 缺少必需的 env 文件变量 | 已集成；门禁见下表 | — | — | `70ca23e6fd1ecc2e9f1ed2f169c66033b4cd3dec` |
| 60 / OPS-07 | P1 — REQLOG 声称历史 tokenmap 会自动获得新权限，但 WriteFile 保留旧 mode | 已集成；门禁见下表 | — | — | `1e449daf74b97b26c25a750bab80833f7d7845a1` |
| 61 / OPS-08 | P1 — REQLOG 的 99% 验收只数 tokenmap 字段，无法检出读侧完全失效 | 已集成；门禁见下表 | — | — | `60549c60198efa9864a53d59ff2b413c230ad4ae` |
| 62 / OPS-09 | P1 — evidence-capture 示例输出根与后续 sha256 校验目录不一致 | 已集成；门禁见下表 | — | — | `1a718cf1c543f18594997bdda02e5879ce95dde2` |
| 63 / OPS-10 | P1 — 用户证据手册把 env-only 连接器配置错误描述成文件凭据登记前提 | 已集成；门禁见下表 | — | — | `0c148f288c1cd606b04845ec612bfffb69376304` |
| 64 / PC-001 | P1 — 预算解析 unknown-field 用例使用必定无效的空 capabilities，移除严格解码后仍绿 | 已集成；门禁见下表 | — | — | `94fac85c427b90ac8759cb5af933fd5c5b5d5af4` |
| 65 / PC-002 | P1 — 三个 SMS Action 契约仍宣称仅 HUMAN，当前运行定义已经允许 SERVICE | 已集成；门禁见下表 | — | — | `dc2b39a56bb9d16c30b50f24512df518ae4f79bb` |
| 66 / POP-10 | P1 — promote 的 test-mode 保护只检查冒号拼接后的第一条路径 | 已集成；门禁见下表 | — | — | `c0523a9661e46047f40c39bf13a484cd7e8b8017` |
| 67 / POP-11 | P1 — 只读窗口证据 helper 将未经校验的时间参数直接拼进 SQL | 已集成；门禁见下表 | — | — | `72b1f9835d7f14e9216487a1466f96aa552f0ffe` |
| 68 / POP-12 | P1 — 部署顺序与安装接线测试用字符串存在性冒充行为断言 | 已集成；门禁见下表 | — | — | `78c123863155cb491028708329ec39bf8ce0f4f2` |
| 69 / POP-12-CPA | P1 — CPA专属生命周期接线测试的顺序断言不充分 | 已修；补充验证通过 | — | — | `e14b41f54ea2b757083be25393b60a1fec6adee8` |
| 70 / POP-13 | P1 — 负向测试被前序错误遮住，删掉真实闸门仍全绿 | 已集成；门禁见下表 | — | — | `8fc9374bfb0956be668991f355bc62d3e436289a` |
| 71 / POP-14 | P1 — 通知隔离测试检查 payload，未检查它已经捕获的环境文件 | 已集成；门禁见下表 | — | — | `07df405170ac3c3a8669c20663c5175c94c4fa13` |
| 72 / PS-01 | P1 — 密钥材料门禁按Git声明扫描集合，却让rg忽略已跟踪的ignored文件 | 已集成；门禁见下表 | — | — | `0942d541a9f00b84cc3f9f5b973e5263f934d846` |
| 73 / PS-02 | P1 — 内容扫描器的Windows负退出码被当作未发现问题 | 已集成；门禁见下表 | — | — | `e6fa0ef7ac8934893d8ea1862de14298b2da4822` |
| 74 / PS-03 | P1 — 生产只读预检把缺失/畸形磁盘与Docker网络输出当作通过 | 已集成；门禁见下表 | — | — | `5e248331fbafa25fbe22ffe1e28261c289e7a3c6` |
| 75 / PS-04 | P1 — New API精确版本预检实际只做整行子串匹配 | 已集成；门禁见下表 | — | — | `e36e785af4198b978dc5a2c98ca73da413dbc3bd` |
| 76 / PS-05 | P1 — 发布产物目录只拒绝叶节点链接，父目录junction可越过项目边界 | 已集成；门禁见下表 | — | — | `3bf75e4c223dfb13311d7ce42ac08c92ceaa91c6` |
| 77 / PS-06 | P1 — SHA256SUMS生成与验证同时忽略隐藏产物，内容改动不被发现 | 已集成；门禁见下表 | — | — | `c5635d15620dd5329ebf2334f18fe2e7ee4e1e62` |
| 78 / PT-04 | P1 — 治理变更守卫未识别 monorepo 路径，且新增 Compose 环境扫描器未登记保护 | 已集成；门禁见下表 | — | — | `d286d56846027cec049616fa1515a12d2698c3f5` |
| 79 / PT-05 | P1 — Compose 环境变量门禁只查文件全局存在，API 缺透传会被 worker 同名变量掩盖 | 已集成；门禁见下表 | — | — | `b5d209470e7ff071d55fbd59b9920b764756317c` |
| 80 / PT-06 | P1 — DB-role负向Compose夹具位于RepoRoot外，端口/镜像断言实际只测到路径拒绝 | 已集成；门禁见下表 | — | — | `106f39a94053979ad889e2bbbd3182e9b9484c19` |
| 81 / PT-07 | P1 — Runway脚手架守卫测试不检查原生子进程退出码，并接受零测试运行 | 已集成；门禁见下表 | — | — | `c4b5dc74a8a92c9dd72f46a19885f81ce2a8c4ae` |
| 82 / RUNEARLY-04 | P1 — RC39 post-migration command block returns success after exact migration comparison fails | 已集成；门禁见下表 | — | — | `1e16dd5c1a124ddcf107620fbfff2dd63cf05a81` |
| 83 / TRIVY-02 | P1 — digest 相同快速路径不验证数据库内容或时效，自称成功而保留不可用缓存 | 已集成；门禁见下表 | — | — | `08faee3faca81dc5c970437e62ee89f148a29aa8` |
| 84 / TRIVY-03 | P1 — 共享 Docker volume 的锁按 worktree 项目目录分散，跨工作树互斥失效 | 已集成；门禁见下表 | — | — | `32bc6376546cfdd93d30427de627449f0734b290` |
| 85 / TRIVY-05 | P1 — 锁文件打开的所有错误都伪装成正常争用跳过 exit75 | 已集成；门禁见下表 | — | — | `6293b58589419f16935c95204fda784c52f55edb` |
| 86 / CONTRACT-DOC-01 | P2 — CR-0007/0008/0009 在同一文件保留互相冲突的当前状态 | 已修；补充验证通过 | — | — | `43b13d96c30b9bebd973ae6a4168ec163f23a0ac` |
| 87 / HSC-P2-01 | P2 — RC110 顶部状态仍是发布前初始值，与后续完成记录不一致 | 已修；补充验证通过 | — | — | `433dfda58aab70b1c4e215bef40cfa92189de776` |
| 88 / IDEP-009 | P2 — 恢复成功文案仍写 source_states=4，实际检查十个状态目录 | 已修；补充验证通过 | — | — | `88da933cd64c3b46aa5b5c06727758ccf32289bb` |
| 89 / INV-DOC-05 | P2 — eligibility repair 退出码表把kind/组合拒绝过宽地列为2 | 已集成；门禁见下表 | — | — | `685adfe9428cf3cc348f90dddd3eac95a9a8ac8f` |
| 90 / PC-003 | P2 — 卡用途 Action 契约遗漏已实现的订阅金额和周期参数 | 已集成；门禁见下表 | — | — | `01cadbdc44319b17ba46a263e4c2ba77c3d84a46` |

## 复审补证

原始测试记录保留为历史；下列补证专门修补原测试无法证明的判据。旧记录中的 green 不能单独作为这些判据的最终证明。

| Finding | 补证判据 | 补证 / 集成 |
|---|---|---|
| PT-03 | 必须实际执行全部七次治理调用并核对 deliberate exit 1；旧仅比较原文件不变可能假绿。 | [补证](<G:/xingmang/logs/full-audit-20260910/phase2/platform-tests/PT-03/integration-correction.patch>)；随原 finding 集成；追加提交 `-` |
| F4 | 资产必须是普通文件且不能是 symlink；旧路径测试不足以单独证明资产类型拒绝。 | [补证](<G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/F4/asset-types/proof.json>)；committed-source-matches；追加提交 `854b4b2c34aa2945c3629165f92a3fd8600d3e50` |
| TRIVY-01 | 失败重试前必须先捕获非空分片集合与哈希；旧失败后快照不足以证明分片保留。 | [补证](<G:/xingmang/logs/full-audit-20260910/phase2/trivy/TRIVY-01/integration-correction/integration-correction.patch>)；随原 finding 集成；追加提交 `-` |
| OPS-09 | 必须逐个正式命令核对两个平台的精确集合；旧跨块匹配不充分。 | [补证](<G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/integration-corrections/OPS-09-integration-correction.patch>)；随原 finding 集成；追加提交 `-` |
| OPS-10 | 每个正式命令独立检查 secret root 及可读性前置；旧跨命令匹配不充分。 | [补证](<G:/xingmang/logs/full-audit-20260910/phase2/platform-docs/integration-corrections/OPS-10-integration-correction.patch>)；随原 finding 集成；追加提交 `-` |

## 完整门禁实测

仅使用明确提供的 full-gates JSON；尚未完成的门禁保持 pending，不把 targeted tests 当作完整验证。额外 normal-regression 门禁需明确提供 kind、命令、时刻与日志，纳入同一验收表。

| 命令 | UTC 起 | UTC 止 | 秒 | 退出码 | 状态 / 日志 |
|---|---|---|---:|---:|---|
| `"C:\Program Files\PowerShell\7\pwsh.exe" -NoProfile -File .\scripts\verify.ps1 (started via scripts/run-detached.ps1)` | 2026-09-10T19:12:15.260074+00:00 | 2026-09-10T19:29:27.712045+00:00 | 1032.458 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/invoice-verify-r6.stdout.log>) |
| `go.exe test -race -p 1 ./...` | 2026-09-10T19:30:44.955448+00:00 | 2026-09-10T19:35:40.864745+00:00 | 295.916 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-go-test.stdout.log>) |
| `go.exe vet ./...` | 2026-09-10T19:35:40.945578+00:00 | 2026-09-10T19:36:06.541019+00:00 | 25.596 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-go-vet.stdout.log>) |
| `pnpm.cmd install --config.verify-deps-before-run=false --frozen-lockfile` | 2026-09-10T18:21:48.577916+00:00 | 2026-09-10T18:21:50.378937+00:00 | 1.807 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-pnpm-install.stdout.log>) |
| `pnpm.cmd -r run typecheck` | 2026-09-10T19:36:06.611585+00:00 | 2026-09-10T19:36:21.669638+00:00 | 15.062 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-typecheck.stdout.log>) |
| `pnpm.cmd -r run test` | 2026-09-10T19:36:21.795145+00:00 | 2026-09-10T19:37:08.744043+00:00 | 46.953 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-test.stdout.log>) |
| `D:\Git\bin\bash.exe scripts/check-governance.sh` | 2026-09-10T19:37:08.827942+00:00 | 2026-09-10T19:37:16.406499+00:00 | 7.579 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance.stdout.log>) |
| `D:\Git\bin\bash.exe tests/security/governance-protected-paths.test.sh` | 2026-09-10T19:44:16.877368+00:00 | 2026-09-10T19:44:20.756329+00:00 | 3.880 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-governance-protection-regression-r2.stdout.log>) |
| `D:\Git\bin\bash.exe tests/security/compose-env-services.test.sh` | 2026-09-10T19:37:17.197304+00:00 | 2026-09-10T19:37:17.574974+00:00 | 0.382 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-compose-env-regression.stdout.log>) |
| `D:\Git\bin\bash.exe tests/security/full-audit-regressions.test.sh` | 2026-09-10T19:44:20.828794+00:00 | 2026-09-10T19:44:59.049654+00:00 | 38.234 | 0 | passed [日志](<G:/xingmang/logs/full-audit-20260910/full-gates/platform-full-audit-regressions-r2.stdout.log>) |

## 需要负责人拍板

- 原保留的 **POP-12-CPA** 已按负责人本次授权完成专属测试加固；原有生产安装/启动流程保持不变。四条 finding 均已关闭，没有由本次修复新增的待拍板项。
- 服务器真相源切换、真实部署验证、GitHub 推送、签名 tag 与旧盘清理均未授权给本轮，也未作为完成条件执行。若安排这些操作，应另行确认目标、窗口与相应验收。
- 手册涉及的真实 secret 可读性、tokenmap 消费权限、观测采样与生产模式/端口选择，仍需负责人在实际环境验收；本轮只修正文档及本地 fail-closed 判据，没有迁移生产权限、身份或端口。

## 未验证与保留范围

本地 fakes、临时 Git 仓库、源码拷贝与 Go overlays 的证据不等于线上验证。未连接服务器、未执行生产 roll-forward/backup/shadow/restart、未读取真实密钥内容。真实生产数据、实际网络/镜像服务、计划任务服务与现有 DB/容器生命周期均不由这些 targeted tests 证明。原 10 项完整门禁全部退出 0；本次补充定向检查另表列出，早期失败尝试保留。

三条 P2 已补充修复：CR 和 RC110 增加有出处的历史摘要并逐字节保留原文；恢复计数来自实际检查列表。CR-0008 的真实读侧 ≥99% 验收与 RC110 用户 34 的后续状态仍未重新核实，文档如实标明；这两项外部验收不由本地状态整理证明。生成物契约、main/tag 和运行时代码边界见补充交付凭据。

## 最终核对与集成验证修正

24 条 P0、61 条 P1、5 条 P2 已按 finding 分别提交并集成。原 86 条加本次四条补充修复合计 90 条；下方原始阶段一记录仅描述当时发现，不代表当前未修状态。上方 HEAD 是四条补充修复后的代码快照，后续汇总提交只更新交付文档。

[完整门禁表及全部失败尝试](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/GATE-RESULTS.md>) · [最终边界与门禁源码一致性](<G:/xingmang/logs/full-audit-20260910/full-gates/preservation-final.json>) · [原 86 条命令与变异证据索引](<G:/xingmang/logs/full-audit-20260910/phase2/final-report-prep/finding-evidence-index.json>)

- main 保持 `9d430fb284e5e9b91327089ae39cef207c42f482`；原切根分支和两枚演练签名 tag 对象均未变。
- 依赖清单与数据库迁移未变；上游、AI manager 与 CPA 运行时代码未改，CPA 仅修改专属测试。未连接服务器、未推送、未合并 main、未读取真实密钥内容。
- 原开票完整门禁在 `824728ee14fe3bd018d73613ee4c7c138495047e` 运行通过；本次后端、前端、agents 和依赖保持相同，增量仅历史文档、最终成功提示与 Compose 注释。平台本次仅三份历史文档和 CPA 测试修改；应用源码与治理实现保持相同。修改覆盖的恢复检查、治理及 CPA 普通入口已定向验证；本次未重复运行应用完整构建、DB/容器或前后端全套。
- 原平台全量 `go test ./...` 包含既有 CPA 包测试；本次另行验证 CPA 专属安装到应用启动的合成执行轨迹及失败拦截，关闭 POP-12-CPA。它不证明真实 CPA 全生命周期或功能适配已验收。Runway DB 测试仍只证明既有 scaffold 的参数边界，完整迁移/角色生命周期未实现或实测。

### TypeScript 生成物内容不变证明

仅通过现有 Go 生成器 `-update` 重建；原有 invoice 下 eol=crlf 规则保留，未修改全局 Git 设置。73 个裸 LF 变为 73 个 CRLF，归一化后逐字节相同；Git 内容对象保持 `c48dcf9af16b213e4ddbc6c6b97413dc92ed21c6`。

| 文件 | 行尾归一化后 SHA-256 |
|---|---|
| 旧文件 | `e51f92b17b061f08f8b4f8b38a3324bf692c6bdd8321635359addff444bf68e5` |
| 新文件 | `e51f92b17b061f08f8b4f8b38a3324bf692c6bdd8321635359addff444bf68e5` |

[新旧文件、生成命令与哈希证据](<G:/xingmang/logs/full-audit-20260910/phase2/integration-fixes/generated-line-endings/normalized-hashes.json>)

### 集成门禁补充提交

以下只补门禁接线、测试证据与运行环境兼容；保留各 finding 的原提交，不改写历史。缺失 executable 或编译失败等夹具错误没有计作行为变异成功。

| 提交 | 内容 | 证据 |
|---|---|---|
| `854b4b2c34aa2945c3629165f92a3fd8600d3e50` | Normal audit gate wiring and F4 additional asset-type regression | [记录](<G:/xingmang/logs/full-audit-20260910/phase2/gate-wiring-invoice/results.json>) / [记录](<G:/xingmang/logs/full-audit-20260910/phase2/gate-wiring-platform/results.json>) / [记录](<G:/xingmang/logs/full-audit-20260910/phase2/invoice-core/F4/asset-types/proof.json>) |
| `4fc80c7bb3f7390d878d1168aead448df0461f49` | Scope strict-transfer verifier assertions to executable commands | [记录](<G:/xingmang/logs/full-audit-20260910/phase2/integration-fixes/runbook-strict-command/results.json>) |
| `7df825b5abc6b640d830b0c4c222b71df3adf5d7` | Include strings import in extracted CLI fixture | [记录](<G:/xingmang/logs/full-audit-20260910/phase2/integration-fixes/repair-cli-import/restored-green.json>) |
| `824728ee14fe3bd018d73613ee4c7c138495047e` | Use local Linux permissions for two allowed POSIX test fixtures | [记录](<G:/xingmang/logs/full-audit-20260910/phase2/invoice-posix-test-routing/results.json>) |
| `b201754f70911b18944bab0b5de17bea5e297b66` | Resolve Git Bash for existing security regression entrypoints in cmd/bin/mingw64 layouts | [记录](<G:/xingmang/logs/full-audit-20260910/phase2/platform-tests/BASH-RESOLUTION/delivery.json>) |

Git Bash 的符号链接设置仅应用于本次验证进程；两个 Unix 权限测试使用本机 WSL 新建的合成临时环境。前次 POSIX 运行方式修正保持对应的生产 validator 和原 Shell fixture 不变；本次 CPA 专属测试改动另见下节。部分 WSL 启动提示使用混合编码，原始 stderr 已保留；成功判据是实际退出码与测试断言。

## 四条补充修复验收

本次负责人明确要求四条全部处理；CPA 的授权限定于该 finding 的专属测试加固。每条独立提交，实际 UTC、退出码、变异与保留证明分别见：

| Finding | 独立提交 | 补充验收 |
|---|---|---|
| POP-12-CPA | `e14b41f54ea2b757083be25393b60a1fec6adee8` | [完整记录](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/FOLLOWUP-POP-12-CPA.md>) |
| CONTRACT-DOC-01 | `43b13d96c30b9bebd973ae6a4168ec163f23a0ac` | [完整记录](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/FOLLOWUP-CONTRACT-DOC-01.md>) |
| HSC-P2-01 | `433dfda58aab70b1c4e215bef40cfa92189de776` | [完整记录](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/FOLLOWUP-HSC-P2-01.md>) |
| IDEP-009 | `88da933cd64c3b46aa5b5c06727758ccf32289bb` | [完整记录](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/FOLLOWUP-IDEP-009.md>) |

文档变异与运行时行为变异分开记录，不将文档关键词核对算作生产执行验证。原 10 项完整门禁的有效源码范围和本次补充检查见 [门禁表](<G:/xingmang/01-core/docs/handoffs/full-audit-20260911/GATE-RESULTS.md>)。

## 原始阶段一报告（原字节保留）

以下完整保留阶段一报告；其中“尚未修复”等文字描述当时快照，当前状态以上表为准。原报告 SHA256 `707dda8dc050765174b373d88bea0f6af146807feb736ee3f694c0e50c64afb8`。

---

# 全量审查与修复 — 2026-09-11

## 阶段一：完整清单（源码只读，尚未开始修复）

基线：`aacab6ef98a63da627a05c555dc894824c7db5ba`；审查源码位于 `G:\xingmang\09-wt\core-mono-cutover`。`01-core` 的 main 仍为 `9d430fb284e5e9b91327089ae39cef207c42f482`。
本次按05提示词重新分级：P0=真实发布/部署/修复会失败或写坏数据；P1=判据/文档/测试无法阻止缺陷；P2=不影响正确性的一致性/卫生。已知四项直接纳入，未重新发现。
归并98条来源记录为 **90条共同根因**：{'P0': 24, 'P1': 61, 'P2': 5}。计划修复 **86条**；CPA专属1条待拍板，另3条P2保留历史/非运行时内容，不顺手整理。
阶段一没有改产品源码、现有refs/index或生产状态。新审查报告与必要的合成fixture写入本次证据目录。阶段二从本基线在01-core建立独立分支，逐项测试先行、一个finding一个提交；不合并main、不推送、不动签名tag。

[全量机器清单、合并来源与原始证据](G:/xingmang/logs/full-audit-20260910/phase1-findings.json)

| 排序 / ID | 级别 / 问题 | 已修 | 未修 | 待拍板 |
| --- | --- | --- | --- | --- |
| 1 / PT-03 | P0 — 治理安全测试用 git checkout 还原迁移，会丢弃运行前未提交的用户改动 | — | 是 | — |
| 2 / POP-06 | P0 — decrypt-secrets 的 heredoc 抢占 YAML 输入，清空输出后没有生成凭据 | — | 是 | — |
| 3 / IDEP-002 | P0 — backup 接受错误的现存 state generation，且恢复服务时会把该路径交给 Compose | — | 是 | — |
| 4 / IT-CLI-01 | P0 — Whitespace-only narrowing filters silently become a bulk dead-row repair | — | 是 | — |
| 5 / POP-04 | P0 — deploy 按 SHA 加锁允许两次部署同时改同一 checkout | — | 是 | — |
| 6 / POP-05 | P0 — Git 安装器接受解析为根目录的目标并会修改根目录权限 | — | 是 | — |
| 7 / F4 | P0 — F4：roll-forward 与 Keycloak 维护入口仍绑定旧 source 根/清单路径 | — | 是 | — |
| 8 / POP-01 | P0 — 平台服务器/本地工具及对应手册混淆Git根与platform项目根 | — | 是 | — |
| 9 / POP-02 | P0 — pre-receive 清除了新提交所在的 Git quarantine 上下文 | — | 是 | — |
| 10 / POP-03 | P0 — promote 完成后留下 pid/sha 文件，后续晋级永久锁死 | — | 是 | — |
| 11 / IDEP-003 | P0 — restore-drill 未转发 cutover runtime，源 runtime 升级后的合法备份无法验证 | — | 是 | — |
| 12 / IDEP-004 | P0 — roll-forward 检查 env 文件的 tag，Compose 却可使用外层导出的另一 tag | — | 是 | — |
| 13 / IDEP-008 | P0 — pipefail 下用 grep -q 匹配大段 Docker 日志，可把真实启动 marker 判成启动失败 | — | 是 | — |
| 14 / INV-AUX-002 | P0 — 投影网络重跑从 network inspect EndpointResource 读取不存在的 Aliases 字段 | — | 是 | — |
| 15 / INV-DOC-01 | P0 — 资格运维表格列出的两个 repair kind 不被当前工具接受 | — | 是 | — |
| 16 / OPS-04 | P0 — 两份 real 切换卡直接 up worker 会丢弃当前生产 override | — | 是 | — |
| 17 / PDOC-03 | P0 — DEPLOY prod 手册/探针要求18089，Compose默认却发布8088 | — | 是 | — |
| 18 / POP-07 | P0 — deploy-local 对 local 鉴权配置的识别与 Compose 解析/生产默认不一致 | — | 是 | — |
| 19 / POP-08 | P0 — mirror-github 默认 git 参数必定被自己的生产白名单拒绝 | — | 是 | — |
| 20 / PT-02 | P0 — 两套测试数据库名称均可能碰撞，跨工作树隔离与删除边界失效 | — | 是 | — |
| 21 / RUNEARLY-01 | P0 — Artifact signature verification uses unset CMD variables and PowerShell-invalid quote escaping | — | 是 | — |
| 22 / RUNEARLY-02 | P0 — Host command blocks still select deploy/.env.production after the release env moved beside source | — | 是 | — |
| 23 / TRIVY-01 | P0 — 续传分片不绑定 OCI digest，上游变化后反复复用旧分片导致刷新无法恢复 | — | 是 | — |
| 24 / TRIVY-04 | P0 — 镜像参数校验拒绝脚本自己的合法 tag+digest 默认值 | — | 是 | — |
| 25 / AGT-TEST-001 | P1 — Schedule persistence test never seeds or asserts published sequence history | — | 是 | — |
| 26 / AGT-TEST-002 | P1 — Partial reconciliation test checks absence of tombstone but not unchanged miss counters | — | 是 | — |
| 27 / AGT-TEST-003 | P1 — Invalid keygen KeyID test also supplies aliased outputs, masking removed KeyID guard | — | 是 | — |
| 28 / CONTRACT-AUTH-01 | P1 — 签发端所谓独立验签测试复用生产 wireClaims 与 ACR 常量，单边改字段或域值仍通过 | — | 是 | — |
| 29 / F-PT-WEB-01 | P1 — 开票断言集成测试只检查 iframe URL，断言投递与重签发接线断开仍全绿 | — | 是 | — |
| 30 / F-PT-WEB-02 | P1 — “不从 Vite 回落开票来源”测试未提供待排除来源，增加违约回落仍通过 | — | 是 | — |
| 31 / F1 | P1 — 输入目录并非实际Git仓库根仍通过：钉版树与平台Git操作入口 | — | 是 | — |
| 32 / F2 | P1 — Git配置隐藏未跟踪文件时，invoice及pinned脏树检查漏判 | — | 是 | — |
| 33 / F3 | P1 — 错误分支的dry-run仍成功并输出固定release分支名 | — | 是 | — |
| 34 / HYG-01 | P1 — 当前入口文档仍给出旧独立仓库路径与已过期的切根状态 | — | 是 | — |
| 35 / IDEP-005 | P1 — shadow-eval 完整 ready 报告会覆盖工具非零退出码；静态套件测不到最终退出判据 | — | 是 | — |
| 36 / IDEP-006 | P1 — cleanup 把可达 Docker daemon 的任何 inspect 错误当资源不存在 | — | 是 | — |
| 37 / IDEP-007 | P1 — roll-forward 忽略 ingest-proxy 重启失败并继续成功路径 | — | 是 | — |
| 38 / INT-TEST-02 | P1 — Keyfile strict-JSON tests pass when unknown-field and trailing-JSON rejection are removed | — | 是 | — |
| 39 / INT-TEST-03 | P1 — Traversal test passes with all LocalStore.OpenAuthorized path guards removed | — | 是 | — |
| 40 / INV-AUX-003 | P1 — export/eval掩盖命令替换失败：Keycloak启动与测试DB环境捕获 | — | 是 | — |
| 41 / INV-AUX-004 | P1 — spool 与投影网络成员门禁丢失进程替换生产者退出码 | — | 是 | — |
| 42 / INV-AUX-005 | P1 — 文件类型的 AND 列表不 fail-closed：secret 目录及备份/恢复/helper symlink guard 均可继续 | — | 是 | — |
| 43 / INV-AUX-006 | P1 — 管理员邀请的异地 ACK 内容不匹配仍通过，现有测试不覆盖消费端 | — | 是 | — |
| 44 / INV-AUX-007 | P1 — 当前 runbook 要求 RC100 管理员安装，但操作器固定拒绝非 RC38 身份 | — | 是 | — |
| 45 / INV-DOC-03 | P1 — blocked-cycle 处置段仍引导跳过 acknowledge 直接解冻 | — | 是 | — |
| 46 / INV-DOC-04 | P1 — 影子评估计划模板要求先于 tag，与本手册已加载签名候选镜像流程相冲突 | — | 是 | — |
| 47 / INV-PG-001 | P1 — Concurrent-index finalizer returns success when mandatory evidence writes fail | — | 是 | — |
| 48 / INV-PG-002 | P1 — Multi-file bash -n gates parse only the first shell script | — | 是 | — |
| 49 / INV-PG-003 | P1 — Readiness plan guard can be bypassed while its static contract gate stays green | — | 是 | — |
| 50 / INV-WIRE-01 | P1 — V3 JSON Schema 契约门禁未验证示例符合 schema，事件名变异后仍通过 | — | 是 | — |
| 51 / INV-WIRE-02 | P1 — 真实批次接收器接受必填 array/boolean 为 null，违反 V3 契约并静默变成空批/false | — | 是 | — |
| 52 / IT-CLI-02 | P1 — Per-account/event repair failures print APPLIED and exit 0 | — | 是 | — |
| 53 / IT-CLI-03 | P1 — Acknowledge repair account-flag rejection test is masked by the missing-event guard | — | 是 | — |
| 54 / ITR-REG-01 | P1 — Unexpected task lookup errors are mistaken for absence before forced registration | — | 是 | — |
| 55 / ITR-REG-02 | P1 — Registration wiring source-text assertions miss branch, WhatIf and refresh-invocation regressions | — | 是 | — |
| 56 / OPS-02 | P1 — REQLOG 容器验证没有 Compose 文件且固定了错误的项目名 | — | 是 | — |
| 57 / OPS-03 | P1 — NewAPI/Sub2API 切换卡仍把 env 缺省当作生效配置 | — | 是 | — |
| 58 / OPS-05 | P1 — SHADOW-COMPARE 用 go run 抹平文档要求的退出码 1 与 2 | — | 是 | — |
| 59 / OPS-06 | P1 — 审计归档 fixture 的 teardown 缺少必需的 env 文件变量 | — | 是 | — |
| 60 / OPS-07 | P1 — REQLOG 声称历史 tokenmap 会自动获得新权限，但 WriteFile 保留旧 mode | — | 是 | — |
| 61 / OPS-08 | P1 — REQLOG 的 99% 验收只数 tokenmap 字段，无法检出读侧完全失效 | — | 是 | — |
| 62 / OPS-09 | P1 — evidence-capture 示例输出根与后续 sha256 校验目录不一致 | — | 是 | — |
| 63 / OPS-10 | P1 — 用户证据手册把 env-only 连接器配置错误描述成文件凭据登记前提 | — | 是 | — |
| 64 / PC-001 | P1 — 预算解析 unknown-field 用例使用必定无效的空 capabilities，移除严格解码后仍绿 | — | 是 | — |
| 65 / PC-002 | P1 — 三个 SMS Action 契约仍宣称仅 HUMAN，当前运行定义已经允许 SERVICE | — | 是 | — |
| 66 / POP-10 | P1 — promote 的 test-mode 保护只检查冒号拼接后的第一条路径 | — | 是 | — |
| 67 / POP-11 | P1 — 只读窗口证据 helper 将未经校验的时间参数直接拼进 SQL | — | 是 | — |
| 68 / POP-12 | P1 — 部署顺序与安装接线测试用字符串存在性冒充行为断言 | — | 是 | — |
| 69 / POP-12-CPA | P1 — CPA专属生命周期接线测试的顺序断言不充分（本轮禁止触碰CPA） | — | — | CPA范围 |
| 70 / POP-13 | P1 — 负向测试被前序错误遮住，删掉真实闸门仍全绿 | — | 是 | — |
| 71 / POP-14 | P1 — 通知隔离测试检查 payload，未检查它已经捕获的环境文件 | — | 是 | — |
| 72 / PS-01 | P1 — 密钥材料门禁按Git声明扫描集合，却让rg忽略已跟踪的ignored文件 | — | 是 | — |
| 73 / PS-02 | P1 — 内容扫描器的Windows负退出码被当作未发现问题 | — | 是 | — |
| 74 / PS-03 | P1 — 生产只读预检把缺失/畸形磁盘与Docker网络输出当作通过 | — | 是 | — |
| 75 / PS-04 | P1 — New API精确版本预检实际只做整行子串匹配 | — | 是 | — |
| 76 / PS-05 | P1 — 发布产物目录只拒绝叶节点链接，父目录junction可越过项目边界 | — | 是 | — |
| 77 / PS-06 | P1 — SHA256SUMS生成与验证同时忽略隐藏产物，内容改动不被发现 | — | 是 | — |
| 78 / PT-04 | P1 — 治理变更守卫未识别 monorepo 路径，且新增 Compose 环境扫描器未登记保护 | — | 是 | — |
| 79 / PT-05 | P1 — Compose 环境变量门禁只查文件全局存在，API 缺透传会被 worker 同名变量掩盖 | — | 是 | — |
| 80 / PT-06 | P1 — DB-role负向Compose夹具位于RepoRoot外，端口/镜像断言实际只测到路径拒绝 | — | 是 | — |
| 81 / PT-07 | P1 — Runway脚手架守卫测试不检查原生子进程退出码，并接受零测试运行 | — | 是 | — |
| 82 / RUNEARLY-04 | P1 — RC39 post-migration command block returns success after exact migration comparison fails | — | 是 | — |
| 83 / TRIVY-02 | P1 — digest 相同快速路径不验证数据库内容或时效，自称成功而保留不可用缓存 | — | 是 | — |
| 84 / TRIVY-03 | P1 — 共享 Docker volume 的锁按 worktree 项目目录分散，跨工作树互斥失效 | — | 是 | — |
| 85 / TRIVY-05 | P1 — 锁文件打开的所有错误都伪装成正常争用跳过 exit75 | — | 是 | — |
| 86 / CONTRACT-DOC-01 | P2 — CR-0007/0008/0009 在同一文件保留互相冲突的当前状态 | — | 是 | — |
| 87 / HSC-P2-01 | P2 — RC110 顶部状态仍是发布前初始值，与后续完成记录不一致 | — | 是 | — |
| 88 / IDEP-009 | P2 — 恢复成功文案仍写 source_states=4，实际检查十个状态目录 | — | 是 | — |
| 89 / INV-DOC-05 | P2 — eligibility repair 退出码表把kind/组合拒绝过宽地列为2 | — | 是 | — |
| 90 / PC-003 | P2 — 卡用途 Action 契约遗漏已实现的订阅金额和周期参数 | — | 是 | — |

## 每条 finding 的失败场景、实测与建议

### 1. PT-03 / P0 — 治理安全测试用 git checkout 还原迁移，会丢弃运行前未提交的用户改动

文件行号：`platform/tests/security/governance-not-hollow.test.sh:30`；`platform/scripts/ci-local.sh:130`

具体失败场景：已跟踪迁移存在未提交用户改动，调用治理安全测试或修好根后的 ci-local。测试先追加自己的 tamper marker，随后 restore_migration 执行 git checkout -- published，将文件恢复到索引版本，用户改动也被抹去。

实际验证/输出：完整安全测试复制夹具中，已提交synthetic migration追加 USER_UNCOMMITTED_CHANGE_MUST_SURVIVE；测试即使最终 exit 1，运行后用户标记消失。

影响链：本地安全门禁与 ci-local 运行的数据保留。

建议修法：迁移变异放到独立Git fixture/临时拷贝执行；若保留原地测试则先拒绝dirty输入，且备份并按原始字节还原，禁止git checkout抹除调用前状态。以未暂存/已暂存改动各一例证明测试退出和中断都保留。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / PT-03](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:\Git\bin\bash.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures4\governance-local\tests\security\governance-not-hollow.test.sh；exit=1`

### 2. POP-06 / P0 — decrypt-secrets 的 heredoc 抢占 YAML 输入，清空输出后没有生成凭据

文件行号：`platform/deploy/scripts/decrypt-secrets.sh:22`；`platform/tests/security/decrypt-secrets-path.test.sh:15`；`platform/docs/runbooks/secrets.md:19`

具体失败场景：合成 sops 成功输出非空 scope/name YAML；脚本以 python3 - 同时从 stdin 读取 Python 源码与 YAML。

实际验证/输出：小数据/抽取接线出现exit0但无输出；较大管道控制出现SIGPIPE exit141。共同结果是stdin数据未被解析，且原流程在成功验证前清空旧输出；两类原始记录均保留。

影响链：decrypt-secrets 的 heredoc 抢占 YAML 输入，清空输出后没有生成凭据

建议修法：把 Python 程序与 YAML 数据放到不同通道；在全量解密、结构和路径校验成功后才原子替换目标，失败保留现有输出；补实际 shell 接线和失败保留测试。现有解析块单测不能代表包装器有效。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-06](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)；[platform-docs / PDOC-02](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `Extract only embedded Python and pipe/heredoc plumbing; use synthetic YAML in own fixture directory；exit=见原记录`

### 3. IDEP-002 / P0 — backup 接受错误的现存 state generation，且恢复服务时会把该路径交给 Compose

文件行号：`invoice/deploy/backup/backup.sh:24`；`invoice/deploy/backup/backup.sh:127`；`invoice/deploy/backup/backup.sh:231`；`invoice/deploy/backup/backup.sh:283`；`invoice/deploy/backup/backup.sh:345`；`invoice/deploy/docker-compose.sources.yml:76`；`invoice/docs/PRODUCTION-RUNBOOK.md:3121`；`invoice/docs/PRODUCTION-RUNBOOK.md:131`；`invoice/deploy/backup/backup.sh:7`

具体失败场景：当前容器 /state 来自 generation-current，调用者误填一个仍完整存在的 generation-obsolete（十目录、state/reconcile/cutover marker 均齐全）；其余参数合法。

实际验证/输出：目录 guards exit 0；record_running_service 仅请求容器 ID 与健康状态，错误 generation 仍 exit 0；真实本地 Compose config 证明外层 SOURCE_STATE_ROOT/SOURCE_CUTOVER_ROOT 覆盖 env 文件的 current 值，渲染全部 agents 的 /state、/cutover 为 obsolete。

影响链：备份错代际 → 签名快照的 DB/状态不一致；resume_services 的 up -d 还会以错误 mount 配置恢复/重建源代理。此结论为条件性代码/配置反例，未访问真实容器或生产数据。

建议修法：只读预检实际 API 文档 volume、十个源代理 /state 与 /cutover 挂载；与规范化输入逐一相等并绑定当前 env/项目身份。不同或查不到拒绝，不自动切代际。手册示例同步当前路径的取得方式。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-002](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)；[invoice-docs / INV-DOC-02](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/backup-obsolete-state-preflight/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/backup-record-state/probe.sh；exit=0`
- `D:/Docker/resources/bin/docker.exe compose --env-file G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\compose\synthetic.env -f G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\compose\docker-compose.sources.yml config --format json；exit=0`
- `python invoice-docs/kind-and-doc-proof.py（提取的无副作用前置判据/静态文档对照）`

### 4. IT-CLI-01 / P0 — Whitespace-only narrowing filters silently become a bulk dead-row repair

文件行号：`invoice/backend/cmd/eligibility-repair/main.go:201`；`invoice/backend/internal/postgresstore/projection_requeue_dead_repair.go:120`；`invoice/backend/internal/postgresstore/ingest_requeue_dead_repair.go:289`；`invoice/backend/internal/postgresstore/ingest_requeue_dead_repair.go:327`

具体失败场景：A caller supplies --kind=projection-requeue-dead --account "   " (or ingest-requeue-dead with a whitespace-only --event/--account), intending a narrowed repair. The CLI accepts the nonempty value; the selector TrimSpace normalizes it to empty and uses the unrestricted all-dead-row branch. With --apply and otherwise valid operator/configuration, unrelated dead jobs/events can be requeued.

实际验证/输出：All three malformed filters reached the extracted pre-I/O boundary without rejection. Exact projection selector sent SELECT ... WHERE status=dead ORDER BY 1 with zero parameters for spaces and tab/newline; valid UUID control retained external_account_id=$1.

影响链：Manual/automated narrow dead projection or ingest requeue -> unintended bulk scope.

建议修法：Reject nonempty whitespace-only filters before I/O. Prefer parsing optional filter presence explicitly so an explicitly supplied blank cannot silently widen scope, while preserving the documented omitted-filter bulk mode. Keep SQL and business/evaluator semantics unchanged. Add whitespace/omitted/valid controls.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / IT-CLI-01](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

### 5. POP-04 / P0 — deploy 按 SHA 加锁允许两次部署同时改同一 checkout

文件行号：`platform/deploy/scripts/deploy.sh:493`

具体失败场景：同一 checkout/staging 的两次调用分别解析到不同 release SHA；A 在 config 暂停，B checkout 到新 SHA。

实际验证/输出：两个不同 SHA 的锁均成功，两个调用都 PASS；A 的 BUILD_COMMIT 为 A，但 build/up 读取的实际 HEAD 已为 B，审计身份与构建源不一致。

影响链：deploy 按 SHA 加锁允许两次部署同时改同一 checkout

建议修法：用 checkout/部署资源维度串行化并在读取/更新共享 FETCH_HEAD、checkout 前取锁；保持正常生命周期顺序，第二调用应拒绝或等待；测试不同 SHA 并发而非只测同 SHA。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-04](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 6. POP-05 / P0 — Git 安装器接受解析为根目录的目标并会修改根目录权限

文件行号：`platform/deploy/scripts/install-git-server.sh:55`

具体失败场景：root 操作者传 --ci-dir //，或 --repo /xingmang.git，执行实际安装。

实际验证/输出：dry-run 对 // 与 repo 父目录 / 都 exit 0；静态命令会把这些目标交给 install -d -m 0750 -o gitci 与后续 chown/chmod。普通 / 负例反而正确拒绝。未实际修改系统路径。

影响链：Git 安装器接受解析为根目录的目标并会修改根目录权限

建议修法：先规范化并核对每个最终目标及 repo_parent，拒绝根目录、根等价拼写、重叠/符号链接等；检查完成后才运行 install/chown。只增加拒绝错误目标，不改变正常所有权策略。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-05](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 7. F4 / P0 — F4：roll-forward 与 Keycloak 维护入口仍绑定旧 source 根/清单路径

文件行号：`invoice/deploy/roll-forward.sh:37`；`invoice/deploy/roll-forward.sh:79`；`invoice/docs/PRODUCTION-RUNBOOK.md:83`；`invoice/deploy/keycloak/run-permanent-master-admin-maintenance.sh:123`；`invoice/deploy/keycloak/invite-permanent-master-admin.sh:63`；`invoice/deploy/keycloak/verify-permanent-master-admin-maintenance.sh:65`；`invoice/deploy/keycloak/verify-permanent-master-admin-maintenance.sh:171`；`invoice/docs/PRODUCTION-RUNBOOK.md:1271`；`invoice/deploy/keycloak/run-permanent-master-admin-maintenance.sh:152`；`invoice/deploy/keycloak/invite-permanent-master-admin.sh:64`；`invoice/deploy/keycloak/invite-permanent-master-admin.sh:94`

具体失败场景：已解包 release/source/invoice/deploy 且 env 在 release 根；从 source/invoice 调用包装器，deploy_dir 仍等于 release/source/deploy。 同类 Keycloak wrapper/operator 虽然 project_root 得到 source/invoice，但把其父目录 source 当作 release_root，随后要求 project_root==release_root/source 而拒绝；后续签名 manifest 路径也仍写 source/deploy。

实际验证/输出：已知 R01 片段 exit 1，旧独立布局 R02 exit 0；本轮不重复复现。 Keycloak 两个提取路径门禁旧 source 控制 exit0、source/invoice 反例 exit1。

影响链：monorepo 发布 → roll-forward → 第一次 migrate Compose 调用失败；此前 keyring placeholder 分支可能已写文件。

建议修法：从受校验的发布源码布局推导 invoice 项目根；支持范围明确的布局并在任何写入前校验全部必需 Compose/辅助文件。保留既定生命周期顺序。 同步 Keycloak wrapper/operator/manifest 路径及其 verifier 的 monorepo fixture；继续保留现有签名/tag 限制。路径适配本身不能授权将 RC38-only 维护工具切到新生产身份。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-001](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)；[invoice-docs / F4](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/layout-run-permanent-master-admin-maintenance-source/source/deploy/keycloak/probe.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/layout-run-permanent-master-admin-maintenance-source-invoice/source/invoice/deploy/keycloak/probe.sh；exit=1`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/layout-invite-permanent-master-admin-source/source/deploy/keycloak/probe.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/layout-invite-permanent-master-admin-source-invoice/source/invoice/deploy/keycloak/probe.sh；exit=1`

### 8. POP-01 / P0 — 平台服务器/本地工具及对应手册混淆Git根与platform项目根

文件行号：`platform/deploy/scripts/deploy.sh:217`；`platform/deploy/scripts/promote.sh:210`；`platform/deploy/git-hooks/post-receive:238`；`platform/docs/runbooks/DEPLOY-SERVER-QUICKSTART.md:18`；`platform/docs/runbooks/GO-LIVE-CHECKLIST.md:11`；`platform/deploy/scripts/deploy-local.sh:416`；`platform/docs/runbooks/REQLOG-RECORDER.md:112`；`platform/docs/runbooks/GIT-WORKFLOW.md:92`；`platform/scripts/dev/worktree-testdb.sh:159`；`platform/scripts/ci-local.sh:13`；`platform/scripts/dev/worktree-testdb.sh:161`；`platform/scripts/verify-real-mode.sh:19`

具体失败场景：checkout 为当前 monorepo，资产在 platform/；用默认 deploy/promote，或仅显式传 platform 下 Compose。

实际验证/输出：默认 deploy 找不到 compose；显式传文件仍被 deploy/compose/* 树路径检查拒绝；传 platform 子目录也因 Git tree 路径不对而失败。promote 找不到版本化 hook。CI 容器根 /workspace 下缺 scripts/check-governance.sh，写 red。

影响链：服务器 deploy/promote/CI 入口仍把 Git 根当成 platform 资产根

建议修法：分离 checkout Git 根与 platform 项目根；更新默认值、严格白名单、tree 相对路径、可信 hook 路径及 CI 工作目录。Compose 路径解析须纳入 fixture。ci-local 内部由 platform-tests 区负责。保持现有分支、项目名、端口及生产步骤。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-01](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)；[platform-docs / PDOC-01](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)；[platform-docs / PDOC-04](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)；[platform-tests / PT-01](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `Static read of helper root resolution plus Path.exists for cmd/migrate, db/migrations, platform/cmd/migrate, platform/db/migrations；exit=见原记录`
- `D:\Git\bin\bash.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\monorepo\platform\scripts\dev\worktree-testdb.sh --pg-url postgres://fixture@127.0.0.1:65432/postgres --print-url；exit=1`
- `D:\Git\bin\bash.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\monorepo\platform\scripts\ci-local.sh；exit=2`
- `D:\Git\bin\bash.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\monorepo\platform\scripts\ci-local.sh；exit=1`

### 9. POP-02 / P0 — pre-receive 清除了新提交所在的 Git quarantine 上下文

文件行号：`platform/deploy/git-hooks/pre-receive:19`

具体失败场景：正常本地 Git push 首次发送新提交，新对象在 receive 隔离对象目录中。

实际验证/输出：hook 清除 GIT_OBJECT_DIRECTORY/GIT_ALTERNATE_OBJECT_DIRECTORIES 后 cat-file 看不到新对象，普通新分支 push exit 1；保留 receive 对象上下文的对照副本相同 push exit 0。

影响链：pre-receive 清除了新提交所在的 Git quarantine 上下文

建议修法：保留并核对 Git receive 提供的 quarantine 对象上下文，仍拒绝非 commit/非快进；新增真实本地 push fixture，不能预先把对象灌入 bare repo。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-02](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 10. POP-03 / P0 — promote 完成后留下 pid/sha 文件，后续晋级永久锁死

文件行号：`platform/deploy/scripts/promote.sh:244`

具体失败场景：一次正常 promote 获得 marker.promote.lock 并写 pid/sha，随后成功推进 main。

实际验证/输出：cleanup 直接 rmdir 非空目录并忽略失败。首轮 exit 0、第二轮与下一 release 都 exit 75；锁目录仍有 pid/sha。

影响链：promote 完成后留下 pid/sha 文件，后续晋级永久锁死

建议修法：仅在本进程拥有锁时删除经过核对的自建 pid/sha，再移除锁目录；失败清理也应覆盖。新增成功后再次晋级与失败后重试测试，不清理未知锁。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-03](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 11. IDEP-003 / P0 — restore-drill 未转发 cutover runtime，源 runtime 升级后的合法备份无法验证

文件行号：`invoice/deploy/backup/restore-drill.sh:260`；`invoice/deploy/docker-compose.sources.yml:54`；`invoice/agents/cmd/source-agent-prod/main.go:283`；`invoice/agents/cmd/source-agent-prod/main.go:637`；`invoice/agents/sourceagent/cutover.go:446`；`invoice/docs/PRODUCTION-RUNBOOK.md:1872`；`invoice/docs/PRODUCTION-RUNBOOK.md:3191`

具体失败场景：合法 state generation 的 sealed manifest runtime=0.1.179，而已批准 source pin=0.1.180；调用者即使设 SUB2API_CUTOVER_RUNTIME_VERSION=0.1.179，restore 的 docker run 也只传 SOURCE_RUNTIME_VERSION=0.1.180。

实际验证/输出：提取的原始 argv 不含 SOURCE_CUTOVER_RUNTIME_VERSION；执行原始 Go 选择函数得到 0.1.180，契约断言 exit 1。增加正确 env 的对照 exit 0，删除该 env 的变异再次 exit 1。

影响链：signed restore drill → source-agent check-state → LoadCutoverManifest 报 cutover manifest source or runtime mismatch；无需改业务校验或解密任何真实文件即可证明 env 合约断开。

建议修法：只转发已存在的SOURCE_CUTOVER_RUNTIME_VERSION/按source的同义环境变量并保留旧fallback；不改pin、manifest格式、源runtime策略或恢复步骤。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-003](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/restore-runtime-env-args/probe.sh；exit=0`
- `C:/Program Files/Go/bin/go.exe run G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\restore-runtime-env-args\runtime-probe.go G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\restore-runtime-env-args\forwarded-env.json；exit=1`
- `C:/Program Files/Go/bin/go.exe run G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\restore-runtime-env-args\runtime-probe.go G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\restore-runtime-env-args\forwarded-env-control.json；exit=0`
- `C:/Program Files/Go/bin/go.exe run G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\restore-runtime-env-args\runtime-probe.go G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\restore-runtime-env-args\forwarded-env-mutant-missing-cutover.json；exit=1`

### 12. IDEP-004 / P0 — roll-forward 检查 env 文件的 tag，Compose 却可使用外层导出的另一 tag

文件行号：`invoice/deploy/roll-forward.sh:46`；`invoice/deploy/roll-forward.sh:79`；`invoice/deploy/docker-compose.prod.yml:4`；`invoice/deploy/docker-compose.sources.yml:4`；`invoice/deploy/docker-compose.idp.yml:5`

具体失败场景：release/.env.production 指定 rc110，操作者 shell 留有 export INVOICE_IMAGE_TAG=0.1.0-rc109，两组镜像均已加载。

实际验证/输出：真实本地 Compose config 使用完全合成 env 与复制 YAML，env rc110 + 外层 rc109 → migrate=invoice-system-tools:rc109、permissions=invoice-postgres:rc109，所有运行服务均 rc109。文件中读出的 rc110 不变。

影响链：镜像 preflight 验 rc110 → migrate/permissions/up 实际跑 rc109 → 最后按 rc110 统计才报失败；检查与副作用的候选身份不一致。与 F4 修好路径后仍独立存在。

建议修法：在前置阶段获取并校验 effective Compose 配置；统一 tag 来源并拒绝冲突 shell/env 值。三项目都绑定批准 tag，不能靠末尾计数补救。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-004](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Docker/resources/bin/docker.exe compose --env-file G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\compose\synthetic.env -f G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\compose\docker-compose.prod.yml config --format json；exit=0`
- `D:/Docker/resources/bin/docker.exe compose --env-file G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\compose\synthetic.env -f G:\xingmang\logs\full-audit-20260910\invoice-deploy\fixtures\compose\docker-compose.prod.yml --profile tools config --format json；exit=0`

### 13. IDEP-008 / P0 — pipefail 下用 grep -q 匹配大段 Docker 日志，可把真实启动 marker 判成启动失败

文件行号：`invoice/deploy/roll-forward.sh:109`；`invoice/deploy/backup/restore-drill.sh:317`；`invoice/deploy/rehearsal/shadow-eval.sh:346`；`invoice/deploy/keycloak/attach-invoice-basic-scope.sh:133`

具体失败场景：Docker 日志流开头已有正确启动 marker，后面还有超过 pipe buffer 的日志；grep -q 找到 marker 后提前退出，日志生产者随后写 pipe 遇 SIGPIPE/写入失败。

实际验证/输出：安全大日志 fake 证明 producer=141、grep=0。执行 roll-forward:112 原句 exit 1 并报 api did not report listening；仅让 grep 读完输出的对照 exit 0；重加 -q 变异又 exit 1。

影响链：真实发布 restart API 健康判据或 restore/shadow PG init-marker 判据在大日志下误失败/超时。仅证明 Shell 管道行为与对应调用语句，未读取真实生产日志。 同根模式也出现在 attach-invoice-basic-scope.sh 的 Keycloak PostgreSQL init-marker 检查。

建议修法：用会消费完整日志流的匹配方式并丢弃输出，或先安全捕获完整日志再匹配；仍检测日志命令非零，不用 || true 放过错误。不调整等待/恢复顺序。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-008](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/marker-pipefail/probe.sh；exit=141`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/marker-original/probe.sh；exit=1`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/marker-draining-control/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/marker-mutant-early-exit/probe.sh；exit=1`

### 14. INV-AUX-002 / P0 — 投影网络重跑从 network inspect EndpointResource 读取不存在的 Aliases 字段

文件行号：`invoice/deploy/provision-projection-networks.sh:49`

具体失败场景：数据库容器已正确接入投影网络并带 required alias；再次运行 provisioning 进入 existing-attachment 分支。network inspect 的 Containers 值是 EndpointResource，只有 Name/EndpointID/MacAddress/IP 地址，没有 Aliases。

实际验证/输出：实际 Go text/template 对精确字段形状执行脚本模板，返回不能求值 $c.Aliases；提取 ensure_attachment exit 1，并误报已连接容器缺少 alias。Go fixture 无 Docker/网络能力。

影响链：投影网络重跑从 network inspect EndpointResource 读取不存在的 Aliases 字段

建议修法：通过 docker container inspect 的 NetworkSettings.Networks[network].Aliases 核对该 alias；保留现有错误成员拒绝与 network connect 行为，补已正确连接/缺 alias 两个形状测试。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-AUX-002](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/network-reattach-shape.sh；exit=1`

### 15. INV-DOC-01 / P0 — 资格运维表格列出的两个 repair kind 不被当前工具接受

文件行号：`invoice/docs/ELIGIBILITY-OPERATIONS.md:888`；`invoice/backend/cmd/eligibility-repair/main.go:91`

具体失败场景：操作者按“Every door passes through this guard”表格，在已定义的invoice_eligibility_repair包装器上传--kind=preanchor-usage或--kind=anchor-balance；其余路径/权限正确。

实际验证/输出：两个kind均在任何secret/DB读取之前被run前置条件拒绝；按main错误路径返回1。提取原判据的合成fixture分别exit1，stderr为unknown --kind。

影响链：管理员按手册执行pre-anchor usage/balance anchor修复时立即失败。按用户P0定义为实际修复调用失败，不表示发生过生产事故。

建议修法：仅把表格中的kind改为源码规范值，并使文档kind库存与CLI接受集合有静态检查；不加新别名或改变修复语义。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / INV-DOC-01](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python invoice-docs/kind-and-doc-proof.py（提取的无副作用前置判据/静态文档对照）`

### 16. OPS-04 / P0 — 两份 real 切换卡直接 up worker 会丢弃当前生产 override

文件行号：`platform/docs/runbooks/SWITCH-NEWAPI-REAL.md:59`；`platform/docs/runbooks/SWITCH-SUB2API-REAL.md:41`

具体失败场景：已用 launch.yaml+server-prod.yaml 部署的 xingmang-launch worker，.env 合法设 ENVIRONMENT=production 且 XM_REQLOG_MODE=file，原请求日志采集依靠 server-prod.yaml 的只读挂载正常工作。按卡片只带 launch.yaml 执行 up -d platform-worker，新模型不再包含 /var/lib/xm/reqlog 的宿主机绑定挂载，worker 会按不完整配置重建；下一轮 file 采集无法再读取原先可读的数据。ENVIRONMENT 可以由原 env 保持 production，这里不依赖它回退为 staging。

实际验证/输出：["静态比较生产 overlay 的 worker environment/volumes 与卡片唯一 -f 参数；未执行 up/config 生产模型或检查真实 env。", "仅引用请求记录器挂载链；未评审或操作 CPA。"]

影响链：real 切换时的 worker 重建；会中断此前依靠生产 override 挂载的请求日志指标采集。

建议修法：切换后通过当前受控部署入口重建 worker，或显式携带同一组已批准 compose/base+override/env/project 参数；不可把只有 launch.yaml 的本地示例作为生产重启命令。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-04](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 17. PDOC-03 / P0 — DEPLOY prod 手册/探针要求18089，Compose默认却发布8088

文件行号：`platform/docs/runbooks/DEPLOY.md:48`；`platform/deploy/compose/server-prod.yaml:141`；`platform/deploy/compose/.env.example:416`；`platform/deploy/scripts/deploy.sh:222`；`platform/deploy/nginx/server-prod.conf.example:37`

具体失败场景：满足部署授权、真相源/路径前置后按 DEPLOY prod 流程，WEB_PORT 未另设（或使用 example 的8088）：Compose发布8088；脚本健康/就绪探针与引用的 nginx 模板指向18089。18089无监听时部署报错/网关不可达；若另一栈监听则存在探错目标风险（未实测）。

实际验证/输出：{"logs": ["PORT-STATIC-RESULTS.json"], "command": "Extract fixed literals from checked-in script/compose template only", "actual": "server-prod default=8088; deploy.sh prod default_port=18089; referenced nginx=18089; no explicit WEB_PORT=18089 prerequisite in DEPLOY.md"}

影响链：可选 DEPLOY0 production 发布流；当前 deploy-local 8088 流不是同一端口契约。

建议修法：只明确DEPLOY0的既有18089端口前置并在不匹配时拒绝；保持deploy-local的8088及现有Compose默认，不选择或修改生产端口。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / PDOC-03](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `Extract fixed literals from checked-in script/compose template only；exit=见原记录`

### 18. POP-07 / P0 — deploy-local 对 local 鉴权配置的识别与 Compose 解析/生产默认不一致

文件行号：`platform/deploy/scripts/deploy-local.sh:258`；`platform/deploy/compose/server-prod.yaml:27`

具体失败场景：合法配置 XM_AUTH_MODE="local" 或 XM_AUTH_MODE=local # comment；或生产 override 使用其默认 local 而 env 未显式写该项。

实际验证/输出：只有完全裸写 local 才走 401/403 auth-gate；引号/注释形状误走开发头 services 烟测，在应用健康、正确返回 401 时将已部署栈报失败。未显式配置的生产纯识别片段也输出 auth_mode_local=0。

影响链：deploy-local 对 local 鉴权配置的识别与 Compose 解析/生产默认不一致

建议修法：用统一 env 解析并按当前 override 的既有默认解析有效鉴权模式；local 校验 auth-gate，保留其它模式既有语义。新增裸写/引号/注释/生产默认形状测试。生产生命周期未运行。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-07](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 19. POP-08 / P0 — mirror-github 默认 git 参数必定被自己的生产白名单拒绝

文件行号：`platform/deploy/scripts/mirror-github.sh:44`；`platform/docs/runbooks/GIT-WORKFLOW.md:53`；`platform/tests/deploy/deploy0-c-mirror.test.sh:94`

具体失败场景：按手册调用 mirror-github.sh --reason ...，未另传 --git-bin。

实际验证/输出：默认 git_bin=git 没有解析成绝对路径，但生产仅接受 /usr/bin/git 或 /usr/local/bin/git，因此在读取 repo 前失败。抽取同一谓词：git exit 1，/usr/bin/git exit 0。

影响链：mirror-github 默认 git 参数必定被自己的生产白名单拒绝

建议修法：与 deploy.sh 一样先解析可执行文件，再校验受控系统路径；补不传 --git-bin 的真实模式前置验证。现有测试全部显式注入 fake 路径，默认值改成不存在名称仍全绿。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-08](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 20. PT-02 / P0 — 两套测试数据库名称均可能碰撞，跨工作树隔离与删除边界失效

文件行号：`invoice/backend/internal/testdb/testdb.go:158`；`invoice/backend/internal/testdb/testdb_test.go:21`；`invoice/backend/internal/postgresstore/store_integration_test.go:55`；`invoice/backend/internal/auth/identity_migrate_integration_test.go:27`；`platform/scripts/dev/worktree-testdb.sh:49`；`platform/docs/runbooks/GIT-WORKFLOW.md:107`

具体失败场景：Two worktrees on the same PostgreSQL server using the shared default invoice_test, e.g. G:\audit-one\core and G:\audit-two\core, both derive invoice_test_core; wt-a and wt_a also collide. The helper routes destructive integration reset operations to one database.

实际验证/输出：Current original pure-test baseline passes, while the four-case collision regression fails on all four distinct-name pairs. No database was contacted; destructive impact follows from the existing DROP SCHEMA public callers.

影响链：Concurrent local release/test preparation can collide at PostgreSQL schema reset and produce failing or invalid test outcomes; not a claim about production DB writes.

建议修法：仅本地测试数据库身份增加完整工作树路径区分；明确保留显式非默认DSN行为，绝不删除/重命名既有数据库，不改数据库schema或生产隔离。此前S4单行路径修复已完成并保留历史不变证明；本轮为新授权的测试隔离加固。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / INT-TEST-01](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)；[platform-tests / PT-02](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `go test -p 1 -count=1 -timeout=60s ./internal/testdb -run ^Test(Rewrite|Sanitize|PerWorktree|Resolve|Worktree) -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\testdb-current-plus-regression.overlay.json ./internal/testdb -run ^TestAuditWorktreeIdentity -v；exit=1`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\testdb-identity-hash-control.overlay.json ./internal/testdb -run ^Test(RewriteURLForWorktreeDerivesDistinct|AuditWorktreeIdentity) -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\testdb-identity-hash-removed-mutant.overlay.json ./internal/testdb -run ^TestRewriteURLForWorktreeDerivesDistinctDatabasesPerWorktree$ -v；exit=0`

### 21. RUNEARLY-01 / P0 — Artifact signature verification uses unset CMD variables and PowerShell-invalid quote escaping

文件行号：`invoice/docs/PRODUCTION-RUNBOOK.md:525`

具体失败场景：Operator supplies the three documented PowerShell path variables and completes signing. Line 538 then passes %RELEASE_ALLOWED_SIGNERS%, %RELEASE_SIGNATURE% and %CHECKSUM_MANIFEST% to CMD without exporting them. Backslash-quote is not PowerShell escaping, so the supposed command string is also split into multiple arguments with literal backslashes. Verification cannot consume the assigned paths and the mandatory release gate throws.

实际验证/输出：fake cmd captured /c plus two fragments, with all three percent placeholders and no assigned local paths; exit 0 is proof assertion, not release success

影响链：Section 3 detached SHA256SUMS verification before artifact transfer

建议修法：Use one reviewed byte-preserving stdin mechanism with the actual PowerShell variables; correctly quote shell/native boundaries. Test fake paths with spaces, and baseline/negative signature verification using synthetic test material in a later authorized fix.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / RUNEARLY-01](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `pwsh -NoProfile -File signature-argv.ps1；exit=见原记录`
- `pwsh -NoProfile -File signature-argv.ps1`

### 22. RUNEARLY-02 / P0 — Host command blocks still select deploy/.env.production after the release env moved beside source

文件行号：`invoice/docs/PRODUCTION-RUNBOOK.md:83`；`invoice/docs/PRODUCTION-RUNBOOK.md:1018`；`invoice/docs/PRODUCTION-RUNBOOK.md:1031`；`invoice/docs/PRODUCTION-RUNBOOK.md:1200`；`invoice/docs/PRODUCTION-RUNBOOK.md:1378`；`invoice/docs/PRODUCTION-RUNBOOK.md:1480`；`invoice/docs/PRODUCTION-RUNBOOK.md:1521`；`invoice/docs/PRODUCTION-RUNBOOK.md:1657`；`invoice/docs/PRODUCTION-RUNBOOK.md:1726`；`invoice/docs/PRODUCTION-RUNBOOK.md:1920`

具体失败场景：Use the declared monorepo installation with only releases/<sha>/.env.production and cwd releases/<sha>/source/invoice. Most early server commands explicitly select invoice/deploy/.env.production, and the readability preflight selects /root/invoice-system/app/deploy/.env.production. Those paths do not identify the release env. The source restart inspection omits --env-file entirely, although its Compose file has mandatory interpolation values. Startup/preflight/migration/projection commands fail before their intended operation, or consume a stale second env file if one exists.

实际验证/输出：declared env exists=true, documented relative env exists=false, ../../.env.production exists=true

影响链：Section 4 readability/scanner recreation; section 5 Keycloak bootstrap/retirement/OIDC; section 6 migration/bootstrap; section 7 projection/restart inspection

建议修法：Set and validate a canonical PRODUCTION_ENV_FILE for the selected release, use it consistently for all Compose and metadata checks, and include it in source-agent ps/inspect commands. Avoid creating a second source-tree env file.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / RUNEARLY-02](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python audit-fixtures.py (pure release-layout fixture)；exit=见原记录`
- `python audit-fixtures.py (pure release-layout fixture)`

### 23. TRIVY-01 / P0 — 续传分片不绑定 OCI digest，上游变化后反复复用旧分片导致刷新无法恢复

文件行号：`invoice/scripts/refresh-trivy-cache.ps1:269`；`invoice/scripts/refresh-trivy-cache.ps1:280`；`invoice/scripts/refresh-trivy-cache.ps1:282`；`invoice/scripts/refresh-trivy-cache-lib.ps1:319`；`invoice/scripts/refresh-trivy-cache-lib.ps1:350`

具体失败场景：上次下载中断，固定 <component>/parts 留下完整大小的旧 blob 分片；下一次 upstream digest 改变（或第一次分片内容被破坏但长度正确）。新下载仍按 part index/length 判断完整而直接复用。等尺寸新 blob 夹具连续两次都重组出旧 A 字节、跳过 curl，并在 OCI digest guard 失败；失败发生在删除 parts 之前，之后 retry 仍用相同坏分片。

实际验证/输出：对新 B blob 的两次尝试都得到 AAAAAAAAAAAA，相同旧 SHA256，digestRejected=true；没有下载新字节。

影响链：["定时/人工缓存刷新 -> OCI 分片复用 -> digest 校验 -> 发布准备"]

建议修法：以 registry/repository/layer digest/size/part plan 绑定分片缓存；最小安全方案使用 layer digest 子目录，保留旧目录证据。对 hash mismatch 拒绝并明确隔离该分片集，不能反复指示 rerun 同样输入。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / TRIVY-01](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\pure-proof.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\pure-tests-baseline\test-extracted-pure.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\pure-tests-mutant-always-pending-parts\test-extracted-pure.ps1；exit=1`

### 24. TRIVY-04 / P0 — 镜像参数校验拒绝脚本自己的合法 tag+digest 默认值

文件行号：`invoice/scripts/refresh-trivy-cache.ps1:41`；`invoice/scripts/refresh-trivy-cache.ps1:48`；`invoice/scripts/refresh-trivy-cache.ps1:55`

具体失败场景：调用者为了显式固定配置，把 SeedImage、TrivyImage 或 SelfCheckImageReference 的脚本默认值原样传入。ValidatePattern 的 @sha256 前字符类没有冒号，拒绝 postgres:18.6-alpine@sha256:… 和 trivy:0.74.0@sha256:…。隐式默认参数不经过同样验证，所以现有无覆盖调用可通过。

实际验证/输出：原始 ParamBlock 隔离 fixture：省略三个参数 exit0；分别原样显式传入三个默认值均在绑定阶段 exit1，尚未执行任何刷新/日志逻辑。

影响链：["人工/包装显式镜像 pin -> PowerShell 参数绑定 -> 刷新入口失败"]

建议修法：使用接受合法 registry[:port]/repository[:tag]@sha256:<64hex> 的严格校验，并用三个实际默认值做显式参数 round-trip；不改任何镜像版本或 digest。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / TRIVY-04](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\param-binder.ps1 -ProxyUrl ；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\param-binder.ps1 -ProxyUrl  -SeedImage postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2；exit=1`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\param-binder.ps1 -ProxyUrl  -TrivyImage ghcr.io/aquasecurity/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969；exit=1`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\param-binder.ps1 -ProxyUrl  -SelfCheckImageReference postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2；exit=1`

### 25. AGT-TEST-001 / P1 — Schedule persistence test never seeds or asserts published sequence history

文件行号：`invoice/agents/sourceagent/state_store_file_test.go:121`；`invoice/agents/sourceagent/state_store_file.go:244`

具体失败场景：FileScheduleStore.Save resets PublishRevision/Sequence/LastBatchHash to zero; named original test remains green because no publish history exists in its fixture

实际验证/输出：["r2-baseline-existing-source-tests", "r2-schedule-mutant-existing-test", "r2-baseline-strengthened-probes", "r2-schedule-mutant-strengthened-probe"]

影响链：Future schedule regression can rewind published sequence and break next source ingestion

建议修法：Seed nonzero FileSequenceStore CAS history; compare full reloaded SequenceState and cursor after saving schedule

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / AGT-TEST-001](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\baseline.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^(TestFileScheduleStoreDurableAndSharesEnvelopeWithCursor|TestReconcilePaginationRestartAndPartialScanNeverCountAsMiss|TestReconcileRequiresRepeatedCompleteMissesAndResetsFalseNegative|TestBlockedProjectionPublishesBlockedAndNeverCountsMissingRows|TestV3EntityStreamMatrixAndTombstoneFailClosed)$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\baseline.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^(TestAuditSchedulePreservesPublishedHistory|TestAuditPartialReconcilePreservesMissCounters)$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\schedule-wipes-history.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^TestFileScheduleStoreDurableAndSharesEnvelopeWithCursor$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\schedule-wipes-history.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^TestAuditSchedulePreservesPublishedHistory$；exit=1`

### 26. AGT-TEST-002 / P1 — Partial reconciliation test checks absence of tombstone but not unchanged miss counters

文件行号：`invoice/agents/sourceagent/reconcile_test.go:176`；`invoice/agents/sourceagent/reconcile.go:338`

具体失败场景：Mutate if !page.HasMore to if true; partial scan counts a miss yet named original test passes below MissThreshold=3

实际验证/输出：["r2-baseline-existing-source-tests", "r2-partial-mutant-existing-test", "r2-baseline-strengthened-probes", "r2-partial-mutant-strengthened-probe"]

影响链：Future incomplete-scan regression can count false misses and reach premature deletion threshold

建议修法：Seed multiple known entries and compare miss counts/full inventory and scanning phase before/after partial ACK and reload

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / AGT-TEST-002](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\baseline.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^(TestFileScheduleStoreDurableAndSharesEnvelopeWithCursor|TestReconcilePaginationRestartAndPartialScanNeverCountAsMiss|TestReconcileRequiresRepeatedCompleteMissesAndResetsFalseNegative|TestBlockedProjectionPublishesBlockedAndNeverCountsMissingRows|TestV3EntityStreamMatrixAndTombstoneFailClosed)$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\baseline.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^(TestAuditSchedulePreservesPublishedHistory|TestAuditPartialReconcilePreservesMissCounters)$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\partial-finishes-cycle.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^TestReconcilePaginationRestartAndPartialScanNeverCountAsMiss$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\partial-finishes-cycle.json -count=1 -p 1 -timeout 60s -v ./sourceagent -run ^TestAuditPartialReconcilePreservesMissCounters$；exit=1`

### 27. AGT-TEST-003 / P1 — Invalid keygen KeyID test also supplies aliased outputs, masking removed KeyID guard

文件行号：`invoice/agents/cmd/source-keygen/main_test.go:95`；`invoice/agents/cmd/source-keygen/main.go:56`

具体失败场景：Bypass ValidateSigningKeyID; both original negative cases still error on aliased private/public outputs and test stays green

实际验证/输出：["r2-keygen-baseline-existing-test", "r2-keygen-mutant-existing-test", "r3-keygen-proof-baseline", "r3-keygen-proof-bypass"]

影响链：Future keygen validation regression can produce unusable signing material under an invalid trust identifier

建议修法：Use distinct valid paths for invalid ID case and assert target error plus no output creation; keep alias case separate

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / AGT-TEST-003](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\baseline.json -count=1 -p 1 -timeout 60s -v ./cmd/source-keygen -run ^TestKeygenRejectsInvalidOrAliasedPaths$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\keygen-bypasses-keyid.json -count=1 -p 1 -timeout 60s -v ./cmd/source-keygen -run ^TestKeygenRejectsInvalidOrAliasedPaths$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\keygen-proof-baseline.json -count=1 -p 1 -timeout 60s -v ./cmd/source-keygen -run ^TestAuditKeygenInvalidIDWithDistinctValidPaths$；exit=0`
- `G:\cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.13.windows-amd64\bin\go.exe test -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\agents\overlays\keygen-proof-bypass.json -count=1 -p 1 -timeout 60s -v ./cmd/source-keygen -run ^TestAuditKeygenInvalidIDWithDistinctValidPaths$；exit=1`

### 28. CONTRACT-AUTH-01 / P1 — 签发端所谓独立验签测试复用生产 wireClaims 与 ACR 常量，单边改字段或域值仍通过

文件行号：`platform/internal/platform/consoleassertion/signer_test.go:124`；`platform/internal/platform/consoleassertion/signer_test.go:160`；`platform/internal/platform/consoleassertion/claims.go:30`；`platform/internal/platform/consoleassertion/consoleassertion.go:66`；`invoice/backend/internal/auth/console_assertion.go:446`

具体失败场景：下一次仅平台把 wireClaims 的 acr JSON 字段拼为 acr_drift，或仅平台把 ACR 常量改成 xingmang-console-totp-v2：平台现有签发/密钥契约测试仍 exit 0，但原样提取的实际 invoice VerifyConsoleAssertion 对真实平台 Sign 输出 exit 1 拒绝；上线会使控制台管理员无法兑换开票会话。候选原始生产行为当前通过合成 producer-consumer 基线，本 finding 是门禁检不出破坏性变更，未声称当前线上登录失败。

实际验证/输出：[{"result": "baseline existing tests exit 0; baseline actual producer-consumer exit 0", "metadata": "auth-mutation-results.json", "logs": ["auth-baseline-existing.log", "auth-baseline-cross.log"]}, {"result": "acr -> acr_drift: existing tests exit 0; actual invoice consumer exit 1 malformed compact serialization", "metadata": "auth-mutation-results.json", "logs": ["auth-mutant-field-existing.log", "auth-mutant-field-cross.log"]}, {"result": "platform ACR -> v2 only: existing tests exit 0; actual invoice consumer exit 1 bad ACR", "metadata": "auth-constant-mutation-results.json", "logs": ["auth-mutant-acr-constant-existing.log", "auth-mutant-acr-constant-cross.log"]}]

影响链：platform Sign -> iframe xm-embed admin-assertion -> invoice VerifyConsoleAssertion -> invoice admin session

建议修法：仅加固测试：用测试独立声明的冻结 JSON 字段清单与字面量域值核对实际签发结果，不再以生产 wireClaims/ACR 作为 oracle；保留合成密钥的实际 producer-consumer 契约 smoke。新增断言分别对错字段和单侧域值变异证明变红。无需改变签发、验签、权限或生产算法。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / CONTRACT-AUTH-01](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python G:\xingmang\logs\full-audit-20260910\contracts\run-auth-mutations.py`
- `python G:\xingmang\logs\full-audit-20260910\contracts\run-auth-constant-mutation.py`

### 29. F-PT-WEB-01 / P1 — 开票断言集成测试只检查 iframe URL，断言投递与重签发接线断开仍全绿

文件行号：`platform/web/apps/admin-web/src/components/InvoiceConsolePanel.test.tsx:97`；`platform/web/apps/admin-web/src/components/InvoiceConsolePanel.test.tsx:181`；`platform/web/apps/admin-web/src/components/InvoiceConsolePanel.tsx:264`；`platform/web/packages/ui-admin/src/EmbeddedConsoleFrame.test.tsx:129`

具体失败场景：local auth + finance.read + configured invoice origin + successful synthetic console-assertion response. Mutate InvoiceConsolePanel ready branch to assertion={undefined}: all 14 existing component tests pass although iframe receives zero admin-assertion messages. Independently mutate onAssertionNeeded={undefined}: the same 14 tests pass although a valid iframe admin-assertion-needed message causes no second signing request.

实际验证/输出：[{"label": "baseline-console-config-retry1", "startUtc": "2026-09-10T15:07:14.897639+00:00", "endUtc": "2026-09-10T15:07:22.633151+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.test.tsx", "src/auth/runtimeConfig.test.ts"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\baseline-console-config-retry1.log"}, {"label": "drop-assertion-existing", "startUtc": "2026-09-10T15:08:30.704795+00:00", "endUtc": "2026-09-10T15:08:33.593285+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.test.tsx"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\drop-assertion-existing.log"}, {"label": "drop-assertion-strengthened", "startUtc": "2026-09-10T15:08:33.673790+00:00", "endUtc": "2026-09-10T15:08:36.095643+00:00", "exitCode": 1, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.audit-proof.test.tsx"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\drop-assertion-strengthened.log"}, {"label": "drop-reissue-existing", "startUtc": "2026-09-10T15:08:45.342328+00:00", "endUtc": "2026-09-10T15:08:47.736140+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.test.tsx"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\drop-reissue-existing.log"}, {"label": "drop-reissue-strengthened", "startUtc": "2026-09-10T15:08:47.805344+00:00", "endUtc": "2026-09-10T15:08:50.940753+00:00", "exitCode": 1, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.audit-proof.test.tsx"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\drop-reissue-strengthened.log"}, {"label": "restored-green", "startUtc": "2026-09-10T15:09:24.717208+00:00", "endUtc": "2026-09-10T15:09:29.838674+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.test.tsx", "src/auth/runtimeConfig.test.ts", "src/components/InvoiceConsolePanel.audit-proof.test.tsx", "src/auth/runtimeConfig.audit-proof.test.ts"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\restored-green.log"}]

影响链：Platform local login -> console assertion issuer -> InvoiceConsolePanel -> EmbeddedConsoleFrame -> invoice assertion redemption; initial sign-in and iframe-requested refresh can regress undetected.

建议修法：Add integration assertions in InvoiceConsolePanel.test.tsx using the real EmbeddedConsoleFrame, fake only fetch, fire iframe load and assert exact admin-assertion payload + targetOrigin; dispatch a valid assertion-needed MessageEvent and assert the second signing request. Keep current product implementation.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / F-PT-WEB-01](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/components/InvoiceConsolePanel.test.tsx src/auth/runtimeConfig.test.ts；exit=0`
- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/components/InvoiceConsolePanel.test.tsx；exit=0`
- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/components/InvoiceConsolePanel.audit-proof.test.tsx；exit=1`
- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/components/InvoiceConsolePanel.test.tsx src/auth/runtimeConfig.test.ts src/components/InvoiceConsolePanel.audit-proof.test.tsx src/auth/runtimeConfig.audit-proof.test.ts；exit=0`

### 30. F-PT-WEB-02 / P1 — “不从 Vite 回落开票来源”测试未提供待排除来源，增加违约回落仍通过

文件行号：`platform/web/apps/admin-web/src/auth/runtimeConfig.test.ts:171`；`platform/web/apps/admin-web/src/auth/runtimeConfig.ts:172`

具体失败场景：The test says invoiceConsoleOrigin has no Vite fallback but passes an env containing only VITE_XM_AUTH_MODE. Mutate the parser to input.invoiceConsoleOrigin ?? Reflect.get(env, "VITE_XM_INVOICE_CONSOLE_ORIGIN"): all 23 existing config tests pass. With no runtime origin and a populated synthetic VITE_XM_INVOICE_CONSOLE_ORIGIN=https://stale-build.example.test, the mutant supplies that origin rather than undefined.

实际验证/输出：[{"label": "baseline-console-config-retry1", "startUtc": "2026-09-10T15:07:14.897639+00:00", "endUtc": "2026-09-10T15:07:22.633151+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.test.tsx", "src/auth/runtimeConfig.test.ts"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\baseline-console-config-retry1.log"}, {"label": "add-vite-fallback-existing", "startUtc": "2026-09-10T15:08:57.791813+00:00", "endUtc": "2026-09-10T15:08:59.358280+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/auth/runtimeConfig.test.ts"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\add-vite-fallback-existing.log"}, {"label": "add-vite-fallback-strengthened", "startUtc": "2026-09-10T15:08:59.441084+00:00", "endUtc": "2026-09-10T15:09:01.216543+00:00", "exitCode": 1, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/auth/runtimeConfig.audit-proof.test.ts"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\add-vite-fallback-strengthened.log"}, {"label": "restored-green", "startUtc": "2026-09-10T15:09:24.717208+00:00", "endUtc": "2026-09-10T15:09:29.838674+00:00", "exitCode": 0, "command": ["D:\\Node\\node.exe", "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web\\node_modules\\vitest\\vitest.mjs", "run", "--config", "audit.vitest.config.ts", "--maxWorkers=1", "--no-file-parallelism", "--no-color", "src/components/InvoiceConsolePanel.test.tsx", "src/auth/runtimeConfig.test.ts", "src/components/InvoiceConsolePanel.audit-proof.test.tsx", "src/auth/runtimeConfig.audit-proof.test.ts"], "cwd": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\fixture\\platform\\web\\apps\\admin-web", "log": "G:\\xingmang\\logs\\full-audit-20260910\\platform-tests\\frontend\\restored-green.log"}]

影响链：Build-time Vite configuration -> same-image multi-environment deployment -> opening invoice administration iframe. The claimed runtime-only source guard does not detect accidental reintroduction of a stale build origin.

建议修法：Populate synthetic competing Vite invoice-origin keys in the RuntimeEnv fixture (type cast if needed), then assert undefined when the runtime field is absent and correct runtime origin when it is present; mutation must fail when the fallback is introduced.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / F-PT-WEB-02](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/components/InvoiceConsolePanel.test.tsx src/auth/runtimeConfig.test.ts；exit=0`
- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/auth/runtimeConfig.test.ts；exit=0`
- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/auth/runtimeConfig.audit-proof.test.ts；exit=1`
- `D:\Node\node.exe G:\xingmang\logs\full-audit-20260910\platform-tests\frontend\fixture\platform\web\apps\admin-web\node_modules\vitest\vitest.mjs run --config audit.vitest.config.ts --maxWorkers=1 --no-file-parallelism --no-color src/components/InvoiceConsolePanel.test.tsx src/auth/runtimeConfig.test.ts src/components/InvoiceConsolePanel.audit-proof.test.tsx src/auth/runtimeConfig.audit-proof.test.ts；exit=0`

### 31. F1 / P1 — 输入目录并非实际Git仓库根仍通过：钉版树与平台Git操作入口

文件行号：`platform/deploy/scripts/configure-remotes.sh:101`；`platform/deploy/scripts/mirror-github.sh:142`；`platform/deploy/scripts/deploy.sh:319`；`platform/deploy/scripts/promote.sh:149`；`invoice/scripts/check-upstream-integrity.ps1:58`

具体失败场景：给 --repo/--checkout 仓库内部普通目录，Git 向上找到父仓库；其余前提合法。

实际验证/输出：configure/mirror 的独立 Linux fixture 对真实根和 nested 子目录都 dry-run exit 0；之后 Git config/push 实际归属父仓库。deploy/promote 同类检查存在，路径错误另受 POP-01 遮挡。

影响链：远端与服务器入口只检查位于 Git 树内，未核对给定目录就是根

建议修法：将规范化物理输入路径与 rev-parse --show-toplevel 规范化结果严格比较。与已知 F1 同类根身份校验缺失，可由主代理合并共同修复范围。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-09](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)；[known-review / F1](G:/xingmang/logs/cutover-adversarial-review-20260910T142222Z-2410446f/REPORT.md)

### 32. F2 / P1 — Git配置隐藏未跟踪文件时，invoice及pinned脏树检查漏判

文件行号：`invoice/scripts/release-image-gate-lib.ps1:756`；`invoice/scripts/check-upstream-integrity.ps1:70`

具体失败场景：GitDirty=true；实际：GitDirty=false，函数探针 exit 0

实际验证/输出：GitDirty=false，函数探针 exit 0

影响链：开票源码完整性/脏树判据 → 发布门禁与产物来源绑定

建议修法：显式选择未跟踪文件扫描模式，保留 invoice 路径限制，添加配置覆盖负例。未实施修复。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[known-review / F2](G:/xingmang/logs/cutover-adversarial-review-20260910T142222Z-2410446f/REPORT.md)

### 33. F3 / P1 — 错误分支的dry-run仍成功并输出固定release分支名

文件行号：`platform/deploy/scripts/deploy-local.sh:503`

具体失败场景：完整或 sparse checkout 当前为错误分支/非发布分支，SHA 匹配当前 HEAD。

实际验证/输出：按简报复用已确认 F3，不重新跑同一案例；当前源码仍在分支判据之前结束 dry-run。正常 fetch 失败回退已要求 release 分支，不应扩大离线放行。

影响链：已知 F3：错误分支 dry-run 返回成功并打印固定 release 名

建议修法：dry-run 验证实际 release 分支并打印实际值；补普通错分支与 detached 触发形状，保持精确 SHA/脏树/正常 fetch 回退既有约束。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-K03](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 34. HYG-01 / P1 — 当前入口文档仍给出旧独立仓库路径与已过期的切根状态

文件行号：`platform/docs/handoffs/CODEX-PROMPT.md:12`；`platform/docs/handoffs/CODEX-PROMPT.md:40`；`platform/docs/handoffs/CODEX-PROJECT-HANDOFF.md:6`；`README.md:14`；`README.md:16`

具体失败场景：在候选 aacab6ef 上按 CODEX-PROJECT-HANDOFF 引用的常驻 CODEX-PROMPT 开始下一轮发布/修复准备：第12行要求打开 K:/星芒统一控制平台/xingmang-platform，第40行将新 worktree 建在 K 盘；这不使用当前 G:/xingmang/01-core 或获批 monorepo 工作树。根 README 第14-19行又称两条发布链尚未切根并指向旧 subtree 同步流程，而当前迁移文档和实现已经区分 monorepo/project root。旧盘若保留仓库会把后续修改指向旧实现，若不可用则入口失败；未访问旧盘，因此其实际存在性未知。

实际验证/输出：无历史/废弃声明的常驻提示词仍使用 K 盘；根入口与迁移说明的当前状态互相矛盾。

影响链：["仓库入口 -> 常驻工作提示词 -> worktree/修改准备", "根 README -> 当前迁移状态与下一次发布准备"]

建议修法：只更新常驻 CODEX-PROMPT 的仓库/worktree 路径与 monorepo 下 cd platform 约定；更新根 README 的候选状态并链接迁移交接。不要把源码适配写成已上线，不执行或改写 subtree 历史，不批量重写历史 handoff 路径。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[hygiene / HYG-01](G:/xingmang/logs/full-audit-20260910/hygiene/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python G:/xingmang/logs/full-audit-20260910/hygiene/collect.py；exit=0`
- `python G:/xingmang/logs/full-audit-20260910/hygiene/finalize.py (read-only excerpt capture)；exit=0`

### 35. IDEP-005 / P1 — shadow-eval 完整 ready 报告会覆盖工具非零退出码；静态套件测不到最终退出判据

文件行号：`invoice/deploy/rehearsal/shadow-eval.sh:434`；`invoice/deploy/rehearsal/shadow-eval.sh:449`；`invoice/deploy/rehearsal/test-shadow-eval.sh:34`；`invoice/deploy/rehearsal/test-shadow-eval.sh:378`

具体失败场景：工具或 Docker 已输出一个完整 ready JSON，但进程返回 1/125/137，或返回 3 与报告 ready 不一致。

实际验证/输出：对原始尾段逐一注入 tool_exit=1/125/137/3，全部最终 exit 0，输出同时显示 tool process exit was 非零。将复制脚本最终 exit 改成恒 0，现有 test-shadow-eval.sh 仍全绿；真实 not_ready 尾段在该变异下 exit 0。

影响链：shadow rehearsal → release gate 使用脚本退出状态 → 工具执行失败或被改坏的最终退出逻辑可被标为 ready。

建议修法：验证 tool_exit、tool_verdict 与 recomputed_exit 的完整组合，不改 evaluator 的 ready/not_ready 算法；补提取尾段/假 docker 贯通测试和恒 0 变异。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-005](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/shadow-tool-exit-0/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/shadow-tool-exit-1/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/shadow-tool-exit-125/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/shadow-tool-exit-137/probe.sh；exit=0`

### 36. IDEP-006 / P1 — cleanup 把可达 Docker daemon 的任何 inspect 错误当资源不存在

文件行号：`invoice/deploy/backup/docker-cleanup-state.sh:6`；`invoice/deploy/backup/docker-cleanup-state.sh:23`；`invoice/deploy/backup/restore-drill.sh:157`；`invoice/deploy/rehearsal/shadow-eval.sh:274`；`invoice/docs/PRODUCTION-RUNBOOK.md:3171`

具体失败场景：container/network 实际仍在；rm 与 inspect 因资源授权或特定 endpoint 错误返回非零，但独立 docker info 成功。

实际验证/输出：两种资源的 fake Docker 明确保留 resource_present=true、rm/inspect denied、info=0，remove_docker_resource_strict 均 exit 0。无资源/daemon 不可达/确实存在三个对照分别 0/2/1。

影响链：restore/shadow exit trap → falsely reports absence/success，可能留下含 restored DB 的容器或网络。未验证当前 Docker daemon 的授权配置，只证明该失败状态被错误合并。

建议修法：单独证实资源不存在；保留 inspect 诊断并区分 not-found 与其它错误，或者以成功的精确资源清单查询判定。同步手册的过强“info 可达即不存在”说法。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-006](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/cleanup-inspect-denied-container/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/cleanup-inspect-denied-network/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/cleanup-control-absent/probe.sh；exit=0`
- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/cleanup-control-unknown/probe.sh；exit=2`

### 37. IDEP-007 / P1 — roll-forward 忽略 ingest-proxy 重启失败并继续成功路径

文件行号：`invoice/deploy/roll-forward.sh:100`；`invoice/deploy/roll-forward.sh:116`

具体失败场景：docker restart invoice-system-prod-ingest-proxy-1 返回 42，而随后 API 探针暂时仍满足通过条件。

实际验证/输出：原始 restart_ingest_proxy 提取函数使用 fake docker=42、inert sleep，函数 exit 0，之后 continued_after_failed_restart=true。

影响链：roll-forward step 5 失败 → trap 被取消 → 后续只看 API 与全局 tag 计数，可能输出 PASS 而未完成宣称的 proxy 恢复。

建议修法：让 helper 返回重启失败；正常调用显式阻断成功路径；EXIT trap 可 best-effort 尝试但将原始状态/恢复状态分别保留。保留现有 restart 次序。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-007](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:/Git/bin/bash.exe /g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/rollforward-ingest-restart-failure/probe.sh；exit=0`

### 38. INT-TEST-02 / P1 — Keyfile strict-JSON tests pass when unknown-field and trailing-JSON rejection are removed

文件行号：`invoice/backend/internal/securefields/keyfile_test.go:28`；`invoice/backend/internal/securefields/keyfile.go:43`

具体失败场景：The unknown and trailing fixtures also contain an empty encryption-key map and invalid base64 index_key=x. If their specific JSON rejection guard disappears, later key validation still errors and satisfies the test.

实际验证/输出：All 4 existing securefields tests pass both separate guard-removal mutants. A valid synthetic keyring with only the targeted malformed property is accepted by the matching mutant; the strengthened test fails.

影响链：Strict keyring loading is a deployment/repair bootstrap prerequisite. Current guards are present; the finding is an ineffective regression gate, not a current bypass in unmodified implementation.

建议修法：Build unknown/trailing inputs from the existing valid synthetic keyring; mutate one property per test and retain a valid positive control. Add exact malformed-base64 cases separately.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / INT-TEST-02](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `go test -p 1 -count=1 -timeout=60s ./internal/securefields -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\keyfile-unknown-existing.overlay.json ./internal/securefields -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\keyfile-trailing-existing.overlay.json ./internal/securefields -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\keyfile-regression-baseline.overlay.json ./internal/securefields -run ^TestAuditKeyfile -v；exit=0`

### 39. INT-TEST-03 / P1 — Traversal test passes with all LocalStore.OpenAuthorized path guards removed

文件行号：`invoice/backend/internal/document/service_test.go:66`；`invoice/backend/internal/document/service.go:128`

具体失败场景：The test asks for ../secret.pdf without creating that sibling file. Replacing OpenAuthorized with direct os.Open(filepath.Join(...)) still returns file-not-found and passes err!=nil.

实际验证/输出：All 3 existing LocalStore tests pass the direct-open guard-bypass mutant. A fully synthetic existing sibling file makes the strengthened traversal test fail under the mutant, while current source passes.

影响链：Document-store boundary regression can pass the local test gate. Current implementation rejects this traversal; no production path access was attempted.

建议修法：Create a readable sibling sentinel inside a synthetic parent sandbox, retain an allowed issued-file control, and assert a non-filesystem-missing policy rejection.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / INT-TEST-03](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `go test -p 1 -count=1 -timeout=60s ./internal/document -run ^Test(SavePDF|OpenAuthorized) -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\document-mutant-existing.overlay.json ./internal/document -run ^Test(SavePDF|OpenAuthorized) -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\document-baseline-regression.overlay.json ./internal/document -run ^TestAuditOpenAuthorized -v；exit=0`
- `go test -p 1 -count=1 -timeout=60s -overlay G:\xingmang\logs\full-audit-20260910\invoice-tests\backend-internal\document-mutant-regression.overlay.json ./internal/document -run ^TestAuditOpenAuthorized -v；exit=1`

### 40. INV-AUX-003 / P1 — export/eval掩盖命令替换失败：Keycloak启动与测试DB环境捕获

文件行号：`invoice/deploy/keycloak/entrypoint.sh:10`；`platform/docs/runbooks/GIT-WORKFLOW.md:94`；`platform/scripts/dev/worktree-testdb.sh:15`；`platform/internal/platform/credentials/store_integration_test.go:18`

具体失败场景：secret 文件包含被 read_secret 明确拒绝的 CR/内部换行，或读取失败。read_secret 在命令替换中 exit 1，而 export 自身返回 0。

实际验证/输出：仅 synthetic-format-input.txt 内容作为输入：stderr 报 invalid secret file，但下游启动哨兵被执行，KC_DB_PASSWORD 为空，exit 0；正确分离赋值/导出的隔离对照 exit 1 且不触达哨兵。

影响链：Keycloak entrypoint 的 export 吞掉 secret 校验失败并继续启动

建议修法：先执行普通赋值并明确检查 read_secret 返回值，再 export；DB 与 bootstrap 两条路径都补无内容泄露的失败断言。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-AUX-003](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)；[platform-docs / PDOC-05](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/entrypoint-invalid-format.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/entrypoint-control-v2.sh；exit=1`
- `Run exact documented shell wrappers with only a synthetic provisioning script that exits17；exit=见原记录`

### 41. INV-AUX-004 / P1 — spool 与投影网络成员门禁丢失进程替换生产者退出码

文件行号：`invoice/deploy/check-pending-spools.sh:30`；`invoice/deploy/provision-projection-networks.sh:63`

具体失败场景：find 因权限/IO 失败且不产生条目，或 Docker member inspect 失败。mapfile/while 只收到 EOF，进程替换退出状态没有进入主 shell 的判断。

实际验证/输出：fake find return73：exit0 并打印 PENDING-SPOOLS-EMPTY/可以改钉子；fake docker inspect return74：validate_members exit0 并到达 accepted。真实 fixture pending 文件控制为 exit1，unexpected member 控制为 exit1。

影响链：spool 与投影网络成员门禁丢失进程替换生产者退出码

建议修法：在普通赋值/临时文件中检查 find/inspect 原生命令状态，成功后才 mapfile/逐项检查；扫描失败应是环境失败而不是空集合。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-AUX-004](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/spools-find-error.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/spools-empty-control.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/spools-pending-control.sh；exit=1`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/network-member-inspect-error.sh；exit=0`

### 42. INV-AUX-005 / P1 — 文件类型的 AND 列表不 fail-closed：secret 目录及备份/恢复/helper symlink guard 均可继续

文件行号：`invoice/deploy/preflight-secret-permissions.sh:14`；`invoice/deploy/backup/backup.sh:29`；`invoice/deploy/backup/restore-drill.sh:54`；`invoice/deploy/backup/restore-drill.sh:76`；`invoice/deploy/rehearsal/shadow-eval.sh:42`；`invoice/deploy/rehearsal/shadow-eval.sh:157`；`invoice/deploy/rehearsal/shadow-eval.sh:214`；`invoice/deploy/validate-keycloak-admin-allowlist.sh:5`

具体失败场景：某个 secret 路径误建为目录，但其 UID/mode 恰好符合该 secret（例如 owner_database_url 目录 UID10001 mode0400）。test -f 位于 AND 列表的非末项，失败不会触发 set -e，后续 stat 匹配即返回成功。 相同 test -f && test ! -L && test -s 模式还接受 symlink：-L 否决处位于非末项，set -e 不终止，继续执行后续语句。

实际验证/输出：fixture 中真实 directory-instead-of-file 路径，stat 仅 fake 回报预期 uid/mode：check_file exit0 并打印 secret_file_gate_accepted。未改变真实文件 owner/mode，也未读取任何真实 secret。 补充用真实 fixture symlink 运行 12 个逐字提取的 guard，全部继续到 accepted，exit0；只使用新建的非敏感 marker，未读取真实 key/env。

影响链：宿主 secret preflight 可把预期 UID/mode 的目录当作合法 secret 文件

建议修法：将 -f、非符号链接、非空组合为一个显式失败分支；check_file 与 check_group_file 共用一致的拒绝方式。 对所有已定位同根类型 guard 使用合并 [[ 条件 ]] 或显式 || die，覆盖 directory、symlink 与 regular file 的独立负/正例；不要只改 preflight 两处。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-AUX-005](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/preflight-directory-secret.sh；exit=0`
- `C:/Windows/System32/wsl.exe --exec bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/fixtures/filetype-symlink/probe-after-existing-link.sh；exit=0`

### 43. INV-AUX-006 / P1 — 管理员邀请的异地 ACK 内容不匹配仍通过，现有测试不覆盖消费端

文件行号：`invoice/deploy/keycloak/invite-permanent-master-admin.sh:575`；`invoice/scripts/test-keycloak-offsite-ack.ps1:1`

具体失败场景：上传一份签名可信但 record_id/backup_manifest_sha256 属于另一记录的 OFFSITE-ACK。签名校验只能证明来自可信签发方；jq -Rn 的布尔结果 false 默认仍 exit0，内容 gate 不会 die。

实际验证/输出：准确提取 jq gate：有效 ACK 与其他记录 ACK 均 exit0/accepted；仅增加 -e 的隔离对照 exit1。复制原 ACK 测试，仅重绑临时目录和保留清理目录，baseline exit0；在同一复制项目删除消费端整个 ACK 内容 gate 后测试仍 exit0，消费端变异存活。

影响链：管理员邀请的异地 ACK 内容不匹配仍通过，现有测试不覆盖消费端

建议修法：使用 jq -eRn 并增加消费端有效/错误 record/错误 hash/错误行数测试；签名验证保持独立，避免把“签名正确”等同于“签名绑定本次备份”。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-AUX-006](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/offsite-ack-valid.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/offsite-ack-other-record.sh；exit=0`
- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/offsite-ack-control-exit-status.sh；exit=1`

### 44. INV-AUX-007 / P1 — 当前 runbook 要求 RC100 管理员安装，但操作器固定拒绝非 RC38 身份

文件行号：`invoice/deploy/keycloak/invite-permanent-master-admin.sh:29`；`invoice/deploy/keycloak/invite-permanent-master-admin.sh:108`；`invoice/docs/PRODUCTION-RUNBOOK.md:1267`

具体失败场景：依当前可粘贴 runbook 准备 RC100 的 SOURCE_TAG/KEYCLOAK_IMAGE 后调用所指向操作器，即使单独修好布局也会被固定 RC38 tag/image 拒绝。

实际验证/输出：提取 exact identity gates，在 SOURCE_TAG=v0.1.0-rc100-signed、KEYCLOAK_CONFIG_IMAGE=invoice-keycloak:0.1.0-rc100 下 exit1：installed source tag is not the exact RC38 signed tag。

影响链：当前 runbook 要求 RC100 管理员安装，但操作器固定拒绝非 RC38 身份

建议修法：只修RC100/当前版可直接运行的错误文档。RC38固定tag/image/attestation保持不动；实际扩展维护工具准入需负责人决定。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-AUX-007](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `bash /mnt/g/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/fixtures/keycloak-runbook-rc100-binding.sh；exit=1`

### 45. INV-DOC-03 / P1 — blocked-cycle 处置段仍引导跳过 acknowledge 直接解冻

文件行号：`invoice/docs/PRODUCTION-RUNBOOK.md:2184`；`invoice/docs/ELIGIBILITY-OPERATIONS.md:831`；`invoice/backend/internal/postgresstore/eligibility_operations.go:92`；`invoice/backend/internal/httpapi/server.go:1271`

具体失败场景：dead事件绑定blocked cycle；操作者按2445-2449“accept data lost and resolve freeze”分支，尚未运行ingest-acknowledge-unreplayable就调用POST .../resolve。

实际验证/输出：关联dead行仍存在，assertNoBlockingDeadEventForFreezeTx返回ErrEligibilityDeadEventUnrepaired，HTTP固定409；与同文2184-2194及Eligibility已明确的先acknowledge后resolve流程相反。

影响链：事件不可重放时的运维修复链；守卫正确，错误在后部旧操作指导。

建议修法：仅更新2445-2449说明并链接2184-2194/Eligibility acknowledge段；不放宽dead-event guard，不新增自动写off。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / INV-DOC-03](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python invoice-docs/kind-and-doc-proof.py（提取的无副作用前置判据/静态文档对照）`

### 46. INV-DOC-04 / P1 — 影子评估计划模板要求先于 tag，与本手册已加载签名候选镜像流程相冲突

文件行号：`invoice/docs/PRODUCTION-RUNBOOK.md:197`；`invoice/deploy/rehearsal/shadow-eval.sh:161`；`invoice/docs/handoffs/RELEASE-RC110.md:46`

具体失败场景：新RC逐项照抄计划模板：Task1源码门禁之后、创建tag之前必须shadow ready；同时遵守§3先签名tag绑定HEAD再产物，以及§11.2先加载该候选镜像才能shadow。

实际验证/输出：模板给出相互冲突的依赖顺序：候选tag/镜像还不存在，shadow-eval在docker image inspect处失败，或迫使操作者自行绕过模板步骤。历史RC110交接已经记录tag→镜像/传输→shadow→发布的实际顺序。

影响链：新RC计划生成与影子评估发布门禁依赖；未执行shadow或生产操作。

建议修法：对齐模板文字至既有已批准发布顺序（tag/受验镜像生成加载后、roll-forward前运行）；保留差异触发--reproject-all、结果阻断规则和负责人批准，不改变发布实现或业务算法。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / INV-DOC-04](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python invoice-docs/kind-and-doc-proof.py（提取的无副作用前置判据/静态文档对照）`

### 47. INV-PG-001 / P1 — Concurrent-index finalizer returns success when mandatory evidence writes fail

文件行号：`invoice/deploy/postgres/apply-source-readiness-index-concurrently.sh:254`；`invoice/docs/PRODUCTION-RUNBOOK.md:721`

具体失败场景：The prebuild/checks finish successfully, then the EXIT finalizer cannot write result.env (for example ENOSPC/EIO) or cannot create the checksum manifest. finalize_record saves the incoming zero status, disables errexit, never folds evidence command failures into status, and exits 0. The simulated manifest failure leaves status=passed plus a zero-byte signing manifest.

实际验证/输出：Injected cat failure exit 73 and manifest pipeline failure exit 74 both produce operator finalizer exit 0; healthy control also exits 0. Empty result or manifest artifacts remain.

影响链：RC39 concurrent index prebuild -> retained evidence -> namespace-bound signing -> migration prerequisite. No live database effect was exercised.

建议修法：Preserve incoming failures and explicitly propagate hash/result/manifest/permission/sync failures into final status, as the balance-cleanup finalizer already does; add isolated failure-injection assertions.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-PG-001](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:\Git\bin\bash.exe finalizer.sh healthy；exit=0`
- `D:\Git\bin\bash.exe finalizer.sh cat-failure；exit=0`
- `D:\Git\bin\bash.exe finalizer.sh manifest-failure；exit=0`

### 48. INV-PG-002 / P1 — Multi-file bash -n gates parse only the first shell script

文件行号：`invoice/scripts/verify-source-readiness-index-operator.ps1:279`；`invoice/scripts/verify-balance-history-cleanup-operator.ps1:175`；`invoice/scripts/verify.ps1:160`

具体失败场景：A later shell operand gains an unterminated if block. Bash treats subsequent filenames as positional arguments to the first script, so the advertised multi-file syntax gate exits 0 and release verify can retain broken deployment helpers. The top-level invoice verify gate has the same defect for all later enumerated deploy/scripts shell paths.

实际验证/输出：For three isolated mutations, direct bash -n target exits 2 with unexpected end of file, while the copied corresponding PowerShell gate exits 0 and prints contracts passed. Both unchanged gates exit 0. Supplement: invoice/scripts/verify.ps1:162 uses the same multi-file bash -n invocation. Exact lines 161-163 with valid first/invalid second return 0; direct parse of the invalid second file returns 2; invalid-first and per-file-loop controls return 1. No full verify was executed.

影响链：invoice/scripts/verify.ps1 -> postgres operator static gates -> deploy/restore helper syntax readiness. Top-level invoice/scripts/verify.ps1 shell syntax validation is directly affected, not only its two operator subgates.

建议修法：Iterate every intended script and run bash -n once per file, preserving each native exit; retain a negative syntax mutation for every non-first operand. Apply the per-file bash -n loop to verify.ps1:160-163 as well as both operator gates; retain invalid-later-operand mutations.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-PG-002](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\invoice-deploy\auxiliary\postgres\fixtures\gates-baseline\invoice\scripts\verify-source-readiness-index-operator.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\invoice-deploy\auxiliary\postgres\fixtures\gates-baseline\invoice\scripts\verify-balance-history-cleanup-operator.ps1；exit=0`
- `D:\Git\bin\bash.exe -n G:/xingmang/logs/full-audit-20260910/invoice-deploy/auxiliary/postgres/fixtures/index-verifier-syntax/invoice/deploy/postgres/verify-source-readiness-index.sh；exit=2`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\invoice-deploy\auxiliary\postgres\fixtures\index-verifier-syntax\invoice\scripts\verify-source-readiness-index-operator.ps1；exit=0`

### 49. INV-PG-003 / P1 — Readiness plan guard can be bypassed while its static contract gate stays green

文件行号：`invoice/scripts/verify-source-readiness-index-operator.ps1:103`；`invoice/deploy/postgres/verify-source-readiness-index.sh:268`

具体失败场景：Insert return 0 as the first line of assert_plan, leaving all expected text and SQL intact. The static gate searches marker strings, so a disabled index/Seq Scan rejection still passes.

实际验证/输出：Unmodified extracted predicate accepts the good index plan (0) and rejects the Seq Scan/no-index plan (1). Early-return mutant accepts the prohibited plan (0), and the full copied static gate remains 0.

影响链：Local source gate -> release-bound readiness verifier -> production planner check. The current original predicate itself rejected the tested invalid plan; the defect is regression-gate effectiveness.

建议修法：Add dynamic fixture assertions for required index presence, ordinary/parallel source_ingest_events Seq Scan rejection, and acceptance of an allowed plan; mutation-test a bypass of those predicates.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / INV-PG-003](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\invoice-deploy\auxiliary\postgres\fixtures\gates-baseline\invoice\scripts\verify-source-readiness-index-operator.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\invoice-deploy\auxiliary\postgres\fixtures\index-plan-predicate-bypass\invoice\scripts\verify-source-readiness-index-operator.ps1；exit=0`
- `D:\Git\bin\bash.exe baseline.sh good.txt；exit=0`
- `D:\Git\bin\bash.exe baseline.sh bad.txt；exit=1`

### 50. INV-WIRE-01 / P1 — V3 JSON Schema 契约门禁未验证示例符合 schema，事件名变异后仍通过

文件行号：`invoice/agents/sourceagent/v3_protocol_test.go:17`；`invoice/agents/sourceagent/v3_protocol_test.go:51`；`invoice/scripts/verify.ps1:589`；`invoice/contracts/source-agent-batch.v3.schema.json:118`

具体失败场景：仅将隔离 schema 的 usageRecord.entity_type.const 从 usage_event 改为 usage_event_BROKEN，保持示例、Go 发送端和接收端不变。6 项契约相关 Go 测试与原 verify.ps1 精确提取的 V3 契约块均 exit 0；真实 JSON Schema 验证拒绝同一示例，exit 1。

实际验证/输出：["logs/sourceagent-contract-baseline-exact-toolchain.json", "logs/sourceagent-contract-schema-mutant.json", "logs/powershell-v3-contract-baseline.json", "logs/powershell-v3-contract-schema-mutant.json", "logs/actual-jsonschema-mutant.json", "fixture-schema-mutant/invoice/contracts/source-agent-batch.v3.schema.json"]

影响链：invoice verify 的契约检查 -> 发布带有失配 schema 的候选 -> 按已发布 schema 验证/生成的接入方与实际 usage_event producer/consumer 不一致；现有门禁不能阻止此类漂移。没有宣称本次跑过完整 verify。

建议修法：让已发布 v1/v2/v3 示例真正通过各自 schema，并增加错误事件名/required/type/引用失效的负向变异；继续保留真实 producer/consumer 验证。使用现有可用校验能力，不升级或新增依赖，不修改生产 schema 定义以迁就错误。 本机既有 PowerShell Test-Json -SchemaFile 已验证能区分该基线/变异，可优先接入现有 verify；无需新增依赖。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / INV-WIRE-01](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

### 51. INV-WIRE-02 / P1 — 真实批次接收器接受必填 array/boolean 为 null，违反 V3 契约并静默变成空批/false

文件行号：`invoice/backend/internal/sourceingest/receiver.go:349`；`invoice/backend/internal/sourceingest/receiver.go:73`；`invoice/backend/internal/sourceingest/receiver.go:412`；`invoice/contracts/source-agent-batch.v3.schema.json:32`；`invoice/contracts/source-agent-batch.v3.schema.json:35`

具体失败场景：保持已发布 usage 示例的其余合法元数据：A 把 records 设为 null、scan_complete 设为 true；B 仅把 scan_complete 设为 null。真实生产 decodeBatch 的精确隔离提取分别返回成功并得到 records=0/scan_complete=true 和 records=1/scan_complete=false；同一输入被 schema 拒绝。

实际验证/输出：["receiver-extraction.json", "receiver-fixture/receiver.go", "receiver-fixture/records-null.json", "receiver-fixture/scan-complete-null.json", "logs/production-decoder-baseline-and-null-proof.json", "logs/jsonschema-null-input-proof.json"]

影响链：通过已有认证的来源发出形状错误批次 -> decodeBatch 放行 -> receiver.go:191-200 将空 Events/ScanComplete 传给 Acceptor；source_sync.go:267-302、403-424 不再保留 null 与显式值的区别，可能把错误空批作为扫描完成输入处理。数据库影响仅按源码链静态推导，本次没有写数据库或验证线上 ACK。

建议修法：在解码边界验证 records 的原始 JSON 类型必须是数组、V3 scan_complete 必须是布尔；保留合法 records=[] 与 scan_complete=false。补缺席/null/错误类型负例和真实解码器回归，不改业务算法或生命周期。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / INV-WIRE-02](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

### 52. IT-CLI-02 / P1 — Per-account/event repair failures print APPLIED and exit 0

文件行号：`invoice/backend/cmd/eligibility-repair/main.go:343`；`invoice/backend/cmd/eligibility-repair/main.go:357`；`invoice/backend/cmd/eligibility-repair/main.go:376`；`invoice/backend/cmd/eligibility-repair/main.go:432`；`invoice/backend/internal/postgresstore/projection_requeue_dead_repair.go:96`

具体失败场景：The store deliberately isolates account/event transactions. A real serialization/query/audit/commit failure is collected in result.Errors and the store returns result,nil. The four CLI wrappers print the report and unconditionally return nil; even if the only account failed and rolled back, the process prints APPLIED and exits 0.

实际验证/输出：An exact extracted projection wrapper with an in-memory store result containing one rolled-back synthetic error returned nil, printed APPLIED / total requeued: 0 / ACCOUNT ERRORS. Exact extracted main + wrapper with all I/O replaced by that fake exited 0. Removing the error-report section makes the existing formatter test fail, proving the test covers text but not success/failure propagation.

影响链：queue-narrow, projection-requeue-dead, ingest-requeue-dead, policy-start-reanchor wrappers -> set -e / exit-code caller records success while repair work remains failed.

建议修法：Keep per-unit continuation/transactions unchanged; after printing the full report return a nonnil aggregate error when result.Errors is nonempty. Add zero/all/partial-error fake-store tests and a process exit assertion. Do not change policy-start-reanchor Blocked/NoOp policy in this fix.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / IT-CLI-02](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

### 53. IT-CLI-03 / P1 — Acknowledge repair account-flag rejection test is masked by the missing-event guard

文件行号：`invoice/backend/cmd/eligibility-repair/main_test.go:328`；`invoice/backend/cmd/eligibility-repair/main.go:192`；`invoice/backend/cmd/eligibility-repair/main.go:195`；`invoice/backend/cmd/eligibility-repair/main.go:201`

具体失败场景：The test claims that --account is rejected for ingest-acknowledge-unreplayable, but supplies accountID without required eventID. The missing-event guard fires first. A meaningful mutant removes the dedicated account rejection AND exempts this kind from the shared generic account guard; the original test and nearby guard tests still pass although event+account now reaches the no-I/O success boundary.

实际验证/输出：Existing extracted argument tests: baseline 0; mutant disabling BOTH account-rejection routes for acknowledge 0. Strong event+account control: baseline 0; mutant 1 with error=<nil>. Earlier mutant deleting only the dedicated guard merely changed the error wording because a generic fallback still rejected it; that earlier result is not evidence of a safety defect.

影响链：Safety regression could silently accept an ignored account restriction on the one-event irreversible acknowledge CLI and pass the asserted guard test.

建议修法：Supply a valid eventID alongside accountID so the target guard is reachable, assert the --account-specific error and empty output; move pure argument-guard tests off DB fixtures. Preserve current production guard.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-tests / IT-CLI-03](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)

### 54. ITR-REG-01 / P1 — Unexpected task lookup errors are mistaken for absence before forced registration

文件行号：`invoice/scripts/register-trivy-refresh-task.ps1:120`；`invoice/scripts/register-trivy-refresh-task.ps1:140`；`invoice/scripts/test-register-trivy-refresh-task.ps1:233`

具体失败场景：Get-ScheduledTask emits a nonterminating PermissionDenied or ResourceUnavailable error and no object; fake registration then succeeds

实际验证/输出：Lookup errors are silenced; Register-ScheduledTask -Force is called once; script returns exit 0

影响链：Daily Trivy cache task registration/update fail-closed behavior

建议修法：Distinguish positively identified not-found from unexpected lookup errors and abort before writes on errors; behavior-test nonterminating/terminating failures; consider no Force on the proven-absent registration path to reject creation races

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / ITR-REG-01](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

### 55. ITR-REG-02 / P1 — Registration wiring source-text assertions miss branch, WhatIf and refresh-invocation regressions

文件行号：`invoice/scripts/test-register-trivy-refresh-task.ps1:212`；`invoice/scripts/register-trivy-refresh-task.ps1:102`；`invoice/scripts/register-trivy-refresh-task.ps1:123`

具体失败场景：Independently mutate copied wrapper: invert existing-task branch; bypass Register ShouldProcess; insert dynamic & $refreshScriptPath invocation

实际验证/输出：All three original-test runs exit 0; independent copied-wrapper executions show wrong mutating action, WhatIf mutation, or synthetic refresh execution; all three independent contracts exit 1

影响链：Tests intended to guard registration/update, WhatIf and scheduling-only guarantees

建议修法：Execute copied wrapper behind Get/Register/Set fakes and a guarded synthetic refresh file; assert branch, WhatIf, no invocation, failure propagation and parameters; keep effective builder assertions and require these mutants to fail

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / ITR-REG-02](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

### 56. OPS-02 / P1 — REQLOG 容器验证没有 Compose 文件且固定了错误的项目名

文件行号：`platform/docs/runbooks/REQLOG-RECORDER.md:153`

具体失败场景：按第 7 步方式二部署的是 xingmang-launch，第 8 步却运行 docker compose -p xingmang-prod exec，且没有 -f。刚执行过的 cwd /srv/deploy/xingmang-platform 中不存在默认 compose.yaml/docker-compose.yaml，故先因缺配置失败；补 -f 后仍会向错误项目查 platform-api。

实际验证/输出：["static-checks.json 与源码静态核对；未调用实际 docker exec。"]

影响链：file 模式部署后验证、回滚后复核。

建议修法：沿用所选部署方式的完整 compose 文件、override、env-file 和项目参数；本机方式用 xingmang-launch。可用不输出 token 映射内容的可读性验证代替 cat 内容。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-02](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 57. OPS-03 / P1 — NewAPI/Sub2API 切换卡仍把 env 缺省当作生效配置

文件行号：`platform/docs/runbooks/SWITCH-NEWAPI-REAL.md:47`；`platform/docs/runbooks/SWITCH-NEWAPI-REAL.md:232`；`platform/docs/runbooks/SWITCH-SUB2API-REAL.md:29`；`platform/docs/runbooks/SWITCH-SUB2API-REAL.md:89`

具体失败场景：数据库已有 mode=fake 行时，按卡片把 XM_*_MODE=real 并重启仍为 fake；已有 mode=real 行时，把 env 改 fake 不能回退。在完全没有 DB 行的正常 env-only 情形，real 生效日志 source=env，卡片却要求 source=database。Sub2API 的生产回退也没有给出停用同步的正确方式。

实际验证/输出：["mode-precedence.log：两个平台 db_fake_env_real 均返回 fake/database；db_real_env_fake 均返回 real/database；no_db_env_real 均返回 real/env。", "运行的是从候选逐行提取的 ResolveEffectiveMode，未接数据库、API 或凭据。"]

影响链：NewAPI/Sub2API real 切换与回退、切换后的日志验收。

建议修法：以既有后台 connector.config.set Action 的 DB 配置作为正式切换流程；标明 env 仅为无 DB 行时的 legacy 缺省，并按真实 source 验证。停采集用既有 XM_*_SYNC_ENABLED=false；不改动态配置或业务策略。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-03](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 58. OPS-05 / P1 — SHADOW-COMPARE 用 go run 抹平文档要求的退出码 1 与 2

文件行号：`platform/docs/runbooks/SHADOW-COMPARE.md:58`

具体失败场景：平台连接串缺失等配置错误让 platform-shadow 返回 2，但文档唯一完整调用是 go run ./cmd/platform-shadow；Go 工具包装后对调用 shell 返回 1。自动归档或运维脚本若按退出码表把 1 视为“已产出 dirty 报告”，就会把未生成报告的运行当成已完成对比。

实际验证/输出：["shadow-direct-exit2.log: 相同 fixture 程序直接执行 exit=2。", "shadow-go-run-exit2.log: go run 相同文件 exit=1，stderr=exit status 2。", "仅运行 dependency-free 退出码 fixture，未运行实际影子对比或连接任何库。"]

影响链：14 天影子对比归档与每日结果分类。

建议修法：先 go build 出 platform-shadow，再直接执行该二进制；保持 0/1/2 原始退出码，不调整业务比较算法。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-05](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 59. OPS-06 / P1 — 审计归档 fixture 的 teardown 缺少必需的 env 文件变量

文件行号：`platform/docs/runbooks/AUDIT-ARCHIVE.md:17`

具体失败场景：第一条启动命令把 XM_ARCHIVE_CREDENTIAL_ENV_FILE 作为单次命令前缀，只对该 docker 进程有效。随后照抄 down 命令时变量未导出，Compose 先解析 archive.yaml 的 ${...:?}，在执行 teardown 前报错，独立 fixture 的容器/卷无法按操作卡收尾。

实际验证/输出：["archive-config-with-env.log: 复制模型+新建无凭据 synthetic env，config --quiet exit=0。", "archive-config-without-env.log: 同模型去掉该变量，config --quiet exit=1，报 required variable missing。", "未执行 up/down，也未访问任何已存在 env 文件。"]

影响链：AUD2 disposable MinIO fixture 启停流程；不代表生产归档已解锁。

建议修法：在 teardown 同样前置该变量，或明确在当前 shell export 并复用同一 operator-managed 文件路径。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-06](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 60. OPS-07 / P1 — REQLOG 声称历史 tokenmap 会自动获得新权限，但 WriteFile 保留旧 mode

文件行号：`platform/docs/runbooks/REQLOG-RECORDER.md:52`

具体失败场景：旧 tokenmap.json 为 root:root 0600，操作者按第 3 步只修 data/ 并相信“下次刷新自动以新权限重写”而跳过可选命令。刷新中的 os.WriteFile(...,0640) 对已存在文件保留 0600；随后 chownGroup 只改组，不加 g+r，因此容器 GID 10001 仍读不到 v1 映射，用户名保持为空。

实际验证/输出：["候选 tokenmap.go 的 WriteFile+chownGroup 静态调用链。", "本机 Go 标准库 C:/Program Files/Go/src/os/file.go:930-936 明确 existing file without changing permissions，且 flags=O_WRONLY|O_CREATE|O_TRUNC。", "没有在真实文件或 POSIX 生产主机上运行权限修改；未把 Windows mode 测试当成 Linux 证明。"]

影响链：记录代理收编后的历史用户名映射可读性修复。

建议修法：说明WriteFile不更改既有mode、真实权限迁移仍须批准；只对齐既有授权读组的文档前置，不自动更改记录器权限行为或真实文件。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-07](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 61. OPS-08 / P1 — REQLOG 的 99% 验收只数 tokenmap 字段，无法检出读侧完全失效

文件行号：`platform/docs/runbooks/REQLOG-RECORDER.md:228`

具体失败场景：将复制的 resolveUserRef 成功分支改成 return nil，所有前缀命中的请求都拿不到 User，但 runbook 的 jq 仍只检查 tokenmap.entries 的 user_id 非空，返回 pct=100。它既没读取请求索引，也没调用真实解析器，因此不能证明文档声明的“前缀命中的记录至少 99% 能解出 User”。

实际验证/输出：["userref-baseline.log: matched_record_has_user=true。", "userref-mutant.log: 将复制实现唯一成功返回改成 nil 后 matched_record_has_user=false。", "jq-doc-gate-baseline.log 与 jq-doc-gate-mutant.log 均 exit=0、pct=100。", "fixture/main.go、mutant.go 和 tokenmap.synthetic.json 全保留；没有修改候选实现。"]

影响链：CR-0008 tokenmap-v2 用户关联验收。

建议修法：把现有jq检查明确降为tokenmap形状检查，不能作为真实读侧99%通过证据；保留既有99%标准并要求实际读侧证据。可以加强现有解析器的合成回归，不新增生产采样API或另创验收政策。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-08](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 62. OPS-09 / P1 — evidence-capture 示例输出根与后续 sha256 校验目录不一致

文件行号：`platform/docs/runbooks/USERS-REAL-APPROVAL.md:100`；`platform/docs/runbooks/USERS-REAL-APPROVAL.md:136`

具体失败场景：capture 示例明确传 --out docs/evidence/users-real，实现仅在 --out 为空时追加平台目录；显式 out 后实际目录是 docs/evidence/users-real/<timestamp>，但第 3 节让操作者 cd docs/evidence/users-real/<platform>/<timestamp>，目录不存在。同一秒跑两个平台还可能撞同一 out 目录而被拒绝覆盖。reqlog 示例已有 /reqlog，不受前一项影响。

实际验证/输出：["static-checks.json: 依据候选 capture.go:244-248 记录 explicit out、actual 与 documentedVerify 三者。", "只静态跟踪文件路径，不运行正式采集，不写源树的 docs/evidence。"]

影响链：用户 real 证据生成、审批前哈希复核。

建议修法：capture 示例分别给 --out docs/evidence/users-real/sub2api 与 /newapi，或省略 --out 采用现有平台化缺省；sha256 校验使用 CLI 实际输出路径。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-09](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 63. OPS-10 / P1 — 用户证据手册把 env-only 连接器配置错误描述成文件凭据登记前提

文件行号：`platform/docs/runbooks/USERS-REAL-APPROVAL.md:44`；`platform/docs/runbooks/USERS-REAL-APPROVAL.md:100`；`platform/docs/runbooks/SWITCH-NEWAPI-REAL.md:49`；`platform/docs/runbooks/SWITCH-SUB2API-REAL.md:31`

具体失败场景：未登记后台文件凭据的环境，操作者严格照两份 SWITCH 卡把 token 写入部署 .env 并使 worker real 正常工作；USERS 卡说这是同一套注册流程，可直接复用同一 credential-ref。实际上 worker 支持 env fallback，而 evidence-capture 仅 FileProvider，既不读取部署 env 也不提供 env fallback，因此正式 capture 在解析 credential-ref 时失败。

实际验证/输出：["静态核对两个 Provider 装配，未尝试解析真实凭据。", "capture.go:101-109 明确注释 deliberately no environment-variable fallback。"]

影响链：Sub2API/NewAPI 用户详情 real 证据采集。

建议修法：明确要求通过现有后台登记到共享文件库，并在宿主机执行时提供可读副本/挂载和正确 --secret-root；将 env legacy worker 配置与 evidence-capture 前提分开。不新增 CLI 明文 token。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-docs / OPS-10](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)

### 64. PC-001 / P1 — 预算解析 unknown-field 用例使用必定无效的空 capabilities，移除严格解码后仍绿

文件行号：`platform/internal/platform/connector/budget_policy_test.go:108`；`platform/internal/platform/connector/budget_policy.go:342`

具体失败场景：去掉 LoadPolicy 中 dec.DisallowUnknownFields() 后，TestLoadPolicyRejectsUnknownFieldsAndOverflow 仍 exit 0。unknown 用例 capabilities=[] 本来就被政策完整性检查拒绝，无法区分未知字段是否被拒绝。

实际验证/输出：{"command": "python mutation_budget.py", "results": "budget-mutation/results.json", "logs": ["budget-mutation/baseline.log", "budget-mutation/mutant.log", "budget-mutation/probe_baseline.log", "budget-mutation/probe_mutant.log"], "scope": "Go -overlay 只替换隔离副本；执行目标纯解析测试，不执行外部调用。"}

影响链：连接器 budgets.v1.json 严格解码回归门禁；未来移除未知字段防护会被此用例误判通过。当前实现仍有防护，未声称当前生产接受未知字段。

建议修法：用有效完整预算作前置成功基线，加入唯一未知字段后断言明确 unknown field 错误；保留空政策和溢出用例为独立校验。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / PC-001](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python mutation_budget.py；exit=见原记录`

### 65. PC-002 / P1 — 三个 SMS Action 契约仍宣称仅 HUMAN，当前运行定义已经允许 SERVICE

文件行号：`platform/contracts/actions/sms.code.fetch.v1.json:6`；`platform/contracts/actions/sms.number.request.v1.json:6`；`platform/contracts/actions/sms.resource.action.v1.json:5`；`platform/internal/platform/sms/actions.go:57`；`platform/internal/platform/sms/actions.go:270`；`platform/internal/platform/sms/actions_request.go:52`；`platform/internal/platform/sms/actions_import.go:32`

具体失败场景：按同 ID/version=1 的契约清单审查发布权限时，三项都得出 SERVICE 不在允许名单；实际代码的对应 Definition.PrincipalTypes 使用 humanOrService=[HUMAN,SERVICE]。sms.number.request notes 还将 XM-SMS4 机器支持写为未来工作，而实现注明已经落地。

实际验证/输出：{"command": "action_scan.go + compare_actions.py", "results": "action-comparison.json", "supporting": "action-definitions.json"}

影响链：Action 契约驱动的发布权限核对或客户端白名单会误判现有 SERVICE 入口；这是文档/实现矛盾，并非新发现的未授权生产放行。

建议修法：只同步三份契约的主体类型与 notes 到已经明确实现的 HUMAN/SERVICE 边界，补声明与 JSON 的针对性对照；不改运行权限、消费者配额或业务路径。 契约同时保留未开通生产机器身份通道的边界，不宣称新的生产准入授权。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / PC-002](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `action_scan.go + compare_actions.py；exit=见原记录`

### 66. POP-10 / P1 — promote 的 test-mode 保护只检查冒号拼接后的第一条路径

文件行号：`platform/deploy/scripts/promote.sh:96`

具体失败场景：bare repo 在隔离目录，但 --checkout、--status-dir 或 --audit-log 单独指向 /srv。

实际验证/输出：对四个路径拼接字符串做 /srv/* 匹配，只检查第一项。抽取纯参数前置：repo=/srv 被拒，其余三种受保护路径都 VALIDATION-PASSED exit 0。未读取或写入这些系统路径。

影响链：promote 的 test-mode 保护只检查冒号拼接后的第一条路径

建议修法：逐项校验所有目标路径及规范化别名，test-mode 不得读写任一受保护目标；补每个参数分别触发的负例。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-10](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 67. POP-11 / P1 — 只读窗口证据 helper 将未经校验的时间参数直接拼进 SQL

文件行号：`platform/deploy/scripts/cr0006-window-evidence.sh:41`

具体失败场景：--since/--until 除非空外无 RFC3339 校验，包含引号和分号的输入会脱离 timestamp 字面值。

实际验证/输出：fake Docker 记录到由输入携带的额外事务/SELECT 语句，脚本没有在 SQL 边界前拒绝并 exit 0。SET default_transaction_read_only 只是之前单独的设置，不能让拼接 SQL 成为安全数据参数。没有连接数据库或验证实际写入。

影响链：只读窗口证据 helper 将未经校验的时间参数直接拼进 SQL

建议修法：严格校验/解析 RFC3339 时间并用 psql 安全变量引用传值，保留只读事务约束；输入拒绝必须发生在 Docker/psql 调用前。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-11](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 68. POP-12 / P1 — 部署顺序与安装接线测试用字符串存在性冒充行为断言

文件行号：`platform/tests/deploy/deploy-local.test.sh:178`；`platform/tests/deploy/deploy0-b.test.sh:102`；`platform/tests/security/cpa-snapshot-runtime-wiring.test.sh:82`；`platform/tests/deploy/deploy0-a.test.sh:388`

具体失败场景：在拷贝实现中将 build 移到 config 前、将 up 移到 build 前、将 snapshot 安装块移到 app 启动后，或把安装器两条 Git config 语句注释掉。

实际验证/输出：相关原断言和基线均 exit 0。具体本地部署 trace build=7/config=8；服务器 trace config/up/build；CPA 仅静态测试未执行生命周期；安装器 grep 仍匹配被注释语句。

影响链：部署顺序与安装接线测试用字符串存在性冒充行为断言

建议修法：修deploy-local/deploy0-b/deploy0-a的非CPA测试；CPA专属测试不修改，分列POP-12-CPA待拍板。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-12](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 69. POP-12-CPA / P1 — CPA专属生命周期接线测试的顺序断言不充分（本轮禁止触碰CPA）

文件行号：`platform/tests/security/cpa-snapshot-runtime-wiring.test.sh:82`

具体失败场景：在拷贝实现中将 build 移到 config 前、将 up 移到 build 前、将 snapshot 安装块移到 app 启动后，或把安装器两条 Git config 语句注释掉。

实际验证/输出：相关原断言和基线均 exit 0。具体本地部署 trace build=7/config=8；服务器 trace config/up/build；CPA 仅静态测试未执行生命周期；安装器 grep 仍匹配被注释语句。

影响链：部署顺序与安装接线测试用字符串存在性冒充行为断言

建议修法：不改CPA代码或专属测试；将静态/既有变异证据交CPA负责人处理。

状态：owner-decision；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-12](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 70. POP-13 / P1 — 负向测试被前序错误遮住，删掉真实闸门仍全绿

文件行号：`platform/tests/deploy/deploy0-a.test.sh:319`；`platform/tests/deploy/deploy0-b.test.sh:109`

具体失败场景：分别删除 trusted CI 哈希比较、production 确认、ready 探针。

实际验证/输出：三种变异都通过既有 suite：坏哈希用例没传 DOCKER_ARGS_PATH，fake Docker 本身失败；prod 用例带 --test-mode 被更早的禁 prod 拒绝；ready 用例让 health/ready 都失败，先在 health 停下。补 fake 输出参数的哈希对照可成功杀死哈希变异。

影响链：负向测试被前序错误遮住，删掉真实闸门仍全绿

建议修法：让每个用例全部前置合法且只破坏目标条件，并断言目标错误文本、执行 trace 和退出码。prod 前置可以用安全抽取逻辑或全命令替身，不运行生产。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-13](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 71. POP-14 / P1 — 通知隔离测试检查 payload，未检查它已经捕获的环境文件

文件行号：`platform/tests/deploy/deploy0-b.test.sh:148`

具体失败场景：删除 deploy.sh 通知 env -i 的 -i，使合成调用者环境进入 fake 通知器。

实际验证/输出：notify.env 实际出现合成 SECRET_SENTINEL，但测试检查 notify.payload，完整 suite 仍 exit 0。基线 notify.env 没有该值。

影响链：通知隔离测试检查 payload，未检查它已经捕获的环境文件

建议修法：断言 notify.env 确实存在且不含合成 sentinel；保留 payload 结构断言，并验证移除 -i 后测试红。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-ops / POP-14](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)

### 72. PS-01 / P1 — 密钥材料门禁按Git声明扫描集合，却让rg忽略已跟踪的ignored文件

文件行号：`invoice/scripts/check-no-secrets.ps1:5`；`invoice/scripts/check-no-secrets.ps1:31`

具体失败场景：已跟踪的生成目录文件同时匹配.gitignore，包含无效合成识别标记；候选文件名不属于禁止名。

实际验证/输出：真实Git+rg控制：可见标记被拒绝1，相同标记移入已跟踪ignored目录后完整原脚本通过0。

影响链：源码材料安全门禁

建议修法：按已枚举的准确候选文件集合执行内容检查，不能再次隐式应用.gitignore/未说明glob排除；保留禁止文件名预检和不输出内容。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-ps / PS-01](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\scanner-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\S00-clean\invoice\scripts\check-no-secrets.ps1 clean；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\scanner-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\S01-visible-synthetic-marker\invoice\scripts\check-no-secrets.ps1 visible；exit=1`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\scanner-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\S02-tracked-ignored-synthetic-marker\invoice\scripts\check-no-secrets.ps1 ignored；exit=0`

### 73. PS-02 / P1 — 内容扫描器的Windows负退出码被当作未发现问题

文件行号：`invoice/scripts/check-no-secrets.ps1:33`

具体失败场景：扫描子进程返回负数崩溃码且stdout空；当前只判断LASTEXITCODE>1。

实际验证/输出：真实无I/O cmd退出-1073741819被rg替身保留；原扫描器仍报告passed并退出0。正数2错误控制正确拒绝1。

影响链：源码材料安全门禁

建议修法：只接受明确的rg 0/1，所有其他原生状态（含负值）失败；加入负/正异常退出控制。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-ps / PS-02](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\scanner-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\S00-clean\invoice\scripts\check-no-secrets.ps1 clean；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\scanner-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\S03-negative-native-exit\invoice\scripts\check-no-secrets.ps1 native-negative；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\scanner-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\S04-positive-native-error\invoice\scripts\check-no-secrets.ps1 native-positive-error；exit=1`

### 74. PS-03 / P1 — 生产只读预检把缺失/畸形磁盘与Docker网络输出当作通过

文件行号：`invoice/scripts/preflight-production.ps1:160`；`invoice/scripts/preflight-production.ps1:165`；`invoice/scripts/preflight-production.ps1:183`；`invoice/scripts/preflight-production.ps1:187`

具体失败场景：SSH退出0但df无数据行/格式坏，或network inspect无记录/坏记录；其余所有前置的合成状态正常。

实际验证/输出：逐字复制的原preflight在四种坏输出下全部0；磁盘满/网络重叠控制各1。只有SSH/HTTP/上游前置为隔离替身，没有访问服务器。

影响链：首次创建前的生产只读预检

建议修法：要求每项检查有可解释的非空数据与严格字段类型；不能把解析失败等同空集合/无冲突，保持现有容量阈值与网络政策。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-ps / PS-03](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 valid；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 df-empty；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 df-malformed；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 net-empty；exit=0`

### 75. PS-04 / P1 — New API精确版本预检实际只做整行子串匹配

文件行号：`invoice/scripts/preflight-production.ps1:155`；`invoice/scripts/preflight-production.ps1:156`

具体失败场景：期待v1.0.0-rc.25，但new-api容器镜像tag为v1.0.0-rc.250。

实际验证/输出：原preflight仍0；不共享前缀的v0.0.0控制拒绝1。

影响链：首次创建前的生产只读预检

建议修法：解析受核对容器的镜像字段并精确比较tag（合法digest后缀单独处理），不更改既有允许版本值。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-ps / PS-04](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 valid；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 version-prefix；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoLogo -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-harness.ps1 G:\xingmang\logs\full-audit-20260910\invoice-ps\preflight-fixture\preflight-production.ps1 version-wrong；exit=1`

### 76. PS-05 / P1 — 发布产物目录只拒绝叶节点链接，父目录junction可越过项目边界

文件行号：`invoice/scripts/release-image-gate.ps1:181`；`invoice/scripts/release-image-gate.ps1:187`；`invoice/scripts/verify-release-image-artifacts.ps1:47`；`invoice/scripts/verify-release-image-artifacts.ps1:51`；`invoice/scripts/release-image-gate-lib.ps1:1072`

具体失败场景：项目release目录本身是指向其它自有fixture目录的junction，leaf候选是普通目录。

实际验证/输出：原NoReleaseReparsePoints和生产者原始目录前缀都通过0，后者在别的fixture路径创建build/logs等；直接leaf junction控制拒绝1。无Docker或生产运行。

影响链：发布产物目录/完整性校验

建议修法：在创建或读取任何产物前验证完整目录链/物理边界；统一生产者和校验器，保留非空目录保护，不自动搬迁/清理任何已存在路径。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-ps / PS-05](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

### 77. PS-06 / P1 — SHA256SUMS生成与验证同时忽略隐藏产物，内容改动不被发现

文件行号：`invoice/scripts/release-image-gate-lib.ps1:1121`；`invoice/scripts/release-image-gate-lib.ps1:1156`

具体失败场景：release目录有合法普通隐藏文件，声明要覆盖全部产物的枚举未加Force。

实际验证/输出：Write-Sha256Sums未列该文件，Assert-Sha256Sums仍true；修改隐藏文件仍通过0，修改可见文件则拒绝1。后续独立传输检查可能另行拒绝，此处不宣称绕过服务器签名链。

影响链：发布产物目录/完整性校验

建议修法：生成与验证使用同一个完整普通文件集合，包括隐藏文件；继续拒绝reparse并只排除SHA256SUMS自身。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-ps / PS-06](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

### 78. PT-04 / P1 — 治理变更守卫未识别 monorepo 路径，且新增 Compose 环境扫描器未登记保护

文件行号：`platform/scripts/guard-governance-files.sh:22`；`platform/scripts/check-governance.sh:85`；`platform/tests/security/governance-not-hollow.test.sh:65`

具体失败场景：Git diff --name-only 返回 platform/PROJECT-CONSTITUTION.md 等路径；protected_globs 仍是 PROJECT-CONSTITUTION.md、scripts/ 等无前缀模式，因此真实monorepo保护文件修改被判未触碰。即便解决前缀，scripts/check-compose-env.py 本身也未列入protected_globs/governance_deps。

实际验证/输出：使用本地真实Git提交diff、仅fetch/merge-base入口替身，修改platform宪法却 exit0 输出未改动治理文件；另将diff安全替身设为 scripts/check-compose-env.py，仍exit0。

影响链：治理守卫/后续重新接线的 GitHub guard，以及基于受保护目录的代码评审提示。此报告不声称当前nested GitHub workflow已被GitHub调度。

建议修法：明确diff根路径并匹配platform前缀（根级workflow另列）；将check-compose-env.py纳入守卫依赖及受保护清单；测试应实际调用守卫并验证未标记真实路径变更exit1，不能只grep清单字符串。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / PT-04](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `D:\Git\bin\bash.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\run-guard.sh；exit=0`
- `D:\Git\bin\bash.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures3\guard-checker.sh；exit=0`

### 79. PT-05 / P1 — Compose 环境变量门禁只查文件全局存在，API 缺透传会被 worker 同名变量掩盖

文件行号：`platform/scripts/check-compose-env.py:28`；`platform/deploy/compose/launch.yaml:276`

具体失败场景：API源码读取XM_SMS_MODE，删除launch.yaml中platform-api的XM_SMS_MODE透传，但保留platform-worker同名配置。

实际验证/输出：实际launch.yaml复制件删除API那一条后，check-compose-env.py仍exit0；API进程无法收到变量、按off默认关闭功能。该变异同时用最小合成Compose和真实launch形状复现。

影响链：governance -> check-compose-env -> Compose部署环境透传；与脚本注释记载的SMS部署缺变量事故同类。

建议修法：按SOURCES的进程到Compose服务映射分别检验environment条目，解析YAML或受控结构解析；不能用整个文件的正则命中作为透传证据。加入API-only删除、worker-only删除、放错服务、注释/其他mapping同名等变异。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / PT-05](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Python314\python.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\compose-env\scripts\check-compose-env.py；exit=0`
- `C:\Python314\python.exe G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures2\compose-real-shape\scripts\check-compose-env.py；exit=0`

### 80. PT-06 / P1 — DB-role负向Compose夹具位于RepoRoot外，端口/镜像断言实际只测到路径拒绝

文件行号：`platform/scripts/test-database-roles.test.ps1:23`；`platform/scripts/test-database-roles.ps1:39`

具体失败场景：两个bad Compose文件创建在系统TEMP，但调用ValidateOnly时RepoRoot仍是平台根。Resolve-RepoFile在语义校验前因路径不在RepoRoot内抛错；Expect-Rejected只要求任意异常。

实际验证/输出：现有测试baseline exit0；复制实现中注释掉唯一Validate-ComposeStatic调用后，整套guard test仍exit0；同一坏端口文件放到RepoRoot内时baseline exit1，而该实现变异exit0，确认真实保护被删却无测试发现。

影响链：数据库角色测试harness的本机端口、镜像pin、安全Compose边界回归。

建议修法：每例建立RepoRoot内部或独立完整repo fixture；先证明坏文件仅改变目标属性且能通过路径预检，再断言对应错误码/诊断。对端口和pin分别禁用校验确认红。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / PT-06](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\dbr\scripts\test-database-roles.test.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\dbr\scripts\test-database-roles.ps1 -ValidateOnly -ComposeFile G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\dbr\tests\security\bad-port.yaml；exit=1`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\dbr\scripts\test-database-roles.ps1 -ValidateOnly -ComposeFile G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\dbr\tests\security\bad-port.yaml；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures2\dbr-baseline\scripts\test-database-roles.ps1 -ValidateOnly -ComposeFile G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures2\outside-bad-port.yaml；exit=1`

### 81. PT-07 / P1 — Runway脚手架守卫测试不检查原生子进程退出码，并接受零测试运行

文件行号：`platform/scripts/test-runway-threshold-db.test.ps1:9`；`platform/scripts/test-runway-threshold-db.ps1:27`

具体失败场景：test.ps1对pwsh子进程包try/catch，但PowerShell默认不会把native非零exit转换为可catch异常，也没有成功时显式报错；被测文件整体改成exit0仍打印passed。另一入口允许-Run指定无匹配测试，Go成功且不含SKIP也被放行。

实际验证/输出：真实guard测试baseline(两个子进程throw)exit0；被测文件全部守卫删除后仍exit0并passed。Go函数安全替身返回真实no-tests输出形状时harness exit0。

影响链：Runway disposable DSN预条件保护的回归信号；脚手架测试范围有效性。

建议修法：每个原生调用捕获并验证非零退出码以及对应错误文本；意外成功必须抛错。harness使用go test -json或明确PASS测试清单验证至少一个要求测试实际执行，并拒绝no-tests。保持当前脚手架定位，不补做未经授权的DB生命周期/ACL实现。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[platform-tests / PT-07](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures\runway\scripts\test-runway-threshold-db.test.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -File G:\xingmang\logs\full-audit-20260910\platform-tests\fixtures2\runway\no-tests-driver.ps1；exit=0`

### 82. RUNEARLY-04 / P1 — RC39 post-migration command block returns success after exact migration comparison fails

文件行号：`invoice/docs/PRODUCTION-RUNBOOK.md:746`；`invoice/docs/PRODUCTION-RUNBOOK.md:780`

具体失败场景：The database contains all required 0013/0014 records with correct checksums plus one unexpected migration row. In a normal Bash shell (no documented set -e), cmp at line 766 exits 1, but the following successful grep for 0014 becomes the block status 0. Similarly the privilege block can overwrite failed earlier privilege assertions with a successful final schema cmp.

实际验证/输出：matching control exit 0; original exact block with injected extra 9999_unapproved.sql row prints FIXTURE-CMP-MISMATCH and exits 0; same fixture with set -euo pipefail exits 1 at the same mismatch

影响链：Historical separately approved RC39 populated-index maintenance evidence gate can falsely pass when scripted/copied as one block

建议修法：Make each mandatory check explicitly abort on failure, or execute the whole sequence in a fail-closed subshell. Preserve the approved lifecycle order. Add a negative fixture with an extra migration and a privilege mismatch.

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / RUNEARLY-04](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python -X utf8 migration-proof-v2.py；exit=见原记录`
- `python -X utf8 migration-proof-v2.py`

### 83. TRIVY-02 / P1 — digest 相同快速路径不验证数据库内容或时效，自称成功而保留不可用缓存

文件行号：`invoice/scripts/refresh-trivy-cache.ps1:261`；`invoice/scripts/refresh-trivy-cache.ps1:263`；`invoice/scripts/refresh-trivy-cache-lib.ps1:478`；`invoice/scripts/refresh-trivy-cache-lib.ps1:491`

具体失败场景：volume 的 .source-digest 与当前上游相同，metadata.json 可解析，但 trivy.db 已缺失/损坏，或 metadata 已过期且 DownloadedAt=0001。读取状态只返回 sidecar 与 metadata；CLI 在 digest 相等后直接 continue，不进入任一实际自检。文件支持的 Docker mock 使用真实 Get-TrivyCacheVolumeComponentState + CLI，缺失 trivy.db 且 NextUpdate=2026-01-02，仍 exit0、Action=unchanged。

实际验证/输出：cli-unchanged-actual-state-reader exit0，打印 already up to date；unchanged-model-state.json 证明 DB 文件缺失，仅 metadata/sidecar 存在。

影响链：["缓存刷新 fast path -> 未修复/未报警 -> 下一次 Trivy/发布门禁仍缺 DB 或需重下"]

建议修法：对 digest 相同路径执行只读缓存完整性/时效校验与必要自检，或在文件缺失/元数据异常时走已存在的 staging 重建路径；不能仅凭 sidecar 宣告当前缓存可用。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / TRIVY-02](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\unchanged-actual-state-reader\invoice\scripts\refresh-trivy-cache.ps1 -SkipJavaDb -ProxyUrl  -TrivyCacheVolume fixture-only -WorkDirectory G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\unchanged-actual-state-reader\logs；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\selfcheck-baseline\predicates.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\selfcheck-mutant-ignore-corruption-warning\predicates.ps1；exit=1`

### 84. TRIVY-03 / P1 — 共享 Docker volume 的锁按 worktree 项目目录分散，跨工作树互斥失效

文件行号：`invoice/scripts/refresh-trivy-cache.ps1:4`；`invoice/scripts/refresh-trivy-cache.ps1:206`；`invoice/scripts/refresh-trivy-cache-lib.ps1:684`；`invoice/scripts/refresh-trivy-cache-lib.ps1:686`；`invoice/scripts/test-refresh-trivy-cache.ps1:239`

具体失败场景：同一 Docker daemon 上的两个 monorepo worktree 都用默认 invoice-release-gate-trivy-0-74-0 volume。锁却在各自 invoice/release 下。隔离两个工作树目录时，相同路径第二次锁如预期拒绝，但 A/B 两条不同锁路径能同时持有；不会阻止两个刷新或刷新与 release gate 并发操作同一个 volume。

实际验证/输出：samePathRejected=true，bothDifferentWorktreeLocksHeld=true；只验证了本地 FileShare.None 行为，未运行任何真实 Docker 并发。

影响链：["多工作树刷新/发布 -> 同名 volume -> 并发 seed/scan"]

建议修法：统一锁位置/命名为稳定的 daemon+volume 身份，所有刷新与 release gate 调用方同步使用同一解析函数；另加两个项目根、相同 volume 的交叉进程互斥测试。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / TRIVY-03](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\pure-proof.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\pure-tests-baseline\test-extracted-pure.ps1；exit=0`

### 85. TRIVY-05 / P1 — 锁文件打开的所有错误都伪装成正常争用跳过 exit75

文件行号：`invoice/scripts/refresh-trivy-cache-lib.ps1:692`；`invoice/scripts/refresh-trivy-cache-lib.ps1:694`；`invoice/scripts/refresh-trivy-cache.ps1:381`；`invoice/scripts/refresh-trivy-cache.ps1:393`

具体失败场景：锁路径意外是目录（亦可对应权限/IO类失败），没有任何其他进程持锁。Enter-TrivyReleaseGateLock catch 丢弃原异常并统一抛 already using shared lock；顶层按该文字标记 skipped 并 exit75。原 CLI 拷贝+外部 mock 对目录锁路径实测 75，latest.json 也写 gate holds the cache volume；配置/文件系统故障被误报为暂时争用。

实际验证/输出：invalid-lock-directory 实测 exit75，输出 SKIPPED: gate holds the cache volume; skipped，实际锁目标是目录。

影响链：["定时刷新 -> 错误分类 -> LastTaskResult/latest.json/人工诊断"]

建议修法：保留异常类型/原始错误，并仅把明确锁占用错误转换成专用争用结果；其他失败走现有 generic nonzero 分支。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-trivy-automation / TRIVY-05](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\pure-proof.ps1；exit=0`
- `C:\Program Files\PowerShell\7\pwsh.exe -NoProfile -NonInteractive -File G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\invalid-lock-directory\invoice\scripts\refresh-trivy-cache.ps1 -SkipJavaDb -ProxyUrl  -TrivyCacheVolume fixture-only -WorkDirectory G:\xingmang\logs\full-audit-20260910\invoice-trivy-automation\refresh-fixtures\invalid-lock-directory\logs；exit=75`

### 86. CONTRACT-DOC-01 / P2 — CR-0007/0008/0009 在同一文件保留互相冲突的当前状态

文件行号：`platform/docs/change-requests/CR-0007-invoice-admin-freeze-queue-operability.md:3`；`platform/docs/change-requests/CR-0007-invoice-admin-freeze-queue-operability.md:87`；`platform/docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md:3`；`platform/docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md:158`；`platform/docs/change-requests/CR-0009-invoice-admin-user-ledger-view.md:3`；`platform/docs/change-requests/CR-0009-invoice-admin-user-ledger-view.md:189`

具体失败场景：同一个变更单从页首读取会被判为 delivered/implemented-verified，而从状态节读取却是 proposed/implemented-pending-verification（CR-0007 方向相反）。评审或修复准备因此需重新追溯执行记录，当前未找到被自动发布脚本消费而直接误放行的证据。

实际验证/输出：[{"result": "entire CR documents read; contradictory line pairs pinned in locations", "metadata": "cross-contract-comparison.json"}]

影响链：变更状态/验收追溯文档；不直接改变运行时代码

建议修法：将各文件保留一个当前状态摘要，把旧的 proposed/pending 段标为原始立单快照并链接已有执行记录。不要补写新的生产批准或把未核实历史生产结论当本次实测。

状态：not-selected-p2；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / CONTRACT-DOC-01](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

本轮处理理由：历史变更单状态标签属于非运行时一致性；本轮不重写历史批准/完成记录。

### 87. HSC-P2-01 / P2 — RC110 顶部状态仍是发布前初始值，与后续完成记录不一致

文件行号：`invoice/docs/handoffs/RELEASE-RC110.md:3`

具体失败场景：审查者先读 handoff 顶部 status，会得到源码门禁待跑且未 tag/传输/部署的结论，但下文在同一历史候选内记录均已完成。

实际验证/输出：文档摘要过时；未证明会使发布脚本失败或直接放过门禁。

影响链：发布交接阅读与状态判断

建议修法：只更新顶部摘要为带 UTC 截止点的历史状态；保留初始过程、影子评估残余风险与部署补记，不写成当前生产实时确认。

状态：not-selected-p2；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / HSC-P2-01](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `git show aacab6ef98a63da627a05c555dc894824c7db5ba:invoice/docs/handoffs/RELEASE-RC110.md`
- `git log --format=%H %cI %s -8 ec43787785c8690e2f048801a4055e7138a91f6e -- docs/handoffs/RELEASE-RC110.md`

本轮处理理由：历史发布记录的初始摘要与追加记录；保留历史正文，未将它当当前生产状态证明。

### 88. IDEP-009 / P2 — 恢复成功文案仍写 source_states=4，实际检查十个状态目录

文件行号：`invoice/deploy/backup/restore-drill.sh:226`；`invoice/deploy/backup/restore-drill.sh:404`；`invoice/deploy/docker-compose.prod.yml:389`

具体失败场景：十个状态目录全部验证后，成功输出仍固定 source_states=4；Compose 邻接注释同样写 all four streams。

实际验证/输出：静态逐行确认；仅文案不影响实际十次检查。

影响链：审计/操作阅读产生计数歧义，当前正确性不受影响。

建议修法：成功摘要从已核查数组长度生成，注释同步十流；无需全套回归。

状态：not-selected-p2；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-deploy / IDEP-009](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)

本轮处理理由：仅成功文案/注释数量，不影响实际十个目录检查；按不顺手整理约束保留。

### 89. INV-DOC-05 / P2 — eligibility repair 退出码表把kind/组合拒绝过宽地列为2

文件行号：`invoice/docs/PRODUCTION-RUNBOOK.md:2282`；`invoice/backend/cmd/eligibility-repair/main.go:132`

具体失败场景：操作者按表把bad flag combination判为exit2；实际unknown kind、无--account的pending-reevaluate、错配--event等在run中返回error。

实际验证/输出：main统一把这些run错误映射为1；只有flag解析/位置参数/非绝对路径等为2。INV-DOC-01同一提取fixture验证unknown kind的前置错误分支，源码main映射明确为1。

影响链：运维人工错误分流；未发现现有调用脚本因此误写或假成功，按P2一致性。

建议修法：仅对齐CLI既有退出码说明，不改变退出行为；可复用本轮已执行的参数错误对照。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[invoice-docs / INV-DOC-05](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `python invoice-docs/kind-and-doc-proof.py（提取的无副作用前置判据/静态文档对照）`

### 90. PC-003 / P2 — 卡用途 Action 契约遗漏已实现的订阅金额和周期参数

文件行号：`platform/contracts/actions/cards.card.usage.set.v1.json:43`；`platform/internal/platform/cards/funds_actions.go:204`；`platform/internal/platform/cards/funds_actions.go:230`

具体失败场景：contracts/actions/cards.card.usage.set.v1.json params 在 service_name 后直接到 next_renewal_on；同 ID/version 的运行 Schema 还接受 subscription_amount:string 与 subscription_cycle:string，周期枚举 monthly/yearly/weekly/other，且 handler 读取二者。

实际验证/输出：{"command": "action_scan.go + compare_actions.py", "results": "action-comparison.json"}

影响链：契约清单和实现字段一致性；未发现仓库生成器直接消费这份 JSON。

建议修法：仅补文档契约中当前Go实现已有的两个可选字段和枚举，不改变运行Schema、数据库或业务。

状态：unfixed；提交：未产生。

原始命令、UTC、退出码与完整输出：[contracts / PC-003](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)

实际命令摘录（其余与完整参数/输出保留在来源机器记录）：

- `action_scan.go + compare_actions.py；exit=见原记录`

## 需要负责人拍板

- **OWNER-01 服务器Git真相源、远端白名单与GitHub镜像目标**：代价/边界：需确认历史衔接、bare repo/hooks安装与发布目标；可能改变实际推送/部署对象。 本轮安全处理：保留原白名单/目标，不操作服务器；只修代码路径和声明适用范围。
- **OWNER-02 两种平台生产入口的端口/项目统一**：代价/边界：涉及8088与18089、Compose project和nginx目标的线上选择。 本轮安全处理：只明确各自既有前置并拒绝配置/探针不一致，不改变默认端口或生产配置。
- **OWNER-03 永久Keycloak管理员维护工具的新发布身份准入**：代价/边界：需批准新signed tag/image/attestation和维护窗口；不能泛化接受任意rc标签。 本轮安全处理：保留RC38-only签名身份，只修路径和文档适用范围。
- **OWNER-04 CPA专属测试与lifecycle保证**：代价/边界：本轮硬约束不碰CPA；修改顺序、后置验证或专属测试需另行授权。 本轮安全处理：POP-12-CPA不修；不调整CPA步骤或新增实测。
- **OWNER-05 REQLOG真实读侧99%验收与既有权限迁移**：代价/边界：需授权真实样本/窗口和实际运维权限变更，不能用形状检查替代读侧成功率。 本轮安全处理：纠正过强文案，保留批准前置；不新建生产采样接口、不chmod真实文件。

## 覆盖与未验证范围

- 未运行生产、真实修复、服务器或上游操作；未读取真实env/密钥。
- 426个平台Go测试和部分invoice/backend测试完成逐路径静态分诊，只有明确列出的高风险用例进行了定向/逐函数和变异验证，不宣称所有分支穷尽验证。
- Git hooks子代理曾被运行时安全审核终止；未执行完成的artifact-controls动态检查没有记作通过，也未重试被拒绝动作。
- 镜像重建、线上登录/健康、真实数据库内容与生产配置未验证；最终只执行本轮要求的本地门禁。

各分区覆盖表与所有原始报告：

- [invoice-deploy](G:/xingmang/logs/full-audit-20260910/invoice-deploy/FINDINGS.md)
- [platform-ops](G:/xingmang/logs/full-audit-20260910/platform-ops/FINDINGS.md)
- [invoice-docs](G:/xingmang/logs/full-audit-20260910/invoice-docs/FINDINGS.md)
- [platform-docs](G:/xingmang/logs/full-audit-20260910/platform-docs/FINDINGS.md)
- [contracts](G:/xingmang/logs/full-audit-20260910/contracts/FINDINGS.md)
- [invoice-tests](G:/xingmang/logs/full-audit-20260910/invoice-tests/FINDINGS.md)
- [platform-tests](G:/xingmang/logs/full-audit-20260910/platform-tests/FINDINGS.md)
- [hygiene](G:/xingmang/logs/full-audit-20260910/hygiene/FINDINGS.md)
- [invoice-trivy-automation](G:/xingmang/logs/full-audit-20260910/invoice-trivy-automation/FINDINGS.md)
- [invoice-ps](G:/xingmang/logs/full-audit-20260910/invoice-ps/FINDINGS.md)

## 阶段二修复与门禁记录

尚未执行修复。本节与上表将随逐finding提交更新，每条记录失败测试、修复后绿色、逐断言变异红、还原绿色、精确UTC/退出码和提交哈希。
最终必跑：invoice/scripts/verify.ps1（经run-detached启动）；platform的go test -race -p 1、go vet、pnpm install --config.verify-deps-before-run=false、pnpm -r typecheck/test、governance（有效基线必须可解析）。
无生产roll-forward/备份/影子评估/重启/env修改/服务器目录操作，无GitHub推送、签名tag改动或旧盘操作。
