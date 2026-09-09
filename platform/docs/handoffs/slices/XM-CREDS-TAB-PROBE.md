# XM-CREDS-TAB-PROBE：把「探测」两格做实，并显示上游版本这个事实本身

- **status:** implemented，未上线。**纯前端**：不改后端、不改契约、不动数据库。
- **branch:** `ai/claude/XM-CREDS-TAB-PROBE`，**基于
  `ai/claude/XM-CREDS-TAB-REFS`**（不是发布线）——它改的是同一个面板的同一片
  区域，从发布线再拉一条只会产生冲突。**合入顺序：REFS 在前，本片在后。**
- **相关：** `XM-UPSTREAM-VERSION-ALERT`（R6 告警）与
  `XM-SUB2API-0_2-MATRIX`（矩阵补 0.2）。三片说的是同一件事的三个面：
  版本变了要告警、矩阵要跟上、**现在到底是什么版本得看得见**。

## 为什么

「连接与凭据」页签上有两格写着「未接入」：统计卡「探测数据状态」与卡片
「健康探测历史」，后者注明「独立探测历史 Query 尚未提供」。

**这句话是错的。** `/api/v1/metrics/history` 一直都在，`ConnectorProbeWorker`
（`internal/platform/jobs/connector_probe.go`）每次探测都往
`sub2api.connector.health` / `newapi.connector.health` 写一条观测，value 是
`{version, supported, healthy, kind, latency_ms, checked_at}`。占位比缺功能更糟：
它让人以为这件事没做，于是没人去看本来就有的数据。

第二个缺口更要紧：**整个控制台没有一处显示"上游现在是什么版本"**。上一片刚加的
R6 只在版本**变化**时告警；没有告警时无从确认。而 `supported`（兼容矩阵判定）
前端根本没读——它只存在于指标 value 里。这个字段是**采集链路的判据**，判假时
采集会停，界面上却一个字都没有。

## 改了什么

**`lib/ops.ts`**：`readConnectorHealthValue` 补读 `supported` 与 `checked_at`
（原来只读四个字段）。运行保障页的连接器健康行在 `supported === false` 时多一句
「矩阵未声明支持」并染成 warning。**只在明确为 false 时说话**——`null` 是"这条
样本没给这个字段"，与 healthy 同一条道理：不知道和坏了是两回事。

**`lib/connectorProbe.ts`（新）**：纯派生，不碰 React。指标键映射、
`describeUpstreamSupport`（把 supported 翻成人话，**判假时说清这是矩阵没跟上、
不是上游坏了**——两者处理方式完全不同：前者改代码里的矩阵，后者查上游）、
`buildProbeRows`（最近一次在前；后端按升序返回）、延迟与健康的显示值。

**`components/ConnectorProbeHistoryCard.tsx`（新）**：纯展示的探测历史表，列就是
原占位承诺的那几样——执行时间、上游版本、兼容矩阵、结果、延迟、采集状态、来源。

**`components/PlatformCredentialsPanel.tsx`**：第三条 Query（要 ops.read，
与另两条一样单独发：没有 ops.read 的操作员失去的是这一格，不是整页）；
「探测数据状态」统计卡变成「上游版本」，显示探测到的版本 + 矩阵判定徽章 +
一句解释；占位卡片换成真表。

### 三处刻意的诚实

1. **`checked_at` 缺失时退回 observed_at 再退回 synced_at，并在行内标注取自
   哪个字段。** 三者可能差很远，不标注就成了一个说不清来历的时间。
2. **一次都没探测过 ≠ 探测了但没给判定。** 没有样本时统计卡上**一个徽章都不
   显示**（包括"未知"），空态说明"连接器探测只在真实对接下运行……这不是故障"。
   把前者显示成后者就是在编事实。
3. **探测历史与上表的「最近观测」是两件事**，卡片说明里写明了：上表说的是服务
   注册表被更新过，这里说的是连接器真的连上去问过。

## 测试

- `lib/connectorProbe.test.ts`（新，17 条）：指标键映射与未知平台返回空串；
  判假文案带版本号且说"不等于上游坏了"、没探到版本时不编一个进去；null 是
  "不知道"不染警告色；最近一次在前；`checked_at` 三级回退与来源标注；
  `value: null` 的失败样本不炸且保留失败信息；**0 ms 是合法值不能显示成破折号**；
  逐点来源原样带出（一条曲线可能混着 fake 与 real）。
- `lib/ops.test.ts`：两条既有断言随新增字段更新；**新增两条**——矩阵判假时备注
  说出来并染 warning、判真那条不多这句话；`supported` 缺字段时不显示判定也不
  改色调。
- `components/PlatformCredentialsPanel.test.tsx`：占位那条测试（"仍明确显示
  未接入"）删掉换成四条真的——表格内容与顺序、矩阵判假时上游仍显示健康、
  空态不编版本号、**403 时只有这一格降级而实例表照常显示**。

**做过变异验证**（[[absence-assertions-must-be-mutation-checked]]）：
- 把 `supported === false` 改成 `!== true`（让"不知道"也算不支持）→ ops 那条
  "缺字段时不显示矩阵判定"如期变红。
- 让统计卡无条件计算徽章 → "空态不编版本号"那条如期变红。
两条"不应出现"的断言都不是恒真的。

门禁：`pnpm -r typecheck` / `test`（1558 条全绿）/ `build` 三条、
`scripts/check-governance.sh` 退出 0、`gitleaks protect --staged` 无泄漏。
后端未触及，未跑 Go 套件。

## 顺带修正的两处过期注释

面板的文件头注释写着「当前平台只有 Service Query」「CredentialRef / 连接登记簿：
尚无 Query」——前一片已经把凭据引用做实了，这句在合入 REFS 那一刻就过期了。
一并改成按四条 Query 分述，并写明分开发的理由。

## follow_ups

- 回看窗口写死 24 小时。探测默认 5 分钟一次，24 小时够看出"最近一直在探测"
  还是"某个点之后停了"。要不要给个时间窗切换器，等有人真的抱怨看不够再说。
- 表里没有分页。24 小时 × 5 分钟上限 288 行，`DataTableV2` 扛得住；如果把窗口
  放宽到 7 天就需要分页了。
- 探测只覆盖 Sub2API 与 NewAPI 两个连接器。别的平台标识进来时这一格显示
  "没有对应的连接器探测指标"，而不是假装在加载。
