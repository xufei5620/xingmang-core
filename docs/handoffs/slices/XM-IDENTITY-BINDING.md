# XM-IDENTITY-BINDING：人员与权限的密钥引用格接通，渠道绑定历史第一次显示出来

- **status:** implemented，未提交（按交接指示不 commit / 不 push）。
- **branch:** `ai/claude/XM-0030a-approval-core`（与另外三个 agent 共用同一个
  worktree，本片只碰下面 `files_changed` 里列出的文件）。基线 `197d0bb`。
- **范围：** 纯前端。**一行 Go 都没改**，`internal/` 与 `cmd/` 全程只读。
- **来源：** 两个已实测核实的前端缺口（下面「重新核实」一节是这次自己跑出来的
  结果，不是照抄任务书）。

## 重新核实（动手之前先确认这两件事今天确实还没做）

| 要核实的 | 怎么查的 | 结果 |
| --- | --- | --- |
| `/identity` 除 `accounts` 外全是「尚未实现」 | 读 `pages/IdentityPage.tsx` | 属实：`IdentityUnavailablePage` 覆盖其余四格，文案「『X』尚未实现 · 阶段 F-A：随后续切片实现」 |
| 凭据管理其实早就整页可用 | 读 `pages/CredentialsPage.tsx`、`pages/SettingsPage.tsx:16`、`httpapi/router.go:554-559` | 属实：`/settings?sub=credentials` 三块面板全接真实端点，`credential.manage` / `connector.manage` 无条件挂载 |
| `IdentityPage` 从不调 `blueprintForPath` | 读同一文件 | 属实：`/identity` 在 `blueprints/index.ts:29` 有蓝图，页面一次都没用过 |
| 绑定历史端点前端零调用 | `grep -rn "platform-channel-bindings" web/` | 属实：**0 命中**（唯一提到 `channel_bindings.go` 的是 `PlatformOverviewPanel.tsx:847` 的一条注释，不是调用） |

两个缺口都还在，没有被别的分支抢先做掉。

## 后端字段名逐字（自己从源码读的，不是凭记忆写的）

**请求**（`internal/platform/httpapi/channel_bindings.go` 的
`ListPlatformChannelBindingsHandler`，路由挂在 `router.go:438-439`，
scope = `finance.read`）：

```
GET /api/v1/finance/platform-channel-bindings
  service_id       必填，uuid（parse 失败 → INVALID_PARAMS「service_id 无效」）
  limit            1–200，默认 50（parseBindingLimit）
  include_history  true / false，默认 false（parseBindingBool）
  cursor           base64url 无填充，服务端 encode/decodeBindingCursor
```

**响应**（`platformChannelBindingPageResponse`）：

```
service   {id, service_type, instance_id, environment}
inventory {state, source, observed_at, complete, truncated,
           reported_count, fetched_count, coverage_partial, evidence}
items[]   {channel_ref:{service_id, external_channel_id},
           channel_name,
           binding: channelBindingResponse | null,
           candidate:{state, evidence_status, upstream_account_ids,
                      reason_codes, platform_assignment_missing, inventory_unknown},
           history: channelBindingResponse[]   // json:"history,omitempty"
          }
next_cursor  string | null
```

`channelBindingResponse`（`binding` 与 `history[]` 同一个结构，约 58 行）：
`{id, upstream_account_id, valid_from, provenance, reason, created_by}`。

三处**容易踩空**的地方，都是这次读源码才发现的：

1. **渠道名的键不一样**。渠道目录 `/platforms/{p}/channels` 里叫 `name`，
   这条端点里叫 `channel_name`。照抄另一个模块的解析会解出 `undefined`。
2. **`history[]` 里没有 `valid_to`**。库表
   `finance.platform_channel_binding` 有这一列，`channelBindingResponse` 没有
   把它序列化出来。所以历史只有「从什么时候开始」。
3. **解绑不产生历史行**。`ChannelBindingStore.Remove`
   （`finance/channel_binding_store.go:234`）只调 `ClosePlatformChannelBinding`
   写 `valid_to`，不插新行；`Close` 那条 SQL 也只改 `valid_to`，所以**解绑时
   写的理由根本不在这张表里**（它在审计链上）。

**排序**：`db/queries/finance.sql` 的 `ListPlatformChannelBindingHistory` 是
`ORDER BY valid_from DESC, id DESC`——最新的在前。前端原样保留，不重排。

## 改了什么

### 缺口 1：人员与权限 → 密钥引用

**`pages/IdentityPage.tsx`** —— 五格的渲染从「一格真数据 + 四格同一句占位」
改成逐格分派：

- **密钥引用**：一张交叉跳转卡（`CredentialsCrossLinkCard`），说明凭据管理在
  `/settings?sub=credentials`、已经能做什么、**那一页覆盖了蓝图这张表的哪三列**
  （密钥引用 / 用途 / 状态）、**这一格自己还差哪五列**（供应方 / 消费者 / 环境 /
  最近使用 / 轮换·到期）。下面照旧渲染蓝图列头。
- **权限规则 / 权限范围 / 会话**：`BlueprintTabView` 渲染冻结的列头，
  落款换成如实归因（下一节）。

**没做 IA 搬迁**：`docs/architecture/ADMIN-IA.md` 与
`ui-admin/src/navigation.ts` 一个字没动。把凭据页在两个地址各渲染一份比放
一个链接更糟——两处的面包屑、返回路径与保存后的落点会立刻分叉。

### 缺口 2：渠道绑定历史

**`api/platformChannelBindings.ts`（新增）** —— 这条端点的第一个前端调用方。
`getChannelBindingHistory({serviceId, externalChannelId})` 带
`include_history=true&limit=200` 翻页，从 `items` 里按
`channel_ref.external_channel_id` 挑出目标那一条。三种结局分开命名：
`ok` / `channel_absent`（翻完了没有这条渠道）/ `truncated`（翻到第 5 页还有
下一页）——「这条渠道没绑过」与「我们没查到」在屏幕上长得一样，但下一步完全
不同。

**`components/ChannelBindingHistory.tsx`（新增）** —— 「上游映射」卡片里的
绑定历史区块。每行是一次绑定的建立：上游账号、生效时间（UTC）、来源
（`manual` → 人工确认，`token_map_backfill` → 令牌映射回填，**认不出来的值
原样显示**）、操作人、理由；与 `binding.id` 相同的那行标「当前生效」。

**`components/ChannelBindingCard.tsx`** —— 挂上上面那个区块（放在动作按钮
下面，不另起一张卡），并在确认/改绑/解绑成功后失效历史查询。

## 四个刻意的决定

### 1. 不自己拼 cursor 去「精确取一条」

服务端的 cursor 就是 `external_channel_id` 的 base64url，候选按 id 升序排
（`EvaluateBindingCandidates` 里 `sort.Strings(ids)`）、过滤条件是 `> cursor`。
所以**理论上**可以拼一个「目标前一条」的 cursor + `limit=1`，让服务端只查一次
历史，而不是为整页每条渠道各查一次。

没有这么做：cursor 是不透明凭据，服务端换个编码前端会静默失灵，而失灵的样子
是「这条渠道没有历史」——一个看起来完全正常的错误答案。这里只用服务端自己给的
`next_cursor`。

代价（服务端为该页每条渠道各查一次历史）在实践中恒等于一页：渠道详情页自己
那条 `listPlatformChannels` 不带 limit、吃服务端默认的 50，也就是说详情页
**只可能**打开排序前 50 的渠道，必然落在 `limit=200` 的第一页里。
真嫌贵的话该加的是**一条按 ChannelRef 取历史的端点**（见 follow_ups），
不是让前端去猜 cursor 的编码。

### 2. 屏幕上必须写出这份数据的两个盲区

一份「最后一条是绑到 X」的列表会被读成「现在绑在 X 上」，而实际可能早就解绑
了。所以说明段逐字写了：没有失效时间、不含解绑、**不要拿下一行的生效时间当
上一行的结束时间**（解绑之后可以长期没有任何绑定，那段空档在这份数据里完全
不可见）。当前生效的以标记那一行为准。

这不是装饰：`history` 里没有 `valid_to`，不写这句话，这个区块就是在暗示一件
它证明不了的事。

### 3. 这一页**不**渲染蓝图的页顶横幅与页级统计格

与 `ChangesPage` 不同，这是一处刻意的偏离：

- 蓝图横幅原话是「仅 UI 设计 · 本页不执行任何真实操作」。这句今天在这一页上
  **是假的**——账号与身份能建账号、改角色、重置密码；
- 四张统计格恒为「—」，摆在那张真实账号表上面，会让整页看起来像还没建。

`ChangesPage` 渲染这两样，是因为它整页确实没有任何写入口。两页情况不同。
有一条测试钉着这个决定（并做过变异验证）。

### 4. 落款就地覆写，不改冻结的蓝图规格文件

`blueprints/governance.ts` 里人员与权限的五句 `source` 都写着「随 F-A 上线」，
而 F-A 里已经交付了两格（XM-LOGIN 的账号与身份、XM-CRED0 的凭据管理）。
一句错的归因比没有归因更糟：它让人以为剩下三格也只是排期问题，而三格缺的
东西完全不同——

| 格 | 蓝图原话 | 今天的事实 |
| --- | --- | --- |
| 权限规则 | 「随 F-A 上线」 | 平台没有「授权策略」这个**可编辑对象**：没有规则表、没有端点、没有 `policy.*`/`rule.*` Action。授权由部署期的两样东西决定——`router.go` 里写死的 `RequireScope`，与 `oidcauth/rolemap.go` 的 `DefaultRoleScopeMap`（可由 `XM_OIDC_ROLE_SCOPES` 覆盖）。等的不是排期 |
| 权限范围 | 「取自 RoleScopeMap；随 F-A 上线」 | 数据在（`DefaultRoleScopeMap`，local 模式也走它，`localauth.RoleScopesFrom`），缺的是一条把它读出来的只读 Query |
| 密钥引用 | 「凭据登记随 F-A 上线」 | **已经能用了**，在 `/settings?sub=credentials` |
| 会话 | 「随 F-A 上线」 | 库表在（`core.staff_session`，迁移 000021；000024 加了 `mfa_at`），端点没有：`LocalAuthHandlers` 上只有登录/登出/查自己/改密码/列账号/TOTP，路由里搜不到列会话的端点。oidc 模式的会话在 Keycloak，平台不落库 |

蓝图数据文件同时被 `blueprints.test.ts` 与占位页消费，不归这一片改——做法照抄
`ChangesPage.tsx` 的 `TAB_SOURCE`，并在 follow_ups 里请求订正 `governance.ts`。

**顺带核实的一件事**：蓝图给权限规则那条恒定 DENY（「AI 不作为 L3/L4 第二审批
人」）今天**确实生效**——`internal/platform/approval/approval.go:188` 对非 HUMAN
主体直接拒（`ErrApproverNotHuman`）。所以这条 note 照原样渲染，只在落款里补一句
它的生效点在审批内核、不在任何规则表里，免得有人去规则页找它。

## 测试

**`api/platformChannelBindings.test.ts`（8 条）** —— 请求参数逐字
（`service_id` / `include_history=true` / `limit=200`，第一页不带 cursor）；
signal 透传；按 `channel_ref.external_channel_id` 挑出目标那条且 history 顺序
原样；`binding` 为 null 时 `currentBindingId` 为 null；`history` 键缺失
（omitempty）按空数组；翻页用服务端给的 `next_cursor` 原样回传；翻完没找到 =
`channel_absent`；服务端一直给 cursor 时第 5 页停下。

**`components/ChannelBindingHistory.test.tsx`（6 条）** —— 喂**两条真实历史**，
断言两行都渲染、**最新的在前**（逐行 `textContent`，不是「页面上有这两个串」）、
每行四个字段都落屏、当前生效只标在一行上、请求 URL 确实带
`include_history=true`；解绑形态（`binding` 为 null）下没有任何行带标记；
从没绑过说「没有绑定记录」（`empty`）而不是「未接入」；候选清单里没有这条渠道
时指名道姓说是哪条；说明段逐字含两个盲区；陌生 `provenance` 原样显示。

**`pages/IdentityPage.test.tsx`（10 条）** —— 密钥引用格给出真实入口
（`href` 逐字 `/settings?sub=credentials`）且不再有「尚未实现」；说清覆盖哪几列
/ 差哪几列；四格的蓝图列头**逐字逐序**；三格落款换成如实归因且屏幕上不再出现
「随 F-A 上线」；恒定 DENY 照常显示并说明生效点；不渲染横幅与统计格；未知
`?sub=` 仍不回落；五格页签齐全。

### 变异验证（缺席型断言逐条删实现跑过）

| 变异 | 期望 | 实际 |
| --- | --- | --- |
| 历史列表 `[...history].reverse()` | 顺序断言变红 | 红（1 条） |
| `isCurrent={true}`（人人都是当前生效） | 「解绑之后没有标记」变红 | 红（2 条） |
| 非 `ok` 状态改成走空状态分支 | 「候选清单里没有这条渠道」变红 | 红（1 条） |
| API 模块删掉 `include_history: "true"` | 参数与渲染断言一起变红 | 红（4 条） |
| `honestBlueprintTab` 直接返回原蓝图（不覆写落款） | 三格归因断言变红 | 红（5 条） |
| 补上 `BlueprintBanner`（再单独补 `BlueprintTiles`） | 「不渲染横幅/统计格」变红 | 两次都红 |

全部变异已回退，回退后重跑 42 条相关用例全绿。

## 门禁

```
pnpm --config.verify-deps-before-run=false -r run typecheck   → 5/5 Done
pnpm --config.verify-deps-before-run=false -r run test        → 158 文件 / 2173 条全绿
                                                                 （admin-web 133 文件 1886 条）
pnpm --config.verify-deps-before-run=false -r run build       → 5/5 Done
```

**`go test` 没跑**（也不该跑）：后端一行没动，且同一个 worktree 里有别的 agent
在用同一个测试库。

## files_changed

新增：

- `web/apps/admin-web/src/api/platformChannelBindings.ts`
- `web/apps/admin-web/src/api/platformChannelBindings.test.ts`
- `web/apps/admin-web/src/components/ChannelBindingHistory.tsx`
- `web/apps/admin-web/src/components/ChannelBindingHistory.test.tsx`
- `web/apps/admin-web/src/pages/IdentityPage.test.tsx`
- `docs/handoffs/slices/XM-IDENTITY-BINDING.md`（本文件）

修改：

- `web/apps/admin-web/src/pages/IdentityPage.tsx`
- `web/apps/admin-web/src/components/ChannelBindingCard.tsx`

明确**没有**碰：`internal/**`、`cmd/**`、`docs/architecture/ADMIN-IA.md`、
`ui-admin/src/navigation.ts`、`src/router.tsx`、`src/router.test.tsx`、
`blueprints/governance.ts`、`api/platformChannels.ts`、`pages/ChannelDetailPage.tsx`，
以及交接里点名的其余页面与组件。

## risks

- **绑定历史每次要拉一整页候选。** 服务端在 `include_history=true` 时逐条查
  历史，所以打开一次渠道详情页会让它为该页每条渠道各查一次。当前规模下可以
  接受（详情页本来就只能打开排序前 50 的渠道），但这是这一块最贵的地方。
- **`truncated` 分支实测跑不到。** 它防的是「服务端一直回同一个 next_cursor」，
  只有单测覆盖，没有真实环境验证过。
- **「当前生效」靠 id 相等判定。** 历史里没有 `valid_to`，只能拿同一次响应的
  `binding.id` 去比。如果将来 `binding` 与 `history` 的取数时刻分开了，这个标记
  会失真。
- **密钥引用格是交叉跳转，不是搬迁。** 有人可能仍然期待在这一格直接操作凭据；
  卡片末尾已经把这件事写在屏幕上。

## follow_ups

1. **订正 `blueprints/governance.ts` 里人员与权限的五句 `source`**（连
   `accounts` 那句「随 F-A 的身份工作台上线」一起——那格已经交付，只是它不走
   蓝图渲染，所以那句话今天不会出现在屏幕上）。`ChangesPage` 也留了同一条请求，
   两处可以一起做。
2. **考虑加一条按 ChannelRef 取绑定历史的只读端点**
   （`GET /api/v1/finance/platform-channel-bindings/{service_id}/{external_channel_id}/history`
   或等价形状）。有了它，前端就不必为看一条渠道的历史去拉一整页候选。
3. **`history[]` 补 `valid_to`**。库表有这一列，序列化出来之后这个区块就能显示
   每一次绑定的**起止**，「解绑之后的空档期」也不再是盲区。
4. **权限范围 / 会话各缺一条只读 Query**（外加定权限归属）。缺什么、在哪，已经
   逐格写在页面落款里。
5. **凭据从设置迁到人员与权限**（ADMIN-IA 搬迁）如果要做，是独立一片：要改导航
   真相源与 `ADMIN-IA.md`，本片一律没碰。
