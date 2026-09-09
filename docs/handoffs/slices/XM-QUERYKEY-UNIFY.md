# XM-QUERYKEY-UNIFY：上游汇总 queryKey 统一 + 跨组件回归线

- **status:** implemented，**未提交、未推送**（按派工要求改完即停）。
- **branch:** `ai/claude/XM-0030a-approval-core`，基线 `8c446e5`。
- **范围：** 纯前端。`internal/`、`cmd/`、`db/` **一行未动**（连读都没读）。
- **来源：** `docs/handoffs/slices/XM-UPSTREAM-DETAIL-COPY.md` 第八节 follow_up 1。
- **修的是一个用户看得见的 bug**，不是整洁性重构：同一份数据两种缓存键，
  react-query 前缀匹配作废，两者互不为前缀，**谁都作废不了谁**。

---

## 〇、先说复核结果（派工要求逐处自己重新定位）

派工那张表**逐处复核无误**——十个行号我重新定位后与派工给的**逐字一致**
（工作树里别的 agent 改过这几个文件，但改动都没落在这些行之前）。派工要求确认的
三件事，逐条如下。

### 0.1 两种 key 的逐字值

自己 `sed -n` 打出来的，不照抄派工：

```
web/apps/admin-web/src/api/finance.ts:593:
export const UPSTREAM_SUMMARY_QUERY = "finance-upstream-summary";
```

- **A 侧**：`[UPSTREAM_SUMMARY_QUERY]`，展开后是 `["finance-upstream-summary"]`
  ——**单元素数组，元素是一个带连字符的整串**。
- **B 侧**：`["finance", "upstreams", "summary"]`——**三元素数组**。

两者互不为前缀（第一个元素就不相等），所以 `invalidateQueries` 打在任一侧,
另一侧的缓存条目连候选集都进不去。

### 0.2 完整读者 / 作废者清单（改动前，行号为我实测）

grep 口径：`finance-upstream-summary`、`UPSTREAM_SUMMARY_QUERY`、`"upstreams"`,
外加一遍 `grep -rn "queryKey" | grep -i upstream` 兜底交叉验证。

| 角色 | A 侧（常量） | B 侧（字面量） |
|---|---|---|
| 读 | `pages/UpstreamDetailPage.tsx:110` | `components/ChannelTable.tsx:77`、`components/FinanceSummaryCards.tsx:149`、`components/ManagedChannelTable.tsx:93`、`pages/ChannelDetailPage.tsx:121`、`pages/FinancePage.tsx:460` |
| 写完作废 | `components/UpstreamAccountDetail.tsx:188`、`pages/UpstreamDetailPage.tsx:129` | `components/ChannelTable.tsx:98`、`components/ManagedChannelTable.tsx:113` |

**合计 6 处读 + 4 处作废 = 10 处**，与派工列出的位置逐处一致。
其中 **A 侧 3 处（本来就是常量，未改）+ B 侧 7 处（字面量，本片改掉）**。

**六处读的 `queryFn` 逐字相同**：全都是 `({ signal }) => listUpstreamSummaries({ signal })`,
没有任何一处带窗口参数。这一点必须先确认再动手——如果某一处传了 `from/to`
而 key 里不体现，统一 key 反而会让两个不同口径的响应互相覆盖。确认结果是
不存在这种情况，所以统一到单元素 key 是安全的。

### 0.3 有没有第三种写法：**没有**

- `grep -rniE "upstream[-_ ]?summary|upstreamsummary"` 命中的其余全是**类型名**
  （`UpstreamSummary` / `UpstreamSummaryPage`）与**测试夹具函数名**
  （`upstreamSummary()`），没有一处是 queryKey。
- 全仓（含 `docs/`、非 `web/` 目录）再 grep 一遍两种字面量，改动后只剩
  **说明性文字**：上一份 handoff 的 follow_up 表、以及本片新写的注释。
- 唯一另一个含 `upstream` 的 queryKey 是 `components/SMSExtrasPanels.tsx:757`
  的 `["sms-upstream-codes", resource.resource_id]`——接码中心的上游短信码,
  与财务上游汇总无关，不在范围内。

### 0.4 对照组校准（派工给的两条「不该被报成分叉」的线）

两条都**没有**被我的口径报成分叉，说明口径是对的：

- `UPSTREAM_ACCOUNTS_QUERY = "finance-upstream-accounts"`，散写处
  （`ChannelTable.tsx:97`、`ManagedChannelTable.tsx:89/112`、
  `ChannelDetailPage.tsx:117`）逐字是 `["finance-upstream-accounts"]`,
  **与常量同值**，互相作废得到 → 不是 bug，**本片不动它**（见第五节 follow_up）。
- `["finance", "channels", "summary"]` 全仓只有一种写法。
  `ChannelProfitView.tsx:354` 与 `Sub2ApiFinanceOverview.tsx:210` 的
  `["finance","channels","summary", range.from, range.to]` 是**它的前缀扩展**,
  作废基础 key 时会一并命中，不是第三种写法。

---

## 一、统一后的状态

统一到 **A 侧常量 `UPSTREAM_SUMMARY_QUERY`**（派工指定；理由是单一定义点、
与仓库另外三条 key 同一套命名）。**七处字面量一次改完**（十处里另外三处本来
就是常量）——派工特别强调不能零敲碎打,
这点在 `FinancePage` 上尤其真：它与同屏渲染的 `FinanceSummaryCards` 共用缓存
（`FinancePage.tsx:432` 的注释写明了这条意图），单改一处会让同屏发两次请求,
并把分叉从两侧变成三处。

改动后全仓 `queryKey` 里再无该数据的字面量数组：

```
components/ChannelTable.tsx:78          queryKey: [UPSTREAM_SUMMARY_QUERY],
components/ChannelTable.tsx:99          invalidateQueries({ queryKey: [UPSTREAM_SUMMARY_QUERY] })
components/FinanceSummaryCards.tsx:150  queryKey: [UPSTREAM_SUMMARY_QUERY],
components/ManagedChannelTable.tsx:94   queryKey: [UPSTREAM_SUMMARY_QUERY],
components/ManagedChannelTable.tsx:114  invalidateQueries({ queryKey: [UPSTREAM_SUMMARY_QUERY] })
components/UpstreamAccountDetail.tsx:188  [UPSTREAM_SUMMARY_QUERY],        （原就是常量）
pages/ChannelDetailPage.tsx:121         queryKey: [UPSTREAM_SUMMARY_QUERY],
pages/FinancePage.tsx:461               queryKey: [UPSTREAM_SUMMARY_QUERY],
pages/UpstreamDetailPage.tsx:110/129    [UPSTREAM_SUMMARY_QUERY],          （原就是常量）
```

五个文件补了 `UPSTREAM_SUMMARY_QUERY` 的 import（按各文件既有的
「值按大小写不敏感字母序、`type` 放最后」的顺序插入，不打乱既有排序）。

---

## 二、跨组件回归测试（本片最重要的交付物）

**新文件：`web/apps/admin-web/src/upstreamSummaryCache.test.tsx`**（2 条用例）。

放在 `src/` 根而不是某个组件目录下，是因为它**不属于任何一个组件**——它断的是
组件之间的关系。同 `src/router.test.tsx` 的位置惯例。

### 2.1 用例一：写者 ↔ 读者（派工点名要的那条）

同一个 `QueryClient` 下同时挂：

- **写者** `components/UpstreamAccountDetail`（上游详情页里发起登记/退款/终止的那一块）;
- **读者** `components/FinanceSummaryCards`（平台概览与跨平台财务页上的资金卡）。

流程：卡上先显示「12 天」→ 通过对话框真实提交一笔订阅批次（走到
`/actions/finance.subscription_batch.register` 的 fetch）→ 桩在写之后改为下发
「4 天」→ **断言卡上变成「4 天」，且「12 天」已经不在**。

判据刻意选**读者屏幕上的数值**，不是「写者调了 `invalidateQueries`」：
后者正是现存组件级用例的写法，而它在分叉状态下**两侧都过**。

### 2.2 用例二：六处读者在缓存里只留一份

四个组件同屏（两个各自的 `MemoryRouter`，共用一个 `QueryClient`）：
`UpstreamDetailPage`、`ChannelDetailPage`、`ChannelTable`（`serviceId` + active
⇒ 内部再挂 `ManagedChannelTable`）、`FinanceSummaryCards`。断言缓存里
**只有一份**上游汇总。

两处做法上的取舍，都写进了文件里的注释：

1. **判据不取「发了几次请求」。** 第一版就是这么写的，**它是错的**：`staleTime`
   默认 0，`ChannelDetailPage` 要等 `/services` 回来才挂上自己的查询，那时缓存
   已经 stale，于是它**本来就会**再取一次数。请求次数反映的是挂载时序,
   统一与分叉两种状态下都会数到 2，**这个判据分辨不了要证的事**。改成数缓存
   条目数之后没有这个噪声。（这一段是实测发现的，不是预判：第一版跑出来
   `expected 2 to be 1`，查 fetch 时序才定位到原因。）
2. **认条目不靠 key。** 拿 key 去 `getQueriesData` 找，等于拿 key 校验 key,
   恒真。改成按**这个端点独有的响应形状**认：`runwayThresholds` 只有
   `/finance/upstreams/summary` 的响应带（渠道汇总只有 `items/from/to`,
   渠道目录带的是 `runwayCoverage`）。

### 2.3 变异验证（四次变异 + 两组对照，逐条实跑，每次只动一处、跑完即还原）

变异都是**改条件**（把某一处 key 换成另一种写法），不是删代码。
记录「红在哪一行」——红在正向锚点上的不算数。（行号按定稿后的文件；
跑变异时文件还在迭代，行号有过位移，对应的断言是同一条。）

| # | 变异 | 用例一 | 用例二 |
|---|---|---|---|
| 1 | `FinanceSummaryCards` 读 key → 字面量 | **红**在目标断言（`:369`，等不到「4 天」） | **红**在目标断言（`:427`，缓存 2 份） |
| 2 | `UpstreamAccountDetail` 作废 key → 字面量 | **红**在目标断言（`:369`） | 绿（不含写路径，符合预期） |
| 3 | `UpstreamDetailPage` 读 key → 字面量 | 绿（不含该页） | **红**在目标断言（`:427`，缓存 2 份） |
| 4 | 对照组甲：只改常量本身的值（十处一起跟着变，仍彼此一致） | 绿 | 绿 |
| 5 | 对照组乙：改一条**无关**的 key（渠道汇总） | 绿 | 绿 |

- 1–3 全部**红在目标断言上**，正向锚点仍绿——变异证明了该证的那一步。
- 4 说明用例断的是「统一」，不是「恰好等于某个字面量」。
- 5 说明用例不是「任何 key 一动就红」的哨兵。

**过程中修掉过一次不合格的变异结果**：第 3 条第一次跑，红在正向锚点
`findByText("OpenAI A")` 上——「找到多个」（渠道名同时是渠道详情页的一个字段和
上游详情页渠道清单里的一个链接），目标断言压根没跑到，**这次变异什么都没证明**。
锚点因此一律改成 `findAllBy*` + `length > 0`，再跑才落到目标断言上。

### 2.4 正向断言的「旧实现下会不会照样绿」自查

- 用例一的前两条（卡上先是「12 天」、写成功回执 `run-batch-1`）**旧实现下照样绿**
  ——它们是锚点，作用是保证目标断言真的跑得到；分叉影响的是「写完之后」。
- 用例一的目标断言（变成「4 天」、「12 天」消失）**旧实现下必红**（变异 1、2 实测）。
- 用例二的锚点（三棵树各渲染出真实数据 + 端点至少被读到一次）旧实现下照样绿;
  目标断言（缓存只留一份）旧实现下必红（变异 1、3 实测）。
- 用例二里 `expect(api.summaryCalls).toBeGreaterThan(0)` 是**防恒真**用的：
  少了它，所有读者都挂在错误态空转时「缓存里只有一份（其实是零份）」会假绿。

---

## 三、更新后的注释

### 3.1 `api/finance.ts:591–622`（派工点名的那段）

原注释在 580–587 行**逐字预言了这个 bug**（「两处各写一遍字符串，改动一处就会
变成『写成功了但表没刷新』，而这种 bug 只在真机上看得见」），而紧跟其下的
`UPSTREAM_SUMMARY_QUERY` 正是被绕过去的那条。

**保留原文不动**（它说的是对的，且服务于另外三条 key），在
`UPSTREAM_SUMMARY_QUERY` 自己的注释里追加两节，把「预言」升级成「有护栏的说明」：

- **「上面那段话说的事，在这一条上真的发生过（2026-09-08 修复）」**——两种写法
  逐字是什么、前缀匹配为什么让它们互不作废、双向后果是哪四个页面，以及它躲过
  所有用例的三条性质（看起来完全正常 / 同屏内不自相矛盾 / 组件级用例两侧都过）。
- **「现在由哪条测试拦着」**——点名 `src/upstreamSummaryCache.test.tsx` 的两条用例
  各断什么、为什么按响应形状认而不按 key 认，并写下一句给下一个人的规矩：
  新增读或作废**用这个常量**；真要新起一个 key，就把它加进那个文件的同屏用例里,
  别让它成为第三种写法。开头一句直说 **「注释拦不住这件事」**——上面那段话
  逐字预言了它，它照样发生了。

### 3.2 `pages/FinancePage.tsx:461–464`（顺带补的一处）

派工点名 `FinancePage` 是「单独改一处会让情况更糟」的例子，那一处原本一句注释都
没有（有注释的是它上面那条渠道汇总的 key）。补了三行：说明它与同屏
`FinanceSummaryCards` 共用一份缓存、key 走常量的原因、护栏在哪个文件。

其余两处既有注释（`UpstreamDetailPage.tsx:102`「不另写字面量数组」、
`ManagedChannelTable.tsx:86`「共用同一个 query key」）统一后**依然逐字成立**,
未改。

---

## 四、门禁

三条全绿，均在 `K:/星芒统一控制平台/wt-XM-0030a` 仓库根执行：

| 门禁 | 结果 |
|---|---|
| `pnpm --config.verify-deps-before-run=false -r run typecheck` | **通过**，5 个包全 Done（`admin-web typecheck: Done`） |
| `pnpm --config.verify-deps-before-run=false -r run test` | **通过**，`admin-web` 137 文件 / **1991 用例全过**（含本片新增 2 条）；ui-admin 261、ui-primitives 16、design-tokens 10 |
| `pnpm --config.verify-deps-before-run=false -r run build` | **通过** |

**build 产物行怎么确认的**（派工要求 grep 确认）：整段输出 `tee` 到日志后
`grep -n "modules transformed"`，日志里有两行，按 pnpm 的包名前缀区分：

```
16:web/apps/ui-storybook build: ✓ 163 modules transformed.
147:web/apps/admin-web build: ✓ 365 modules transformed.
```

第 147 行前缀是 `web/apps/admin-web build:`，即 **admin-web 自己的产物行**
（不是 storybook 的）。再 `grep -niE "ELIFECYCLE|Failed|error TS"` 全日志无命中,
`web/apps/admin-web/dist/` 下 `index.html` 与 `assets/index-BXYS-3_O.css`
的时间戳与本次运行一致（09:19），确认是本次真写出来的，不是旧产物。

### 4.1 一次与本片无关的偶发红（已定位，不是我造成的）

`pnpm -r run test` 之前，我先在 `admin-web` 单独跑过一次全量，出现 **1 条红**：
`src/router.test.tsx:1611`（`getByLabelText("环境")` 找到多个元素，
「登记服务（写路径）」那组）。判定依据三条：

1. **单独跑该用例：绿。** 单独跑整个 `router.test.tsx`：**163/163 全绿**。
2. **重跑全量：1991/1991 全绿**，`pnpm -r run test` 门禁也全绿——不可复现,
   是并行负载下的时序抖动。
3. **import 路径不通到我改的东西。** 该用例走的是 `/registry` →
   `pages/RegistryPage.tsx` → `RegisterServiceDialog`；`RegistryPage.tsx` 的
   import 清单里没有 `ChannelTable` / `FinanceSummaryCards` /
   `ManagedChannelTable` / `ChannelDetailPage` / `FinancePage` 中的任何一个,
   也不读上游汇总那条 key。而 `pages/RegistryPage.tsx` 正在被另一个 agent 改
   （工作树里已有未跟踪的 `pages/RegistryPage.test.tsx`），**按派工约束我没有碰它**。

未跑 `go test`（派工明确要求不跑），后端本片一行未动。

---

## 五、files_changed

**改动（7 个文件）**

| 文件 | 改了什么 |
|---|---|
| `web/apps/admin-web/src/api/finance.ts` | 只加注释（`UPSTREAM_SUMMARY_QUERY` 的说明扩写），**常量值一个字没动** |
| `web/apps/admin-web/src/components/ChannelTable.tsx` | 读 + 作废共 2 处字面量 → 常量；补 import |
| `web/apps/admin-web/src/components/FinanceSummaryCards.tsx` | 读 1 处 → 常量；补 import |
| `web/apps/admin-web/src/components/ManagedChannelTable.tsx` | 读 + 作废共 2 处 → 常量；补 import |
| `web/apps/admin-web/src/pages/ChannelDetailPage.tsx` | 读 1 处 → 常量；补 import |
| `web/apps/admin-web/src/pages/FinancePage.tsx` | 读 1 处 → 常量；补 import；补 3 行注释 |
| `web/apps/admin-web/src/pages/UpstreamDetailPage.tsx` | **未改**（原就是常量），列在这里只为说明它在清单里被核对过 |

**新增（1 个文件）**

- `web/apps/admin-web/src/upstreamSummaryCache.test.tsx`

**未碰（派工点名的禁区，逐条确认）**：任何 `internal/` / `cmd/` 下的 Go 文件、
`K:/发票/` 与其他 `wt-*` 工作树、`pages/RegistryPage.tsx`、
`pages/PlaceholderPage.tsx`、`blueprints/governance.ts`。未 `git commit` / `push`。

---

## 六、risks

1. **统一后同屏请求数会变**（这是修复的目的，但值得让验收知道）：上游详情页
   与渠道管理页现在与其他读者共用一份缓存，同屏首次挂载时少发请求；反过来,
   任一处写操作现在会让**所有**挂着的读者重新取数。六处读者的 `queryFn` 逐字
   相同（第 0.2 节已核），不存在「取到别人口径的数」的风险。
2. **用例二依赖 `runwayThresholds` 是该端点独有的字段**。若将来渠道汇总端点也
   开始下发同名字段，这条用例的「认条目」会误判（多认出一份）。届时症状是
   这条用例**变红**（不是变绿），属于安全方向的失效，且注释里写明了判据来源。
3. **本片不覆盖 `ChannelTable` / `ManagedChannelTable` 两处作废 key 的行为**。
   两条用例里的写路径只有 `UpstreamAccountDetail` 那一条。这两处的写入口是
   `UpstreamAccountDialog`，而它的必填项走 Radix `Select`，在 jsdom 里驱动一次
   完整提交成本与脆性都明显偏高。**这两处的读 key 已被用例二覆盖**（它们在同屏
   四个组件之内），未覆盖的只有「它们作废的 key 是否还与常量一致」。
   见 follow_up 2。

---

## 七、follow_ups

1. **`UPSTREAM_ACCOUNTS_QUERY` 的四处散写字面量，今天是对的，但是同一类隐患。**
   `ChannelTable.tsx:97`、`ManagedChannelTable.tsx:89/112`、
   `ChannelDetailPage.tsx:117` 写的是 `["finance-upstream-accounts"]`,
   **与常量逐字同值**，所以互相作废得到，不是 bug——派工也明确把它列为对照组。
   但它与本片修的东西只差一次手滑：任何人把其中一处改成
   `["finance","upstream-accounts"]` 就复现同样的症状。
   **本片按派工范围没有动它**（它不在 bug 范围内，且这四个文件正被别的 agent 改,
   多余改动会增加冲突面）。建议后续单独一小步收掉。
2. **补 `ChannelTable` / `ManagedChannelTable` 写路径的那一半覆盖。**
   做法建议：`vi.mock` 掉 `UpstreamAccountDialog`，换成一个直接调用 `onDone` 的
   按钮——被测的是父组件 `onDone` 里那段真实的作废代码，对话框本身有自己的用例
   （`components/UpstreamAccountDialog.test.tsx`），不是把被测对象换成假的。
   这样就能把这两处写者也接进 `upstreamSummaryCache.test.tsx` 的同屏断言里。
3. **`web/apps/admin-web/dist/assets/` 里堆了 7 份历史 `index-*.js`**（最早
   9-7 18:29）。与本片无关，只是跑 build 时看见了，提一句免得被当成产物异常。
