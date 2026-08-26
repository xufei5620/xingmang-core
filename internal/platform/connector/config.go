package connector

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Config 是一个 Connector 连接的配置（ADR-004：连接配置 Schema）。
//
// 只接受 CredentialRef，**不接受任何内联明文凭据**（ADR-014、宪法 7 条）。
type Config struct {
	ServiceInstanceID string
	Environment       string
	Endpoint          string // 必须 https
	CredentialRef     string // secret://<scope>/<name>
	TargetAllowlist   []string
	Timeout           time.Duration
}

// writeIntentParams 是出现在只读端点配置里就说明配置错了的查询参数。
// 只读通道的 endpoint 不该带这些。
var writeIntentParams = []string{"readonly=false", "read_only=false", "mode=rw", "sslmode=disable&rw"}

// Validate 校验只读连接配置（ADR-018 闸 1：拒绝可写连接配置）。
func (c Config) Validate() error {
	if strings.TrimSpace(c.ServiceInstanceID) == "" {
		return fmt.Errorf("service_instance_id 为空: %w", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("environment 为空: %w", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("endpoint 为空: %w", ErrInvalidConfig)
	}

	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("endpoint 无法解析: %w", ErrInvalidConfig)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("endpoint 必须是 https（实际 %q）: %w", u.Scheme, ErrInvalidConfig)
	}
	lowered := strings.ToLower(c.Endpoint)
	for _, p := range writeIntentParams {
		if strings.Contains(lowered, p) {
			return fmt.Errorf("endpoint 含可写意图参数 %q，只读通道不允许: %w", p, ErrInvalidConfig)
		}
	}

	// 凭据只能是 CredentialRef——内联 DSN、裸 token 一律拒绝
	if strings.TrimSpace(c.CredentialRef) == "" {
		return fmt.Errorf("credential_ref 为空: %w", ErrInvalidConfig)
	}
	if _, err := secrets.ParseCredentialRef(c.CredentialRef); err != nil {
		return fmt.Errorf("credential_ref 必须是 secret://<scope>/<name> 形态: %w", ErrInvalidConfig)
	}

	if len(c.TargetAllowlist) == 0 {
		return fmt.Errorf("target_allowlist 不能为空（ADR-004）: %w", ErrInvalidConfig)
	}
	// endpoint 的主机必须在自己的 allowlist 内，否则配置自相矛盾——
	// 这种配置能通过校验但一个请求都发不出去，属于最难排查的故障
	endpointHost := hostOnly(u.Host)
	found := false
	for _, h := range c.TargetAllowlist {
		if hostOnly(h) == endpointHost {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("endpoint 主机 %q 不在 target_allowlist %v 内: %w",
			endpointHost, c.TargetAllowlist, ErrInvalidConfig)
	}

	// 规格 §18.1-4：所有外部 I/O 必须有超时
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout 必须为正: %w", ErrInvalidConfig)
	}
	return nil
}

// ErrInvalidConfig：连接配置非法。
var ErrInvalidConfig = errInvalidConfig{}

type errInvalidConfig struct{}

func (errInvalidConfig) Error() string { return "invalid connector config" }
