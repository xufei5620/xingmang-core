# XM-USERS-OFFSTATE1：消费明细 / API Key 元数据面板的「未接入」空状态

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-USERS-OFFSTATE1`（base `release/v0.1-launch` @ `5d1403a`）

## commit

`fc5bec2` — fix(admin-web): honest off-state for daily usage and key metadata panels

## summary

`XM-UX-OFFSTATE` 给 `listPlatformUsers`/`getPlatformUser` 接上了「未挂载路由」
判据（`looksLikeUnmountedRoute` → `FeatureNotMountedError` → `ApiStateView`
渲染 `PageState kind="unavailable"`），但同一个文件里另外两个函数——
`listPlatformUserDailyUsage`（用户详情「消费明细」子页签下的近 7 天趋势）、
`listPlatformUserKeys`（「API Key」子页签）——一直没有做同样的翻译，`client.get`
的 404 原样抛出。本片把同一套判据补齐到这两个函数。

### 判据核实：这两个端点确实存在「结构化 404」与「未挂载 404」两种不同的 404

复核 `internal/platform/httpapi/users_daily_usage.go`、`users_keys.go` 与
`internal/platform/platformusers/service.go` 的 `translateError`：

- 连接器返回 `platformusers.ErrNotFound`（具体这个用户没有日消费/Key 记录，
  `connectors/platformusers/daily_usage.go:76`、`key_metadata.go:186`）时，
  `translateError` 翻成 `action.CodeNotRegistered`，`StatusForCode` 映射成
  **404 且带 `error.code: ACTION_NOT_REGISTERED`**（`internal/platform/
  httpapi/response.go:41-42`）——这与 `getPlatformUser` 现有的「具体用户不存在」
  分支同构，**不能**被误判成未接入。
- 整组 `XM_PLATFORM_USERS_MODE=off`（`platformUserService` 为 nil）时，
  `router.go` 里 `d.PlatformUserDailyUsage`/`d.PlatformUserKeys` 与
  `d.PlatformUsers`/`d.PlatformUserDetails` 一样不挂载，chi 走默认
  `NotFoundHandler`，纯文本 404 解析不出 `error.code`——这才是
  `looksLikeUnmountedRoute` 应该命中的那一种。

所以修法与 `listPlatformUsers` 逐字同构：`try { client.get(...) } catch
(error) { if (looksLikeUnmountedRoute(error)) throw new
FeatureNotMountedError(error, USERS_NOT_MOUNTED_DESCRIPTION); throw error; }`，
description 复用既有常量（未新造第二句需要跟环境变量名保持同步的文案）。

`ApiStateView.tsx` 早已认识 `FeatureNotMountedError` 并渲染
`kind="unavailable"`，`DailyUsagePanel`/`KeyMetadataPanel` 也早已经过
`ApiStateView` 包壳——本片**没有改动渲染层任何一行**，纯粹是 API 客户端层
补齐同一个判据。

### 一处主动核实并如实记录：今天这条路径在唯一已知调用方处不可达

`grep` 确认 `listPlatformUserDailyUsage`/`listPlatformUserKeys` 只有
`DailyUsagePanel`/`KeyMetadataPanel` 两个调用方，而这两个面板只在
`PlatformUserDetailPage.tsx` 的 `LookupResult`（即 `getPlatformUser` 已经
返回 `kind: "found"`）内部渲染。核对 `cmd/platform-api/main.go`：
`PlatformUsers`/`PlatformUserDetails`/`PlatformUserDailyUsage`/
`PlatformUserKeys` 四个 Deps 字段都由**同一个** `platformUserService` 变量
经四个 `*OrNil` 辅助函数得出，因此今天它们只会同时为 nil 或同时非 nil——
`XM_PLATFORM_USERS_MODE=off` 时整个详情页已经在 `getPlatformUser` 这一层
显示「未接入」，`DailyUsagePanel`/`KeyMetadataPanel` 根本不会挂载、不会
发出它们自己的请求。这与 `XM-UX-OFFSTATE` 的 `follow_ups`
一节判断一致（「三者共用同一个 `*platformusers.Service`，做不到独立开关」），
写这份交接前特意复核过一遍，确认这个结构性事实至今没有变化。

**因此本片是防御性/一致性修复，不是修一个今天能在页面上点出来的可见 bug**：
价值在于（a）消除 `users.ts` 文件内两个函数与其余四个函数之间行为不一致
的隐患；（b）为 `XM-UX-OFFSTATE` 交接里明确预见的未来变化（某天
`platformusers.go` 真的把 detail/daily-usage/keys 拆成可以独立开关）先做好
准备，届时不需要再补一片；（c）任何反代/中间层未来注入的纯文本 404
（哪怕不是通过 `XM_PLATFORM_USERS_MODE=off` 触发）也会被正确分类。团队
交接里描述的「用户详情页面对这两个子页签，会因为整组端点未挂载而显示
加载失败」在当前代码路径下不会真的发生——已按 `[[flag-spec-deviations-inline]]`
的教训在实现当下记录这一发现，而不是事后才提。

## files_changed

实现 + 测试（2，均为在既有文件上修改）：

- `web/apps/admin-web/src/api/users.ts` —— `listPlatformUserDailyUsage`、
  `listPlatformUserKeys` 补上 `looksLikeUnmountedRoute` → 抛
  `FeatureNotMountedError` 的分支，复用既有的 `USERS_NOT_MOUNTED_DESCRIPTION`
- `web/apps/admin-web/src/api/users.test.ts` —— 每个函数各加两条用例
  （未挂载 404 → `FeatureNotMountedError`；结构化 404 → 原样 `ApiError`，
  不误判）

新建测试（2，此前这两个组件没有专门的组件测试文件）：

- `web/apps/admin-web/src/components/DailyUsagePanel.test.tsx` —— 正常
  渲染逐日序列 1 条 + 未接入态 1 条（未接入态断言：显示「未接入」与环境
  变量名、不给「重试」按钮、不显示 `UNKNOWN` 错误码、不渲染表格）
- `web/apps/admin-web/src/components/KeyMetadataPanel.test.tsx` —— 同上
  结构，正常渲染 Key 元数据行 1 条 + 未接入态 1 条

未改动 `ApiStateView.tsx`（团队交接允许「只在严格必要时」改它——核实后
不需要）、`DailyUsagePanel.tsx`、`KeyMetadataPanel.tsx` 本身（两者都已经过
`ApiStateView` 包壳，翻译在 API 层做完就对渲染层透明）。

## tests_run

在 `web/` 目录（worktree 用镜像脚本补齐 `node_modules`，命令带
`--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验，见既定坑记录）：

- `pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck` —— PASS（`tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false --filter admin-web run test` —— PASS，103 个测试文件、1472 个用例全绿（含本片新增/扩展的用例）

（本片其余门禁——`pnpm -r run typecheck`/`test`、`bash scripts/
check-governance.sh`、`gitleaks`、`gofmt`——与 XM-OPS-TAILS1 一起在两片
都完成后跑了一遍覆盖全部改动的最终门禁，结果见团队交接消息，不在这份
单片文档里重复。）

## not_run

- 后端 `go test ./...`：本片未改动任何 `.go` 文件，未跑；核实用的
  `translateError`/`StatusForCode`/`main.go` Deps 装配等结论均来自阅读
  既有代码，不需要跑测试验证（它们不是本片改动的一部分）。
- 真实浏览器 / Playwright 实测：未跑。判据与 `XM-UX-OFFSTATE` 相同——纯
  函数级别的结构判断（状态码 + JSON 解析是否成功），vitest + jsdom 的
  手写 404 stub 对本片改动的置信度足够，没有布局/CSS 相关改动。
- Storybook 构建：未跑。本片没有新增或修改任何 `ui-admin`/`ui-primitives`
  组件。
- 生产环境验证：未跑，且如「一处主动核实」一节所述，今天的生产环境里
  这条路径在唯一已知调用方处不可达，没有可以拿来验证的真实触发场景。

## risks

- **今天不可达，价值是防御性的**：如上文「一处主动核实」一节，这次修改
  在当前代码路径下无法通过正常 UI 操作触发（`getPlatformUser` 会先一步
  显示未接入，两个子面板根本不会挂载）。如果验收线认为这类「面向未来、
  今天验证不了」的改动不值得现在做，可以把这次的两个测试文件与
  `users.ts` 的改动一起回退，不影响 `XM-USERS-OFFSTATE1` 之外的任何东西。
- **`USERS_NOT_MOUNTED_DESCRIPTION` 现在被四个函数共用**：与
  `XM-UX-OFFSTATE` 交接里记录的风险一致（环境变量名硬编码在文案里，
  改名不会被类型系统捕获）；共用同一个常量意味着改一处即可同步四处，
  比每个函数各写一份更不容易漏改，但也意味着如果未来某个函数需要一句
  更精确的文案（比如日消费与整组用户管理确实分属不同的开关），需要先把
  这个常量拆开。

## follow_ups

- 如果将来 `platformusers.go` 真的让 detail/daily-usage/keys 三者的路由
  能独立于彼此挂载（今天做不到，见上文核实），这次的修复就会从「防御性」
  变成「今天就能点出来的真实修复」，届时不需要再补代码，只需要验收线
  确认真实触发路径存在并按需要补一条真实浏览器走查。
- 未在本片验证 `DailyUsagePanel`/`KeyMetadataPanel` 在 501
  `ADVANCED_CONTROLS_REQUIRED`（`real` 模式下 v2 未实装）时的呈现——
  这条路径本来就不是「未接入」判据要处理的范围（501 不是「路由未挂载」，
  是「路由挂载了但这个能力这次请求不支持」，`ApiStateView` 现有的通用
  错误态已经能显示出 `翻页`/`每日消费趋势尚未接通真实数据源` 这类具体
  文案），如果验收线希望这条路径也显示成更「温和」的未接入态而不是通用
  错误态，需要单独立项讨论（那会改变 `looksLikeUnmountedRoute` 判据本身
  的语义边界，不是这次「补齐既有判据」的范围）。
