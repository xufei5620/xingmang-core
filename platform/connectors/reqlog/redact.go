package reqlog

import (
	"net"
	"net/netip"
	"strings"
	"unicode/utf8"
)

// MaskedIPSuffix 是被抹掉那一段的占位符。
const MaskedIPSuffix = "x"

// UnparsedIPPlaceholder 是解析不出 IP 时的返回值。
//
// 为什么不返回空串（那样界面显示「—」，最省事）：空串的意思是「reqlog 没记
// IP」，而解析失败的意思是「reqlog 记了，但形状和我们以为的不一样」。后者在
// 本契约还是 DRAFT 的阶段是一个**要有人看一眼**的信号——真实 API 若把 IP
// 记成 `[2001:db8::1]:443` 或 `1.2.3.4, 5.6.7.8`（经过几层反代的
// X-Forwarded-For），整列会齐刷刷变成这个值，一眼就能发现。
//
// 为什么不返回原值：那就等于没有脱敏。解析失败**从不**回退到透传（宪法 7 条
// 的同一条思路：拦不住就拒绝，不放行）。
const UnparsedIPPlaceholder = "invalid-ip"

// MaskIP 按契约口径脱敏客户端 IP。
//
// 口径（本函数是唯一实现，契约文档 §IP 脱敏抄的是这里）：
//
//	IPv4  保留前三段，末段抹掉        203.0.113.42        → 203.0.113.x
//	IPv6  保留前 48 位（前三组），其余抹掉  2001:db8:1234:5678::1 → 2001:db8:1234:x
//	带端口 先剥端口再按上面处理       203.0.113.42:51820  → 203.0.113.x
//	空串   返回空串（「没记」与「记了但看不清」是两回事）
//	其他   返回 UnparsedIPPlaceholder
//
// 为什么保留到 /24 而不是抹得更狠：运营要回答的问题里有「是不是同一个网段在
// 刷」，/24 刚好够回答它；再往前抹一段，这个问题就只能靠翻 reqlog 原始控制台
// 解决——而那条路绕开了平台的权限与审计，把人赶过去是最坏的结果。
//
// 为什么不做成可配置：一个「脱敏强度」开关的默认值迟早会被人为了排查方便调到
// 最松，然后留在那里。口径要变就改这里、改契约文档、过一次评审。
func MaskIP(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// 先试着剥端口。SplitHostPort 对不带端口的裸 IPv6（"2001:db8::1"）会报错，
	// 所以失败时保留原串继续往下走，而不是当场判为非法。
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	// 去掉 IPv6 字面量的方括号：SplitHostPort 只在带端口时剥它。
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")

	addr, err := netip.ParseAddr(s)
	if err != nil {
		return UnparsedIPPlaceholder
	}
	// IPv4-mapped IPv6（::ffff:203.0.113.42）按 IPv4 处理：它就是一个 IPv4 地址，
	// 按 IPv6 规则抹会得到 "::ffff:x" 这种既不好读也没保护到谁的东西。
	addr = addr.Unmap()

	if addr.Is4() {
		octets := addr.As4()
		return joinMasked([]string{
			itoa(octets[0]), itoa(octets[1]), itoa(octets[2]),
		}, ".")
	}

	// IPv6：取前三组。addr.StringExpanded() 给的是固定八组的完整写法，
	// 按它切才不会被 "::" 压缩形态骗到（"2001:db8::1" 的第三组是 0，
	// 而不是字符串里下一个出现的那个数）。
	groups := strings.Split(addr.StringExpanded(), ":")
	if len(groups) < 3 {
		return UnparsedIPPlaceholder
	}
	kept := make([]string, 0, 3)
	for _, g := range groups[:3] {
		// 去掉前导零，回到人眼熟悉的写法（0001 → 1，0000 → 0）
		trimmed := strings.TrimLeft(g, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		kept = append(kept, trimmed)
	}
	return joinMasked(kept, ":")
}

func joinMasked(kept []string, sep string) string {
	return strings.Join(kept, sep) + sep + MaskedIPSuffix
}

// itoa 是 strconv.Itoa 对 byte 的小包装，避免调用处到处写类型转换。
func itoa(b byte) string {
	if b == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for b > 0 {
		i--
		buf[i] = '0' + b%10
		b /= 10
	}
	return string(buf[i:])
}

// IsPathSafeID 判断记录标识能不能原样放进 URL 的一个路径段。
//
// 只放行 RFC 3986 的 unreserved 字符集（字母、数字、`-._~`）。比「转义一下就行」
// 严得多，理由是转义在这条链路上靠不住：id 要穿过前端 encodeURIComponent、
// 浏览器地址栏、反向代理、chi 的路由匹配，中间任何一环对 `%2F` 的处理与别处
// 不一致，症状都是「这条记录明明在列表里，点进去却 404」。
//
// 与其在四个地方各写一遍转义并祈祷它们一致，不如让 id 从源头就不需要转义。
func IsPathSafeID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '.', r == '_', r == '~':
		default:
			return false
		}
	}
	return true
}

// TruncateUTF8 把 s 截到不超过 max 字节，并返回是否发生了截断与原始字节数。
//
// 关键是**不切开一个字符**：直接 s[:max] 会在多字节字符中间下刀，产出一个
// 尾部是替换符（U+FFFD）的字符串。对一段中文对话而言那只是难看，但对
// JSON 编码而言它是一个合法的替换字符，会静静地混进内容里——排查的人
// 会以为上游真的返回了乱码。
//
// max <= 0 表示不截断（调用方显式表达「不限」，而不是「限成 0」）。
func TruncateUTF8(s string, max int) (out string, truncated bool, originalBytes int64) {
	originalBytes = int64(len(s))
	if max <= 0 || len(s) <= max {
		return s, false, originalBytes
	}
	cut := max
	// 从 max 往回退，退到一个 rune 起始位置为止。UTF-8 的续字节形如
	// 10xxxxxx，最多连续 3 个，所以这个循环最多走 3 步。
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true, originalBytes
}
