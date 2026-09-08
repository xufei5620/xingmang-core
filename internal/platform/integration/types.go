package integration

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

var (
	// ErrNotFound：登记不存在（或不在调用者的环境里）。
	ErrNotFound = errors.New("integration: 登记不存在")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("integration: 必填字段为空")
	// ErrInvalidFormat：字段格式非法（凭据引用、状态、触发类型…）。
	ErrInvalidFormat = errors.New("integration: 字段格式非法")
	// ErrDuplicate：唯一键冲突（同环境下 principal_id 或规则名重复）。
	ErrDuplicate = errors.New("integration: 该登记已存在")
)

// ClientStatus 是调用方登记的状态。
//
// **只描述登记状态，不描述放行状态**：ClientDisabled 不会让该身份的请求被
// 拒绝（见 doc.go 第 1 条）。取值刻意只有两个——一个「暂停中」之类的第三态
// 会立刻让人以为它有运行期含义。
type ClientStatus string

const (
	ClientActive   ClientStatus = "active"
	ClientDisabled ClientStatus = "disabled"
)

// ParseClientStatus 解析调用方状态；闭集，不认识的值直接拒。
func ParseClientStatus(s string) (ClientStatus, error) {
	switch ClientStatus(s) {
	case ClientActive, ClientDisabled:
		return ClientStatus(s), nil
	default:
		return "", fmt.Errorf("status %q 只能是 active / disabled: %w", s, ErrInvalidFormat)
	}
}

// APIClient 是一条 API 调用方登记。
type APIClient struct {
	ID            uuid.UUID
	PrincipalID   string
	PrincipalType principal.Type
	DisplayName   string
	Purpose       string
	Owner         string
	// ExpectedScopes 是**期望**的权限范围，不是生效的权限范围。
	// 字段名带 Expected 前缀是刻意的：叫 Scopes 会让读代码的人以为它参与鉴权。
	ExpectedScopes []string
	// CredentialRef 空串 = 未登记凭据引用。非空时一定是合法的
	// secret://<scope>/<name>——校验在 Validate 里，库层 CHECK 是第二道。
	CredentialRef string
	Status        ClientStatus
	Notes         string
	Environment   string
	CreatedAt     time.Time
	CreatedBy     string
	UpdatedAt     time.Time
	UpdatedBy     string
}

// Validate 校验一条调用方登记的必填与格式。
func (c APIClient) Validate() error {
	if strings.TrimSpace(c.PrincipalID) == "" {
		return fmt.Errorf("principal_id: %w", ErrMissingField)
	}
	if _, err := principal.ParseType(string(c.PrincipalType)); err != nil {
		return fmt.Errorf("principal_type: %w", ErrInvalidFormat)
	}
	if strings.TrimSpace(c.DisplayName) == "" {
		return fmt.Errorf("display_name: %w", ErrMissingField)
	}
	if _, err := ParseClientStatus(string(c.Status)); err != nil {
		return err
	}
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	// 凭据引用只校验**形状**，绝不解析它的值：本包一行读明文的代码都没有
	// （宪法 7 条）。空串是合法的——员工账号这类调用方本来就没有 API Key。
	if ref := strings.TrimSpace(c.CredentialRef); ref != "" {
		if _, err := secrets.ParseCredentialRef(ref); err != nil {
			return fmt.Errorf("credential_ref 必须形如 secret://<scope>/<name>: %w", ErrInvalidFormat)
		}
	}
	for _, s := range c.ExpectedScopes {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("expected_scopes 含空项: %w", ErrInvalidFormat)
		}
	}
	return nil
}

// RuleStatus 是自动化规则登记的状态。
//
// 三个取值**都不会让任何 Action 跑起来**——它们描述的是这条登记处在编写、
// 已定稿、还是已作废，不是运行状态（见 doc.go 第 2 条）。刻意不用
// enabled/active 这类词：那会让「启用」看起来像一个运行期开关。
type RuleStatus string

const (
	// RuleDraft：还在写。
	RuleDraft RuleStatus = "draft"
	// RuleRegistered：已定稿的登记。**仍然不会执行**。
	RuleRegistered RuleStatus = "registered"
	// RuleDisabled：已作废的登记。
	RuleDisabled RuleStatus = "disabled"
)

// ParseRuleStatus 解析规则登记状态；闭集。
func ParseRuleStatus(s string) (RuleStatus, error) {
	switch RuleStatus(s) {
	case RuleDraft, RuleRegistered, RuleDisabled:
		return RuleStatus(s), nil
	default:
		return "", fmt.Errorf("status %q 只能是 draft / registered / disabled: %w", s, ErrInvalidFormat)
	}
}

// TriggerKind 是规则**登记**的触发条件类别。
//
// 它只被记录，不被订阅、不被轮询、不被监听：本包没有任何代码会因为
// TriggerSchedule 就去起一个定时器。
type TriggerKind string

const (
	TriggerManual   TriggerKind = "manual"
	TriggerSchedule TriggerKind = "schedule"
	TriggerEvent    TriggerKind = "event"
	TriggerWebhook  TriggerKind = "webhook"
)

// ParseTriggerKind 解析触发类别；闭集。
func ParseTriggerKind(s string) (TriggerKind, error) {
	switch TriggerKind(s) {
	case TriggerManual, TriggerSchedule, TriggerEvent, TriggerWebhook:
		return TriggerKind(s), nil
	default:
		return "", fmt.Errorf("trigger_kind %q 只能是 manual / schedule / event / webhook: %w", s, ErrInvalidFormat)
	}
}

// AutomationRule 是一条自动化规则登记。
type AutomationRule struct {
	ID          uuid.UUID
	Name        string
	Description string
	TriggerKind TriggerKind
	// TriggerDetail 是触发条件的自由文本（cron 串、事件名、来源地址…）。
	// 刻意是自由文本而不是结构化字段：结构化会让人以为它被解析过，
	// 而今天没有任何代码解析它。
	TriggerDetail string
	// TargetActionID/Version 是规则**打算**调用的 Action。没有任何代码读它
	// 去执行；是否指向一个已注册的 Action 由读取端点对照注册表后如实标出。
	TargetActionID      string
	TargetActionVersion string
	Status              RuleStatus
	Notes               string
	Environment         string
	CreatedAt           time.Time
	CreatedBy           string
	UpdatedAt           time.Time
	UpdatedBy           string
}

// Validate 校验一条规则登记的必填与格式。
func (r AutomationRule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name: %w", ErrMissingField)
	}
	if _, err := ParseTriggerKind(string(r.TriggerKind)); err != nil {
		return err
	}
	if strings.TrimSpace(r.TargetActionID) == "" {
		return fmt.Errorf("target_action_id: %w", ErrMissingField)
	}
	if strings.TrimSpace(r.TargetActionVersion) == "" {
		return fmt.Errorf("target_action_version: %w", ErrMissingField)
	}
	if _, err := ParseRuleStatus(string(r.Status)); err != nil {
		return err
	}
	if strings.TrimSpace(r.Environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	return nil
}

// AutomaticExecution 恒为 false，并且**不是**一个配置项。
//
// 它存在的理由是让「规则不会自动执行」这句话有一个可以被端点直接序列化、
// 被测试直接断言的落点，而不是只活在文案里：httpapi 把它原样放进响应，
// 前端照它渲染。要让它变成 true，改的不是一个开关而是一次产品裁定加一整套
// 执行器与审批接线（ADMIN-IA §5.4.1）。
func AutomaticExecution() bool { return false }
