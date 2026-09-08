package extapp

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AppStatus 是前端站点的**登记状态**。
//
// 登记簿口径，不是探活结果——本包一次都不请求站点（同 server.AssetStatus 的
// 「没有 Agent 上报就不建模在线/离线」）。三档与 core.server_asset 一致。
type AppStatus string

const (
	// AppPlanned 规划中：已经决定要做，还没上线。
	AppPlanned AppStatus = "planned"
	// AppActive 在线：正在对外提供服务。
	AppActive AppStatus = "active"
	// AppRetired 已下线：不再提供服务，登记保留供追溯。
	AppRetired AppStatus = "retired"
)

// ParseAppStatus 解析登记状态；只接受三个精确小写值。
func ParseAppStatus(s string) (AppStatus, error) {
	switch AppStatus(s) {
	case AppPlanned, AppActive, AppRetired:
		return AppStatus(s), nil
	default:
		return "", fmt.Errorf("status %q 必须是 planned / active / retired 之一: %w", s, ErrInvalidFormat)
	}
}

// AuthMode 是前端站点的登录方式。
//
// 三个取值**逐字取自 deploy/docker/web-app-config.sh 的 XM_WEB_AUTH_MODE**——
// 那是这套前端运行时真正认得的三个值（容器启动时校验，不认的直接拒绝启动），
// 不是这里现编的枚举。空串 = 未登记。
type AuthMode string

const (
	// AuthDevHeader 开发态 X-Dev-* 身份头。
	AuthDevHeader AuthMode = "dev-header"
	// AuthOIDC Keycloak 授权码登录。
	AuthOIDC AuthMode = "oidc"
	// AuthLocal 管理台自带账号密码登录（会话是 HttpOnly Cookie）。
	AuthLocal AuthMode = "local"
)

// ParseAuthMode 解析登录方式；空串合法（未登记）。
func ParseAuthMode(s string) (AuthMode, error) {
	switch AuthMode(s) {
	case "", AuthDevHeader, AuthOIDC, AuthLocal:
		return AuthMode(s), nil
	default:
		return "", fmt.Errorf("auth_mode %q 必须是 dev-header / oidc / local 之一（留空 = 未登记）: %w",
			s, ErrInvalidFormat)
	}
}

// ReleaseKind 区分「发布」与「回滚」。
type ReleaseKind string

const (
	// ReleaseDeploy 一次正向发布。
	ReleaseDeploy ReleaseKind = "deploy"
	// ReleaseRollback 一次回滚。回滚同样是「让某个版本上线」，所以
	// Release.Version 记的是**回滚到的那个版本**，不是被回滚掉的那个。
	ReleaseRollback ReleaseKind = "rollback"
)

// ParseReleaseKind 解析发布类型；空串按 deploy 处理（绝大多数记录是正向发布）。
func ParseReleaseKind(s string) (ReleaseKind, error) {
	switch ReleaseKind(s) {
	case "":
		return ReleaseDeploy, nil
	case ReleaseDeploy, ReleaseRollback:
		return ReleaseKind(s), nil
	default:
		return "", fmt.Errorf("kind %q 必须是 deploy / rollback 之一: %w", s, ErrInvalidFormat)
	}
}

var (
	// ErrNotFound：登记簿里没有这条记录。
	ErrNotFound = errors.New("extapp: not found")
	// ErrMissingField：必填字段为空。
	ErrMissingField = errors.New("extapp: missing required field")
	// ErrInvalidFormat：字段格式非法。
	ErrInvalidFormat = errors.New("extapp: invalid format")
)

// appKeyPattern 与 registry 的标识符规则一致（core.service.instance_id 用的
// 同一条）：两张表里的键长得一样，才能互相对照着读。
var appKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// hostnamePattern 只接受**主机名**，不接受 URL。
//
// 这条规则顺带关掉了一整类泄漏：`https://user:pass@host/...` 这种把凭据写进
// 地址的写法，`@`、`:`、`/`、`?` 一个都过不了主机名正则——同
// registry.validateEndpointCarriesNoCredential 要挡的那条链
// （表单 → Action → 审计摘要 → 审计页回显，终点是一条**改不掉**的记录），
// 只是这里靠字段形态本身就关掉了，不需要一道专门的检查。
//
// 与 db/migrations/000053 的 ext_app_primary_domain_format CHECK **逐字同形**：
// 领域校验是第一道闸，库层 CHECK 是第二道（纵深防御）。两边写法漂开的后果是
// 「领域放行、库层拒绝」，那会以 500 的形态出现，而人看到的是「保存失败」
// 却没有任何指向字段的说明。
var hostnamePattern = regexp.MustCompile(
	`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// commitPattern：7~40 位小写十六进制，短 SHA 与全长 SHA 都收。
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func requireNonEmpty(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s 不能为空: %w", field, ErrMissingField)
	}
	return nil
}

// App 是前端应用登记簿的一行（ADMIN-IA §5.4.1）。
type App struct {
	ID uuid.UUID

	// AppKey 是稳定的机器可读标识（admin-web、console……）。
	AppKey string
	// DisplayName 是表格里显示的名字，可以是中文。
	DisplayName string

	// PrimaryDomain 是主机名（admin.solov.cc），空串 = 还没有域名。
	PrimaryDomain string
	// AuthMode 空串 = 未登记登录方式。
	AuthMode AuthMode

	Owner  string
	Status AppStatus
	Notes  string

	Environment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate 校验 App 的领域不变量。
func (a App) Validate() error {
	if err := requireNonEmpty("app_key", a.AppKey); err != nil {
		return err
	}
	if !appKeyPattern.MatchString(a.AppKey) {
		return fmt.Errorf("app_key=%q 只能是小写字母、数字与连字符，且不以连字符开头（最长 64）: %w",
			a.AppKey, ErrInvalidFormat)
	}
	if err := requireNonEmpty("display_name", a.DisplayName); err != nil {
		return err
	}
	if err := requireNonEmpty("owner", a.Owner); err != nil {
		return err
	}
	if a.PrimaryDomain != "" && !hostnamePattern.MatchString(a.PrimaryDomain) {
		// **不回显整个原值以外的东西，也不做任何"猜你想填什么"的补全**：
		// 这个字段最常见的填错就是粘了一整条 URL，而那条 URL 有可能带着
		// 凭据。错误信息里只说形态要求，把原值原样带出去是必要的（人要知道
		// 自己填了什么），但绝不把它拆开或重新拼装成一个"修好的"地址。
		return fmt.Errorf("primary_domain=%q 必须是主机名（如 admin.solov.cc），"+
			"不是 URL：不要带 https://、路径、端口或用户信息段: %w", a.PrimaryDomain, ErrInvalidFormat)
	}
	if _, err := ParseAuthMode(string(a.AuthMode)); err != nil {
		return err
	}
	if _, err := ParseAppStatus(string(a.Status)); err != nil {
		return err
	}
	if err := requireNonEmpty("environment", a.Environment); err != nil {
		return err
	}
	return nil
}

// Release 是一次**已经发生**的发布（core.ext_app_release 的一行）。
//
// 本结构体不驱动任何发布：它是一条记录，不是一个命令。见包注释。
type Release struct {
	ID    uuid.UUID
	AppID uuid.UUID

	// Version 是这次上线的版本 / 构建标签。回滚时记的是**回滚到的那个版本**。
	Version string
	// CommitSHA 空串 = 没取到提交号（不是每次发布都取得到）。
	CommitSHA string
	Kind      ReleaseKind

	// ReleasedAt 是发布**真正发生**的时刻（可以是过去）；登记时刻是 CreatedAt。
	ReleasedAt time.Time
	// ReleasedBy 是执行那次发布的人，可能不是来登记的人——登记者由审计链记录。
	ReleasedBy string

	Notes string

	CreatedAt time.Time
}

// Validate 校验 Release 的领域不变量。
func (r Release) Validate() error {
	if r.AppID == uuid.Nil {
		return fmt.Errorf("app_id 不能为空: %w", ErrMissingField)
	}
	if err := requireNonEmpty("version", r.Version); err != nil {
		return err
	}
	if err := requireNonEmpty("released_by", r.ReleasedBy); err != nil {
		return err
	}
	if r.ReleasedAt.IsZero() {
		return fmt.Errorf("released_at 不能为空: %w", ErrMissingField)
	}
	if r.CommitSHA != "" && !commitPattern.MatchString(r.CommitSHA) {
		return fmt.Errorf("commit_sha=%q 必须是 7~40 位小写十六进制: %w",
			r.CommitSHA, ErrInvalidFormat)
	}
	switch r.Kind {
	case ReleaseDeploy, ReleaseRollback:
	default:
		return fmt.Errorf("kind %q 必须是 deploy / rollback 之一: %w", r.Kind, ErrInvalidFormat)
	}
	return nil
}
