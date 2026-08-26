package registry

import (
	"errors"
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
