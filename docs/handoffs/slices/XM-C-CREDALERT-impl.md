# XM-C-CREDALERT-impl · 平台连接与凭据 / 告警页签

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-CREDALERT-impl`
- implementation commit: `c222390`
- base: `e98080d` (`release/v0.1-launch`, current D0-b merge)
- worktree: `K:/星芒统一控制平台/wt-xmC-CREDALERT-impl`

## summary

本片从 LOCAL 的 `8b94a6c` 拆出最小完整依赖，补齐 Sub2API / NewAPI 平台详情的
「连接与凭据」和「告警」两格。路由仍由已批准的 `PLATFORM_NAV_ITEMS` 驱动，
因此 `?tab=creds`、`?tab=alerts` 可深链、刷新和旧 `?tab=connection` redirect
行为不变。

- 连接与凭据页读取 `GET /api/v1/services`，按 `service_type` 精确筛选实例，
  展示实例、环境、状态、接入地址、负责人和最近观测/新鲜度；加载、空态、错误
  和表内筛选均复用既有 `ApiStateView` / `DataTableV2`。
- CredentialRef、连接登记簿和独立探测历史没有对应 Query 时保持完整目标布局，
  明确标为「未接入」，不从地址、财务字段或其它自由文本猜凭据状态。
- 告警页读取 `GET /api/v1/alerts` 的 `status=all&limit=200`，只按稳定的
  `source_metric_key` 平台前缀归属（`sub2api.` / `newapi.`）；展示严重度、状态、
  规则/详情、来源指标、首次/最近/恢复时间、去重命中次数和通知投递结果。
- 平台告警视图是只读聚焦阅读；确认、静默和批量写操作继续集中在全局「告警与故障」
  页面，经 Action、权限和审计链执行。
- 未引入后端写路径、迁移、scope 或凭据材料；复用已存在的 services/alerts 只读
  契约和 `serviceFreshness` / `describe*` 展示口径。

## files_changed

- `web/apps/admin-web/src/components/PlatformCredentialsPanel.tsx`
- `web/apps/admin-web/src/components/PlatformCredentialsPanel.test.tsx`
- `web/apps/admin-web/src/components/PlatformAlertsPanel.tsx`
- `web/apps/admin-web/src/components/PlatformAlertsPanel.test.tsx`
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`
- `web/apps/admin-web/src/router.test.tsx`
- `docs/handoffs/slices/XM-C-CREDALERT-impl.md`

## 格 → 数据源 / 证据映射

| 页面格 / 交互 | 来源或规则 | 当前状态 |
| --- | --- | --- |
| 已登记实例、环境、状态、地址、负责人 | `GET /api/v1/services?environment=<caller-env>`；`listServices` | 已接只读；按 `service_type` 精确匹配 |
| 最近观测 / 新鲜度 | `ServiceItem.observed_at` + `stale_seconds`；`serviceFreshness`（阈值 1800 秒） | 已接；未初始化、陈旧、新鲜分别显示，不把未知当 0 |
| CredentialRef 状态 | 连接登记簿 Query（当前未提供） | 未接入；显示边界说明，不显示样例或正文 |
| 健康探测历史 | 独立探测历史 Query（当前未提供） | 未接入；服务最近观测不冒充凭据探测 |
| 平台告警列表 | `GET /api/v1/alerts?environment=<caller-env>&status=all&limit=200`；`listAlerts` | 已接只读；含已解决记录，最多 200 条 |
| 告警平台归属 | `source_metric_key.startsWith(<platform> + ".")` | 已接；不读取标题/详情/dedup key 猜平台 |
| 告警过滤 / 搜索 | `DataTableV2` 严重度、状态筛选 + 短搜索；筛选在搜索之前 | 已接客户端交互 |
| 告警处置 | 全局 `/alerts` 的 `alerts.alert.acknowledge@1` / `alerts.silence.create@1` | 本页明确只读；不绕过 Action |

## tests_run

- `pnpm -r run typecheck` — PASS（5 个 workspace 项目；WSL 临时 archive，Node 22.22.0 / pnpm 11.24.0；仅有仓库要求 Node >=24 的 engine warning）
- `pnpm -r run test` — PASS（design-tokens 10、ui-primitives 16、ui-admin 219、admin-web 890；合计 5 个测试项目）
- `pnpm --filter ui-storybook run build` — PASS（WSL 临时 archive；保留既有单个大 chunk warning）
- `pnpm --filter admin-web run build` — PASS（WSL 临时 archive；保留既有单个大 chunk warning）
- `go fmt ./...` — PASS；本片无 Go 源码改动
- `go vet ./...` — PASS
- `go test -p 1 -count=1 ./...` — PASS
- `$env:GOVERNANCE_BASE_REF='origin/main'; $env:GOVERNANCE_REQUIRE_BASE='0'; D:/Git/bin/bash.exe scripts/check-governance.sh` — PASS
- `gitleaks.exe git --redact --no-banner --log-opts=e98080d..HEAD` — PASS（1 commit scanned, no leaks found）
- `git diff --check e98080d..HEAD` — PASS
- 平台目标测试（包含路由回归）：`PlatformCredentialsPanel.test.tsx`、`PlatformAlertsPanel.test.tsx`、`router.test.tsx` — PASS（3 files / 148 tests）

## tests_not_run

- 未访问真实 Sub2API / NewAPI、真实告警渠道或生产 services/alerts 数据。
- 未配置、读取、轮换或展示任何真实 CredentialRef、Token、账号密码。
- 未执行 Docker、服务器、Keycloak、第三方上游写操作或部署。
- 未新增/运行凭据登记簿与独立探测历史 Query；两块 UI 目前按契约缺口显示「未接入」。

## risks

- 平台告警归属依赖后端稳定的 `source_metric_key` 命名空间；无稳定指标来源的告警
  仍只会出现在全局告警中心，不应被误读为平台零告警。
- 连接页的 1800 秒服务新鲜度阈值沿用既有服务口径；独立凭据验证状态和轮换周期
  尚无读端点，页面不会推断它们。
- `status=all&limit=200` 是平台页的 bounded read；超过上限的历史记录需到全局
  告警中心按其分页/筛选能力查询。
- WSL 门禁使用 Node 22（仓库 engine 要求 Node >=24），当前实现与测试全绿但应在
  验收线的 Node 24 环境再复跑一次；Windows 本地 pnpm 曾受共享 esbuild 文件锁影响。

## follow_ups

- 验收线在当前 `release/v0.1-launch` 上审读本 Handoff、复跑同一组门禁后合入。
- 连接登记簿 Query 就绪后，仅接入 CredentialRef、用途、轮换状态、最近验证等
  非敏感引用字段；继续禁止凭据正文回前端。
- 独立探测历史 Query 就绪后，再替换「未接入」卡片，并保留服务观测与凭据验证的
  两套新鲜度语义。
- 若未来需要平台页确认/静默，另立明确的权限与 Action 交互设计；当前只读边界
  不应被快捷按钮悄悄扩大。
