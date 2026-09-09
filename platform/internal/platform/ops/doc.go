// Package ops 实现规格 §9.1 的数据新鲜度模型。
//
// 铁律：
//   - **禁止裸数字冒充实时完整数据**：任何指标值都必须与 Freshness 一起返回，
//     本包不提供只返回值的路径；
//   - 新鲜度是**派生状态**而非存储字段：库里只存 observed_at / status /
//     is_partial / 阈值等事实，状态在查询时算。时间推移会自动让「新鲜」变成
//     「延迟」，不需要任何后台任务去翻转标记；
//   - staleness_seconds 不持久化（规格 §9.1 明文要求），每次按 now - observed_at 算；
//   - observed_at 可空，表示「从未成功采集过」——这是「未初始化」状态的来源，
//     不要用零值时间冒充。
//
// 本包管两张语义相反的表：ops.metric_observation 覆盖式保存最新态，回答
// 「现在是什么」；ops.metric_observation_sample 追加式保存样本，回答
// 「这段时间是怎么变的」（XM-0024）。分表的理由见 docs/modules/ops/README.md。
// 样本不参与新鲜度判定——不要对 ListSamples 的返回值调用 Freshness()。
package ops
