# Codex UI 对齐接手简报(2026-08-28)

> 给 UI 原型的作者(Codex):你的原型已被采纳为平台 UI 的**唯一视觉/IA/交互权威**。
> 骨架(导航/页签/组件库)已按原型落地,现在请你把**每页内部功能分布**逐格补齐。
> 本简报是你在 xingmang-platform 仓库工作的全部上下文入口。

> **交付通道更新（2026-08-29）**：服务器裸仓库为默认 `origin`，GitHub remote
> 命名为 `github` 仅作镜像。过渡期不创建 PR；每片使用独立分支并提交
> `docs/handoffs/slices/XM-….md`，验收线本地审读后合入。详见
> `docs/runbooks/GIT-WORKFLOW.md`。

## 0. 你的原型在哪、平台长什么样

- 原型快照(只读):`C:\Users\58439\.codex\visualizations\2026\08\27\01a041e2-0397-71c2-9333-ba805861b187\backups\productivity-layer-final\`
  ——**最终态 = serve_xingmang_v4.py 的 build_page() 渲染结果**(IA v4.3),不是 v3.3 基线 HTML。
- 平台实机:http://127.0.0.1:8088(compose 项目 xingmang-launch;改前端后
  `docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file deploy/compose/.env build web && ... up -d web` 重建)。
- 已落地对照:docs/architecture/ADMIN-IA.md v3(导航/页签逐字)+
  docs/superpowers/plans/2026-08-28-xm-0041-prototype-alignment.md(差距清单)。

## 1. 铁律(仓库宪法摘要,CI 会拦)

1. **读走 Query、写走 Action**:页面只调 /api/v1/*;写操作一律
   POST /api/v1/actions/{id}/versions/{v}/execute,禁止绕过;
2. **金额禁 float**:后端 int64 scale-6 微单位,前端必须用
   `web/apps/admin-web/src/lib/money.ts` 的 `formatScaledMinorUnits`;
3. **凭据只显示引用**(secret://…),明文一个字不进前端;
4. **无数据源的格 = 按原型摆出布局,但显示「未接入」+一句话说明归哪条线**。
   **禁止把原型样例数字硬编进平台页面**——原型里的 96,402/98.2% 是设计稿样例,
   平台上没有数据源就标未接入,这是验收红线(交接文档 §9.1 新鲜度铁律);
5. 组件禁硬编码颜色/圆角/阴影,只用 design-tokens 语义 token;
6. 先查现有组件再新建:DataTableV2/PageState/StatTile/MetricCard/FreshnessBadge/
   PageHeader/ContextStrip 都在 `web/packages/ui-admin`(Storybook 有全部八态)。

## 2. 你能用的数据端点(已实装,形状看 internal/platform/httpapi/ 对应 handler+test)

| 端点 | 给哪些格供数 |
|---|---|
| GET /api/v1/metrics(scope ops.read) | 概览指标卡(sub2api.cost.daily/revenue.daily/users.* 等) |
| GET /api/v1/metrics/history | sparkline/近7日曲线 |
| GET /api/v1/alerts | 「需要处理的事」列表 |
| GET /api/v1/finance/channels/summary | 渠道管理行(供给成本/计费消耗/毛利/runway/coverage) |
| GET /api/v1/finance/upstreams/summary | 上游管理汇总列 |
| GET /api/v1/finance/upstream-accounts | 登记簿(接入方式/倍率/凭据Ref/platform_id/group_rate) |
| GET /api/v1/finance/subscription-batches / proxy-assets | 订阅批次/代理资产展开区 |
| GET /api/v1/finance/profit-daily | 利润台账(from/to 区间) |
| GET /api/v1/platforms/{p}/users | 用户管理表(fake,契约 v1) |
| GET /api/v1/platforms/{p}/requests(+/{id}) | 请求详情列表/正文(reqlog fake) |
| GET /api/v1/audit/events | 最近活动/审计 |

开发期身份:请求头 X-Dev-Principal-ID/Type + X-Dev-Scopes(参照
web/apps/admin-web/src/api/client.ts,前端已封装,直接用现有 api/ 模块)。

## 3. 分工(避免撞车,只做「你的」)

**Claude 线正在做(别碰)**:XM-0051 概览页、XM-0052 渠道管理页、XM-0053 用户管理页。

**你的(按序,一片一分支 Handoff)**:
1. **XM-C001 支付与财务·资金概览**:八卡(区间成功到账/待处理/失败/退款冲正/
   手续费/净现金流入/使用收入/渠道毛利)+资金对账面板+经营利润桥+最近事件表。
   支付侧七格无数据源(支付系统 M3)→布局照原型+未接入;使用收入/渠道毛利两格
   接 finance/channels/summary 合计;
2. **XM-C002 请求详情页对齐**:顶部统计区间+四卡(请求数/成功/失败/平均耗时)、
   表格补「渠道/上游」「输入/输出」「计费」列(reqlog fake 契约在
   contracts/connectors/reqlog.read.v1.md,fake 可扩字段,real 骨架标 DRAFT 待核对);
3. **XM-C003 上游管理表格列对齐**:上游名称/联系人/接入分组三列(登记簿今天没有
   这三个字段——**需要迁移**:仿 db/migrations/000012 加可空列+Action 参数扩展,
   迁移号取当前最大+1,先 `ls db/migrations/` 确认)+KEY/账号计数+总余额·有效期列
   (从 upstreams/summary 取);
4. **XM-C004 NewAPI 各页镜像**:按 NewAPI 的原型页(V["newapi/*"])同方法对齐。

## 4. 工作方式(与 Claude 线相同的门禁)

- 每片自建 worktree:`git worktree add K:/星芒统一控制平台/wt-xmC00N -b ai/codex/XM-C00N-<slug> origin/release/v0.1-launch`(先 fetch);
- 门禁:`pnpm -r run typecheck`、`pnpm -r run test`、`pnpm --filter ui-storybook run build`、
  改了 Go 就 `go fmt`(别用裸 gofmt,本机 1.25 与 CI 1.27 分歧)+`go vet`+`go test -p 1 ./...`、
  `bash scripts/check-governance.sh`,全绿再提;
- worktree 里 pnpm install 会挂(Windows rename 锁),解法看
  `C:\Users\58439\.claude\projects\K----------\memory\windows-toolchain-quirks.md`
  (镜像 node_modules 脚本/跳依赖校验开关);
- 分支 base `release/v0.1-launch`,**不要自己合并或部署生产**,分支内附 Handoff
  (status/branch/commit/summary/files_changed/tests_run/not_run/risks/follow_ups);
  服务器门禁全绿后由验收线审读、合入并部署；GitHub 镜像由
  `deploy/scripts/mirror-github.sh` 显式执行一次;
- gitleaks 会把指标键字面量误判成密钥:禁加 allowlist,抽常量;
- 疑义以「原型渲染态字面 > 交接文档 > 本简报」为序,真裁不了写进 Handoff 的 risks。
