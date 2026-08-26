package action

import (
	"errors"
	"fmt"
)

// RiskLevel 是 Action 风险等级（ADR-003 风险等级表）。
type RiskLevel string

const (
	L0 RiskLevel = "L0" // 保存个人视图、低影响偏好
	L1 RiskLevel = "L1" // 修改低风险平台配置、确认普通告警
	L2 RiskLevel = "L2" // 批量配置、启停低风险资源
	L3 RiskLevel = "L3" // 服务切换、账号批量导入、敏感配置
	L4 RiskLevel = "L4" // 退款、生产基础设施高影响动作、开票关键动作
)

// ErrInvalidRiskLevel：风险等级非法。
var ErrInvalidRiskLevel = errors.New("invalid risk level")

// ParseRiskLevel 解析风险等级。
func ParseRiskLevel(s string) (RiskLevel, error) {
	switch RiskLevel(s) {
	case L0, L1, L2, L3, L4:
		return RiskLevel(s), nil
	default:
		return "", fmt.Errorf("risk level %q: %w", s, ErrInvalidRiskLevel)
	}
}

// RequiresAdvancedControls 判断该等级是否需要 Action Advanced Controls
// （幂等键、写后读取确认、审批、Step-up MFA、冷却、Kill Switch）。
// Foundation-A 未实现这些控制，因此 L2 及以上在本阶段一律拒绝执行。
func (r RiskLevel) RequiresAdvancedControls() bool {
	return r == L2 || r == L3 || r == L4
}
