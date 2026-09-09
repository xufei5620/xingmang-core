package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validService() Service {
	return Service{
		ID:              uuid.New(),
		ServiceType:     "sub2api",
		InstanceID:      "sub2api-prod",
		Environment:     EnvProduction,
		Endpoint:        "https://api.solov.cc",
		Owner:           "platform",
		HealthCheckPath: "/healthz",
		RunbookPath:     "docs/runbooks/sub2api.md",
		Status:          ServiceActive,
	}
}

func TestServiceValidateAcceptsValid(t *testing.T) {
	if err := validService().Validate(); err != nil {
		t.Fatalf("合法 Service 被拒绝: %v", err)
	}
}

func TestServiceValidateRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Service){
		"空 ServiceType":    func(s *Service) { s.ServiceType = "" },
		"非法 ServiceType":   func(s *Service) { s.ServiceType = "Sub2API" },
		"空 InstanceID":     func(s *Service) { s.InstanceID = "" },
		"非法 InstanceID":    func(s *Service) { s.InstanceID = "prod_1" },
		"空 Environment":    func(s *Service) { s.Environment = "" },
		"非法 Environment":   func(s *Service) { s.Environment = Environment("prod") },
		"空 Endpoint":       func(s *Service) { s.Endpoint = "" },
		"非 https Endpoint": func(s *Service) { s.Endpoint = "ftp://api.solov.cc" },
		"空 Owner":          func(s *Service) { s.Owner = "" },
		"非法 Status":        func(s *Service) { s.Status = ServiceStatus("unknown") },
	} {
		s := validService()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("%s：应被拒绝但通过了", name)
		}
	}
}

// TestServiceValidateRejectsCredentialInEndpoint 回归 Codex 冷审 PR #47 第 2 条
// 与 PR #43 head `419ecf8`：「serviceForm.ts 允许带 userinfo/token 的 URL，经
// serviceSummary→审计 API→AuditPage 原样回显，形成明文凭据链」。
//
// 挡的是一整条链：表单 URL → registry.service.create → serviceSummary 把
// endpoint 原样写进审计摘要 → 不可篡改的审计链 → /api/v1/audit/events → 审计页。
// 链的终点是一条**改不掉**的记录，所以唯一能真正关闭它的位置是入口。
// 审计摘要的强制脱敏按**键名**匹配，而这里凭据藏在一个叫 endpoint 的字段的
// **值**里，键名脱敏看不见它——两道防线各管一段，缺一不可。
func TestServiceValidateRejectsCredentialInEndpoint(t *testing.T) {
	// 用明显是假的占位串，避免像真凭据（本仓禁止代码里出现明文凭据）
	for name, endpoint := range map[string]string{
		"userinfo 带口令":      "https://alice:pw-placeholder@api.solov.cc",
		"userinfo 只有用户名":    "https://alice@api.solov.cc",
		"查询参数 token":        "https://api.solov.cc/v1?token=placeholder",
		"查询参数 access_token": "https://api.solov.cc/v1?access_token=placeholder",
		"查询参数 api_key":      "https://api.solov.cc/v1?api_key=placeholder",
		"查询参数 apiKey 驼峰":    "https://api.solov.cc/v1?apiKey=placeholder",
		"查询参数 apikey 无分隔":   "https://api.solov.cc/v1?apikey=placeholder",
		"查询参数 x-api-key":    "https://api.solov.cc/v1?x-api-key=placeholder",
		"查询参数 accessToken":  "https://api.solov.cc/v1?accessToken=placeholder",
		"查询参数 SECRET 大写":    "https://api.solov.cc/v1?SECRET=placeholder",
		"查询参数 password":     "https://api.solov.cc/v1?password=placeholder",
		"混在多个参数中间":          "https://api.solov.cc/v1?env=prod&auth_token=placeholder&v=2",
	} {
		s := validService()
		s.Endpoint = endpoint
		err := s.Validate()
		if err == nil {
			t.Fatalf("%s：带凭据的 endpoint 必须被拒绝（%s）", name, endpoint)
		}
		if !errors.Is(err, ErrCredentialInEndpoint) {
			t.Fatalf("%s：错误应可 errors.Is 到 ErrCredentialInEndpoint, got %v", name, err)
		}
		// 错误信息本身不许把刚拦下的凭据再抄一份出去——它会进日志与 API 响应
		if strings.Contains(err.Error(), "placeholder") {
			t.Fatalf("%s：错误信息回显了凭据值: %v", name, err)
		}

		// internal_endpoint 走同一条规则
		s2 := validService()
		s2.InternalEndpoint = strings.Replace(endpoint, "https://", "http://", 1)
		if err := s2.Validate(); !errors.Is(err, ErrCredentialInEndpoint) {
			t.Fatalf("%s：internal_endpoint 也必须被拒, got %v", name, err)
		}
	}
}

// TestServiceValidateAcceptsCleanEndpoints：正常的 URL 一个都不许被误伤。
//
// 误拒的代价是运维直接登记不了服务，所以判据只看参数**名**，不做长度或熵的
// 启发式；而 `key` 只按词段精确匹配（`monkey` / `keyword` 必须放行）。
func TestServiceValidateAcceptsCleanEndpoints(t *testing.T) {
	for name, endpoint := range map[string]string{
		"裸域名":    "https://api.solov.cc",
		"带路径":    "https://api.solov.cc/v1/admin",
		"带端口":    "https://api.solov.cc:8443/v1",
		"无害查询参数": "https://api.solov.cc/v1?env=prod&page=2",
		// key 的同形词：只按词段精确匹配，不按子串——否则 ?monkey=1 会被误拒
		"参数名含 monkey": "https://api.solov.cc/v1?monkey=1",
		"参数名 keyword": "https://api.solov.cc/v1?keyword=abc",
		"路径里含 token":  "https://api.solov.cc/v1/tokens/list",
		"片段":          "https://api.solov.cc/v1#section",
	} {
		s := validService()
		s.Endpoint = endpoint
		if err := s.Validate(); err != nil {
			t.Fatalf("%s：合法 endpoint 被误拒（%s）: %v", name, endpoint, err)
		}
	}
}

func TestServiceValidateEnvironmentErrorIsTyped(t *testing.T) {
	s := validService()
	s.Environment = Environment("prod")
	if err := s.Validate(); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("环境错误应可 errors.Is 到 ErrInvalidEnvironment, got %v", err)
	}
}

func TestParseServiceStatus(t *testing.T) {
	if got, err := ParseServiceStatus("degraded"); err != nil || got != ServiceDegraded {
		t.Fatalf("ParseServiceStatus(degraded) = %q, %v", got, err)
	}
	if _, err := ParseServiceStatus("broken"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("非法状态应 ErrInvalidStatus, got %v", err)
	}
}
