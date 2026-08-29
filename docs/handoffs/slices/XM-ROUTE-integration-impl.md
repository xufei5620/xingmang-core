# XM-ROUTE-integration-impl · 统一详情路由整合准备

## status

BLOCKED

阻塞原因：当前 `release/v0.1-launch@e98080d` 尚未包含本片要挂载的 Supply detail 与 Server detail 页面，直接加入 import/route hunk 会导致 admin-web 编译失败。按要求本分支只保存冲突审计与整合顺序，不复制或修改其它 READY 分支。

## branch / commit / base

- branch: `ai/codex/XM-ROUTE-integration-impl`
- base: `release/v0.1-launch` at `e98080d`
- worktree: `K:/星芒统一控制平台/wt-xmROUTE-integration-impl`
- implementation: 未开始（无 router/source 变更）

## read-only conflict audit

| 目标 | 当前 release | 依赖分支 | 结论 |
| --- | --- | --- | --- |
| 用户详情 `platforms/:serviceType/users/:userId` | 路由、loader、v1 页面已存在 | `ai/codex/XM-C-USER0-impl` 更新 v2 页面/契约语义 | 不新增第二条路由；USER0 合入后只保留现有路径 |
| 服务器详情 `platforms/server/detail/:serverId` | `ServerDetailPage.tsx` 不存在；`blueprints/server.ts` 尚无 preview-id helper | `ai/codex/XM-LOCAL-detail-impl` | 依赖未合入，当前不能导入/注册 |
| Supply 渠道详情 | `ChannelDetailPage.tsx` 不存在 | `ai/codex/XM-SUPPLY-detail-impl` | 依赖未合入，当前不能导入/注册 |
| Supply 上游详情 / 新增上游 | `UpstreamDetailPage.tsx`、`SupplierCreatePage.tsx` 不存在 | `ai/codex/XM-SUPPLY-detail-impl` | 依赖未合入，当前不能导入/注册 |
| MAP0 | 无 router hunk；修改平台渠道目录、映射 API 与 `PlatformDetailPage` | `ai/codex/XM-C-MAP0-impl` | 先合入渠道目录，再统一补详情链接/路由；不在本片复制 MAP0 |

证据命令（只读）：

- `git cat-file -e e98080d:web/apps/admin-web/src/pages/ServerDetailPage.tsx` → 不存在。
- `git cat-file -e e98080d:web/apps/admin-web/src/pages/ChannelDetailPage.tsx`、`UpstreamDetailPage.tsx`、`SupplierCreatePage.tsx` → 均不存在。
- `git diff e98080d..ai/codex/XM-C-USER0-impl -- web/apps/admin-web/src/router.tsx` 只有 v2 语义注释，无新增用户路径。
- `git diff e98080d..ai/codex/XM-C-MAP0-impl -- web/apps/admin-web/src/router.tsx` 为空。

## planned single router hunk (待依赖合入后实施)

统一 router 只允许一处声明以下路径，并将静态/专用路径置于通用动态平台路由之前：

1. `platforms/server/detail/:serverId` → `ServerDetailPage`，loader 调用 `isServerDetailPreviewId`，未知 ID 404。
2. `platforms/:serviceType/upstream/detail/:channelId` → `ChannelDetailPage`，loader 只接受 `sub2api|newapi` 且拒绝空 ID。
3. `platforms/:serviceType/suppliers/new` → `SupplierCreatePage`；必须排在 `suppliers/:upstreamId` 之前，避免 `new` 被当作上游 ID。
4. `platforms/:serviceType/suppliers/:upstreamId` → `UpstreamDetailPage`，同样执行平台/非空 ID 门禁。
5. 复用现有 `platforms/:serviceType/users/:userId`，不再添加平行用户详情路径。

loader 只负责路径语义与对象 ID 门禁，不读取列表缓存、不跨平台回落、不执行 Action；页面本身继续保持 UI-only/只读边界。路由顺序还要避开现有 `platforms/:serviceType`、`placeholderRoutes`、请求详情和旧路径 redirect。

## required merge order / unblock condition

1. 验收线先分别合入并确认 `XM-C-USER0-impl`、`XM-C-MAP0-impl`、`XM-SUPPLY-detail-impl`；Server detail 需先解决其自身 Handoff 的全量测试阻塞并获准合入（或提供等价 READY 页面/blueprint slice）。
2. 以更新后的 release 新 SHA 重建本路由分支；只提取一份 router.tsx + router.test.tsx hunk，避免四个分支各自改 router 造成重复声明。
3. 补未知平台、空 ID、`suppliers/new` 优先级、服务器未知 fixture、用户旧路径兼容等深链测试，再跑全 frontend/Go/governance/gitleaks/diff 门禁。

## verification

- 本分支无实现代码，未运行实现门禁；`git diff --check e98080d..HEAD` 应在 Handoff 提交后复核。
- 未执行路由注册、API 请求、凭据读取、迁移或生产操作。

## handoff

本 BLOCKED 分支仅作为路由冲突审计记录。解除条件满足后不得在此基础上盲目补丁：应从当时最新 release 重新起统一路由切片，并在 Handoff 更新依赖 SHA、路由测试与完整门禁证据。
