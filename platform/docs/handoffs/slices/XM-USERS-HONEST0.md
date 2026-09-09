# XM-USERS-HONEST0：用户管理页三处"诚实透传"（注册时间、服务端筛选、样本措辞）

status: implemented（本地门禁见下）
branch: ai/claude/XM-USERS-HONEST0（自 release/v0.1-launch@8ca2910）
依据：2026-09-06 Sub2API 缺口普查（用户管理区，只读调研 + 对抗核实）里三条
"零审批、零依赖、纯前端"的缺口；产品负责人同日要求"完善 Sub2API 页的所有功能"。

## 做了什么

1. **注册时间**：后端详情端点早已下发 `registered_at`（`users_detail.go`），前端在
   `api/users.ts` 的映射层把它丢了，页面因此挂着一块"未接入"面板。现在
   `PlatformUserLookupResult.found` 带 `registeredAt: string | null`，基本信息区多一格
   `注册时间`：非空按 UTC 展示；`null` 写"上游未提供"——这是"链路通了但字段缺"，与
   "未接入"（整组路由没挂载）是两件事，措辞刻意不同。真实链路上今天恒为 null
   （`created_at` 被两层脱敏白名单丢弃，扩宽属读取范围扩大，须新审批，见 follow_ups）。
2. **服务端筛选**：`q` / `status` 后端早就接受，面板从未传。现在两者进 URL
   （`?q=&status=`）、随请求下发、进 react-query 的 queryKey（条件变了不命中旧缓存）。
   控件放在表格外层（DataTableV2 空表时不渲染 toolbarExtra，筛出零条的人要能清掉条件），
   表内搜索关闭（两个搜索框会打架），客户端 status 筛选删除（只筛已加载页会让
   `next_cursor`/总数失真）。搜索词在回车或失焦时才提交：每敲一字触发一次上游全量翻页
   扫描不可接受。`status` 只认契约值 active/limited/disabled/unknown，其它值忽略。
3. **样本措辞**：warnbar 那句"下面的逐用户流水来自样本数据源"此前在真实模式也显示。
   现在与详情页同一判据（`shouldShowDemoBanner([page.data_source], appDemoDataConfig)`）：
   演示源保留原句；真实源换成"真实上游下这几列显示「—」是契约边界，不是数据缺失"。
4. **子页签渲染表化**（后续三个子页签的地基）：`Sub2ApiUnsupported` 改名
   `Sub2ApiDetailTabs`；三元分支换成 `SUB2_DETAIL_PANELS`（value → 组件）表，没登记的按
   description 渲染"未接入"面板；三份重复的金额格式化收敛到
   `platformOrdersColumns.amountBodyText`（详情页那份删除）。

## 明确不变

- 不扩任何读取范围：没有新的上游端点、没有新的白名单字段、没有新 scope。
- 不动契约、连接器、handler；`registered_at` 早在信封里。
- 四个子页签的文本与顺序、"同一时刻只显示一个 tabpanel"、NewAPI 分支不含这四个页签——
  既有断言全部原样通过。

## 文件

- `web/apps/admin-web/src/api/users.ts`
- `web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx`（+ `.test.tsx`）
- `web/apps/admin-web/src/components/PlatformUsersPanel.tsx`（+ `.test.tsx`）

## 测试

- 详情页：`注册时间` 从 level-3 heading 断言改为基本信息区里的 dt/dd 断言（null →
  "上游未提供"）；新增非空用例（`registered_at: "2026-08-01T02:03:04Z"` → 页面出现该日期，
  且没有"上游未提供"）。
- 列表页新增 7 条：URL 的 q/status 原样下发；改状态重新取数不复用缓存；搜索词回车才提交；
  未知 status 被忽略；只剩一个 searchbox；演示源/真实源两种 warnbar 措辞。
- 本地：`tsc --noEmit` 全工作区 0 错误；两份测试文件 51/51；全工作区 test + admin-web build
  见 ACCEPTANCE-LOG 条目。

## follow_ups

- `注册时间` 在真实模式仍恒 null：`created_at` 需扩 `sub2apiUserPolicy`/`sub2apiUserItem`
  白名单并重采证据、新审批事件签字（宪法五 AI 红线：由验收线/负责人执行）。
- 精确详情端点 `GET /api/v1/admin/users/:id`（详情从最坏 20 次翻页降到 1 次）同上需审批，
  可与 `created_at` 合并成同一次采集。
- 消费明细 / 充值记录 / 开票记录三个子页签分别被 scope 口径、payment.read 归属、跨线
  CR 阻塞，已登记到晨间清单等产品负责人拍板。
