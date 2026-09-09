# XM-0030b-ui：审批队列页

- **status:** implemented，未上线。**接好但未启用**——审批端点在 platform-api
  里没有挂载，页面会自己显示「尚未启用」而不是伪造一个空队列。
- **branch:** `ai/claude/XM-0030a-approval-core`（接着 XM-0030b-api 提交），
  基线 `a49c2b8`。
- **来源：** 设计稿 §4「前端」一行；后端契约见 XM-0030b-api。

## 改了什么

- **`web/apps/admin-web/src/api/approvals.ts`（新增）** —— 五个端点的客户端。
  裸 404 翻成 `FeatureNotMountedError`（同 cards/requests 的既有做法）。
- **`src/lib/approvals.ts`（新增）** —— 分组、过期判定、票数措辞、可用动作、
  参数展示行。纯函数，与 `lib/workbench.ts` 同一条纪律。
- **`src/components/ApprovalQueue.tsx`（新增）** —— 队列（按等级分组）+ 详情
  对话框（参数、每一票、同意/驳回/执行/撤回）。
- **`src/pages/ActionsPage.tsx`** —— 「待审批」子页签换成真队列；页面级门禁
  横幅措辞更新。
- **`src/auth/session.ts`** —— 新增 `currentPrincipalId()`。

## 三个刻意的分寸

### 1. 不按权限藏按钮

**scope 在任何鉴权模式下都不下发到前端**（`currentUserRoles()` 非 local 模式
一律返回 null，且它给的是角色不是 scope）。所以按权限藏按钮只能靠猜，而猜错的
两个方向都不好：藏错了让有权限的人找不到入口，没藏住则点了必然 403。

结论是**按知道的判、不知道的不猜**：

| 事实 | 前端知道吗 | 怎么处理 |
|---|---|---|
| 单的状态（PENDING/APPROVED/是否过期） | 知道 | 按它藏按钮 |
| 归属（谁提交的、我投过没有） | 知道（`currentPrincipalId()`） | 按它藏按钮 |
| 权限（approval.decide / l4） | **不知道** | 不藏，服务端拒绝后就地显示原因 |

`abilitiesFor` 因此**不接受 scope 参数**——这不是漏了，是这一片的结论。
一条测试专门钉住它：同一张单对任何身份都给投票入口。

### 2. 过期按 `expires_at` 算，不照抄 `status`

`ExpirePending` 定时任务在 XM-0030c 之前**没有任何调度器在调**，所以库里的
status 会一直停在 PENDING，而服务端在执行那一刻才按 `expires_at` 判过期。
界面若照抄 status，队列看起来会比实际长——一屏「待审批」里混着一批其实谁也
批不动的单。

详情里把这个差别说出来：「已过期（库中仍记为待审批——过期清理任务尚未上线）」。
XM-0030c 上线后这句话会自然消失（两者不再有差别），不必回来删。

### 3. 执行不让改参数

参数在审批那一刻冻结，改参数等于换一件事、要重新提交。所以详情里的参数是
**只读展示**，执行请求体是空的 `{}`。一条测试钉住这一点——带一份参数过去会让
人误以为界面能改它。

## 顺带修的四处

1. **宪法条款引用错了三处。** 「AI 不作为第二审批人」是**第 10 条**，不是 24 也
   不是 28。我在 XM-0030a 的五处代码注释里写了 24，`blueprints/governance.ts`
   与 `blueprints/ext.ts` 两处**用户可见文案**里写了 28。全部改成 10。
2. **`PlaceholderPage.tsx` 里有一份 `/actions` 的门禁横幅是死代码。**
   `/actions` 在 navigation.ts 里 `built: true`，永远走 ActionsPage
   （`placeholderRoutes` 只收 `!item.built`）。它与 ActionsPage 里那份**逐字
   重复**，于是这一片更新措辞时只改到了活的那一份——一个不会渲染的副本除了
   制造这种漂移没有别的作用，删掉。
3. **`api/actions.ts` 与 `navigation.ts` 的注释都说 approval「只有 .gitkeep」**
   ——三片之前就不成立了。一并更新。
4. **两处 JSX 文案里写了 Markdown 强调符 `**`**，会原样渲染成星号。去掉。
   （`grep -rn '>\s*[^<]*\*\*'` 确认仓库别处没有这个先例。）

## 测试

**`src/lib/approvals.test.ts`（34 条）** —— 分组顺序与不认识的等级不丢弃、
组内按到期排序；过期判定的六种情形（含终态不谈过期、`expires_at` 解析不出
时不硬判）；票数措辞的六种（差票/缺特权票/两者都缺/特权票已投/条件满足却
未落定/非 PENDING 不谈）；可用动作的八种；参数行排序与各类值。

**`src/components/ApprovalQueue.test.tsx`（14 条）** —— 分组呈现、理由与票数、
过期显示与说明、参数与 hash、投票请求体、只有已批准给执行按钮且不带参数、
待审批不给执行（带对照组）、服务端拒绝就地显示且队列不被替换、**未启用时说明
原因**（用逐字模拟 chi 裸 404 的替身）、取满说截断 / 没取满不说、空态、
默认只查 PENDING、切换筛选重新取数。

**`src/router.test.tsx`（改 2 条）** —— 门禁措辞随实装进度更新；「待审批」那条
改成断言**队列独有的元素**（状态筛选器）。

### 一处自查纠正

`router.test.tsx` 那条我第一版写的是 `findByRole("heading", {name:"待审批"})`
——**旧的静态占位也叫「待审批」**，那条断言两边都绿，等于没测接线。变异验证
（把子页签退回静态占位）抓到了它。换成状态筛选器后变异如期变红。

### 变异验证（六项）

| 变异 | 期望 | 结果 |
|---|---|---|
| `displayStatus` 照抄 status | 三条过期用例变红 | 红 |
| `canVote` 加一条 scope 判断 | 「不按权限藏按钮」变红 | 红 |
| 不把裸 404 翻成「未启用」 | 未启用那条变红 | 红 |
| 执行按钮无条件显示 | 「待审批不给执行按钮」变红 | 红 |
| 截断提示无条件显示 | 「没取满不说截断」变红 | 红 |
| 子页签退回静态占位 | router 那条变红 | 红（第一版恒真，据此改断言） |

门禁：`pnpm -r typecheck` 退出 0、`pnpm -r test` **1967 条全绿**、
`admin-web build` 成功、后端 `go test -p 1 -count=1 ./...` 全绿、
`scripts/check-governance.sh` 退出 0、`gitleaks protect --staged` 无泄漏。

## risks

- **审批单的 params 会原样显示给有 `approval.read` 的人。** 目前 Action 参数
  不含凭据（凭据走 CredentialRef），但**日后新增 Action 时这条要复查**：一个
  把密钥当参数传的 Action 会让密钥出现在审批队列的详情里。
- 队列**没有自动轮询**。审批是人对人的等待，几秒刷新一次没有意义，而一个会
  自己动的列表在投票过程中重排更烦人。刷新按钮在页头。
- `currentPrincipalId()` 在 oidc 模式取的是 `preferred_username`。
  `oidc.ts` 的注释说它与审计里的 principal_id 一致；若日后不一致，「撤回自己的
  单」和「我投过票了」两处会失准（**只影响提示，不影响安全**——服务端仍按
  真实身份裁决）。

## follow_ups

- **XM-0030c**：`PENDING > 4h` 告警规则 + Runbook（含「审批人失联怎么办」）+
  `ExpirePending` 的 River 定时任务。
- **启用是单独一片，要先问产品负责人**（已在晨间清单）：给
  `cmd/platform-api/main.go` 注入 `approval.NewService(...)`，并把
  `approval.read` / `approval.decide` / `approval.l4` 加进 `DefaultRoleScopeMap`
  ——**没有这一步谁也拿不到这三个 scope**，队列会对所有人 403。启用必须排在
  XM-0030c 之后，否则会出现一个没人盯着的队列。
- **再之后**：cards/sms/registry 里被迫降级成 L1 的 Action 恢复正确等级。
- 队列没有分页。后端上限 100（`Service.List` 是 N+1，见 XM-0030b-api 的 risks）。
  真到了上百条要处理的是积压而不是翻页，界面已经把这句话说出来了。
