package publishing

// 内容发布的三个 scope。
//
// 为什么是三个而不是一个：**「谁能写稿」和「谁能对外发出去」不是同一个判断**。
// 这与 fund.limit.manage / fund.withdraw 的拆法同源（rolemap.go 那段注释）——
// 拿到编辑权的人改不了「以谁的名义发」，也按不下发布；拿到发布权的人也抬不高
// 自己的权限面。今天这两把钥匙可能由同一个人持有，但审计里是两条独立记录，
// 想拆给两个人时拆得开；并成一个 scope 就再也拆不开了。
//
// 为什么不是四个：`publishing.manage` 一把管草稿、素材与渠道登记。渠道登记
// 写的是 handle 与 CredentialRef 的**引用**（明文另由 credential.manage 管），
// 与草稿同属「登记簿」一档，没有必要再细分一层没人会分别授予的权限。
const (
	// ScopeRead 读这一页的全部内容：日历、草稿与版本、素材、渠道、发布记录。
	//
	// 渠道那一格会回显 CredentialRef（**引用，不是明文**），所以它的泄漏面
	// 与 registry.read 同档：能看出「用哪把钥匙」，看不出钥匙是什么。
	ScopeRead = "publishing.read"

	// ScopeManage 写草稿、素材与渠道登记（四个 L1 + 一个 L2 Action）。
	ScopeManage = "publishing.manage"

	// ScopePublish 提起一次对外发布。
	//
	// **刻意不进 admin**（rolemap.go），与 fund.withdraw / sms.purchase 同一条
	// 设计意图：把「对外不可逆」的动作从日常操作角色里拿出来。审批拦的是
	// 「这一篇发不发」，权限拦的是「谁能提起这件事」——给 admin 等于把第一道
	// 闸拆掉，只剩审批一道。专门角色是 content-publisher。
	ScopePublish = "publishing.publish"
)
