# XM-INVCON0：平台侧嵌入开票控制台管理端（CR-0005 平台线 g–k）

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-INVCON0-admin-embed`（base `release/v0.1-launch` @ `b9b72ac`）

## commit

四个小提交，逐个可独立编译/测试通过：

- `6b89f4c` — feat(ui-admin): add EmbeddedConsoleFrame for iframe-embedded consoles
- `f9cacb5` — feat(admin-web): add invoiceConsoleOrigin to runtime config contract
- `a1d6660` — feat(deploy): plumb XM_INVOICE_CONSOLE_ORIGIN to the web container
- `afa6299` — feat(admin-web): embed invoice console in platform and governance finance tabs（HEAD）

## summary

依据 `docs/change-requests/CR-0005-invoice-admin-console.md`（产品负责人
2026-09-02 指令）平台线 g–k：星芒控制台以 iframe 承载开票系统管理端
（`invoice-system` 的 `/embed/admin/<sub2api|newapi|global>`），鉴权、审批、
双人复核、审计全部仍由开票系统执行；平台侧不新增任何到开票系统的数据
通道，不展示任何开票数字。开票线的对应切片 XM-INV-ADMIN-EMBED（nginx
入口、嵌入管理模式、按平台过滤、CSP 改 `frame-ancestors`）不在本仓库，
由验收线在开票系统那边跟进。

**g（三处入口）**

- Sub2API → 支付与财务 → 开票：原来的「阻塞点是 CR-0002」占位换成
  `InvoiceConsolePanel mode="sub2api"`。
- NewAPI → 支付与财务：新增第 3 个子页签「开票」（`InvoiceConsolePanel
  mode="newapi"`）。这推翻了 `docs/architecture/ADMIN-IA.md` §8.2 #2
  「NewAPI 不补开票」的裁定——已在该文件 §2.2、§6.2、§8.2 #2 三处记录
  CR-0005 推翻此裁定，改导航顺序按文件自己的规矩（先改文档、再改
  `navigation.ts`、再改测试）。
- 治理 → 跨平台财务 → 开票集成：`web/apps/admin-web/src/pages/PlaceholderPage.tsx`
  新增 `governanceSubTabOverride(path, tabId)`，在查蓝图之前先认
  `/finance` + `invoicing`，渲染 `InvoiceConsolePanel mode="global"`；
  `FINANCE_BLUEPRINT` 里 `invoicing` 这条蓝图数据本身**没删**（仍是
  `blueprints.test.ts` 用来跟 `navigation.ts` 对账的数据源），只是不再经
  `BlueprintTabView` 渲染。/finance 页面其余五格（财务总览/支付通道/
  财务对账/异常与冻结/财务配置）不变。

**h（EmbeddedConsoleFrame，`web/packages/ui-admin/src/EmbeddedConsoleFrame.tsx`）**

Props `origin`/`path`（三选一字面量联合）/`title`。`src = origin + path`，
**不加 `sandbox`**（开票线的登录走弹出顶层窗口 + iframe 内同站 Cookie，
`sandbox` 会同时挡掉弹窗与 Cookie）；`allow="clipboard-write"`；
`referrerPolicy="strict-origin"`。只接受 `event.origin === origin` 且形状是
`{type:"xm-embed", version:1, kind:"height", height:number}` 的
`postMessage`，据此设置高度（钳制在 480–4000px）；收到消息前用视口高度
（`h-[70vh]`）兜底。加载失败（原生 `error` 事件或 15s 超时）显示
`PageState kind="error"` + 「在新窗口打开」链接（`href = origin + path`）。

调试过程中发现一个值得记录的真实 React 限制：**React 的合成事件系统不给
`<iframe>` 接 `onError`**——`react-dom` 按标签分发原生监听时只对
`iframe/object/embed` 接了 `"load"`，`"error"` 只接给
`img/link/source/embed`（`react-dom-client.development.js` 的 host config
按标签 `switch`，`iframe` 分支只有一行 `listenToNonDelegatedEvent("load",
...)`）。这不是 jsdom 测试环境的怪癖，是任何浏览器下都成立的行为——传
`onError` prop 不报错，但永远不会被调用。改用**回调 ref**在真实 DOM 节点
上手动 `addEventListener("error", ...)`（React 19 支持 ref 回调返回清理
函数），绕开合成事件系统；回调 ref 由 React 在 commit 阶段随节点挂载/卸载
调用，天然跟手节点自己的生命周期（含 `failed` 状态翻转导致的重新挂载），
不依赖某个 `useEffect` 恰好在同一轮里重新执行。

**i（配置）**

- `web/apps/admin-web/src/auth/runtimeConfig.ts`：`window.__XM_CONFIG__`
  契约新增 `invoiceConsoleOrigin`。静态直传（同 reqlog/CPA 先例，没有
  `authMode` 那种 Vite 构建期回落层），校验为「不带路径/查询/片段的
  https 来源」，形状不对时按未配置处理并记进 `problems`，不抛异常。
- `deploy/compose/launch.yaml`：web 服务新增
  `XM_INVOICE_CONSOLE_ORIGIN: ${XM_INVOICE_CONSOLE_ORIGIN:-}`。
- `deploy/docker/web-app-config.sh`：读取该变量，非空时写入
  `/app-config.js` 的 `window.__XM_CONFIG__.invoiceConsoleOrigin`；只做
  非致命的 `https://` 前缀提醒（不像 oidc 三项那样 `exit 1`）——真正的
  形状校验在前端。
- `deploy/compose/.env.example`：补充示例与说明段落。
- 三处入口缺省时统一显示 `PageState kind="unavailable"` 标题「开票」，
  描述「未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）」，不渲染
  iframe（`InvoiceConsolePanel`）。

**j（可见性）**

`web/apps/admin-web/src/components/InvoiceConsolePanel.tsx` 用
`appApiConfig.scopes.includes(FINANCE_READ_PERMISSION)` 判断，缺失时显示
`PageState kind="denied"`（复用默认标题「无权访问」，不额外传 title，与
`ApiStateView` 里其它 403 的呈现方式一致）。`scopes` 是可注入 prop（默认
取 `appApiConfig.scopes`），与 `UpstreamAccountDetail.tsx` 现有的同一种
测试注入模式一致。这只是体验层门禁——真正的裁决在开票系统自己的登录里，
菜单可见不等于授权。

**k（不展示开票数字）**

`InvoiceConsolePanel` 的三种状态（denied/unavailable/正常）都不读取、不
拼接任何开票记录数或金额；正常态下内容完全在 iframe 内部，对这个组件
永远不透明。`InvoiceConsolePanel.test.tsx` 有一条测试专门断言三态下容器
文本里都不出现金额形状的字符串。

## files_changed

新增（6）：

- `web/packages/ui-admin/src/EmbeddedConsoleFrame.tsx`
- `web/packages/ui-admin/src/EmbeddedConsoleFrame.stories.tsx`
- `web/packages/ui-admin/src/EmbeddedConsoleFrame.test.tsx`
- `web/apps/admin-web/src/components/InvoiceConsolePanel.tsx`
- `web/apps/admin-web/src/components/InvoiceConsolePanel.test.tsx`
- `web/apps/admin-web/src/components/PlatformFinancePanel.test.tsx`（此前
  没有测试文件）

修改（10）：

- `web/packages/ui-admin/src/index.ts` —— 导出 `EmbeddedConsoleFrame`
- `web/packages/ui-admin/src/navigation.ts` —— NewAPI 财务子页签补
  `["invoices", "开票"]`
- `web/packages/ui-admin/src/navigation.test.ts` —— 新增 Sub2API/NewAPI
  财务子页签断言
- `web/apps/admin-web/src/auth/runtimeConfig.ts` —— `invoiceConsoleOrigin`
  字段与校验
- `web/apps/admin-web/src/auth/runtimeConfig.test.ts` —— 对应用例
- `web/apps/admin-web/src/components/PlatformFinancePanel.tsx` —— Sub2API
  `invoices` 换成嵌入面板，NewAPI 新增 `invoices` 分支
- `web/apps/admin-web/src/pages/PlaceholderPage.tsx` —— 治理
  `/finance?sub=invoicing` 的渲染覆盖
- `web/apps/admin-web/src/lib/platforms.test.ts` —— 更新 NewAPI 财务子
  页签断言（原断言「只有 2 格且没有开票」）
- `web/apps/admin-web/src/router.test.tsx` —— 三处：NewAPI 财务页签计数
  ×2、Sub2API `invoices` 内容断言（原断言 CR-0002 占位文案）
- `docs/architecture/ADMIN-IA.md` —— §2.2/§6.2/§8.2 #2 记录 CR-0005 推翻
  裁定
- `deploy/compose/launch.yaml`、`deploy/compose/.env.example`、
  `deploy/docker/web-app-config.sh` —— 见上「配置」小节

未改动 `docs/handoffs/ACCEPTANCE-LOG.md`（按分工，验收线专属文件）。

## tests_run

Windows worktree 用 `link-node-modules.mjs` 镜像脚本补齐 `node_modules`
（未跑 `pnpm install`），因此四条命令都带
`--config.verify-deps-before-run=false`；四条门禁**串行**执行，均在
`K:/星芒统一控制平台/wt-xmINVCON0` 下：

- `pnpm --config.verify-deps-before-run=false -r run typecheck` —— PASS，
  5 of 6 workspace projects（`tsc --noEmit`，全部 `Done`，无报错）
- `pnpm --config.verify-deps-before-run=false -r run test` —— PASS，全量
  118 个测试文件、1581 个用例全绿：
  - `design-tokens`：1 文件 / 10 用例
  - `ui-primitives`：7 文件 / 16 用例
  - `ui-admin`：17 文件 / 253 用例（含本片新增
    `EmbeddedConsoleFrame.test.tsx` 19 条、`navigation.test.ts` 新增 2 条）
  - `admin-web`：93 文件 / 1302 用例（含本片新增
    `InvoiceConsolePanel.test.tsx` 6 条、`PlatformFinancePanel.test.tsx`
    5 条，及既有文件里更新/新增的用例）
  - `ui-storybook`：无单测脚本（构建即验证，见下一条）
- `pnpm --config.verify-deps-before-run=false --filter ui-storybook run build`
  —— PASS，`storybook build` 成功产出
  `EmbeddedConsoleFrame.stories-*.js`（3 个故事：Sub2Api/NewApi/Global）
- `bash scripts/check-governance.sh` —— PASS（exit 0，无输出）

调试记录：第一次跑 `ui-admin` 测试时 `EmbeddedConsoleFrame.test.tsx` 有 2
条失败（`onError`/切换 `path` 两条用例），根因是上面「h」小节记录的
React 限制（`<iframe>` 的 `onError` 永不触发），改用回调 ref 后重跑全绿，
详见上文。

## not_run

- 后端 `go test ./...`：本片未改动任何 `.go` 文件，未跑；按仓库规则合入
  前建议验收线仍复跑一次作为基线确认。
- `gitleaks`：未单独调用（`check-governance.sh` 内部是否含 gitleaks 未
  逐行核实，退出码 0 视为通过；本片新增字符串只有环境变量名、URL 与中文
  说明文案，没有形似密钥的字面量，参考记忆里 XM-0037d/XM-0049 的误报
  经验，风险低）。
- 真实浏览器 / Playwright / Chrome DevTools 实测：未跑。`EmbeddedConsoleFrame`
  的 postMessage 过滤、高度钳制、超时判定都是纯逻辑，vitest + jsdom（含
  `vi.useFakeTimers`）已覆盖；`onError` 那个真实 React 限制正是在这次
  vitest 运行中被发现并修正的，具有较高置信度。但**没有对着真实的
  `invoice.solov.cc` iframe 做过端到端联调**——同源 Cookie、弹出顶层窗口
  登录、`frame-ancestors` 是否放行控制台来源，这些依赖开票线切片
  XM-INV-ADMIN-EMBED 落地后才能验证，属于两线联调范畴，不在本片单独可
  验证的范围内。
- 生产/预发环境下配置 `XM_INVOICE_CONSOLE_ORIGIN` 后的实际渲染：未跑，
  需要验收线在两线都合入后按 CR-0005「验证」一节执行。

## risks

- **两线耦合**：本片只完成平台侧。开票线（XM-INV-ADMIN-EMBED）落地
  `frame-ancestors https://console.solov.cc`、嵌入管理模式与按平台过滤
  之前，三处入口即使配了 `XM_INVOICE_CONSOLE_ORIGIN` 也打不开（要么被
  开票系统现有的 `frame-ancestors 'none'` 拒绝承载，要么 `/embed/admin/*`
  路由还不存在）——这是预期状态，不是本片的 bug，但验收线合入本片时不要
  误判为「配了却打不开」的回归。
- **postMessage 高度同步依赖嵌入应用真的按规范发消息**：如果开票系统的
  嵌入页迟迟不发 `{type:"xm-embed", version:1, kind:"height", height}`，
  用户会一直看到 `70vh` 的视口兜底高度，不算错，但不是最终体验；这属于
  两线联调时才能对齐的细节。
- **`InvoiceConsolePanel` 的 scope 判断只是体验层门禁**：`appApiConfig.scopes`
  在 oidc/local 生产模式下不是从真实会话 claims 实时读取的（仍是构建期/
  运行时静态配置，与仓库里其它用到 `appApiConfig.scopes` 的地方同一个
  已知局限，见 `api/config.ts` 的注释），真正兜底的是开票系统自己的 OIDC
  登录与授权——这与 CR-0005 j 条「菜单可见不等于授权」的设计意图一致，不
  是本片引入的新缺口。
- **`FINANCE_BLUEPRINT.invoicing` 数据与实际渲染分叉**：治理 `/finance`
  页面的「开票集成」子页签，其蓝图数据（`集成契约状态`/`文件引用原则`
  两张键值卡）仍然存在于 `governance.ts`，但运行时已经不再渲染它，改渲染
  `InvoiceConsolePanel`。这是刻意的（蓝图数据留给
  `blueprints.test.ts`/`BlueprintView.test.tsx` 的对账与单测用），但如果
  以后有人只看 `governance.ts` 不看 `PlaceholderPage.tsx` 的
  `governanceSubTabOverride`，可能会误以为蓝图卡片仍在渲染——已在
  `governanceSubTabOverride` 与 `FINANCE_BLUEPRINT.invoicing` 附近都留了
  注释指向对方。

## follow_ups

- **运维部署前置操作**：在服务器 web 服务的环境变量里设置
  `XM_INVOICE_CONSOLE_ORIGIN=https://invoice.solov.cc`（`deploy/compose/.env.example`
  已给出同样的示例行），随下一次 web 重新部署生效；缺省时三处开票页签会
  持续显示「未配置开票控制台来源」，不影响其余功能。这一步依赖开票线
  XM-INV-ADMIN-EMBED 已经先行落地（否则配了来源也打不开，见上「风险」）。
- 第二阶段（原生迁入，CR-0005 明确「不在本 CR 授权范围」）：只读连接器
  （XM-0028/0029）、原生列表/详情、Foundation-B 就绪后的开票管理
  Action——这些均需另立切片，本片不做任何铺垫代码之外的准备。
- 若后续要给 `EmbeddedConsoleFrame` 增加更多嵌入场景（不只是开票），
  `postMessage` 的版本化 schema（`version:1`）已经预留了演进空间，加
  `version:2` 时记得两端同步、不做隐式兼容旧版形状。
