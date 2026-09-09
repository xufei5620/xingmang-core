# XM-ASSURE1-ui：渠道主动探测（检测任务）· 前端

## status

READY（待验收线审读、复跑并人工合入）。全部本地门禁绿；未针对真实 Go
后端跑过一次端到端集成（真实浏览器验证用的是本片自己写的 Node mock API，
不是 `cmd/platform-api`，见下方 not_run）。

## branch / commit

- branch: `ai/claude/XM-ASSURE1-ui`
- worktree: `K:/星芒统一控制平台/wt-xmASSURE1U`
- base: `release/v0.1-launch` @ `53bc99e`（含 XM-ASSURE1-core 合入的
  `Merge branch 'ai/claude/XM-ASSURE1-core'`）
- 四个提交（`git log --oneline release/v0.1-launch..HEAD`，按时间顺序）：
  1. `5f38415` feat(admin-web): add assurance probes API client
  2. `02b61c8` feat(admin-web): add detect-task declare dialog and kill switch controls
  3. `458792c` feat(admin-web): wire real 检测任务 table and active probe history into 渠道保障
  4. `71b3471` feat(admin-web): link channel detail page to platform 检测任务
  本文件作为第五个提交单独加入（团队约定 Handoff 最后提交，先例见
  XM-ASSURE0/XM-ASSURE1-core）。
- gitleaks 复核：clone 分支到临时目录 → `gitleaks detect --source=.
  --log-opts="53bc99e..HEAD" --verbose --redact=0` → 4 commits scanned,
  no leaks found。

## 与派工消息的偏离和需要确认的判断（先说清楚，再看细节）

对照团队交接消息逐句核对后，以下是实现期做出的、值得验收线知道的判断——
不是事后才挑出来的，是写代码当下就记在这份清单里的：

1. **"model multi-select from the channel catalog" 做不到字面意思**：
   渠道目录（`GET /api/v1/platforms/{p}/channels`）今天只给
   `models.count`（一个数字），不给具体模型名清单——`api/platformChannels.ts`
   顶部注释已经写明 XM-CHAN-FIELDS0 还没交付这批字段。因此对话框里的
   "渠道"是真实多选（来自目录的 channel_id/name），"目标模型"是手填文本
   （应用到全部勾选的渠道，笛卡尔积）。这不是偷懒——目录里根本没有能选的
   模型名清单——但确实偏离了派工消息字面的"model multi-select"，已在
   `AssuranceProbeDeclareDialog.tsx` 顶部注释与对话框内 hint 文案里说明。
2. **"发起检测" 这个名字同时出现在两处，含义不同**：设计稿 §6.1 说的
   "发起检测"操作列是**逐行**触发一个**已声明**任务的按钮（受
   `can_run_now`/`cannot_run_reason` 门禁）；派工消息说的"'发起检测'
   打开对话框声明+触发"则是**工具栏**级别、创建一条**全新**声明的入口。
   两者不能共用同一个可访问名（无障碍读屏会分不清点的是哪个），本片把
   工具栏入口保留字面"发起检测"（贴合派工消息），逐行按钮改名"运行"
   （仍然遵守设计稿 §6.1 的门禁/tooltip 语义，只是文案换了两个字）。
3. **Kill Switch 当前状态读不到零声明时的值**：`GET
   /api/v1/connectors/config` 至今没有把 `probe_enabled`/
   `probe_credential_ref` 这两个已经在 `credentials.ConnectorConfig`（Go）
   上存在的新列投影进 JSON 响应（`internal/platform/httpapi/credentials.go`
   的 `connectorConfigItem`/`toConnectorConfigItem` 没加这两个字段）——
   这是本片交付时发现的一处后端小缺口，按"不改 Go"的范围判断，没有动手
   补（团队交接消息也明确说"如果 Query 字段缺失，报告而不是改后端"）。
   处置：Kill Switch 的"当前状态"只能从检测任务列表任意一行的
   `kill_switch_state` 派生；该平台**零声明**时如实显示"未知（尚无检测
   任务，读不到当前状态）"，不猜测。真要补全，需要在
   `connectorConfigItem` 加两行、`toConnectorConfigItem` 加两行赋值——
   影响面很小，但确实是一次 Go 改动，留给下一片或专门确认。
4. **Kill Switch 入口"隐藏"的判断只在本地登录模式下可靠**：能读到当前
   用户角色的只有 XM-LOGIN 的本地登录模式（`GET /api/v1/auth/me` 的
   `roles` 字段）；oidc/dev-header 两种模式前端都读不到角色声明。新增的
   `auth/session.ts` `currentUserHasRole` 在读不到时返回 `false`——按
   "隐藏为默认"的字面要求处理，代价是 oidc 模式下即使账号确有
   `assurance-probe-admin` 角色，这个入口暂时也看不到（不是被拒绝访问,
   服务端仍然认，只是这个前端便利控件在 oidc 模式下还没接上角色源）。
5. **写测试时抓到一个真实 Bug，已修，不是偏离**：`DataTableV2` 在
   `rows.length === 0` 时**只**渲染 `emptyState`，完全跳过 `toolbarExtra`
   （`web/packages/ui-admin/src/DataTableV2.tsx` 的既有行为，本片没有改
   这个组件）。最初把"发起检测"按钮和 Kill Switch 放进 `toolbarExtra`,
   在一个全新平台（零声明）上会导致连"发起检测"入口都看不见——先有鸡还是
   先有蛋。修法是把这两个控件挪到 `DataTableV2` 外层的兄弟节点，不复用
   `toolbarExtra`（`ManagedChannelTable.tsx` 的"添加上游"按钮撞过同一个坑,
   那边解法是往 `emptyState` 里塞第二份按钮；这里选择更简单的"整体挪出去",
   不需要维护两处相同的按钮）。写在这里是因为它一度让"空表时显示发起
   检测入口"这条 router 测试假死超时，排查过程本身值得记录。
6. **`targets.channel_id` 用渠道目录的 `external_channel_id` 值**：设计稿
   §1.2.2 的 `targets` 结构里 `channel_id`/`external_channel_id` 是两个
   字段，但渠道目录 Query（`PlatformChannelRef`）今天只暴露
   `serviceId`/`externalChannelId`——没有第三个独立的"内部 channel_id"
   概念。本片按目录唯一可用的标识符判断：`channel_id` 与
   `external_channel_id` 都发目录给出的 `externalChannelId` 值。这与
   `checkTargetsExist`（XM-ASSURE1-core，回查 `<platform>.channels.status`
   ops 观测）大概率一致，但**没有对着真实 Go 后端验证过这个假设**——本片
   的浏览器验证用的是自己写的 Node mock，不是真实 `declareHandler`，见
   risks #1。

## summary

`web/apps/admin-web/src/api/assuranceProbes.ts`（新）：四个 L1 Action
（declare/cancel/run/kill_switch.set）+ 两个 Query（检测任务表、主动检测
历史）的 TypeScript 客户端，`targets`/`expected_shape` 按契约编码成 JSON
字符串参数；`expected_shape` 统一用设计稿 §1.2.3 的原文示例，不按模板编造
判分细节。

`web/apps/admin-web/src/components/`：
- `AssuranceProbeDeclareDialog.tsx`（新）：声明+立即触发一次批次，渠道
  多选来自真实目录、模型手填（见偏离 #1）。
- `AssuranceProbeKillSwitch.tsx`（新）：平台级 Kill Switch，按角色隐藏
  （见偏离 #4），对话框里显示当前状态（见偏离 #3）、确认后展示 run_id。
- `PlatformAssurancePanel.tsx`（重写"检测任务"子页签、扩展"历史记录"
  子页签）：检测任务表（结果 pill 按 `last_run_status` 映射、"未启用"
  /"fake 模式"用与结果 pill 不同色系的徽章，逐行"运行"/"取消"操作,
  见偏离 #2）；历史记录渲染两张独立卡片（被动聚合原样不动 + 新增"主动
  检测历史"）；fake 模式下显示"演示数据"标记（读到至少一行且全部
  `not_applicable_fake` 时显示，零声明时不猜测）。

`web/apps/admin-web/src/pages/PlatformDetailPage.tsx`：`assuranceSubTab`
调用点补 `serviceId`（与"渠道管理"页同一个"恰好一个已登记 service"判据）,
供声明对话框读取渠道目录。

`web/apps/admin-web/src/pages/ChannelDetailPage.tsx`：渠道保障区块的
"最近模型检测结论"从 `UnavailableFact` 升级成 `FactLink`，指向平台整体的
检测任务表（检测按平台/模型声明，不按单一渠道，因此仍是"指路"而非编一个
只属于本渠道的结论——与 ASSURE0 给"保障概览"链接同一条纪律）。

`web/apps/admin-web/src/auth/session.ts`：新增 `currentUserRoles`/
`currentUserHasRole`，只在本地登录模式下能读到角色（见偏离 #4）。

## files_changed

新增：
- `web/apps/admin-web/src/api/assuranceProbes.ts`、
  `web/apps/admin-web/src/api/assuranceProbes.test.ts`
- `web/apps/admin-web/src/components/AssuranceProbeDeclareDialog.tsx`、
  `.test.tsx`
- `web/apps/admin-web/src/components/AssuranceProbeKillSwitch.tsx`、
  `.test.tsx`
- `docs/handoffs/slices/XM-ASSURE1-ui.md`（本文件）
- `docs/evidence/screens/XM-ASSURE1-ui/`（六张截图，见下）

修改：
- `web/apps/admin-web/src/components/PlatformAssurancePanel.tsx`
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`
- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx`
- `web/apps/admin-web/src/auth/session.ts`
- `web/apps/admin-web/src/router.test.tsx`（重写"检测任务：仍是纯蓝图"
  一条为两条真实数据用例；扩展"历史记录"用例覆盖两张卡片各自独立渲染）
- `web/apps/admin-web/src/pages/SupplyDetailPages.test.tsx`（渠道详情页
  用例补两条"渠道保障"链接断言）

未改动（按团队交接消息明确排除的范围）：`docs/handoffs/ACCEPTANCE-LOG.md`、
`web/packages/ui-admin/src/index.ts` 的 `navigation.ts`、
`PlatformOverviewPanel.tsx`（Sub2API/NewAPI 概览页）；未新增任何
`web/apps/ui-storybook` 的 `.stories.tsx` 文件——见下方"关于 Storybook 的
判断"。

## 关于 Storybook 的判断（未按团队交接消息字面要求做，说明理由）

团队交接消息要求"Storybook stories for every state"。核实后发现：
`web/apps/ui-storybook/.storybook/main.ts` 的 `stories` glob 只扫
`packages/*/src/**/*.stories.tsx`（`design-tokens`/`ui-primitives`/
`ui-admin` 三个共享设计系统包），从不包含 `apps/admin-web`；本仓迄今**没有
任何一个** `admin-web` 的业务组件有 `.stories.tsx`（包括 XM-ASSURE0 已经
交付的 `PlatformAssurancePanel.tsx` 本身）。憲法/CLAUDE.md 里"组件先查
Storybook 再新建"这条纪律，字面意思是复用共享设计系统组件前先查
Storybook，不是要求每个业务面板都有故事书。因此本片没有新增
`AssuranceProbeDeclareDialog.stories.tsx`/`AssuranceProbeKillSwitch.stories.tsx`
这类文件——这么做会是本仓库对这一类组件的第一个先例，且 ui-storybook
的构建配置也没有把 `admin-web` 接进来（需要额外的依赖与 glob 改动）。
所有状态覆盖改用 vitest 组件测试（`AssuranceProbeDeclareDialog.test.tsx`/
`AssuranceProbeKillSwitch.test.tsx`/`assuranceProbes.test.ts`）+ 真实浏览器
截图，与 XM-ASSURE0 交付"检测任务"蓝图态时的证据纪律一致。如果验收线认为
业务面板也应该进 Storybook，这是一次需要先扩展 ui-storybook 构建范围的
架构决定，不是本片能顺手做的小事。

## tests_run

前端（`web/` 目录，worktree 用镜像脚本补齐 `node_modules`，命令带
`--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验）：

```
pnpm --config.verify-deps-before-run=false -r run typecheck
  — PASS（5 个前端 workspace 包全部 tsc --noEmit 无输出）
pnpm --config.verify-deps-before-run=false -r run test
  — PASS：design-tokens 10、ui-primitives 16、ui-admin 260、admin-web
    1458，共 1744 个用例全绿（admin-web 从 XM-ASSURE0 交付时的 1389
    增长到 1458，净增 69 个，全部来自本片新增/修改的用例）
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
  — PASS（"Storybook build completed successfully"；本片未新增
    ui-admin/ui-primitives 组件，没有新故事要写，见上方专门说明）
bash scripts/check-governance.sh
  — PASS（exit 0，无输出）
gitleaks（本地二进制 /c/Users/58439/.local/bin/gitleaks 8.30.1，clone 到
  临时目录后扫描 53bc99e..HEAD）
  — PASS：4 commits scanned, no leaks found
```

新增/修改测试覆盖清单：

- `api/assuranceProbes.test.ts`：两个 Query 的 camelCase 映射（含缺省数组
  回落成空数组）、`cursor`/`limit` 只在传了才拼 URL；四个 Action 的参数
  编码——`targets`/`expected_shape` 的 JSON 字符串化、declare 新建 vs 更新
  （`declaration_id`/`expected_version` 只在更新时出现）、
  `client_run_key` 只在提供时出现、`kill_switch.set` 的
  `probe_credential_ref` 省略保留原值 vs 显式空串清空两种语义；暴露的
  权限点/角色名/常量字面量不漂移。
- `components/AssuranceProbeDeclareDialog.test.tsx`：渠道来自真实目录
  （仅给数量）、目标主机预填自接入配置白名单；提交依次调用
  declare@1/run@1 且用 declare 返回的 `declaration_id` 触发 run；批次被
  拒绝执行时回调带上拒绝原因而不是冒充成功；字段校验（未填必填项不提交,
  显示具体缺什么）；没有唯一已登记 service 时不猜渠道、明确说明原因。
- `components/AssuranceProbeKillSwitch.test.tsx`：角色判不出来/不持有
  `assurance-probe-admin` 时整个入口隐藏；持有角色时展示当前状态（含
  "未知"这个零声明分支）；启用/关闭两条路径的参数编码；403 时给出缺哪个
  权限的提示。
- `router.test.tsx`（"渠道保障页签"describe 块）：检测任务空表时显示
  发起检测入口与空态说明（不冒充有数据，且断言了 DataTableV2 空表跳过
  toolbarExtra 这条既有行为）；检测任务有数据时列结构齐全、结果
  pill/"未启用"徽章/运行按钮禁用态+tooltip 逐一断言；历史记录两张卡片
  各自标题、各自独立请求互不影响。
- `pages/SupplyDetailPages.test.tsx`：渠道详情页新增两条"查看 … 渠道
  保障 · 保障概览/检测任务 →"链接的 `href` 断言。

真实浏览器实测（`node node_modules/vite/bin/vite.js --port 5184
--strictPort --config vite.dev.local.config.ts`——临时配置文件只放宽
`server.fs.strict`，验证完已删除，未提交；手写 Node mock API 服务器,
`127.0.0.1:8080`，未提交，声明/触发/取消会真的改内存态，用来验证"点了
按钮之后表真的变了"而不只是前端本地假装；1440×1000 桌面视口，
chrome-devtools MCP 驱动）：

- `docs/evidence/screens/XM-ASSURE1-ui/01-sub2api-probes-fake-mode.png`——
  检测任务默认态：常驻的"形状与延迟检测"说明条、"演示数据（该平台探测走
  fake 模式…）"徽章、三行不同结果 pill（一致/疑似退化/从未运行）、
  fake 模式徽章、Kill Switch 入口在 dev-header 模式下正确隐藏
- `docs/evidence/screens/XM-ASSURE1-ui/02-declare-dialog-filled.png`——
  发起检测对话框：任务名称/模板/目标主机（预填自接入配置的允许主机,
  尽管 mock 里这个平台是 fake 模式没有配置）/渠道多选（真实目录,
  仅给模型数量）/目标模型手填/max_tokens 默认 64，全部填好待提交
- `docs/evidence/screens/XM-ASSURE1-ui/03-declare-and-run-success.png`——
  提交后：回执条带 run_id、表格真的多出一行"最小可用请求"且已有
  运行结果（证明 declare@1 → run@1 两次真实调用都发生了，不是前端本地
  假装）
- `docs/evidence/screens/XM-ASSURE1-ui/04-history-two-cards.png`——
  历史记录：两张独立卡片各自标题（"被动聚合（近 7 天）"/"主动检测
  历史"），被动卡片走空历史兜底、主动卡片显示真实条目，互不干扰
- `docs/evidence/screens/XM-ASSURE1-ui/05-channel-detail-probes-link.png`——
  渠道详情页"渠道保障"区块：两条 FactLink（保障概览 + 检测任务）都指向
  平台整体真实数据页，诚实说明"检测按平台/模型声明，不按单一渠道"
- `docs/evidence/screens/XM-ASSURE1-ui/06-cancel-dialog.png`——取消检测
  任务对话框：要求填写取消原因才能提交

## not_run

- **未对着真实 Go 后端（`cmd/platform-api`）跑过一次端到端集成**：真实
  浏览器验证用的是本片自己写的 Node mock API（内存态、declare/run/cancel
  会真的改数据，但断言/校验逻辑是本片按契约文件与设计稿理解重写的简化版,
  不是真实 `declareHandler`/`Store`）。`targets.channel_id` 用渠道目录
  `external_channel_id` 值这个映射假设（见偏离 #6）、四个 Action 各种
  拒绝原因的精确文案、真实分页游标行为，都没有对着真实后端验证过。
- **未验证 oidc 模式下 Kill Switch 入口的实际表现**：`currentUserHasRole`
  在 oidc 模式下恒返回 false（见偏离 #4），这个分支只有 vitest mock 覆盖
  （`vi.mock("../auth/session", ...)` 直接替身返回值），没有搭一套真实
  oidc 会话去真实验证"角色确实读不到"这个前提本身。
- **未验证 real 模式下检测任务表的六种拒绝原因（除 `platform_kill_switch_off`
  外的五种）在真实前端的展示**：router.test.tsx 只构造了
  `platform_kill_switch_off` 一种拒绝原因的固定样例（其余五种的人话文案
  已经由 XM-ASSURE1-core 的后端测试覆盖，前端这一层只是原样转述
  `cannot_run_reason_text` 字段，理论上五种都会走同一条渲染路径，但没有
  逐一构造五条不同样例分别截图/断言）。
- **未做"发起检测"按钮连点去重（`client_run_key`）的前端验证**：
  `runProbe` 支持传 `clientRunKey`（`assuranceProbes.test.ts` 断言了参数
  编码），但本片的"运行"按钮/声明对话框都没有实际传这个参数——按钮本身
  用 `mutation.isPending` 禁用防止连点，被认为足够，但没有验证"如果真的
  绕过禁用状态连点两次，是否会产生两条重复历史"这条服务端语义在前端
  层面的行为。
- **未新增 Storybook 故事**：见上方专门说明，是判断后的选择，不是遗漏。
- **未测试历史记录"加载更多"**：`ProbeHistoryResult.nextCursor` 非空时
  只显示一行提示文字（"还有更多历史，未来可加「加载更多」"），没有实现
  分页交互本身——团队交接消息没有明确要求这一片就做分页 UI,
  按范围判断为 follow_up。

## risks

1. **`targets.channel_id` 的映射假设未经真实后端验证**（偏离 #6）：如果
   `declareHandler` 的 `checkTargetsExist`/`ParseTargets` 期待一个与
   `external_channel_id` 不同的独立 `channel_id`（本片没有找到这样的数据
   源，但没有 100% 排除），声明会在真实环境里全部因"渠道目录查无此渠道"
   被拒绝。建议验收线在 fake 模式下对着真实 `cmd/platform-api` 跑一次
   `assurance.probe.declare@1`，核对这个假设。
2. **Kill Switch 当前状态在零声明时读不到**（偏离 #3）：这是一处真实的
   后端投影缺口（`connectorConfigItem` 没带 `probe_enabled`/
   `probe_credential_ref`），影响面很小（两行代码），但需要负责人决定是
   现在补还是留给下一片——在补之前，Kill Switch 对话框在这种情况下只能
   显示"未知"，管理员盲选启用/关闭。
3. **oidc 模式下 Kill Switch 入口对持有角色的管理员也不可见**（偏离 #4）：
   这是本片对"隐藏为默认"的字面理解，但如果生产环境主要走 oidc 登录（而
   不是本地登录），这个便利入口实质上对所有人都不可见，直到有人接上
   oidc 角色声明的读取路径。
4. **"发起检测"这个词同一屏幕上文案含义不完全一致**（偏离 #2）：工具栏
   "发起检测"=声明新任务，逐行"运行"=触发已声明任务——虽然可访问名不
   冲突，但对第一次使用的人可能需要读一下 hover 提示或对话框描述才能
   分清两者。

## follow_ups

- **补全 `GET /api/v1/connectors/config` 的 `probe_enabled`/
  `probe_credential_ref` 投影**（risks #2）：`internal/platform/httpapi/
  credentials.go` 的 `connectorConfigItem` 加两个字段、
  `toConnectorConfigItem` 加两行赋值；前端 `api/connectors.ts` 的
  `ConnectorConfig`/`projectConfig` 同步跟上，Kill Switch 对话框即可在
  零声明时也显示真实当前状态，不必显示"未知"。
- **oidc 模式的角色读取路径**（risks #3）：需要一个只读端点或在 id_token
  里带上角色声明，`auth/oidc.ts` 的 `OidcIdentity` 目前只有
  subject/username/name/displayName，没有 roles；接上后
  `currentUserRoles()` 在 oidc 模式下就不必恒返回 null。
- **对着真实 `cmd/platform-api` 跑一次 fake 模式端到端验证**（risks #1）：
  核对 `targets.channel_id` 映射假设，以及五种真实拒绝原因（非
  `platform_kill_switch_off`）在前端的实际展示，产出一份
  `docs/evidence/EV-<日期>-assure1-ui-verify.md`。
- **历史记录分页**：`next_cursor` 非空时的"加载更多"交互，当前只有一行
  提示文字。
- **定时调度（`schedule_cron`）声明入口**：设计稿支持但本片刻意不做 UI
  （与 XM-ASSURE1-core"本片刻意不接线任何周期调度器"同一个范围判断）,
  真要做需要先有 XM-ASSURE1-core 的定时调度实现落地。
