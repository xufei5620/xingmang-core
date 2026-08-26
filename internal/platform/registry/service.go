package registry

import (
	"fmt"
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
	if s.InternalEndpoint != "" &&
		!strings.HasPrefix(s.InternalEndpoint, "http://") &&
		!strings.HasPrefix(s.InternalEndpoint, "https://") {
		return fmt.Errorf("internal_endpoint=%q 必须是 http(s): %w", s.InternalEndpoint, ErrInvalidFormat)
	}
	if err := requireNonEmpty("owner", s.Owner); err != nil {
		return err
	}
	if _, err := ParseServiceStatus(string(s.Status)); err != nil {
		return err
	}
	return nil
}
