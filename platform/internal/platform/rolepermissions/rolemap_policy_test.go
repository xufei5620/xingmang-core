package rolepermissions

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultRoleScopeMapIsConservative(t *testing.T) {
	m := DefaultRoleScopeMap()
	staff := m["staff"]
	admin, ok := m["admin"]
	if !ok {
		t.Fatal("默认表应保留 admin 位（Realm 里今天还没有这个角色）")
	}
	if !slices.Contains(staff, "registry.read") || !slices.Contains(staff, "ops.read") {
		t.Fatalf("staff 至少应有两个读权限, got %v", staff)
	}
	if slices.Contains(staff, "audit.read") {
		t.Fatal("staff 默认不该含 audit.read：PERMISSIONS.md 论证过它要单独授予")
	}
	// XM-0046：逐用户资金明细与 request.read 同一档，staff 同样不该默认拿到。
	// ops.read 看到的是聚合数字，这是逐条——「看板角色不应顺带看见全平台客户的账」
	if slices.Contains(staff, "platform.users.read") {
		t.Fatal("staff 默认不该含 platform.users.read：它是逐用户的资金明细，不是看板数字")
	}
	if slices.Contains(staff, "platform.user_keys.read") || slices.Contains(admin, "platform.user_keys.read") {
		t.Fatal("staff/admin 默认不该含 platform.user_keys.read：凭据库存必须独立授予")
	}
	if got := m["key-metadata-reader"]; !slices.Contains(got, "platform.user_keys.read") {
		t.Fatalf("专门的 key-metadata-reader 角色应显式映射 platform.user_keys.read, got %v", got)
	}
	// XM-CRED0：写明文凭据与切换真实上游默认仍不进 staff（credential-admin
	// 角色继续独立授予）。XM-LOGIN（2026-08-30）把这两项加进了 admin——
	// 见 DefaultRoleScopeMap 第 6 条：本地登录的 bootstrap 管理员需要一上线
	// 就能管凭据/连接器，不必先登录再手动申请第二个角色。credential-admin
	// 的最小面语义不受影响，两条路径并存。
	for _, sc := range []string{"credential.manage", "connector.manage"} {
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s：凭据写入与连接器切换必须独立授予", sc)
		}
		if got := m["credential-admin"]; !slices.Contains(got, sc) {
			t.Fatalf("专门的 credential-admin 角色应显式映射 %s, got %v", sc, got)
		}
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应含 %s（XM-LOGIN：bootstrap 管理员需要一上线就能用）, got %v", sc, admin)
		}
	}
	// XM-CARD0（2026-09-04）：卡片四权限。admin 全给，staff 一个都不给。
	//
	// 给 admin 的理由与上面 credential.manage 同一条：本地登录的 bootstrap
	// 管理员就是运营负责人，不给就等于功能对唯一能用它的人 403，而平台今天
	// 没有第二个角色可申请。card.reveal（明文卡面）一并给——它与 card.issue
	// 的分档意义在**对外开放时不下放**，那时外部用户走的是另一个角色，
	// 不是今天这张表。
	for _, sc := range []string{"card.read", "card.issue", "card.manage", "card.reveal"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应含 %s（否则卡片管理对唯一的管理员也是 403）, got %v", sc, admin)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s：开卡花真钱、卡面是明文，必须独立授予", sc)
		}
	}
	// XM-CARD6（2026-09-05）：提现权限**不给 admin**。
	//
	// 与卡片四权限的处理刻意不同。卡片那四个给 admin 的理由是「不给就等于
	// 功能对唯一能用它的人 403」；提现不适用同一条理由——它把钱转出平台、
	// 不可逆，而 admin 是日常操作账号。一个被盗用的 admin 会话不该能把
	// 资金池搬空。所以它由专门的 fund-operator 角色持有，需要人显式授予。
	//
	// 这与「定义了权限却没挂到任何角色」（那是 bug，功能对所有人 403）
	// 是两回事：这里挂在一个真实存在、可以被指派的角色上。
	for _, sc := range []string{"fund.withdraw", "fund.address.manage"} {
		if slices.Contains(admin, sc) {
			t.Fatalf("admin 不该含 %s：提现不可逆，日常操作账号不该持有它", sc)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 更不该含 %s", sc)
		}
		if got := m["fund-operator"]; !slices.Contains(got, sc) {
			t.Fatalf("fund-operator 角色应持有 %s，否则这个功能没有任何角色能用, got %v", sc, got)
		}
	}

	// XM-SMS0（2026-09-05）：接码中心的四个权限。
	//
	// **sms.purchase 不给 admin**，与 fund.withdraw 同一条理由：它花真钱
	// 且不可退（买到的号就是买到了），而 admin 是日常操作账号。由专门的
	// sms-operator 角色持有。
	//
	// 其余三个给 admin：看清单、看号码、连接测试与人工核对都是日常运营
	// 要做的事，不给就等于这个功能对唯一能用它的人 403——这正是卡片上线
	// 当天撞上的那件事。
	for _, sc := range []string{"sms.read", "sms.reveal", "sms.manage"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应持有 %s，否则接码页对唯一能用它的人是 403", sc)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 不该持有 %s", sc)
		}
	}
	if slices.Contains(admin, "sms.purchase") {
		t.Fatal("admin 不该持有 sms.purchase：买号花真钱，日常操作账号不该带它")
	}
	if got := m["sms-operator"]; !slices.Contains(got, "sms.purchase") {
		t.Fatalf("sms-operator 应持有 sms.purchase，否则这个功能没有任何角色能用, got %v", got)
	}

	// XM-EXT-PUBLISHING（2026-09-08）：内容发布的三个权限。
	//
	// **publishing.publish 不给 admin**，与 fund.withdraw / sms.purchase 同一条
	// 理由：对外发布不可逆、而且是公开的（删了也已经被抓取、被截图）。
	// 「谁能以公司的名义说话」是一次显式的组织授予，不该由「他是管理员」
	// 顺带获得。读与编辑给 admin，否则这一页对唯一能用它的人也是 403。
	for _, sc := range []string{"publishing.read", "publishing.manage"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应持有 %s，否则内容发布页对唯一能用它的人是 403", sc)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 不该持有 %s：草稿正文与渠道登记不是看板数据", sc)
		}
	}
	if slices.Contains(admin, "publishing.publish") {
		t.Fatal("admin 不该持有 publishing.publish：对外发布不可逆，日常操作账号不该带它")
	}
	if slices.Contains(staff, "publishing.publish") {
		t.Fatal("staff 更不该持有 publishing.publish")
	}
	// 挂在一个真实存在、可以被指派的角色上——否则「不给 admin」就成了
	// 「谁都用不了」（那是 bug，不是最小权限）。
	if got := m["content-publisher"]; !slices.Contains(got, "publishing.publish") {
		t.Fatalf("content-publisher 应持有 publishing.publish，否则这个功能没有任何角色能用, got %v", got)
	}
	// 发布人也要看得见自己要发的东西，否则他必须同时被授予 admin 才用得起来。
	if got := m["content-publisher"]; !slices.Contains(got, "publishing.read") {
		t.Fatalf("content-publisher 应持有 publishing.read, got %v", got)
	}
	// 但发布人**不该**顺带拿到编辑权：改稿与发稿是两件事。
	if slices.Contains(m["content-publisher"], "publishing.manage") {
		t.Fatal("content-publisher 不该持有 publishing.manage：改稿与发稿刻意分开")
	}

	// XM-CARD6（2026-09-05 改）：额度从环境变量搬进数据库、由后台调整之后，
	// 「改不了」这道物理屏障没有了。替代它的是**两把钥匙**：
	//   fund.limit.manage（改额度）给 admin，
	//   fund.withdraw（发起提现）给 fund-operator。
	// 被盗用的 fund-operator 抬不高自己的天花板；被盗用的 admin 抬得高
	// 天花板却提不了现。合成一个权限就再也拆不开了。
	if !slices.Contains(admin, "fund.limit.manage") {
		t.Fatal("admin 应持有 fund.limit.manage：额度要有人能调，而调它的不该是提现的那个角色")
	}
	if slices.Contains(m["fund-operator"], "fund.limit.manage") {
		t.Fatal("fund-operator 不该持有 fund.limit.manage：那等于让提现的人自己抬高自己的上限")
	}
	if slices.Contains(staff, "fund.limit.manage") {
		t.Fatal("staff 不该持有 fund.limit.manage")
	}

	// XM-LOGIN：admin 管理本地登录账号，staff 不该有这个能力。
	if !slices.Contains(admin, "staff.manage") {
		t.Fatalf("admin 应含 staff.manage（XM-LOGIN 账号管理）, got %v", admin)
	}
	if slices.Contains(staff, "staff.manage") {
		t.Fatal("staff 默认不该含 staff.manage：账号管理是管理员能力")
	}
	for _, sc := range staff {
		if strings.Contains(sc, ".manage") && sc != "ui.saved_view.manage" {
			t.Fatalf("staff 默认不该含写权限: %q", sc)
		}
	}
	// 改这张表意味着改「登录进来的人默认能做什么」——不是重构，是授权决定
	// XM-0039：请求元数据与请求正文分两档，**admin 只拿前者**。
	//
	// 这条断言钉的是一次产品裁定（2026-08-28 验收）：看全平台用户对话正文的
	// 应该是显式授权的客诉/风控岗，不是每个管理员顺带获得的能力。
	// admin 角色今天在 Realm 里还不存在，所以这一行不会命中——正因为不会命中，
	// 才更需要一条测试拦住「以后顺手加回去」：没有人会回过头质疑一张已经跑了
	// 半年的默认表。要授予就用 XM_OIDC_ROLE_SCOPES 显式配一个专门的角色。
	if !slices.Contains(admin, "platform.users.read") {
		t.Fatalf("admin 应含 platform.users.read（用户管理页签的读权限）, got %v", admin)
	}
	if !slices.Contains(admin, "request.read") {
		t.Fatalf("admin 应含 request.read（请求元数据列表）, got %v", admin)
	}
	if !slices.Contains(admin, "ui.saved_view.manage") {
		t.Fatalf("admin 应含仅限自己的个人 SavedView 权限, got %v", admin)
	}
	if slices.Contains(staff, "finance.platform_channel_binding.manage") || slices.Contains(admin, "finance.platform_channel_binding.manage") {
		t.Fatal("渠道绑定 L1 写权限必须由人工显式授予，不能进入默认角色")
	}
	// XM-SERVER0：服务器登记簿的写权限**不属于**渠道绑定那一类必须人工显式
	// 授予的高风险面——它写的是主机名/规格/供应商/到期日这类纯记录字段
	// （拍板「服务器只做记录」），不触碰第三方系统、不影响成本或收入归属，
	// 与已经默认给 admin 的 finance.upstream_account.manage 同一档（登记簿，
	// 不是执行）。所以这里断言的方向与上面渠道绑定那条**相反**：admin 应该
	// 含它，staff 不该含它。
	if !slices.Contains(admin, "server.manage") {
		t.Fatal("admin 应含 server.manage：服务器登记簿是纯记录字段，" +
			"不触碰第三方系统，与 finance.upstream_account.manage 同一档，" +
			"不属于渠道绑定那类必须人工显式授予的高风险面")
	}
	if slices.Contains(staff, "server.manage") {
		t.Fatal("staff 默认不该含 server.manage：写权限只给 admin 与显式授权角色")
	}
	// XM-EXT-APP：前端应用登记簿同上一条同一档——纯记录字段，且平台没有
	// 发布通道（发布是 Platform Lifecycle Operation），改这张表不会让任何
	// 站点发生变化。方向同样是 admin 含、staff 不含。
	if !slices.Contains(admin, "extapp.manage") {
		t.Fatal("admin 应含 extapp.manage：前端应用登记簿是纯记录字段，" +
			"与 server.manage 同一档；不给的话这个功能对唯一的管理员也是 403")
	}
	if slices.Contains(staff, "extapp.manage") {
		t.Fatal("staff 默认不该含 extapp.manage：写权限只给 admin 与显式授权角色")
	}
	if slices.Contains(admin, "request.content.read") {
		t.Fatal("admin 默认**不该**含 request.content.read：" +
			"用户与模型的完整对话要显式授权给客诉/风控岗，" +
			"「admin 是全权角色」不构成理由（见 DefaultRoleScopeMap 注释第 4 条）")
	}
	// staff 更不该有这两项里的任何一项
	for _, sc := range []string{"request.read", "request.content.read"} {
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s", sc)
		}
	}
	// XM-EXT-INTEGRATION（2026-09-08）：「接口与自动化」两张登记簿的读写。
	//
	// 方向与 server.manage 那条相同：两个都给 admin（登记簿是纯记录字段，
	// 调用方登记簿不是授权面、规则登记簿没有执行器），两个都不给 staff。
	//
	// **读侧也不给 staff** 是这里与 registry.read 分家的全部理由：
	// integration.read 返回的是「哪些机器身份该来调我们、期望持有哪些
	// scope」外加 action_run 里观测到的调用方——那是一张授权面的地图，
	// 看板角色不该顺带拿到。
	for _, sc := range []string{"integration.read", "integration.manage"} {
		if !slices.Contains(admin, sc) {
			t.Fatalf("admin 应含 %s：登记簿是纯记录字段（不发凭据、不授权、无执行器），"+
				"不给就等于「接口与自动化」页对唯一能用它的人 403, got %v", sc, admin)
		}
		if slices.Contains(staff, sc) {
			t.Fatalf("staff 默认不该含 %s：调用方登记簿是一张授权面的地图，"+
				"看板角色不该顺带拿到", sc)
		}
	}
}
