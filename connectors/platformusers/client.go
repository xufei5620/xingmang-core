package platformusers

import (
	"context"
	"fmt"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Mode 选择 fake / real 客户端。
type Mode string

const (
	// ModeFake 使用样本数据。**当前唯一走得通的模式**。
	ModeFake Mode = "fake"
	// ModeReal 连真实上游。骨架已在,响应解析未实装(见 RealClient)。
	ModeReal Mode = "real"
)

// ParseMode 解析模式;空值按 fake。
func ParseMode(raw string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ModeFake:
		return ModeFake, nil
	case ModeReal:
		return ModeReal, nil
	default:
		return "", fmt.Errorf("platformusers: 未知模式 %q(只支持 fake / real)", raw)
	}
}

// RealConfig 是 real 模式的配置。
//
// 端点与凭据引用都在这里,但**明文凭据不在**:CredentialRef 由
// secrets.SecretProvider 在构造请求头的那一瞬才解析(ADR-014、宪法 7 条)。
type RealConfig struct {
	// Source 是平台标识(sub2api / newapi)。
	Source string
	// Endpoint 是上游只读端点,必须 https(ADR-018 闸 1)。
	Endpoint string
	// CredentialRef 形如 secret://<scope>/<name>。
	CredentialRef string
	// TargetAllowlist 是允许连接的主机精确清单(ADR-004)。
	// **留空不是「放行一切」而是「一个请求都发不出去」**——护栏 fail closed。
	TargetAllowlist []string
	// Secrets 解析 CredentialRef。
	Secrets secrets.SecretProvider
}

// RealClient 是真实上游的只读客户端**骨架**。
//
// ⚠️ **响应解析未实装,调用即返回 not_supported。**
//
// 端点普查已经确认了上游确实有这两个入口:
//
//	sub2api  GET /admin/users     管理员面板的用户清单
//	newapi   GET /api/user        NewAPI 的用户清单
//
// 但**响应字段名与分页形态尚未对着实现核对过**。此时编一组字段名填进来,
// 接入那天的人会拿到一串 bad_response,并且第一反应是「我配错了」——
// 而真正的原因是我们猜错了形状。返回 not_supported 至少把话说清楚:
// 这条链路还没接通,不是上游坏了(reqlog 连接器踩过同一个坑,做法相同)。
//
// **接入清单**(核对完这几项就能实装 ListUsers):
//
//  1. 分页形态:offset/limit 还是不透明游标?本契约用游标,若上游只有
//     offset 分页,`NextCursor` 的「不重不漏」保证不成立,要在契约里降级说明;
//  2. 字段名:用户 id / 用户名 / 邮箱 / 余额 / 状态 分别叫什么;
//  3. **余额的单位与标度**:分?元?浮点?本契约要 int64 最小货币单位,
//     上游若给浮点字符串,转换必须走定点解析而不是 ParseFloat(宪法 13 条);
//  4. 逐用户充值/消费到底有没有:原型的 warnbar 说 v1 没有。没有就让
//     `PeriodRecharge`/`PeriodConsumed` 保持 Known=false,**不要填 0**;
//  5. 状态枚举:上游的取值集合,补进 ParseUserStatus;
//  6. 联系方式字段:除邮箱外是否还有手机号——有就一并走 MaskEmail 之外的
//     打码,明文不得出连接器。
type RealClient struct {
	cfg RealConfig
}

// NewRealClient 构造真实客户端,并在构造期就把护栏校验做掉。
//
// 校验放在构造期而不是首次调用:一个配置错误的客户端应该让进程在启动时就
// 说清楚,而不是等到有人打开用户页签才报错——那时候已经有人在等着看数据了。
func NewRealClient(cfg RealConfig) (*RealClient, error) {
	source, err := ParseSource(cfg.Source)
	if err != nil {
		return nil, err
	}
	cfg.Source = source

	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.Endpoint)), "https://") {
		return nil, fmt.Errorf("platformusers: endpoint 必须是 https(ADR-018 闸 1),得到 %q", cfg.Endpoint)
	}
	if len(cfg.TargetAllowlist) == 0 {
		// 留空按「一个都不许连」处理,而不是「随便连」
		return nil, fmt.Errorf("platformusers: 目标主机白名单为空——护栏 fail closed(ADR-004)")
	}
	if strings.TrimSpace(cfg.CredentialRef) == "" {
		return nil, fmt.Errorf("platformusers: 缺少 CredentialRef(凭据只经引用,宪法 7 条)")
	}
	if !strings.HasPrefix(cfg.CredentialRef, "secret://") {
		return nil, fmt.Errorf("platformusers: CredentialRef 形状不对,应为 secret://<scope>/<name>")
	}
	if cfg.Secrets == nil {
		return nil, fmt.Errorf("platformusers: 缺少 SecretProvider")
	}
	return &RealClient{cfg: cfg}, nil
}

// ListUsers 尚未实装,返回 not_supported。
func (c *RealClient) ListUsers(_ context.Context, _ ListFilter) (UserPage, error) {
	return UserPage{}, notSupported(c.cfg.Source,
		"真实用户端点的响应形状待核对(XM-0046:见 RealClient 的接入清单)")
}

// connectorBadSource / connectorBadCursor 是 fake 侧的参数错误。
//
// 归 bad_response 而非 internal:参数是调用方给的,让它去改请求。
func connectorBadSource(source string) error {
	return connector.NewError(connector.KindBadResponse, "platformusers.list_users",
		fmt.Errorf("未知来源 %q", source))
}

func connectorBadCursor(cursor string) error {
	return connector.NewError(connector.KindBadResponse, "platformusers.list_users",
		fmt.Errorf("游标 %q 不合法", cursor))
}
