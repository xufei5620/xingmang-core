package registry

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// capabilityPattern：<系统>.<资源>.<动作>，允许 3~4 段小写点分。
var capabilityPattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9_]+){2,3}$`)

// writeVerbs 是判定「写能力」的动作词集合（ADR-004：写通道需 Kill Switch）。
var writeVerbs = map[string]struct{}{
	"import": {}, "create": {}, "update": {}, "delete": {}, "manage": {},
	"assign_group": {}, "disable": {}, "enable": {}, "approve": {}, "reject": {},
	"attach": {}, "freeze": {}, "resolve": {}, "restart": {}, "publish": {},
	"refund": {}, "switch": {}, "execute": {},
}

// semverPattern 用于 Connector 版本（精确三段语义化版本）。
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Capability 是 Connector 当前支持的一项能力（规格 §8.2）。
type Capability string

// ParseCapability 解析并校验能力名。
func ParseCapability(s string) (Capability, error) {
	if s == "" {
		return "", fmt.Errorf("capability: %w", ErrMissingField)
	}
	if !capabilityPattern.MatchString(s) {
		return "", fmt.Errorf("capability=%q 须形如 <system>.<resource>.<action>: %w", s, ErrInvalidFormat)
	}
	return Capability(s), nil
}

func (c Capability) String() string { return string(c) }

// IsWrite 判定该能力是否会改变外部系统状态。
func (c Capability) IsWrite() bool {
	parts := strings.Split(string(c), ".")
	if len(parts) < 3 {
		return false
	}
	_, ok := writeVerbs[parts[len(parts)-1]]
	return ok
}

func validateCapabilities(field string, caps []Capability, wantWrite *bool) error {
	for _, c := range caps {
		if _, err := ParseCapability(string(c)); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		if wantWrite != nil && c.IsWrite() != *wantWrite {
			return fmt.Errorf("%s 含能力 %q 与集合语义不符（期望 is_write=%v）: %w",
				field, c, *wantWrite, ErrInvalidFormat)
		}
	}
	return nil
}

func validateAllowlist(field string, entries []string) error {
	if len(entries) == 0 {
		return fmt.Errorf("%s: %w", field, ErrAllowlistRequired)
	}
	for i, e := range entries {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("%s[%d] 为空: %w", field, i, ErrInvalidFormat)
		}
		if strings.Contains(e, "/") || strings.Contains(e, " ") {
			return fmt.Errorf("%s[%d]=%q 应为主机名而非 URL: %w", field, i, e, ErrInvalidFormat)
		}
	}
	return nil
}

// Connector 是某类被管理系统的连接实现登记（规格 §2.2 Connector Registry）。
type Connector struct {
	ID                        uuid.UUID
	Key                       string // sub2api / newapi / cpa ...
	Version                   string // 精确语义化版本
	ContractVersion           string // 契约版本号
	ConnectionSchemaPath      string // contracts/connectors/*.json
	TargetAllowlist           []string
	ReadCapabilities          []Capability
	WriteCapabilities         []Capability
	SupportedUpstreamVersions []string
	CompatibilityTestPath     string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// Validate 校验 Connector 的领域不变量（ADR-004）。
func (c Connector) Validate() error {
	if err := ValidateIdentifier("key", c.Key); err != nil {
		return err
	}
	if err := requireNonEmpty("version", c.Version); err != nil {
		return err
	}
	if !semverPattern.MatchString(c.Version) {
		return fmt.Errorf("version=%q 须为精确三段语义化版本: %w", c.Version, ErrInvalidFormat)
	}
	if err := requireNonEmpty("contract_version", c.ContractVersion); err != nil {
		return err
	}
	if err := requireNonEmpty("connection_schema_path", c.ConnectionSchemaPath); err != nil {
		return err
	}
	if err := validateAllowlist("target_allowlist", c.TargetAllowlist); err != nil {
		return err
	}
	no, yes := false, true
	if err := validateCapabilities("read_capabilities", c.ReadCapabilities, &no); err != nil {
		return err
	}
	if err := validateCapabilities("write_capabilities", c.WriteCapabilities, &yes); err != nil {
		return err
	}
	seen := make(map[Capability]struct{}, len(c.ReadCapabilities))
	for _, r := range c.ReadCapabilities {
		seen[r] = struct{}{}
	}
	for _, w := range c.WriteCapabilities {
		if _, dup := seen[w]; dup {
			return fmt.Errorf("能力 %q 同时出现在读写集合: %w", w, ErrInvalidFormat)
		}
	}
	return nil
}

// ConnectionStatus 是一条实际连接的状态。
type ConnectionStatus string

const (
	ConnectionEnabled  ConnectionStatus = "enabled"
	ConnectionDisabled ConnectionStatus = "disabled"
	ConnectionKilled   ConnectionStatus = "killed" // Kill Switch 已拉闸
)

// ParseConnectionStatus 解析连接状态。
func ParseConnectionStatus(s string) (ConnectionStatus, error) {
	switch ConnectionStatus(s) {
	case ConnectionEnabled, ConnectionDisabled, ConnectionKilled:
		return ConnectionStatus(s), nil
	default:
		return "", fmt.Errorf("connection status %q: %w", s, ErrInvalidStatus)
	}
}

// Connection 是 Connector 到某个 Service 实例的实际连接（规格 §2.3）。
// 只保存 CredentialRef，绝不保存明文凭据。
type Connection struct {
	ID          uuid.UUID
	ConnectorID uuid.UUID
	ServiceID   uuid.UUID
	Environment Environment

	CredentialRef       string // secret://<scope>/<name>
	TargetAllowlist     []string
	GrantedCapabilities []Capability
	KillSwitch          string // 具写能力时必填

	Status                  ConnectionStatus
	DetectedUpstreamVersion string
	VersionFingerprint      string
	LastVerifiedAt          *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasWriteCapability 判断本连接是否被授予任何写能力。
func (c Connection) HasWriteCapability() bool {
	for _, capability := range c.GrantedCapabilities {
		if capability.IsWrite() {
			return true
		}
	}
	return false
}

// Validate 校验 Connection 的领域不变量（ADR-004、ADR-014、宪法 7 条）。
func (c Connection) Validate() error {
	if c.ConnectorID == uuid.Nil {
		return fmt.Errorf("connector_id: %w", ErrMissingField)
	}
	if c.ServiceID == uuid.Nil {
		return fmt.Errorf("service_id: %w", ErrMissingField)
	}
	if _, err := ParseEnvironment(string(c.Environment)); err != nil {
		return err
	}
	if err := requireNonEmpty("credential_ref", c.CredentialRef); err != nil {
		return err
	}
	if _, err := secrets.ParseCredentialRef(c.CredentialRef); err != nil {
		return fmt.Errorf("credential_ref=%q（只接受 secret://<scope>/<name>）: %w",
			c.CredentialRef, ErrInvalidCredentialRef)
	}
	if err := validateAllowlist("target_allowlist", c.TargetAllowlist); err != nil {
		return err
	}
	if err := validateCapabilities("granted_capabilities", c.GrantedCapabilities, nil); err != nil {
		return err
	}
	if c.HasWriteCapability() && strings.TrimSpace(c.KillSwitch) == "" {
		return fmt.Errorf("granted_capabilities 含写能力: %w", ErrKillSwitchRequired)
	}
	if _, err := ParseConnectionStatus(string(c.Status)); err != nil {
		return err
	}
	return nil
}
