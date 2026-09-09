package localauth

import (
	"fmt"
	"net"
	"strings"
)

// AdminIPAllowlist 是管理员会话签发/步进的来源 IP 白名单（CR-0006
// XM_CONSOLE_ADMIN_IP_ALLOWLIST）。零值（未调用 ParseAdminIPAllowlist 或
// 传入空串）表示未启用——不限制任何来源，与既有"空=不限制"惯例一致
// （对齐 credentials.ConnectorConfig.TargetAllowlist 的语义）。
//
// 这是纵深防御，不是唯一防线：与断言签发端点（XM-INVCON1）各自独立校验，
// 理由见 CR-0006 正文 c 条——即便某一层配置漂移，另一层仍然把关。
type AdminIPAllowlist struct {
	nets []*net.IPNet
}

// ParseAdminIPAllowlist 解析逗号分隔的 CIDR 列表（如
// "10.0.0.0/8,203.0.113.4/32"）。空白输入返回零值（未启用）。
func ParseAdminIPAllowlist(raw string) (AdminIPAllowlist, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AdminIPAllowlist{}, nil
	}
	var nets []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return AdminIPAllowlist{}, fmt.Errorf("XM_CONSOLE_ADMIN_IP_ALLOWLIST 含非法 CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}
	if len(nets) == 0 {
		return AdminIPAllowlist{}, nil
	}
	return AdminIPAllowlist{nets: nets}, nil
}

// Enabled 判断名单是否生效（空名单＝不限制，见类型注释）。
func (a AdminIPAllowlist) Enabled() bool { return len(a.nets) > 0 }

// Allowed 判断 ip（点分十进制或 IPv6 文本）是否落在名单内。
// 未启用时总是放行；ip 解析失败（脏数据/占位符）时按 fail closed 拒绝——
// 一个连自己 IP 是什么都判断不出来的请求，不该被"看起来像放行"的默认值蒙混过关。
func (a AdminIPAllowlist) Allowed(ip string) bool {
	if !a.Enabled() {
		return true
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, n := range a.nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}
