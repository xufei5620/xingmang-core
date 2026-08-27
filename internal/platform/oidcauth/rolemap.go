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
//     （交接文档 §9.4 把它列为高敏数据）。
//
// ⚠️ **admin 这一行里的 request.content.read 是本文件里最该被审定推翻的一项。**
// 它写在这里的理由只是「admin 是全权角色，全权就该包含它」，而这个理由在
// 「全平台用户对话内容」面前不一定站得住：真实运营里需要看正文的往往是少数
// 客诉/风控岗，不是每个管理员。Realm 里真要创建 admin 角色的那天，
// 正确做法多半是把这一项拆到一个单独的角色上，用 XM_OIDC_ROLE_SCOPES 覆盖。
//
// 结果：CR-0001 执行完当天切过来，员工能登录、能看服务清单与运营指标；审计页、
// 请求列表、请求正文与写操作都会 403，直到有人显式授权。
// Fail Closed 比「先放开再收」便宜得多。
func DefaultRoleScopeMap() map[string][]string {
	return map[string][]string{
		"staff": {"registry.read", "ops.read"},
		"admin": {
			"registry.read",
			"ops.read",
			"audit.read",
			"registry.service.manage",
			"registry.connector.manage",
			"registry.connection.manage",
			"request.read",
			"request.content.read",
		},
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
