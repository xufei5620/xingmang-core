package assurance

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Target 是一次检测任务声明里的一个探测目标（设计稿 §1.2.2）。
//
// channel_id / external_channel_id 必须能在渠道目录里查到（Handler 在
// declare@1 里回查，见 store.go CheckChannelExists 的注释：本仓库的渠道
// 目录——core.connector_config 关联的 <platform>.channels.status ops
// 观测——里的 channel_id 字段实际就是上游自己的账号 ID，与
// external_channel_id 是同一个 ID 空间（XM-0052 已经发现"内部 channel_id"
// 与这套 ID 互不相认，见 docs/handoffs/slices/XM-CHAN-MERGE0-*.md），所以
// 本包只对 channel_id 做存在性校验；external_channel_id 是可选的展示态
// 冗余字段，不做二次校验。
type Target struct {
	ChannelID         string `json:"channel_id"`
	ExternalChannelID string `json:"external_channel_id,omitempty"`
	Model             string `json:"model"`
}

// Validate 校验单个 Target 的必填字段。
func (t Target) Validate() error {
	if strings.TrimSpace(t.ChannelID) == "" {
		return fmt.Errorf("targets[].channel_id 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(t.Model) == "" {
		return fmt.Errorf("targets[].model 为空: %w", ErrInvalidInput)
	}
	return nil
}

// ExpectedShape 是浅层断言的声明（设计稿 §1.2.3）：只做形状/长度/超时层面的
// 断言，不做语义正确性判断（§7.5）。
type ExpectedShape struct {
	MinLength   int      `json:"min_length"`
	MaxLength   int      `json:"max_length"`
	MustContain []string `json:"must_contain,omitempty"`
	TimeoutMS   int      `json:"timeout_ms"`
}

// MaxExpectedTimeoutMS 是 expected_shape.timeout_ms 的声明上限。
//
// **不是** Job 实际执行时用的硬超时（那个固定 30s，见
// jobs/assurance_probe.go 的 perTargetTimeout，设计稿 §3.2 步骤 5）——这里
// 限的是声明时"运营期望多久内应该有响应"这个展示态数字不能声明得比 Job
// 的硬顶还宽，否则会出现"没超时却被断言成超时"的自相矛盾配置。
const MaxExpectedTimeoutMS = 30_000

// Validate 校验 ExpectedShape 的取值范围。
func (e ExpectedShape) Validate() error {
	if e.MinLength < 0 || e.MaxLength <= 0 || e.MinLength > e.MaxLength {
		return fmt.Errorf("expected_shape.min_length/max_length 不合法: %w", ErrInvalidInput)
	}
	if e.TimeoutMS <= 0 || e.TimeoutMS > MaxExpectedTimeoutMS {
		return fmt.Errorf("expected_shape.timeout_ms 必须在 (0, %d] 内: %w", MaxExpectedTimeoutMS, ErrInvalidInput)
	}
	return nil
}

// ParseTargets / ParseExpectedShape 把 Action 参数里的 JSON 字符串解成结构。
//
// **为什么是 JSON 字符串而不是 Action Schema 的原生数组/对象类型**：
// internal/platform/action.Schema 今天只支持四种字段类型（string/int/bool/
// string_slice，见 action/schema.go），没有"对象数组"或"嵌套对象"——设计稿
// 里 `targets: array<...>`/`expected_shape: object` 是数据模型层面的形状，
// ADR-019"边界与后果"一节明确写着这份 JSON 结构是设计稿、字段名/结构在
// 实现前仍可能被验收线要求调整，因此这里选择向内核已有的原语靠拢（与
// connector.config.set@1 把 target_allowlist 编码成逗号分隔字符串是同一类
// 折衷），而不是给 Schema 新增一种字段类型去匹配一份未冻结的草稿。
func ParseTargets(raw string) ([]Target, error) {
	var out []Target
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("targets 不是合法 JSON 数组: %v: %w", err, ErrInvalidInput)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("targets 不能为空: %w", ErrInvalidInput)
	}
	for i, t := range out {
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
	}
	return out, nil
}

// ParseExpectedShape 解析 expected_shape 的 JSON 字符串。
func ParseExpectedShape(raw string) (ExpectedShape, error) {
	var out ExpectedShape
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ExpectedShape{}, fmt.Errorf("expected_shape 不是合法 JSON 对象: %v: %w", err, ErrInvalidInput)
	}
	if err := out.Validate(); err != nil {
		return ExpectedShape{}, err
	}
	return out, nil
}

// Declaration 是 assurance.probe_declaration 的一行。
type Declaration struct {
	ID                string
	Platform          string
	Environment       string
	Name              string
	PromptTemplateKey string
	TargetHost        string
	Targets           []Target
	MaxTokens         int
	ExpectedShape     ExpectedShape
	ScheduleCron      string
	Status            string
	Version           int
	CreatedAt         time.Time
	CreatedBy         string
	UpdatedAt         time.Time
	UpdatedBy         string
	CancelledAt       *time.Time
	CancelledBy       string
	CancelReason      string
}

// Run 是 assurance.probe_run 的一行。
type Run struct {
	ID            string
	DeclarationID string
	Platform      string
	Environment   string
	Trigger       string
	RequestedBy   string
	ClientRunKey  string
	Status        string
	RefusalReason string
	ActionRunID   string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
}

// Result 是 assurance.probe_result 的一行。
//
// **严禁**任何存储被探测模型原始返回文本的字段（威胁模型 §7.1）：Verdict/
// ErrorKind 必须是探测代码计算好的结构化结论。
type Result struct {
	ID                 string
	RunID              string
	ChannelID          string
	ExternalChannelID  string
	Model              string
	Status             string
	Verdict            string
	LatencyMS          *int
	FirstTokenMS       *int
	MeasuredFirstToken bool
	TokensUsed         *int
	HTTPStatus         *int
	ErrorKind          string
	EvidenceRef        string
	ObservedAt         time.Time
	CreatedAt          time.Time
}
