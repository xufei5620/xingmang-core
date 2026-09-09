# ✅ 版本已确认(2026-08-28,用户提供线上版本号)

**admin.solov.cc 线上 = SoloAI v2.5.130,commit 14e6f617**(= Merge PR #211
codex/mail-analysis-review-clusters)。已 git checkout 核实:0172/0176/0177/0179/
0180/0182 增强迁移全在、internal/platformdb 目录在、relay_profit.go 有
ErrProfitNothingKnown(两侧失败不静默)+revToday==nil 判断+ratio_snapshot。

**结论:线上是"增强版",不是 v2.5.102 核心版。**
→ **本文件下方初版报告(增强版描述)对线上正确,平台按它对齐。**
→ 上一节"更正为 v2.5.102 核心版"仅适用于 GitHub 默认分支(非线上),已作废,保留供追溯。

平台影子对比按**增强版**口径,这也更好——采纳其全部良好设计:
- 取数失败**存 NULL/跳过绝不写 0**(ErrProfitNothingKnown,relay_profit.go:129)
- ratio_snapshot 逐行冻结倍率(migrations/0176)
- platform_id 多平台四桶归集(0179/0182)
- newapi 自营库直查收入(platformdb/channel_revenue.go)
- runway 燃尽预警(0177)
平台新建即从第一天写全这些,无 SoloAI 演进期的 NULL 兼容包袱。

---

# ⚠️ 更正(代理自查):区分「现行 v2.5.102」与「未发布增强版」

上一版报告混入了本地未合并特性分支(阶段C/D/E)的增强,误当现行。按 GitHub
默认分支 HEAD=8f6b3b08(v2.5.102)逐个 git cat-file 核对后:

**现行 v2.5.102 确有(标准答案,照搬)**:成本公式 actual_cost÷recharge_ratio、
两取数函数(端点/鉴权/type=2/÷500000)、收入=sub2api admin today.user_cost、
relay_profit_daily 六列(station,day,token_id,account_id,revenue,cost,updated_at)、
利润=收入-成本、CST/USD、每站一个 recharge_ratio(无模型成本单价表)、
5min 余额扫描落 relay_balance_history。

**现行没有(下方"增强版"章节描述的是未发布分支,勿当现行)**:
- ratio_snapshot(倍率不冻结,改倍率历史无痕漂移)
- platform_id/多平台四桶归集
- relay_model_usage_hourly、runway 燃尽、newapi 自营库直查收入、multi_provider(0172)

**现行的已知缺陷(平台可做得更好,别照抄)**:
- **取数失败写 0 不是跳过/NULL**(relay_profit.go:112,117 普通 float64)→ 成本失败当天
  利润虚高≈收入、收入失败利润转负,且不报错。平台影子对比要么复刻此行为求 14 天 0 差异,
  要么对齐到已修复版——**取决于线上真实版本**。

**‼️ 必须产品确认**:admin.solov.cc 线上实际跑哪版无法从源码判断。GitHub 默认分支=v2.5.102,
但 dist 快照带 0180/0182(增强版)——可能有更新内部部署。两版在"失败写0/有无 ratio_snapshot/
platform_id/runway"上行为不同,直接决定影子对比能否 14 天 0 差异。**接成本线前必须确认线上版本。**

---

# EV:SoloAI 现行成本/毛利核算方法(2026-08-27,v2.5.102)

- 来源:Opus soloai-cost-study 代理对 K:/soloai/soloai-v2-src 的只读研究
- 用途:平台成本/毛利核算的**标准答案**,影子对比口径依据;引用带 文件:行号
- 一句话:成本=真实上游实扣(非估算非人工),平台可几乎照搬

## 成本核算全链(照搬)
① 配对:relay_stations.sub2api_token_map = {上游令牌id:自营账号id}(migrations/0090)
② 成本取数(核心,直接查上游 url+key):
   - Sub2API:GET {base}/v1/usage,Bearer sk-令牌 → usage.today.actual_cost(USD)
     (relaymon/sub2api.go:263 FetchSub2APIUsageCost)
   - NewAPI:GET {base}/api/log/self/stat?type=2(只consume不含充值) → data.quota÷500000
     (relaymon/newapi.go:221;NewAPIQuotaPerUSD=500000)
③ 折算真实付款:成本 = 上游实扣 ÷ recharge_ratio(每上游账号一个除数,人工维护,migrations/0084)
④ 落库 relay_profit_daily(station,day,token_id):revenue/cost/ratio_snapshot/platform_id
   今日覆盖刷新、过去日冻结(postgres/relay_profit.go:46)
⑤ 收入:自营平台实收——sub2api=user_cost=SUM(usage_logs.actual_cost);
   newapi=SUM(quota_data.quota)/500000(只读连自营库)。生产实测与库逐位相等
⑥ 毛利=收入-成本;毛利率=毛利/收入(收入≤0 为 nil 不显示)

## 上游余额路径(产品负责人问的"url+key 查余额":SoloAI 早在做)
RelayRenewalScheduler 每 5 分钟扫,refreshBalance 用 url+解密凭证拉 Profile.Balance,
变化才落 relay_balance_history(migrations/0080)。**但余额仅用于 runway 燃尽预警,
不参与毛利成本**——成本用更精确的每令牌 actual_cost 标量。
边界:查的是上游 reseller 站(Sub2API/NewAPI 实例)账号余额,非 OpenAI/xAI 各家原厂。

## 三个诚实边界(平台必须对齐)
1. 成本只有"每令牌每日一个标量",**无模型维度**——模型级成本无可测来源,禁摊派(migrations/0180 最该读)
2. 自营库 account_stats_cost 是**售价不是成本**(实测 258 账号售价45.02 vs 真实实扣5.81,差7.75倍且逐账号不同)
3. recharge_ratio 无历史→涨价与调倍率分不开,ratio_snapshot 逐行冻结(migrations/0176)

## 不静默纪律(影子对比照抄)
- 取数失败写 NULL/跳过绝不写 0(否则利润凭空=收入)
- 平台归属四桶(有效/指向已删/NULL/未配对),不 COALESCE 不 INNER JOIN 静默丢金额
- 今日覆盖过去冻结

## 口径常量
USD 全程;credits÷500000;NUMERIC 非分;**CST +08:00 无夏令时,收入成本切日共用同一时间权威**;
累计=自上次重置起每日快照之和(非上游30天窗口)

## 平台对齐清单
照搬:成本取数两函数(端点/鉴权/type=2/÷500000)、折算公式、配对模型、CST/USD/换算常量、
ratio_snapshot、三条不静默纪律。
XM-0037 登记簿:SoloAI **无按模型成本单价表**,唯一成本配置是每上游账号一个 recharge_ratio;
登记簿应建模为"每上游账号:base_url+凭证+recharge_ratio+令牌↔账号映射+倍率快照",
不要照抄模型成本矩阵(不存在且无来源)。
可简化:平台新建无 NULL 历史包袱,第一天起每行写全 ratio_snapshot+platform_id。

## 与上游余额线合并
成本入账用 actual_cost/ratio(权威);余额差分留作交叉校验/兜底(会被充值污染,SoloAI 正因此不用它算成本);
relaymon client 可直接复用,扩原厂余额才是新工作。
