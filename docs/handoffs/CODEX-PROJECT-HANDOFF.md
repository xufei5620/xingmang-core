# 星芒统一控制平台 · 项目全量交接文档(致 Codex)

> 2026-08-28。自此后续开发由 Codex 负责;Claude 线仅保留验收合入与部署。
> 工作方法遵循 Anthropic《The AI-Native SDLC Playbook》
> (https://claude.com/blog/the-ai-native-sdlc-playbook):制品驱动、自验证先于人审、
> 人类只守审批点。配套常驻提示词:`docs/handoffs/CODEX-PROMPT.md`。

## 一、项目是什么

自研自托管**运营控制平面**,统一管理 Sub2API、NewAPI、CPA、开票、支付等独立系统。
模块化单体:Go 后端 + React 管理端 + PostgreSQL + River 队列。当前形态:
**v1.0 只读运营台**已全量落地(21 个 PR #80~#100 合入 `release/v0.1-launch`),
本机 staging 栈 http://127.0.0.1:8088(compose 项目 `xingmang-launch`)。

## 二、权威文件链(冲突时从上往下裁)

1. `PROJECT-CONSTITUTION.md` — 28 条宪法(读写分离/金额整数/凭据引用等);
2. `docs/adr/` + `docs/architecture/BASELINE-v2.1.md` — 架构决策;
3. **UI 原型渲染态** — 视觉/IA/交互唯一权威:快照
   `C:\Users\58439\.codex\visualizations\2026\08\27\01a041e2-0397-71c2-9333-ba805861b187\backups\productivity-layer-final\`,
   最终态=serve_xingmang_v4.py 的 build_page() 输出(V[...] 取**最后一次**赋值);
4. `docs/architecture/ADMIN-IA.md` v3 — 导航/页签逐字定稿+六条已裁定;
5. `contracts/` — API/Action/Connector 契约;`VERSIONS.lock` 版本锁;
6. 各任务设计稿与 Handoff:`docs/superpowers/plans/`(成本核算设计稿含 §12 拍板
   记录、原型对齐差距清单、最终冲刺报告 2026-08-28-final-sprint-report.md)。

## 三、已完成(别重做)

- **成本核算线 a~e 全量**:登记簿(finance.upstream_account/token_map,含
  group_rate/platform_id)→ 利润台账 profit_daily(scale-6 微美元,三条不静默
  纪律)→ 订阅摊销(批次/代理资产/损失科目)→ 看板 runway+balance_history →
  影子对比工具(cmd/platform-shadow);采集任务 finance_cost_sync 每 5min;
- **UI**:靛蓝主题+四分组导航+四平台页签(IA v3)+运营工作台+Ctrl K 搜索+
  DataTableV2/PageState+概览/用户管理/渠道管理三页已逐格对齐原型;
  上游管理登记簿 UI(四写 Action);服务器/治理段/扩展蓝图态;
- **数据通道**:Sub2API 真实客户端(XM-0017)、NewAPI 真实客户端(第五道闸:
  伪 GET 写端点黑名单)、NewAPI 收入 DSN 通道、reqlog 请求详情只读网关(fake);
- **加固**:审计链规范转义修复(canonical_version v2)、API 限流、保留策略
  (审计永不删)、R1~R5 告警规则。

## 四、任务路线图(你的 intent 队列,按序)

**A. UI 逐页对齐收尾**(简报 `CODEX-UI-ALIGNMENT-BRIEF.md` 已列细节):
XM-C001 支付与财务·资金概览 → C002 请求详情页 → C003 上游管理列扩展(登记簿
加名称/联系人/分组三列,需迁移)→ C004 NewAPI 各页镜像。

**B. UI 残余增强**:用户详情页(0053 留了灰箭头)→ 订阅批次/代理资产登记表单
(现在只能走 Action API,scope `finance.subscription.manage` 未进开发态默认清单)
→ SavedView 持久化 → PeriodControls 提组件库。

**C. 各 Handoff 的 follow-up 汇总**(做完 A/B 后逐个消化):
平台渠道↔上游账号映射(渠道表回到原型行粒度的前提)| 用户 read contract v2
(逐用户流水)| runway 阈值设置面 UI | 审计归档方案 | DB 角色拆分(REVOKE 空
承诺,#97 有证据脚本)| 限流跨副本一致 | 指标样本降采样。

**D. 用户输入门控的(条件满足才做)**:Sub2API 真实凭据切换
(docs/runbooks/SWITCH-SUB2API-REAL.md)→ 影子对比 14 天;reqlog 真实客户端
(等 /root/reqlog 源码核对契约 DRAFT);sub2api 余额解析(等 /admin/accounts
真实响应样本);newapi 余额(等上游开 CHANNEL_UPDATE_FREQUENCY)。

**新增(2026-08-28 追加)**:
- **开票线 CR-0003**(`docs/change-requests/CR-0003-invoice-platform-scoped-users.md`):
  开票系统用户按平台隔离(K:/发票 仓库,已批准的产品需求,可与 A 期并行做);
- **F. 第二轮需求四期闸门**(处置表 `docs/requirements/ROUND2-INTAKE-2026-08-28.md`):
  P0 闸门期(R2-10/13/14/15,任何自动写/插件/高频采集的前置)→ P1-A 可信运营
  (R2-05/06/08/16/17)→ P1-B 经营集成(R2-01/02/03/07/11/12)→ P2 体验
  (R2-04 依赖 F-B/R2-09)。排在 E 期各里程碑启动前逐条消化,10 条否决信号
  永久有效不得复活。

**E. 里程碑级(每个先出设计稿走审批)**:Foundation-B 写操作体系(审批中心
XM-0030 设计稿在 docs/superpowers/plans/,**待产品负责人拍板后才可实施**)|
M1.5 渠道保障(NewAPI 逐渠道健康解析器已保留在渠道代码注释处)| M2 服务器
Agent | M3 支付 | M4 CPA | 开票二期(等 CR-0002 与 Codex 开票线冻结口径)。

## 五、审批点(人类保留,任何时候不越)

不合并 PR(CI 绿后由 Claude 验收线/用户合并);不直推 `main`;不碰生产系统与
Keycloak;不改上游三方源码(K:/sub2api-src、K:/newapi-src、K:/soloai-src 只读);
真实凭据由用户自配(.env,gitignored),代码只写 `secret://` 引用;涉及迁移/
新 scope/契约变更的切片**先提 plan 获批再实施**。

## 六、工程环境速查(实测坑,全部踩过)

- Go:`GOPROXY=https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct`;
  **格式化只用 `go fmt`**(裸 gofmt 是本机 1.25,与 CI 1.27 规则分歧);
  全量测试 `go test -p 1 ./...`(本机 TUN 会让 httptest 用例并行时成片假失败,
  单包重跑必过,以 CI 为准);
- 前端:worktree 里 `pnpm install` 必挂(Windows rename 锁),用镜像 node_modules
  方案+`pnpm --config.verify-deps-before-run=false -r run …`(详见
  `C:\Users\58439\.claude\projects\K----------\memory\windows-toolchain-quirks.md`);
- 网络:代理 127.0.0.1:10808 抖动时,`env -u HTTP_PROXY -u HTTPS_PROXY -u
  http_proxy -u https_proxy -u NO_PROXY -u no_proxy <cmd>` 绕过重试(八个变量);
- gitleaks 会把指标键字面量误判成密钥:**禁加 allowlist**,抽常量+squash 重写;
- Docker Desktop 在 `D:\Docker\Docker Desktop.exe`;栈重建:
  `docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file
  deploy/compose/.env build <svc> && … up -d <svc>`;迁移随 up 自动跑;
- 集成测试连真库配方与更多坑:上面那份 quirks 文件,开工前通读一遍。

## 七、验收标准(每片 PR 必须满足)

CI 四项全绿(governance/secret-scan/backend/frontend);Handoff 完整
(status/branch/commit/summary/files_changed/tests_run/not_run/risks/follow_ups);
UI 片附「格→数据源→状态」映射表;**原型样例数字零硬编**,无数据源的格=原型
布局+「未接入」+归属说明;金额一律 `formatScaledMinorUnits`;合计不全必标
覆盖率;凭据永不明文。
