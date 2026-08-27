package finance

// ScopeRead 是读取成本登记簿所需的权限。
//
// **不复用 ops.read**（对照 alerts.ScopeRead 复用它的理由）：告警内容就是运营
// 指标的判读结果，泄漏面完全相同；登记簿不是——它列的是每个上游账号的
// **凭据引用、充值倍率与令牌映射**。倍率是商业条款（我们从上游拿到几折），
// 令牌映射是成本归属的对账键，两样都比看板上的余额数字敏感一个量级。
// 能看指标的人不该自动能看到我们跟每个上游谈的价。
const ScopeRead = "finance.read"

// ScopeAccountManage 是登记与修改上游账号所需的权限（L1 动作）。
//
// 登记簿写操作与读分开授予：登记一个账号 = 决定「平台从哪里、用哪套凭据、
// 按什么倍率算成本」，改错了不会报错，只会让台账从那一刻起静静地错着。
const ScopeAccountManage = "finance.upstream_account.manage"

// ScopeRatioManage 是修改充值倍率所需的权限（L1 动作）。
//
// 与 ScopeAccountManage **分开**是有意的（设计稿 §6.3）：倍率是唯一会改变
// 成本口径的字段，且它可随时改写、SoloAI 侧无历史（迁移 0084）。
// 平台虽然逐行冻结 ratio_snapshot 让历史不被追溯篡改，但「谁能动这个数」
// 仍应是一道独立的授权面——它直接决定毛利报表长什么样。
const ScopeRatioManage = "finance.recharge_ratio.manage"

// ScopeTokenMapManage 是维护令牌映射所需的权限（L1 动作）。
//
// 映射写错的后果是**成本记到别的渠道上**：两条渠道的毛利一个虚高一个虚低，
// 合计却完全正确——这是最难从总数上看出来的一类错误，
// 所以它的授权面也单独留一个（§11.11 拍板：映射手工 Action 维护）。
const ScopeTokenMapManage = "finance.token_map.manage"

// ScopeSubscriptionManage 是维护订阅批次与代理资产所需的权限（L1 动作）。
//
// 六个动作（批次的登记 / 退款 / 终止，代理的登记修改 / 退款 / 终止）**共用一个
// scope**，与登记簿那边刻意拆成三个不同：拆分的依据是爆炸半径，而这六个
// 动作写的是同两张表、改的是同一个数——某条订阅渠道的每日成本。
// 再拆一层不会带来任何实际隔离，只会多一处要维护的授权。
//
// 与 ScopeAccountManage 分开则是必要的：登记簿决定「用哪套凭据、按什么口径算」，
// 这里决定「为它付了多少钱」。前者是接入配置，后者是财务凭证——
// 能配一条渠道的人不必然该能改我们为它付过的账。
const ScopeSubscriptionManage = "finance.subscription.manage"
