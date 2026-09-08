package oidcauth

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// platformScopePrefixes 是**平台自己**的细粒度权限命名空间。
//
// 这些串按 ADR-016 / CR-0001 §5 绝不允许出现在 Keycloak 令牌里：Realm 只回答
// 「这是不是员工、认证强度够不够」，「能不能改服务」由平台数据库回答。
// 令牌里冒出来一个 `registry.service.manage`，说明有人在 Realm 里建了不该建的
// 角色——那是配置漂移，不是授权。
var platformScopePrefixes = []string{
	"registry.",
	"ops.",
	"audit.",
	"platform.",
	"action.",
	"connector.",
	// XM-0039：request.read / request.content.read。后者管的是用户与模型的
	// 完整对话内容——正因为它敏感，更不能让它以 Realm 角色的形式存在：
	// Keycloak 里建出一个 request.content.read 角色，就等于把「谁能看全平台
	// 用户对话」这个决定挪出了平台数据库的管辖（ADR-016 / CR-0001 §5）。
	"request.",
	// XM-B003：个人表格视图的细粒度 scope 同样只存在于平台授权层。
	// Realm 里出现 ui.saved_view.manage 是配置漂移，不是一次合法授权。
	"ui.",
	// XM-CRED0：credential.manage 会把明文写进 SecretProvider 目录。
	// 它以 Realm 角色的形式出现，等于把「谁能换掉平台的上游凭据」这个决定
	// 挪出了平台数据库的管辖（ADR-016 / CR-0001 §5）。
	"credential.",
	// XM-LOGIN：staff.manage 管理本地登录账号（创建、改角色、启停、重置
	// 密码）。同上一条理由——它不该以 Realm 角色的形式出现。
	"staff.",
	// XM-EXT-PUBLISHING：publishing.publish 决定「谁能以公司的名义对外说话」。
	// Realm 里出现一个 publishing.publish 角色，等于把这个决定挪出平台数据库的
	// 管辖（ADR-016 / CR-0001 §5）——与 credential./staff. 同一条理由。
	"publishing.",
}

// looksLikePlatformScope 判断一个角色名是否长成平台细粒度权限的样子。
func looksLikePlatformScope(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	for _, p := range platformScopePrefixes {
		if strings.HasPrefix(r, p) {
			return true
		}
	}
	return false
}

// DefaultRoleScopeMap 是「粗粒度 Realm 角色 → 平台细粒度 scope」的默认翻译表。
//
// ⚠️ **上生产前必须人工审定**（见 docs/modules/httpapi/AUTH-SWITCH.md）。
// 这张表是代码里的默认值，不是产品决定——它决定了「登录进来的员工默认能看到
// 什么」，属于授权策略，应当由人拍板并落到 XM_OIDC_ROLE_SCOPES。
//
// 两处刻意的保守，与任务书给的示例不同，理由写在这里以便审定时推翻：
//
//  1. **staff 不含 audit.read**。docs/modules/httpapi/PERMISSIONS.md 已经论证过
//     `audit.read` 比 `ops.read` 高一档：审计事件带 before/after 摘要，等于把每次
//     写操作的内容摊开。「看板角色拿到 ops.read 不应顺带看见全平台的操作明细」
//     ——把 audit.read 塞进唯一的 staff 角色，等于让每个员工默认看见全部操作明细，
//     和那段论证直接冲突。要给，就该是显式的人工决定；
//
//  2. **admin 这个角色今天并不存在**。CR-0001 §5 只创建 `staff` 一个 Realm Role。
//     这里预留 admin 的映射是为了「加角色时不用改代码」，但 Realm 里加角色本身
//     需要另开一张变更单。在那之前这一行永远不会命中。
//
//  3. **staff 不含 request.read / request.content.read**（XM-0039）。同上一条
//     论证再进一档：request.read 是**逐条**的调用清单（谁、几点、什么模型、
//     多少 token），足以还原一个人的使用轨迹；request.content.read 更是用户与
//     模型之间的完整对话——用户自己粘进去的合同、简历、身份信息、源码都在里面
//     （交接文档 §9.4 把它列为高敏数据）；
//
//  4. **admin 也不含 request.content.read**（XM-0039 验收裁定，2026-08-28）。
//     这一项曾经写在下面那张表里，理由是「admin 是全权角色，全权就该包含它」——
//     产品负责人按最小权限原则推翻了它：看全平台用户对话正文的应该是**显式授权
//     的客诉/风控岗**，而不是每个管理员顺带获得的能力。
//
//     裁定还有一句更要紧的理由：**今天不改，以后就石化了**。admin 角色在 Realm
//     里还不存在（CR-0001 只建了 staff），所以这一行今天不会命中；等它真的被
//     创建那天，没有人会回过头来质疑一张已经跑了半年的默认表。
//
//     要授予就用 XM_OIDC_ROLE_SCOPES 显式配一个专门的角色。
//     `resolver_test.go` 的 TestDefaultRoleScopeMapIsConservative 钉住了这个决定。
//
//  6. **admin 额外含 staff.manage / credential.manage / connector.manage**
//     （XM-LOGIN，2026-08-30）。这是对上面 XM-CRED0 那条"不进 staff/admin"
//     原则的一次显式收窄，理由与那条本身一样刻意：本地登录（XM-LOGIN）的
//     bootstrap 管理员（cmd/staff-bootstrap）必须一上线就能管账号、配凭据、
//     切连接器，而不必先登录一次再手动申请第二个角色——冷启动阶段压根没有
//     "已登录的人"能去审批那次申请。三个 scope 只加进 admin，不加进 staff：
//     staff 依然不该有任何 .manage 能力（见 TestDefaultRoleScopeMapIsConservative
//     对 staff 的断言）。专门角色 credential-admin 的语义不受影响——它仍然
//     是"只给凭据/连接器写权限、不给别的"的最小面，两条路径并存。
//     要撤销就用 XM_OIDC_ROLE_SCOPES 显式覆盖 admin 的映射。
//
//  7. **API Key 元数据使用专门角色**（KEY_SCOPE_APPROVAL，2026-08-30）。
//     `platform.user_keys.read` 只允许看到前缀、状态与时间元数据；它不进入
//     staff/admin 默认映射，因为即使没有完整 Key，凭据库存仍是敏感面。开发态
//     默认清单会显式携带该 scope 以便演示 Fake 能力；生产 OIDC 必须给需要它的
//     人工审定角色（这里预留 `key-metadata-reader`），不会因 admin 角色自动获得。
//
//  5. **staff 不含 platform.users.read**（XM-0046）。与第 3 条同一档：
//     它是**逐用户**的资金明细（余额、区间充值、区间消费、最后活跃），
//     即便邮箱已在契约层打码，一份逐用户清单也足以还原一家客户的经营规模。
//     ops.read 看到的是聚合数字，这是逐条——「看板角色拿到 ops.read 不应顺带
//     看见全平台客户的账」。给 admin，是因为它与 request.read 泄漏面相当
//     （一个回答「他用了什么」，一个回答「他花了多少」）。
//
// 结果：CR-0001 执行完当天切过来，员工能登录、能看服务清单与运营指标；审计页、
// 请求列表、请求正文与写操作都会 403，直到有人显式授权。
// Fail Closed 比「先放开再收」便宜得多。
func DefaultRoleScopeMap() map[string][]string {
	return map[string][]string{
		// ui.saved_view.manage 只读写调用者自己的低影响偏好；owner 与 Environment
		// 均由 Principal 派生，所以给 staff 不会扩大到任何其他人的视图或业务数据。
		"staff": {"registry.read", "ops.read", "ui.saved_view.manage"},
		"admin": {
			"registry.read",
			"ops.read",
			"audit.read",
			// XM-ACTIONS0：跨 Action 执行记录列表。比 audit.read 低一档（不含
			// before/after 正文），管理员默认可读；staff 仍不给。
			"action.read",
			"registry.service.manage",
			"registry.connector.manage",
			"registry.connection.manage",
			// request.read 在这里，request.content.read **刻意不在**——
			// 元数据列表回答「这个人用得多不多」，正文回答「这个人问了什么」。
			// 见上面第 4 条。
			"request.read",
			// XM-0046：逐用户资金清单，与 request.read 同一档（见上面第 5 条）
			"platform.users.read",
			"ui.saved_view.manage",
			// XM-LOGIN：见上面第 6 条——bootstrap 管理员需要一上线就能管
			// 账号、凭据与连接器，不必先登录再手动申请第二个角色。
			"staff.manage",
			"credential.manage",
			"connector.manage",
			// 生产上线（2026-08-31）：bootstrap 管理员就是运营负责人，成本看板、
			// 登记簿与告警处理不能再要求第二个角色——否则平台详情页的成本面板
			// 对唯一的管理员也是 403。finance.read 与各 manage 仍不给 staff：
			// 金额与凭据来源是高敏信息，普通运营按需单独授予。
			// finance.platform_channel_binding.manage **刻意不在**：渠道绑定的
			// L1 写权限按既有裁定必须人工显式授予（resolver_test 钉住）。
			"finance.read",
			"finance.upstream_account.manage",
			"finance.recharge_ratio.manage",
			"finance.token_map.manage",
			"finance.subscription.manage",
			"alerts.alert.manage",
			"alerts.silence.manage",
			// XM-SERVER0（2026-08-31）：服务器登记簿的写权限。**与
			// finance.platform_channel_binding.manage 不是同一类**，所以进
			// admin 而不是像那条一样刻意排除：渠道绑定写的是「哪个上游账号
			// 在给哪个平台供给」，绑错会让成本记到错误渠道且不报错，属于
			// 设计稿裁定必须人工显式授予的高风险面；服务器登记簿写的是
			// 主机名/规格/供应商/到期日这类纯记录字段（拍板「服务器只做
			// 记录」），不触碰任何第三方系统、不影响任何成本或收入归属，
			// 改错了改回来即可——与已经在这张表里的
			// finance.upstream_account.manage 等同一档（登记簿，不是执行）。
			// resolver_test 的 TestDefaultRoleScopeMapIsConservative 断言
			// admin 含它、staff 不含它。
			"server.manage",
			// XM-ASSURE1-core（渠道主动探测/检测任务）：declare/cancel/run 三个
			// L1 Action 与 finance.upstream_account.manage 等同一档——纯配置写
			// / 触发一次受多重闸约束的探测批次，进 admin。**probe.kill_switch
			// 刻意不在这里**：ADR-019 决策·四·#4 明确要求"允许这个平台探测
			// 花钱"是一个独立可授予/可审计的权限，与本行放宽 admin 的其它
			// scope 不是同一类判断——见下面专门角色 assurance-probe-admin。
			"assurance.probe.manage",
			"assurance.probe.run",
			// XM-0030（2026-09-07 产品负责人指示启用）：审批中心的两个常规权限。
			//
			// approval.read（看队列）本该比 approval.decide 面更宽——提交人要能
			// 查自己那张单的进度，值班的人要能看见积压。但本表今天只有
			// staff/admin 两档，没有承载「只读审批队列」的第三档，所以两个都进
			// admin，**staff 仍然一个都不给**：一个看板角色能看见「谁在申请改
			// 什么」，等于把变更意图提前泄漏给不需要知道的人。
			//
			// **approval.l4 刻意不在这里**——理由与 assurance.probe.kill_switch
			// 完全相同：设计稿 §7 写明「首批特权票仅产品负责人」，写进 admin
			// 等于发给每一个管理员。见下面专门的 approval-l4 角色。
			"approval.read",
			"approval.decide",
			// XM-CARD0（2026-09-04）：Infini 虚拟卡的四个权限档。
			//
			// 全部给 admin，理由与上面 credential.manage 同一条：本地登录的
			// bootstrap 管理员就是运营负责人，不给就等于这个功能对唯一能用它
			// 的人也是 403，而平台今天没有第二个角色可以申请。上线当天就是
			// 这么撞上的——页面报「需要权限: card.read」。
			//
			// 四档分开定义的意义不在今天这张表，而在**对外开放时**：那时
			// 外部用户走另一个角色，只给 card.read，开卡与卡面都不下放。
			// staff 一档都不给：开卡花真钱，卡面是明文卡号。
			"card.read",
			"card.issue",
			"card.manage",
			"card.reveal",
			// **提现权限（fund.withdraw）刻意不在这里**（XM-CARD6）。
			//
			// 卡片四权限给 admin 的理由是「不给就等于功能对唯一能用它的人
			// 403」。提现不适用：它把钱转出平台、不可逆，而 admin 是日常
			// 操作账号——一个被盗用的 admin 会话不该能把资金池搬空。
			// 见下面的 fund-operator。
			//
			// 但**调整额度**（fund.limit.manage）在这里，而且刻意就该在
			// 这里。额度 2026-09-05 从环境变量搬进了数据库、改成后台可调
			// （产品负责人决定：要登服务器改文件再重启才能动的数字，实际上
			// 没人会去动）。搬完之后「改不了」这道物理屏障就没有了，能替代
			// 它的只有两把钥匙分持：
			//
			//   fund.limit.manage（admin）        —— 能抬高天花板，不能提现
			//   fund.withdraw    （fund-operator）—— 能提现，抬不高天花板
			//
			// 拿到任一把都搬不空资金池，要两把都拿到才行。今天同一个人两个
			// 角色都持有，这个分离在实践上是名义的；但审计里两件事是两条
			// 独立记录，而且想拆给两个人时拆得开——并成一个权限就再也拆
			// 不开了。
			"fund.limit.manage",
			// XM-SMS0（2026-09-05）：接码中心。三个日常权限给 admin
			// ——看清单、看号码与验证码、连接测试与人工核对都是运营要做的
			// 事，不给就等于这个功能对唯一能用它的人 403（卡片上线当天
			// 就是这么撞上的）。
			//
			// **sms.purchase 刻意不在这里**：买号花真钱且不可退，
			// 与 fund.withdraw 同一档，见下面的 sms-operator。
			//
			// sms.reveal 与 card.reveal 同档：号码在库里是明文列（本仓
			// 没有列加密工具，而 PAN/CVV 已经是明文列），这道闸是
			// 「谁能看号码」剩下的唯一约束。
			"sms.read",
			"sms.reveal",
			"sms.manage",
			// XM-EXT-PUBLISHING（2026-09-08）：内容发布的读与编辑。
			//
			// 与卡片/接码同一条理由：不给就等于这个功能对唯一能用它的人也是
			// 403。publishing.manage 写的是草稿、素材与渠道**登记**（凭据只经
			// CredentialRef，明文另由 credential.manage 管），一件都发不出去。
			//
			// **publishing.publish 刻意不在这里**——理由与 fund.withdraw /
			// sms.purchase 完全相同：把「对外不可逆」的动作从日常操作角色里
			// 拿出来。发布本身是 L3（两票且审批人≠提交人），但审批拦的是
			// 「这一篇发不发」，权限拦的是「谁能提起这件事」；给 admin 等于
			// 把第一道闸拆掉，只剩审批一道。见下面的 content-publisher。
			"publishing.read",
			"publishing.manage",
		},
		// KEY_SCOPE_APPROVAL：元数据-only 的 Key 清单由专门角色授予；不要把它
		// 加进 staff/admin，否则一个普通运营角色会顺带看到全平台凭据库存。
		"key-metadata-reader": {"platform.user_keys.read"},
		// XM-0039 / 2026-08-28 裁定：用户与模型的完整对话正文由专门角色显式授予
		// （客诉 / 风控岗），admin 刻意不带。生产上线后请求详情已接真实数据
		// （XM-REQLOG-MERGE），运营负责人要看正文就给自己加这个角色——授予动作
		// 本身走 staff.account.set_roles 进审计链，而不是把 scope 悄悄塞进 admin。
		"request-content-reader": {"request.content.read"},
		// XM-CRED0：粘贴 / 轮换 / 吊销上游凭据与把连接器切到真实上游，由专门角色
		// 授予。**不进 staff/admin**：credential.manage 会把明文写进 SecretProvider
		// 目录，connector.manage 决定 worker 下一轮连哪台上游——两者都不是
		// 「管理员顺带获得」的能力，要给就在 Realm 里建这个角色并人工审定。
		"credential-admin": {"credential.manage", "connector.manage"},
		// XM-ASSURE1-core：谁能打开"这个平台允许探测花钱"的开关，由专门角色
		// 独立授予（ADR-019 决策·四·#4）——不进 admin，即便 admin 已经拿到了
		// assurance.probe.manage/run（上面的裁定：declare/cancel/run 是普通
		// 配置写与受闸约束的触发，kill_switch 是"批准花真钱"本身，两者不是
		// 同一类判断，一并给 admin 会让这条独立授权的设计意图落空）。
		"assurance-probe-admin": {"assurance.probe.kill_switch"},
		// XM-0030：L4 的特权票。**默认不发给任何人**——设计稿 §7 写明「具体
		// 人选映射进 RoleScopeMap，不进 Keycloak」，且「首批特权票仅产品负责人」。
		//
		// 没有人持有它时，L4 的审批单会一直停在 PENDING（Settle 要求至少一张
		// 特权票），直到过期。**这是刻意的 fail closed，不是缺陷**：L4 是拉闸级
		// 操作，宁可等一个人回来，也不要让它被凑够普通票批掉。处置见
		// docs/runbooks/APPROVAL-QUEUE.md §3.3。
		//
		// 这个角色顺带包含 approval.decide：持特权票的人当然也要能投普通票，
		// 否则他必须同时被授予 admin 才投得了。
		"approval-l4": {"approval.decide", "approval.l4"},
		// XM-CARD6（2026-09-05）：资金提现。与 assurance-probe-admin 同一条
		// 设计意图——把「批准花真钱」这类判断从日常操作角色里拿出来。
		//
		// 提现比那还重一档：它把钱转到**平台之外**，不可逆、不可追回。
		// cards.withdraw.execute 现在是 L3（XM-RISK-RESTORE），发起提现要过
		// 审批中心的两票且审批人≠提交人；但「谁持有这个权限」仍是第一道闸
		// ——审批拦的是「这一笔做不做」，权限拦的是「谁能提起这件事」。
		// 给 admin 等于把第一道闸拆掉，只剩审批一道。
		//
		// 两个权限一起给：登记地址决定「钱能去哪儿」，提现决定「什么时候去」，
		// 分开成两个串是为了以后想拆的时候拆得开，今天由同一个角色持有。
		"fund-operator": {"fund.withdraw", "fund.address.manage"},
		// XM-SMS0（2026-09-05）：买号。与 fund-operator 同一条设计意图
		// ——把「花真钱」从日常操作角色里拿出来。
		//
		// 接码的钱比提现小得多（一个号几毛到几块），但它的花法更容易失控：
		// 数量上限 200，一次手滑就是两百个号；而买到的号不可退。
		// 独立角色让「谁能花这笔钱」是一次显式授予。
		"sms-operator": {"sms.purchase"},
		// XM-EXT-PUBLISHING（2026-09-08）：对外发布。与 fund-operator /
		// sms-operator 同一条设计意图——把「对外不可逆」从日常操作角色里拿出来。
		//
		// 它不花钱，但它比那两个更公开：发出去的内容即便删除也已经被抓取、
		// 被截图。**「谁能以公司的名义说话」是一次显式的组织授予**，不该由
		// 「他是管理员」顺带获得。
		//
		// 顺带包含 publishing.read：不给的话，持发布权的人看不到自己要发的
		// 草稿与渠道，必须同时被授予 admin 才用得起来——那正是这次拆分想
		// 避免的。读的泄漏面（草稿正文 + 渠道引用）本来就窄于发布权本身。
		"content-publisher": {"publishing.read", "publishing.publish"},
	}
}

// ParseRoleScopeMap 解析 XM_OIDC_ROLE_SCOPES 的 JSON 形态：
//
//	{"staff":["registry.read","ops.read"],"admin":["audit.read"]}
//
// 用 JSON 而不是 `role=a,b;role2=c` 这类自造分隔符：映射表要被人审、被 diff，
// 自造格式的转义规则迟早出事，而 JSON 的错误位置 Go 会直接告诉你。
//
// 拒绝把**平台形态的串当作角色名**（键）：那意味着有人打算在 Keycloak 里创建
// `registry.service.manage` 角色，正是 ADR-016 禁止的东西。左边是 Keycloak 的
// 粗粒度角色，右边才是平台 scope，方向写反了就该在启动时炸掉而不是运行时困惑。
func ParseRoleScopeMap(s string) (map[string][]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var raw map[string][]string
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("解析角色映射表失败（应为 {\"role\":[\"scope\",...]} 形态）: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("角色映射表为空：留空环境变量用默认表，别显式配一个空表")
	}
	out := make(map[string][]string, len(raw))
	for role, scopes := range raw {
		role = strings.TrimSpace(role)
		if role == "" {
			return nil, fmt.Errorf("角色名为空")
		}
		if looksLikePlatformScope(role) {
			return nil, fmt.Errorf("角色名 %q 长成平台细粒度权限的样子："+
				"ADR-016 禁止在 Keycloak 建这类角色，映射表左边应是 staff 这类粗粒度角色", role)
		}
		clean := make([]string, 0, len(scopes))
		for _, sc := range scopes {
			if sc = strings.TrimSpace(sc); sc != "" {
				clean = append(clean, sc)
			}
		}
		if len(clean) == 0 {
			return nil, fmt.Errorf("角色 %q 没有映射到任何 scope：要表达「不给权限」就别列这个角色", role)
		}
		sort.Strings(clean)
		out[role] = slices.Compact(clean)
	}
	return out, nil
}

// roleTranslation 是一次角色翻译的结果。
type roleTranslation struct {
	Scopes     []string // 已去重排序的平台细粒度权限
	DriftRoles []string // 令牌里长成平台权限样子的角色：一律忽略，调用方负责记 warn
	Matched    []string // 命中映射表的粗粒度角色，仅用于日志排障
}

// translateRoles 把令牌里的粗粒度角色翻译成平台 scope。
//
// 顺序是**先剔漂移再查表**：即便有人在映射表里也写了 `registry.read` 当键，
// 令牌里的 `registry.read` 角色也不会被当成一次合法授权。
//
// 未命中映射表的角色静默忽略——Keycloak 默认就会发
// `default-roles-solov-staff` / `offline_access` / `uma_authorization`，
// 为它们记日志只会把真正的漂移警告淹掉。
func translateRoles(roles []string, m map[string][]string) roleTranslation {
	var t roleTranslation
	seen := make(map[string]struct{})
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if looksLikePlatformScope(role) {
			t.DriftRoles = append(t.DriftRoles, role)
			continue
		}
		scopes, ok := m[role]
		if !ok {
			continue
		}
		t.Matched = append(t.Matched, role)
		for _, sc := range scopes {
			if _, dup := seen[sc]; dup {
				continue
			}
			seen[sc] = struct{}{}
			t.Scopes = append(t.Scopes, sc)
		}
	}
	// 排序让 Principal.Scopes 与审计日志可复现；多角色叠加时顺序不该取决于
	// Keycloak 把角色数组排成什么样
	sort.Strings(t.Scopes)
	sort.Strings(t.DriftRoles)
	sort.Strings(t.Matched)
	return t
}
