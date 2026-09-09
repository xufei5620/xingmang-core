# XM-INVCON1-FALLBACK：断言登录未启用时回落到旧版直接 iframe（CR-0006 过渡期修复）

## status

READY（待验收线审读、复跑并人工合入）。

## branch

`ai/claude/XM-INVCON1-FALLBACK`（base `release/v0.1-launch` @ `9811343`，
含 XM-INVCON1），worktree `K:/星芒统一控制平台/wt-xmINVCON1FB`。

## summary

生产当前运行在 `XM_INVOICE_CONSOLE_ASSERTION_ENABLED=false`（CR-0006 第一
阶段的签发/兑换两侧尚未走完"生成密钥→两侧同步清单→启用"这套上线步骤，见
`docs/handoffs/slices/XM-INVCON1.md` 的"生产上线步骤"一节）。在这个配置下，
`internal/platform/consoleassertion` 的签发端点根本不挂载路由（`Deps.
ConsoleAssertion` 为 nil），前端请求得到 chi 的纯文本 404。XM-INVCON1 交付
时把这个 404 判成 `unavailable` 态，渲染一块整页替换的 `PageState
kind="unavailable"`（"开票系统未启用断言登录…"），三处开票页签因此在整个
过渡期里对本地登录的运营用户直接不可用——而 XM-INVCON0（CR-0005 第一阶段）
交付时，同一个位置本来是一个能打开的、走开票系统自己弹窗 OIDC 登录的直接
iframe。这是一处过渡期回归：本该继续可用的旧登录路径被新状态机"藏"起来了。

本片的修复只有一处判定改变：`InvoiceConsolePanel` 的 `mapAssertionError`
把"签发请求收到未挂载路由的 404"这一分支，从渲染 `unavailable`
`PageState`，改成回落到 `EmbeddedConsoleFrame` 的**旧版直接渲染**——与
`authMode !== "local"` 分支、以及 XM-INVCON0 交付时完全相同的调用方式（同一
个 `origin + path` 拼法、不传 `assertion`/`onAssertionNeeded`），只在它上方
加一条常驻、不可关闭的诚实提示：

> 控制台断言登录尚未启用，当前使用开票系统自身的登录（过渡期）

开票系统自己的弹窗 OIDC 登录完全不受影响——这条路径本来就没有被 XM-INVCON1
改动过，只是被错误的 `unavailable` 判定挡住了入口。

### "unavailable" 态被移除，不是改名

`AssertionState` 联合类型里原来的 `unavailable` 变体被**移除**，替换为新的
`legacy` 变体，而不是"重命名后原样保留"。核对了 XM-INVCON1 记录的完整错误
码表（`FINANCE_SCOPE_REQUIRED`/`ADMIN_NETWORK_DENIED`/
`ADMIN_STEP_UP_REQUIRED`/`INVALID_PARAMS`/`PERMISSION_DENIED`/`INTERNAL`）
与 `cmd/platform-api/consoleassertion.go` 的 `buildConsoleAssertionHandlers`
实现（`!cfg.ConsoleAssertion.Enabled` 时直接 `return nil, nil`，路由完全不
挂载，不是挂载后返回某个"已禁用"的结构化错误）：**后端契约里不存在一个
"已挂载但功能被禁用"的运行态**——`XM_INVOICE_CONSOLE_ASSERTION_ENABLED=false`
在前端能观察到的唯一信号就是 404。任务书原文预留了"404 / disabled 错误码"
两种情形，但代码库里第二种情形不存在，因此本片只实现并测试了 404 这一条，
在 `mapAssertionError` 里留了一段注释记录这个核实结果，供验收线确认（如果
产品负责人一侧另有"未来会加一个显式禁用码"的计划，需要另一轮改动）。

`unavailable` 原本渲染的整页 `PageState`，其唯一触发条件（404）现在被
`legacy` 接管；核对后没有找到其它任何会让 `mapAssertionError` 落到
`unavailable` 的路径。因此这个状态**没有保留的必要**，按团队指示"如果没有
真实场景就删掉"处理。真正的"已挂载但失败"场景（如 `INTERNAL`/500）走的是
未改动的 `error` 分支，行为不变。

### 诚实提示的实现方式

新增一个纯展示组件 `EmbeddedConsoleLegacyNotice`（`web/packages/ui-admin/
src/EmbeddedConsoleFrame.tsx`，与 `EmbeddedConsoleFrame` 同文件、一并导出），
只接受 `children` 文案、不预置任何默认文案——`ui-admin` 包一贯不认识"开票"
这类具体业务名词（`EmbeddedConsoleFrame` 本身同一条原则：不认识、不展示
iframe 内容），文案完全由调用方传入。放进 `ui-admin` 而不是直接内联在
`InvoiceConsolePanel` 里，是为了能在 Storybook 里单独展示这个"过渡期回落"
组合态（`EmbeddedConsoleFrame.stories.tsx` 新增 `LegacyFallbackWithNotice`
故事），不必为只有 admin-web 的 `InvoiceConsolePanel` 单独开一套 Storybook
接入（该组件历来只有 vitest 覆盖，没有故事文件，这与仓库现状一致——只有
`ui-admin`/`ui-primitives` 两个包接入了 Storybook）。

## 可见性判断未受影响

`InvoiceConsolePanel` 顶层的 `finance.read` scope 门禁（CR-0005 平台线 j）
在 `InvoiceConsoleFrameWithAssertion` 之前就已经返回，`legacy` 分支完全
继承这条门禁——不持有 `finance.read` 的账号仍然先看到"无权访问"，永远不会
走到断言签发请求这一步，遑论回落到旧版 iframe。`denied`/`step-up` 两态的
判定与渲染逻辑本片**未改动一行**。

## files_changed

修改：

- `web/apps/admin-web/src/components/InvoiceConsolePanel.tsx`——
  `AssertionState` 的 `unavailable` 变体换成 `legacy`；`mapAssertionError`
  的 404 分支改判；渲染分支替换为"提示 + 旧版 `EmbeddedConsoleFrame`"组合。
- `web/apps/admin-web/src/components/InvoiceConsolePanel.test.tsx`——原
  "端点未挂载→显示未接入说明"用例改写为断言回落行为（提示文案 + 可用
  iframe + 不出现"无权访问"/"加载失败"），新增一条"不误报无权访问"用例；
  "其它错误码"用例改名，明确其覆盖"已挂载但 500"这一独立场景，行为未改。
- `web/packages/ui-admin/src/EmbeddedConsoleFrame.tsx`——新增导出
  `EmbeddedConsoleLegacyNotice`（纯展示，`role="status"`）。
- `web/packages/ui-admin/src/EmbeddedConsoleFrame.test.tsx`——新增其 vitest
  用例。
- `web/packages/ui-admin/src/EmbeddedConsoleFrame.stories.tsx`——新增
  `LegacyFallbackWithNotice` 故事，组合渲染提示 + 旧版 iframe。
- `web/packages/ui-admin/src/index.ts`——导出新组件与其 props 类型。
- `docs/handoffs/slices/XM-INVCON1.md`——`follow_ups` 补一条指向本片的记录。

新增：

- `docs/evidence/screens/XM-INVCON1-FALLBACK/01-legacy-fallback-with-notice.png`
- `docs/evidence/screens/XM-INVCON1-FALLBACK/02-assertion-enabled-normal-ready-state.png`
- 本文档。

**无 Go 改动**：本片完全是前端状态机与展示层修复，未触碰
`internal/platform/consoleassertion`、`cmd/platform-api` 或任何后端契约；
未新增数据库迁移。

## tests_run

前端（`pnpm --config.verify-deps-before-run=false` 前缀，worktree 根
`node_modules` 是指回主检出的 junction，串行执行）：

```bash
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
```

- `typecheck`：PASS，5 of 6 workspace 项目（`ui-storybook` 无该脚本，与
  XM-INVCON1 基线一致），全部 `Done`，无报错。
- `test`：PASS，全量：
  - `design-tokens`：1 文件 / 10 用例
  - `ui-primitives`：7 文件 / 16 用例
  - `ui-admin`：17 文件 / **261** 用例（较 XM-INVCON1 交付时的 260 条 +1，
    新增 `EmbeddedConsoleLegacyNotice` 的渲染用例）
  - `admin-web`：98 文件 / **1438** 用例（较 XM-INVCON1 交付时的 1437 条
    +1：原 1 条"端点未挂载"用例拆成 2 条，净增 1）
- `storybook build`：PASS，`storybook build` 打印"Storybook build
  completed successfully"；`grep` 确认产物
  `EmbeddedConsoleFrame.stories-*.js` 内含 `LegacyFallbackWithNotice`
  字符串。
- 治理：`bash scripts/check-governance.sh`——exit 0，无输出。
- gitleaks：`gitleaks protect --staged --verbose`——`no leaks found`
  （对暂存区 ~7.85KB 改动扫描）。

**真实浏览器走查**（Docker/真实 platform-api 在本环境不可用，沿用
XM-INVCON1 记录的既有坑位与配方；mock API 改自 XM-INVCON1 走查用的那份
脚本，加一个 `/debug/set-assertion-mounted` 开关，`mounted:false` 时对
`/api/v1/auth/console-assertion` 返回**纯文本** 404（不是
`{error:{code,...}}`），精确复刻 chi 对未挂载路由的真实响应形状；跨源 mock
开票控制台页面复用 XM-INVCON1 走查时生成、仍在有效期内且已被本机当前
Windows 用户信任的自签证书，未新生成证书、未新增证书库残留）：

1. **assertion 未挂载（`mounted:false`）**：本地登录（密码 + TOTP）后导航到
   Sub2API → 支付与财务 → 开票，正确显示提示条"控制台断言登录尚未启用，
   当前使用开票系统自身的登录（过渡期）"+ 完整渲染的 iframe（mock 开票页
   显示"等待控制台断言…"，证明确未投递任何断言）——
   `01-legacy-fallback-with-notice.png`。
2. **assertion 已挂载（`mounted:true`）**：同一页签重新加载，提示条消失，
   iframe 正常呈现，且 mock 开票页正确收到断言并显示其内容前 24 字符
   （"已收到断言：mock.eyJzY29wZSI6InN1YjJ…"），证明本片改动没有破坏
   XM-INVCON1 的正常签发/投递路径——
   `02-assertion-enabled-normal-ready-state.png`。

两次走查浏览器控制台均 0 error。走查完成后已还原
`web/apps/admin-web/public/app-config.js`（`git diff` 确认为空）、删除
临时的 `vite.dev.local.config.ts`（未纳入提交）、终止三个临时进程（mock
platform-api :8080、mock 开票控制台 :8081、vite dev :5184）。

## not_run

- **"disabled 错误码"场景**：见上文"unavailable 态被移除，不是改名"一节
  ——核实后确认当前后端契约与 XM-INVCON1 交接文档记录的错误码表里都没有
  这个码，只有 404 是真实信号，因此没有为一个不存在的错误码编测试。如果
  验收线知道产品负责人一侧有其它权威来源要求新增这样一个码，请指出，
  改动只需要在 `mapAssertionError` 里加一个 `case`。
- **两侧真实断言签发/兑换的端到端联调**：与 XM-INVCON1 记录的既有缺口
  相同（未生成真实密钥），本片不改变这条缺口的状态。
- **后端 `go test ./...`**：本片无 Go 改动，未跑；`go build`/`go vet` 同样
  未跑，风险评估为极低（未触碰任何 `.go` 文件）。建议验收线合入前仍复跑
  一次作为基线确认（同 XM-INVCON0 交接文档的既定纪律）。
- **`LoginPage.tsx` 两步登录集成路径的自动化测试缺口**：XM-INVCON1 交接
  文档已记录，本片未新增覆盖，不在本片范围内。

## risks

- **`unavailable` 态的移除是一处破坏性变更**：如果有其它调用方（本次搜索
  未发现）依赖 `InvoiceConsolePanel` 内部这个已经是模块私有类型的
  `AssertionState.unavailable`，会编译失败——但该类型未导出（`type
  AssertionState` 没有 `export`），影响面确认仅限本文件。
- **依赖后端"未挂载=404、不返回结构化禁用码"这条既有纪律**：如果后续有人
  在 `internal/platform/consoleassertion`/`cmd/platform-api` 改成"挂载但
  返回一个禁用码"的实现方式（本片未改动这部分，也没有计划这么做的迹象），
  前端会把它当成 `error`（挂载但失败）处理而不是 `legacy`——不会崩溃或
  显示错误信息之外的东西，但过渡期体验会退回成"加载失败带重试"而不是这条
  更友好的旧版回落，需要那时再补一个 `case`。
- **走查用的自签证书（CN=127.0.0.1）已提前存在于本机当前用户 Root 信任库,
  2026-09-04 16:59 UTC 到期**：本片复用而非新生成，未增加清理负担，
  XM-INVCON1 交接文档已记录其清理方式，到期后自然失效，无需额外操作。

## follow_ups

- 无新增待办——本片是对 XM-INVCON1 遗留状态机缺口的针对性修复，
  `docs/handoffs/slices/XM-INVCON1.md` 的 `follow_ups`/`风险`/生产上线步骤
  各条待办原样有效，未被本片改变。
