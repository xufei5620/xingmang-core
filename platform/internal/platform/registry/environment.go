package registry

import "fmt"

// Environment 是部署环境（规格 §20.5：每个对象显式绑定，生产权限不继承）。
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// ParseEnvironment 解析环境名；只接受三个精确小写值。
func ParseEnvironment(s string) (Environment, error) {
	switch Environment(s) {
	case EnvDevelopment, EnvStaging, EnvProduction:
		return Environment(s), nil
	default:
		return "", fmt.Errorf("environment %q: %w", s, ErrInvalidEnvironment)
	}
}

func (e Environment) String() string { return string(e) }

// IsProduction 用于风险判定（高风险动作在生产环境需额外控制）。
func (e Environment) IsProduction() bool { return e == EnvProduction }

// AllEnvironments 返回全部合法环境，供种子数据与枚举校验使用。
func AllEnvironments() []Environment {
	return []Environment{EnvDevelopment, EnvStaging, EnvProduction}
}
