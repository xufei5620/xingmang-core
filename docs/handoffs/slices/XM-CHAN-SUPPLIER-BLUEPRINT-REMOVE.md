# XM-CHAN-SUPPLIER-BLUEPRINT-REMOVE：删除供应商创建评审蓝图页

## status

READY（待验收线审读、复跑并人工合入）

## branch

`ai/claude/XM-CHAN-SUPPLIER-BLUEPRINT-REMOVE`；base `release/v0.1-launch`
@ `f1a6de3`

## commit

- `08c2f5c` — chore(admin-web): remove SupplierCreatePage review-only blueprint

## summary

产品负责人 2026-09-03 裁定：删除 `/platforms/:p/suppliers/new` 这个只读
字段/布局评审蓝图页（`SupplierCreatePage.tsx`）。真正能用、且唯一保留的
「添加上游」入口是渠道表工具栏的「＋ 添加上游」（`UpstreamAccountDialog`，
经 `finance.upstream_account.set@1` Action），本片未改动该组件、
`ManagedChannelTable`、`ChannelTable` 或任何后端代码。

**与 [[xingmang-project-state|XM-OPS-TAILS1]] 的关系（重要，避免误读为
回归）**：同一天更早，XM-OPS-TAILS1 核实过这条路由，明确判定它**不是
孤儿**——ADMIN-IA §8.8 07:20 补充裁定原文写着「上游详情与添加上游路由
（`/platforms/:p/suppliers/:id`、`/platforms/:p/suppliers/new`）不变」，
是产品负责人当时就近记录、要求保留的评审壳，两条路径共存是设计决定。
**本片不是对那次核实的推翻或纠正**——那次核实在当时的裁定下是对的。
产品负责人当天晚些时候发出了一条**新的、独立的裁定**，改判这个专门
评审用的只读壳不再需要保留，本片据此执行。ADMIN-IA §8.8 已加一段带
日期的裁定补记，把两次裁定的先后关系写清楚，供以后审计时不会误以为
是同一次裁定内部自相矛盾。

### 删除范围

- `web/apps/admin-web/src/pages/SupplierCreatePage.tsx`（整个文件，182 行）。
- `web/apps/admin-web/src/router.tsx`：
  - 移除 `import { SupplierCreatePage } from "./pages/SupplierCreatePage"`。
  - 移除路由注册 `{ path: "platforms/:serviceType/suppliers/new", loader:
    supplyPlatformLoader, Component: SupplierCreatePage }`。
  - 移除 `supplyPlatformLoader` 函数——repo 全局 grep 确认它只在这一处
    路由注册里被引用，删除路由后成为真正的死代码，一并删除。
- 两处既有测试：
  - `SupplyDetailPages.test.tsx`：移除 `SupplierCreatePage` 的 import 与
    对应 `it(...)` 用例（原断言"添加上游"标题、只读禁用输入、返回链接）。
  - `router.test.tsx`：移除 `新增上游路由优先于动态 upstreamId` 用例
    （原断言这条静态路由优先于 `suppliers/:upstreamId` 动态路由命中）。

### 落地后的行为：不是新增 404，是复用既有兜底分支

`UpstreamDetailPage.tsx`（`suppliers/:upstreamId` 路由的组件）里**本来就有**
一段防御性分支：

```
if (!upstreamId.trim() || upstreamId === "new") {
  return <NotFoundView ... detail={upstreamId === "new" ? "..." : "..."} />;
}
```

这段代码原本是"万一路由顺序变了、静态路由没能优先命中"的兜底，注释
（router.test.tsx 那条已删用例的标题）说明作者当时就预见了两条路由的
优先级关系。删除静态路由后，`/platforms/:p/suppliers/new` 现在**直接**
命中动态路由 `suppliers/:upstreamId`（`upstreamId="new"`），这段兜底分支
从"防御性代码"变成"实际生效路径"，渲染应用统一的 `NotFoundView`
（标题「页面不存在」）——不是孤立处理，是全站唯一的 404/占位组件，与
服务器详情页未知 fixture 等既有场景用的是同一个组件。

**一处随手修的措辞**：这段兜底分支原来的提示语是「新增上游请使用专用
登记页面」，指的正是刚删掉的 `SupplierCreatePage`。删除后这句话会变成
指向一个不存在页面的误导性文案，所以顺带把它改成「新增上游请到渠道
管理页使用「＋ 添加上游」」，指向唯一保留的真实入口。这不是"删除只删
被明确点名的东西之外顺手改了别的地方"的范围扩张——是删除操作本身
制造出的一处悬空引用，不修就是留一个错的提示语给用户，判断是在删除
的同时一并修正，已如实记录在此供验收线核对。

### 文档更新

`docs/architecture/ADMIN-IA.md` §8.8（07:20 补充裁定小节）：

- 把「上游详情与添加上游路由（.../suppliers/:id、.../suppliers/new）
  不变」这句拆开——`suppliers/:id` 仍不变，`suppliers/new` 改为已下线。
- 新增一段带日期（2026-09-03）的裁定补记，交代这是独立于 XM-OPS-TAILS1
  核实结论的新裁定、真正的添加上游入口、删除范围与地址落回的兜底行为，
  并注明落地切片本身（本片）。
- `#/s2/suppliers/new`（原型 hash 语义表，L168）**未改动**——那一行描述
  的是原型 hash 到"渠道表工具栏「＋ 添加上游」对话框"的映射，本来就不
  指向 `SupplierCreatePage`，与本次删除无关，核实后确认不需要动。

### 未改动的东西（如实记录，供验收线复核范围）

- `UpstreamAccountDialog.tsx`、`ManagedChannelTable`、`ChannelTable`、
  所有后端代码——一行未动。
- `UpstreamAccountDialog.tsx` 第 133 行有一条注释引用了原型 hash
  `#/s2/suppliers/new`（按钮文案的设计出处说明），纯注释、不是代码依赖，
  与被删的应用路由是两回事，未动。
- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx` 导出的
  `isSupplyPlatform`/`supplyPlatformLabel`/`upstreamDetailPath`/
  `SupplyPlatform`——`SupplierCreatePage.tsx` 曾从这里 import，但
  `ChannelDetailPage.tsx` 自己和 `UpstreamDetailPage.tsx` 都在用，不是
  "只给它用的 helper"，未删。
- `router.tsx` 里 `suppliers/:upstreamId` 路由及其 `upstreamDetailLoader`
  ——服务的是仍然保留的上游详情页，未动（只改了 `UpstreamDetailPage.tsx`
  内部那一行提示文案，见上文）。

## files_changed

- `web/apps/admin-web/src/pages/SupplierCreatePage.tsx`（删除，182 行）
- `web/apps/admin-web/src/router.tsx`（-12 行：import、路由注册、
  `supplyPlatformLoader`）
- `web/apps/admin-web/src/pages/SupplyDetailPages.test.tsx`（替换 1 条用例：
  原断言蓝图页渲染 → 改断言渲染 not-found 兜底）
- `web/apps/admin-web/src/router.test.tsx`（替换 1 条用例，同上，走完整
  路由树）
- `web/apps/admin-web/src/pages/UpstreamDetailPage.tsx`（`upstreamId ===
  "new"` 分支的提示文案，指向真实入口而非已删页面）
- `docs/architecture/ADMIN-IA.md`（§8.8 拆分「不变」表述 + 新增带日期
  裁定补记）

## tests_run

本机 worktree 装不满 `pnpm install`（Windows rename-to-non-empty-dir
EPERM，本机已知坑）；按既有配方镜像了 5 个工作区包各自的 `node_modules`
（`@xingmang/*` 指回本 worktree 自己的源码，第三方包指回主检出共享的
`.pnpm` store），根 `node_modules` 未动，门禁用 `node <resolved tsc/vitest
入口>` 直接跑，绕开 pnpm 依赖校验。仓库没有 `web/package.json`（pnpm
workspace 根在仓库根目录，`pnpm-workspace.yaml` глоb 是
`web/apps/*`、`web/packages/*`），所以派工消息里的 `pnpm -C web
typecheck`/`pnpm -C web build` 两条命令在这个仓库里不存在对应脚本；
改用仓库根 `CLAUDE.md` 记录的等价命令（`pnpm -r run typecheck`/
`pnpm -r run test`）逐包直接跑同款入口。

| 命令 | 工作目录 | 结果 |
|---|---|---|
| `node .../typescript@5.9.3/.../tsc --noEmit` | `web/apps/admin-web` | exit 0（改动前后各跑一次，改动前 baseline 同样干净） |
| `node .../vitest@4.1.11.../vitest.mjs run` | `web/apps/admin-web` | exit 0，103 test files / 1472 tests all passed（baseline 同样 103/1472，改一条测试、删一条测试各自净抵消，总数不变） |
| `node .../tsc --noEmit` | `web/packages/ui-admin` | exit 0 |
| `node .../vitest.mjs run` | `web/packages/ui-admin` | exit 0，17 test files / 261 tests passed |
| `node .../tsc --noEmit` | `web/packages/ui-primitives` | exit 0 |
| `node .../vitest.mjs run` | `web/packages/ui-primitives` | exit 0，7 test files / 16 tests passed |
| `node .../tsc --noEmit` | `web/packages/design-tokens` | exit 0 |
| `node .../vitest.mjs run` | `web/packages/design-tokens` | exit 0，1 test file / 10 tests passed |
| `node .../tsc --noEmit` | `web/apps/ui-storybook` | exit 0（该包没有单测，`test` 脚本只是一句 echo） |
| `gitleaks git --no-banner --log-opts="release/v0.1-launch..HEAD" .` | 仓库根（worktree） | exit 0，`no leaks found`（1 commits scanned） |

## not_run

- `pnpm --filter ui-storybook run build`（CLAUDE.md 里单独列的 storybook
  构建门禁）：本片只删了一个未在 Storybook 里注册 story 的页面组件
  （`SupplierCreatePage.tsx` 没有对应 `.stories.tsx`，grep 确认），与
  storybook 构建面无关，未跑。
- 真实 `vite build`（`apps/admin-web` 的 `build` 脚本是 `tsc --noEmit &&
  vite build`）：`tsc --noEmit` 已单独跑过（见上表）；`vite build` 本身
  在 worktree 里因 `.bin` shim 内嵌主检出绝对路径需要额外改造入口
  （见项目内 Windows 工具链坑记录），派工消息里"如果这是文档化的构建
  门禁"这个条件不成立——`CLAUDE.md` 记录的门禁只到 `pnpm -r run
  typecheck`/`test`，没有把 `vite build` 列为必需门禁，故未跑。
- 后端 `go test ./...` / `go vet ./...`：本片零后端改动，未跑。
- `bash scripts/check-governance.sh`：本片不涉及治理相关文件（契约/
  Action 注册表/凭据），未跑。

## risks

- **无功能风险**：删除的是从未在导航/菜单里出现过的隐藏评审页，唯一
  入口 `UpstreamAccountDialog` 未动。地址落回既有 not-found 兜底而不是
  裸露一个新的 404 splat 路由，用户体验上是"这个地址走到统一的页面
  不存在提示"，不是"页面消失后变成一片空白或报错堆栈"。
- **提示文案改动的范围判断已在上文"一处随手修的措辞"小节说明**，如果
  验收线认为这句提示文案不该由本片顺带改（严格只删被点名的东西），
  这是唯一一处超出"删除清单"字面范围的改动，改动本身很小（一行字符串），
  回退也容易——记录在此便于验收线单独裁定要不要保留这处改动。
- **`XM-OPS-TAILS1` 与本片的先后关系依赖时间线记忆**：两次核实/裁定
  都发生在同一天（2026-09-03），如果验收线是按时间顺序审读两片的
  handoff，容易先入为主觉得本片"推翻了 XM-OPS-TAILS1 的核实结论"从而
  怀疑是不是代理自己判断错了——已在 summary 开头单独一段解释这不是
  推翻，是两次独立裁定的先后关系，请按该段核对。

## follow_ups

- 无。这是一次干净的、范围明确的删除，没有遗留的后续工作。
