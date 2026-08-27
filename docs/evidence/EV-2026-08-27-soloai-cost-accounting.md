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
