package action

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// actionIDPattern：<域>.<资源>.<动作>，与 Capability 同族命名（规格 §8.2、§18.5）。
var actionIDPattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9_]+){2,3}$`)

// validEnvironments 与 registry.Environment 的三值一致；此处不 import registry
// 以避免循环依赖（registry 会 import action 来注册自己的 Action）。
var validEnvironments = []string{"development", "staging", "production"}

// ErrInvalidDefinition：Action 声明非法。
var ErrInvalidDefinition = errors.New("invalid action definition")

// Handler 是 Action 的实际执行体。参数已通过 Schema 校验；
// Handler 内部只做业务，不再重复做权限与环境判断。
type Handler func(ctx context.Context, params map[string]any) (any, error)

// Definition 是一个 Action 的静态声明（规格 §18.5）。
type Definition struct {
	ID             string
	Version        string
	RiskLevel      RiskLevel
	Permission     string
	Schema         Schema
	Environments   []string // 允许执行的环境；显式列举，生产不从测试继承
	PrincipalTypes []principal.Type
}

// Validate 校验声明本身是否完整合法。
func (d Definition) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("action id 为空: %w", ErrInvalidDefinition)
	}
	if !actionIDPattern.MatchString(d.ID) {
		return fmt.Errorf("action id %q 须形如 <domain>.<resource>.<verb>: %w", d.ID, ErrInvalidDefinition)
	}
	if d.Version == "" {
		return fmt.Errorf("action %s 缺少版本: %w", d.ID, ErrInvalidDefinition)
	}
	if _, err := ParseRiskLevel(string(d.RiskLevel)); err != nil {
		return fmt.Errorf("action %s: %w", d.ID, err)
	}
	if d.Permission == "" {
		return fmt.Errorf("action %s 缺少权限声明: %w", d.ID, ErrInvalidDefinition)
	}
	if len(d.Environments) == 0 {
		return fmt.Errorf("action %s 未声明允许的 Environment: %w", d.ID, ErrInvalidDefinition)
	}
	for _, e := range d.Environments {
		if !slices.Contains(validEnvironments, e) {
			return fmt.Errorf("action %s 声明了非法 environment %q: %w", d.ID, e, ErrInvalidDefinition)
		}
	}
	if len(d.PrincipalTypes) == 0 {
		return fmt.Errorf("action %s 未声明允许的 Principal 类型: %w", d.ID, ErrInvalidDefinition)
	}
	for _, pt := range d.PrincipalTypes {
		if _, err := principal.ParseType(string(pt)); err != nil {
			return fmt.Errorf("action %s: %w", d.ID, err)
		}
	}
	return nil
}

type entry struct {
	def     Definition
	handler Handler
}

// Registry 是 Action 注册表。业务模块在初始化时反向注册，
// 内核不 import 任何业务包。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry // key = id + "@" + version
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]entry)}
}

func key(id, version string) string { return id + "@" + version }

// Register 登记一个 Action；同 ID+版本重复注册会失败（版本化语义）。
func (r *Registry) Register(def Definition, h Handler) error {
	if err := def.Validate(); err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("action %s 的 handler 为 nil: %w", def.ID, ErrInvalidDefinition)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(def.ID, def.Version)
	if _, dup := r.entries[k]; dup {
		return fmt.Errorf("action %s 版本 %s 重复注册: %w", def.ID, def.Version, ErrInvalidDefinition)
	}
	r.entries[k] = entry{def: def, handler: h}
	return nil
}

// Lookup 按 ID+版本查找。
func (r *Registry) Lookup(id, version string) (Definition, Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key(id, version)]
	if !ok {
		return Definition{}, nil, false
	}
	return e.def, e.handler, true
}

// List 返回全部已注册声明（顺序不保证，调用方自行排序）。
func (r *Registry) List() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.def)
	}
	return out
}
