# XM-UX-OFFSTATE：用户管理/请求详情在端点未挂载时的「未接入」空状态

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-UX-OFFSTATE`（base `release/v0.1-launch` @ `c00c853`）

## commit

`19e7486` — fix(web): 用户管理/请求详情在端点未挂载时显示未接入而非加载失败

## summary

生产环境 `XM_PLATFORM_USERS_MODE=off`、`XM_REQLOG_MODE=off`（真实客户端尚未
交付），后端 `internal/platform/httpapi/router.go` 因此整组不挂载
`/api/v1/platforms/{platform}/users*` 与 `/api/v1/platforms/{platform}/requests*`
（见 `cmd/platform-api/platformusers.go` `buildPlatformUsers`、
`cmd/platform-api/reqlog.go` `newRequestLogService`：off 时返回 `nil`，
`router.go` 用 `if d.PlatformUsers != nil` / `if d.RequestLogs != nil` 门控）。
chi 对没挂载的路由回默认的纯文本 404，前端 `api/client.ts` 的 `toApiError`
解析不出 JSON，`code` 落回 `UNKNOWN`，于是「用户管理」「请求详情」两个页签
（以及各自的完整详情页）显示成红色的「加载失败 请求失败（HTTP 404）
（错误码 UNKNOWN）」，运营会误判成故障，而事实是这条链路本来就还没接。

### 判据：怎么把「没挂载」从「这个具体资源不存在」里分出来

后端已挂载路由但资源本身不存在时（比如某个用户 id 没有记录），走的是
`internal/platform/httpapi/response.go` 的 `WriteError`，一定会写出带
`error.code`（比如 `ACTION_NOT_REGISTERED`）的 JSON 错误包
（见 `users_detail.go` `GetPlatformUserHandler`）。这与 chi 默认
`NotFoundHandler` 的纯文本 404 是**结构性**差异，不是文案差异，因此可以
用「404 且解析不出 `error.code`」作为可靠判据，而不是猜测响应文案或维护
一份端点白名单。

新增：

- `web/apps/admin-web/src/api/client.ts`
  - `looksLikeUnmountedRoute(error): error is ApiError` —— 404 且
    `code === "UNKNOWN"`（即响应体不是平台标准错误包）时为真。**只是一个
    结构信号**，本身不代表「未接入」——具体解释权留给调用方。
  - `FeatureNotMountedError extends ApiError` —— 携带一句给人看的
    `description`（含哪个环境变量、接入后会怎样），由调用方在明知某个
    端点组按环境变量可选挂载时主动抛出。

调用方（只在这两组端点上应用，没有改动 `client.ts` 对其它端点的行为）：

- `web/apps/admin-web/src/api/users.ts`：`listPlatformUsers`、
  `getPlatformUser` 捕获到 `looksLikeUnmountedRoute` 时抛
  `FeatureNotMountedError`；`getPlatformUser` 原有的「404 → `{kind:
  "notFound"}`」分支保留，只在**没有** `error.code` 时才改道，具体用户
  不存在（有 code）仍然是 `notFound`，与既有测试 `users.test.ts:113` 的
  行为逐字一致。
- `web/apps/admin-web/src/api/requests.ts`：`listPlatformRequests`、
  `getPlatformRequestContent` 同样只在无 `error.code` 时改道；具体请求
  id 过了保留期那类带 code 的 404（`RequestDetailPage.test.tsx` 既有用例）
  不受影响，继续显示成「加载失败」。

渲染（集中在唯一一处适配层，四个页面/面板共用）：

- `web/apps/admin-web/src/components/ApiStateView.tsx`：识别到
  `FeatureNotMountedError` 时渲染 `PageState kind="unavailable"`（默认
  标题就是「未接入」，不必显式传）、`description` 取自错误对象、**不给
  重试按钮**（off 不会因为再点一次变成 on）、不带错误码/request_id
  （没有障要报）。`PlatformUsersPanel`、`RequestsPanel`、
  `PlatformUserDetailPage`、`RequestDetailPage` 全部经过这一层，因此
  四处一次性生效，不必逐页面改错误渲染逻辑。
- `web/apps/admin-web/src/components/PlatformUsersPanel.tsx`：额外隐藏了
  页面顶部那条契约提示 warnbar（原文明说「下面的逐用户流水来自样本数据
  源」）——未接入场景下面渲染的是空状态，不是样本表格，继续显示这句话
  等于把「没接」说成「接了但是假的」。

### 未处理的相邻面板

`PlatformUserDetailPage` 里 `DailyUsagePanel`/`KeyMetadataPanel`（子页签
「消费明细」下的按日趋势、「API Key」子页签）分别调用
`listPlatformUserDailyUsage`/`listPlatformUserKeys`，本片**没有**改动
它们的 404 处理。原因：`XM_PLATFORM_USERS_MODE=off` 时顶层
`getPlatformUser` 会先 404（同一个 `off` 门控），`ApiStateView` 在那一层
就已经显示「未接入」，`FoundUserDetail` 及其子面板根本不会渲染——这两个
面板在 off 场景下是不可达的，因此不在本次验收范围内需要处理。若未来
`platformusers.go` 允许 detail/daily-usage/keys 独立于 list 单独关闭
（当前实现里三者共用同一个 `*platformusers.Service`，做不到），需要单独
补一片。

## files_changed

实现（4）：

- `web/apps/admin-web/src/api/client.ts` —— `FeatureNotMountedError`、
  `looksLikeUnmountedRoute`
- `web/apps/admin-web/src/api/users.ts` —— `listPlatformUsers`、
  `getPlatformUser` 改道
- `web/apps/admin-web/src/api/requests.ts` —— `listPlatformRequests`、
  `getPlatformRequestContent` 改道
- `web/apps/admin-web/src/components/ApiStateView.tsx` —— 识别并渲染
  `FeatureNotMountedError`
- `web/apps/admin-web/src/components/PlatformUsersPanel.tsx` —— 未接入
  时隐藏契约提示 warnbar

测试（8，均为在现有文件上新增用例，未新建测试文件）：

- `web/apps/admin-web/src/api/client.test.ts`
- `web/apps/admin-web/src/api/users.test.ts`
- `web/apps/admin-web/src/api/requests.test.ts`
- `web/apps/admin-web/src/components/ApiStateView.test.tsx`
- `web/apps/admin-web/src/components/PlatformUsersPanel.test.tsx`
- `web/apps/admin-web/src/components/RequestsPanel.test.tsx`
- `web/apps/admin-web/src/pages/PlatformUserDetailPage.test.tsx`
- `web/apps/admin-web/src/pages/RequestDetailPage.test.tsx`

未改动 `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`、
`web/apps/admin-web/src/pages/RequestDetailPage.tsx`、
`web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx`、
`web/apps/admin-web/src/components/RequestsPanel.tsx` 本身——它们已经
统一经过 `ApiStateView`，改这一层就够了，不需要逐页改错误分支。

## tests_run

在 `web/` 目录串行执行（Windows 下 `pnpm install` 会因符号链接 rename
锁而挂起，本 worktree 用镜像脚本直接补齐了 `node_modules`，因此每条命令
都带 `--config.verify-deps-before-run=false` 跳过 pnpm 的依赖校验）：

- `pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck` —— PASS（`tsc --noEmit` 无输出）
- `pnpm --config.verify-deps-before-run=false --filter admin-web run test` —— PASS，79 个测试文件、1134 个用例全绿（含本片新增的 14 个用例）
- `pnpm --config.verify-deps-before-run=false --filter admin-web run build` —— PASS（`tsc --noEmit && vite build`，产物体积与改动前一致，907 KB 的 chunk-size 警告是既有的，非本片引入）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）
- `gitleaks protect --staged -v` —— PASS（`no leaks found`，本片新增字符串只有环境变量名与说明文案，熵值不会撞 XM-0037d/XM-0049 踩过的 `*_key:` 误报）

## not_run

- 后端 `go test ./...`：本片未改动任何 `.go` 文件（只是消费既有的
  `WriteError`/chi 未挂载 404 这两个既有行为），未跑；建议验收线按常规
  仍复跑一次作为基线确认。
- 真实浏览器/Playwright 实测：未跑。判据是纯函数级别的结构判断
  （状态码 + JSON 解析是否成功），vitest + jsdom 已经用手写的
  `json: () => Promise.reject(new SyntaxError(...))` 精确模拟了 chi 的
  纯文本 404 场景，对本片改动的置信度足够；没有布局/CSS 相关改动，不
  属于「必须真浏览器才看得出」的那类 bug（参考
  `windows-toolchain-quirks` 记忆里 XM-0045 的判断标准）。
- Storybook 构建：未跑。本片没有新增或修改任何 ui-admin/ui-primitives
  组件，`PageState` 的 `kind="unavailable"` 分支是已有能力（原有
  `PlatformOverviewPanel`/`PlatformUserDetailPage` 已在用），无需补
  Storybook 素材。
- 生产环境验证（真实 `XM_PLATFORM_USERS_MODE=off`/`XM_REQLOG_MODE=off`
  下点开两个页签）：未跑，需验收线合入部署后核实。

## risks

- **判据依赖 chi 的默认行为**：如果未来后端框架升级后 chi 的
  `NotFoundHandler` 改成返回 JSON（哪怕是 `{}` 这种没有 `error.code` 的
  形状），`looksLikeUnmountedRoute` 仍然成立（判据只看「有没有可解析的
  `error.code`」，不依赖响应体是不是纯文本）；但如果反代/网关在这条路径
  上注入了一个带 `error.code` 的自定义 404 页面，会被误判成「具体资源不
  存在」而不是「未接入」——这类反代 404 页面目前仓库里没有，风险低但
  记录在案。
- **`description` 里的环境变量名是硬编码字符串**，与后端 `parseUsersMode`/
  `parseReqlogMode` 认的变量名（`XM_PLATFORM_USERS_MODE`、
  `XM_REQLOG_MODE`）没有类型层面的绑定，两边改名不会互相报错。当前只有
  这两个变量，人工核对过一致；如果后续变量改名，需要记得同步这两处
  `*_NOT_MOUNTED_DESCRIPTION` 字符串。
- **`PlatformUsersPanel` 隐藏 warnbar 的判断只看 `query.error`**：如果
  将来这个面板的查询模型换成多个并行 query（目前只有一个），需要重新
  核对「未接入」判断是否还覆盖所有会渲染表格的路径。

## follow_ups

- 上面「未处理的相邻面板」一节提到的 `DailyUsagePanel`/`KeyMetadataPanel`：
  当前在 off 场景下不可达，暂不需要处理；如果 `platformusers.go` 将来
  支持 detail/daily-usage/keys 独立开关，需要补一片同样的改道。
- 可以考虑把 `looksLikeUnmountedRoute` 判据用到未来同样走「connector/
  mode=off 则整组不挂载」这个模式的新端点（`Credentials`、`LocalAuth`
  等 router.go 里已经有同样纪律的可选挂载组），届时按需在对应 api/*.ts
  里复用，而不必再发明一次判据；本片只在明确要求的两组端点上落地，没有
  提前改动其它面板，避免在没有对应门禁测试覆盖的地方改变行为。
