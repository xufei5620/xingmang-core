package registry

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ServiceStatus 是被管理系统实例的当前状态。
type ServiceStatus string

const (
	ServiceActive   ServiceStatus = "active"
	ServiceDegraded ServiceStatus = "degraded"
	ServiceRetired  ServiceStatus = "retired"
)

// ParseServiceStatus 解析服务状态。
func ParseServiceStatus(s string) (ServiceStatus, error) {
	switch ServiceStatus(s) {
	case ServiceActive, ServiceDegraded, ServiceRetired:
		return ServiceStatus(s), nil
	default:
		return "", fmt.Errorf("service status %q: %w", s, ErrInvalidStatus)
	}
}

// Service 是一个被管理系统实例（规格 §2.2 Service Registry）。
// 动态事实（版本、水位）以此处快照为准，架构文档不写死（规格 §1.2）。
type Service struct {
	ID          uuid.UUID
	ServiceType string // sub2api / newapi / cpa / invoice / payment
	InstanceID  string // 全局唯一实例标识，如 sub2api-prod
	Environment Environment

	Endpoint         string // 对外地址，必须 https
	InternalEndpoint string // 内网地址，可空
	Owner            string // 负责人或团队
	HealthCheckPath  string // 健康检查路径，可空
	NativeConsoleURL string // 原生后台入口，可空
	RunbookPath      string // 运行手册路径，可空

	Status          ServiceStatus
	SourceWatermark string     // 数据水位，采集后回写
	ObservedAt      *time.Time // 最近一次成功采集时间（UTC），未采集为 nil

	CreatedAt time.Time
	UpdatedAt time.Time
}

// 判定「这个查询参数名装的是凭据」的两份清单。
//
// 与 audit.defaultSensitiveKeys 是同族但**刻意分开**：那份服务于「审计摘要里
// 的字段名」，这份服务于「URL 查询参数名」，两处的误报代价不同——审计摘要多
// 脱敏一个字段无所谓，登记 Service 时误拒一个合法 endpoint 会直接挡住运维。
// 共用一份清单只会让某一边被迫将就。
//
// 为什么分成两份而不是一份子串匹配：`key` 有大量无害的英文同形词
// （`monkey`、`keyword`、`turkey`），纯子串匹配会把 `?monkey=1` 判成凭据。
// 而 `token` / `secret` / `password` 没有这个问题——URL 查询参数里出现这三个
// 词的连续字母，基本不可能是别的意思。于是：
var (
	// credentialWordsAnywhere：整个参数名里只要**含有**这些连续字母就判定为
	// 凭据。覆盖 `accesstoken`、`clientsecret`、`userpassword` 这类没有分隔符
	// 的写法——那些切不出词。
	credentialWordsAnywhere = []string{"token", "secret", "password"}
	// credentialWordsExact：只有当参数名**切出来的某一段**恰好等于它才判定。
	// 参数名按非字母数字与驼峰边界切段，于是 `api_key` / `apiKey` /
	// `x-api-key` 都能切出 `key`，而 `monkey` 只有 `monkey` 一段，切不出。
	credentialWordsExact = []string{
		"key", "apikey", "passwd", "pwd", "credential", "credentials",
	}
)

// splitParamWords 把参数名切成小写词段：按非字母数字切一次，再按驼峰边界切。
//
// 驼峰也要切，因为表单里复制来的 URL 常写成 `apiKey` / `accessToken`；
// 只按下划线与连字符切会漏掉它们。
func splitParamWords(name string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			// 驼峰边界：前一个字符是小写字母或数字时另起一段
			if i > 0 {
				prev := runes[i-1]
				if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
					flush()
				}
			}
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return words
}

// looksLikeCredentialParam 报告一个查询参数名是否像凭据。
func looksLikeCredentialParam(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range credentialWordsAnywhere {
		if strings.Contains(lower, w) {
			return true
		}
	}
	for _, word := range splitParamWords(name) {
		for _, w := range credentialWordsExact {
			if word == w {
				return true
			}
		}
	}
	return false
}

// validateEndpointCarriesNoCredential 拒绝把凭据写进 URL 的 endpoint。
//
// 挡的是一整条泄漏链（XM-0031，回归 Codex 冷审 PR #47 第 2 条与 PR #43 head
// `419ecf8`「serviceForm.ts:75-84 又允许带 userinfo/token 的 URL，经
// serviceSummary→审计 API→AuditPage 原样回显，形成明文凭据链」）：
//
//	表单 URL → registry.service.create → serviceSummary 把 endpoint 原样写进
//	审计摘要 → 审计链（不可篡改、永久保存）→ /api/v1/audit/events → 审计页回显
//
// 链上任何一环都不该是「记得脱敏」，因为链的终点是一条**改不掉**的记录。
// 在入口拒绝是唯一能真正关闭它的位置：审计摘要的强制脱敏（audit.ActionSink）
// 按键名匹配，而这里凭据藏在一个叫 `endpoint` 的字段的**值**里，键名脱敏看不见它。
//
// 两种形态：
//   - userinfo：`https://user:pass@host/…`，RFC 3986 的 URL 里带凭据的标准写法；
//   - 查询参数：`https://host/api?access_token=…`，更常见于「复制一个能用的
//     链接过来」。
//
// 只看参数**名**不看值：值可能是任何东西，而「这个位置放的是凭据」这件事由
// 名字表达。同理不做长度或熵的启发式——那会既误伤又漏判。
//
// 注意这不是「URL 里没有凭据」的证明，只是把两种最常见的形态挡在门外。
// 真正的凭据通道是 Connection.CredentialRef（ADR-014、宪法 7 条）。
func validateEndpointCarriesNoCredential(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s=%q 不是合法 URL: %w", field, raw, ErrInvalidFormat)
	}
	if u.User != nil {
		// **不把 raw 放进错误信息**——错误会进日志与 API 响应，
		// 原样回显等于亲手把刚拦下的凭据又抄了一份出去。
		return fmt.Errorf("%s 含 userinfo（user:pass@）；凭据只经 CredentialRef: %w",
			field, ErrCredentialInEndpoint)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("%s 的查询串无法解析: %w", field, ErrInvalidFormat)
	}
	for name := range query {
		if looksLikeCredentialParam(name) {
			// 只回显参数**名**，绝不回显值——错误会进日志与 API 响应。
			return fmt.Errorf("%s 的查询参数 %q 看起来是凭据；凭据只经 CredentialRef: %w",
				field, name, ErrCredentialInEndpoint)
		}
	}
	return nil
}

// Validate 校验 Service 的领域不变量。
func (s Service) Validate() error {
	if err := ValidateIdentifier("service_type", s.ServiceType); err != nil {
		return err
	}
	if err := ValidateIdentifier("instance_id", s.InstanceID); err != nil {
		return err
	}
	if _, err := ParseEnvironment(string(s.Environment)); err != nil {
		return err
	}
	if err := requireNonEmpty("endpoint", s.Endpoint); err != nil {
		return err
	}
	if !strings.HasPrefix(s.Endpoint, "https://") {
		return fmt.Errorf("endpoint=%q 必须是 https: %w", s.Endpoint, ErrInvalidFormat)
	}
	if err := validateEndpointCarriesNoCredential("endpoint", s.Endpoint); err != nil {
		return err
	}
	if s.InternalEndpoint != "" {
		if !strings.HasPrefix(s.InternalEndpoint, "http://") &&
			!strings.HasPrefix(s.InternalEndpoint, "https://") {
			return fmt.Errorf("internal_endpoint=%q 必须是 http(s): %w", s.InternalEndpoint, ErrInvalidFormat)
		}
		if err := validateEndpointCarriesNoCredential("internal_endpoint", s.InternalEndpoint); err != nil {
			return err
		}
	}
	if err := requireNonEmpty("owner", s.Owner); err != nil {
		return err
	}
	if _, err := ParseServiceStatus(string(s.Status)); err != nil {
		return err
	}
	return nil
}
