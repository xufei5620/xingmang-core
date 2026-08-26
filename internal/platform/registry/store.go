package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry/gen"
)

// ErrNotFound：请求的注册对象不存在。
var ErrNotFound = errors.New("registry: not found")

// Store 是 Registry 的 PostgreSQL 仓储。
//
// 写方法一律先跑领域 Validate() 再触库：领域校验是第一道闸，数据库 CHECK 是
// 第二道（纵深防御）。所有写入必须由 Action 层调用（ADR-003）。
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// --- 类型转换助手：pgtype.Timestamptz ↔ time.Time（库内一律 UTC，规格 §18.7）---

func tsFromPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func tsToPtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time.UTC()
	return &t
}

func tsToTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time.UTC()
}

// nonNilStrings 把 Go 的 nil slice 归一化为空数组。
// 库中这些列是 NOT NULL DEFAULT '{}'，但显式传 NULL 会覆盖 DEFAULT 并触发
// 非空约束——Go 里 nil slice 语义上就是「空」，在此统一（CI 首次集成测试发现）。
func nonNilStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func toCapabilities(ss []string) []Capability {
	out := make([]Capability, 0, len(ss))
	for _, s := range ss {
		out = append(out, Capability(s))
	}
	return out
}

func fromCapabilities(cs []Capability) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, string(c))
	}
	return out
}

func serviceFromRow(r gen.CoreService) Service {
	return Service{
		ID:               r.ID,
		ServiceType:      r.ServiceType,
		InstanceID:       r.InstanceID,
		Environment:      Environment(r.Environment),
		Endpoint:         r.Endpoint,
		InternalEndpoint: r.InternalEndpoint,
		Owner:            r.Owner,
		HealthCheckPath:  r.HealthCheckPath,
		NativeConsoleURL: r.NativeConsoleUrl,
		RunbookPath:      r.RunbookPath,
		Status:           ServiceStatus(r.Status),
		SourceWatermark:  r.SourceWatermark,
		ObservedAt:       tsToPtr(r.ObservedAt),
		CreatedAt:        tsToTime(r.CreatedAt),
		UpdatedAt:        tsToTime(r.UpdatedAt),
	}
}

func connectorFromRow(r gen.CoreConnector) Connector {
	return Connector{
		ID:                        r.ID,
		Key:                       r.Key,
		Version:                   r.Version,
		ContractVersion:           r.ContractVersion,
		ConnectionSchemaPath:      r.ConnectionSchemaPath,
		TargetAllowlist:           r.TargetAllowlist,
		ReadCapabilities:          toCapabilities(r.ReadCapabilities),
		WriteCapabilities:         toCapabilities(r.WriteCapabilities),
		SupportedUpstreamVersions: r.SupportedUpstreamVersions,
		CompatibilityTestPath:     r.CompatibilityTestPath,
		CreatedAt:                 tsToTime(r.CreatedAt),
		UpdatedAt:                 tsToTime(r.UpdatedAt),
	}
}

func connectionFromRow(r gen.CoreConnection) Connection {
	return Connection{
		ID:                      r.ID,
		ConnectorID:             r.ConnectorID,
		ServiceID:               r.ServiceID,
		Environment:             Environment(r.Environment),
		CredentialRef:           r.CredentialRef,
		TargetAllowlist:         r.TargetAllowlist,
		GrantedCapabilities:     toCapabilities(r.GrantedCapabilities),
		KillSwitch:              r.KillSwitch,
		Status:                  ConnectionStatus(r.Status),
		DetectedUpstreamVersion: r.DetectedUpstreamVersion,
		VersionFingerprint:      r.VersionFingerprint,
		LastVerifiedAt:          tsToPtr(r.LastVerifiedAt),
		CreatedAt:               tsToTime(r.CreatedAt),
		UpdatedAt:               tsToTime(r.UpdatedAt),
	}
}

func wrapNotFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return err
}

// CreateService 登记一个被管理系统实例。
func (s *Store) CreateService(ctx context.Context, in Service) (Service, error) {
	if err := in.Validate(); err != nil {
		return Service{}, err
	}
	row, err := s.q.CreateService(ctx, gen.CreateServiceParams{
		ID:               in.ID,
		ServiceType:      in.ServiceType,
		InstanceID:       in.InstanceID,
		Environment:      string(in.Environment),
		Endpoint:         in.Endpoint,
		InternalEndpoint: in.InternalEndpoint,
		Owner:            in.Owner,
		HealthCheckPath:  in.HealthCheckPath,
		NativeConsoleUrl: in.NativeConsoleURL,
		RunbookPath:      in.RunbookPath,
		Status:           string(in.Status),
		SourceWatermark:  in.SourceWatermark,
		ObservedAt:       tsFromPtr(in.ObservedAt),
	})
	if err != nil {
		return Service{}, fmt.Errorf("create service: %w", err)
	}
	return serviceFromRow(row), nil
}

// GetServiceByInstance 按业务唯一键读取。
func (s *Store) GetServiceByInstance(ctx context.Context, serviceType, instanceID string) (Service, error) {
	row, err := s.q.GetServiceByInstance(ctx, gen.GetServiceByInstanceParams{
		ServiceType: serviceType,
		InstanceID:  instanceID,
	})
	if err != nil {
		return Service{}, wrapNotFound(err, "get service")
	}
	return serviceFromRow(row), nil
}

// ListServicesByEnvironment 列出某环境下全部服务。
func (s *Store) ListServicesByEnvironment(ctx context.Context, env Environment) ([]Service, error) {
	if _, err := ParseEnvironment(string(env)); err != nil {
		return nil, err
	}
	rows, err := s.q.ListServicesByEnvironment(ctx, string(env))
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	out := make([]Service, 0, len(rows))
	for _, r := range rows {
		out = append(out, serviceFromRow(r))
	}
	return out, nil
}

// UpdateServiceObservation 回写采集观测（数据新鲜度，规格 §9.1）。
func (s *Store) UpdateServiceObservation(
	ctx context.Context, id uuid.UUID, watermark string, observedAt time.Time, status ServiceStatus,
) (Service, error) {
	if _, err := ParseServiceStatus(string(status)); err != nil {
		return Service{}, err
	}
	at := observedAt.UTC()
	row, err := s.q.UpdateServiceObservation(ctx, gen.UpdateServiceObservationParams{
		ID:              id,
		SourceWatermark: watermark,
		ObservedAt:      tsFromPtr(&at),
		Status:          string(status),
	})
	if err != nil {
		return Service{}, wrapNotFound(err, "update service observation")
	}
	return serviceFromRow(row), nil
}

// CreateConnector 登记一个 Connector 类型版本。
func (s *Store) CreateConnector(ctx context.Context, in Connector) (Connector, error) {
	if err := in.Validate(); err != nil {
		return Connector{}, err
	}
	row, err := s.q.CreateConnector(ctx, gen.CreateConnectorParams{
		ID:                        in.ID,
		Key:                       in.Key,
		Version:                   in.Version,
		ContractVersion:           in.ContractVersion,
		ConnectionSchemaPath:      in.ConnectionSchemaPath,
		TargetAllowlist:           nonNilStrings(in.TargetAllowlist),
		ReadCapabilities:          fromCapabilities(in.ReadCapabilities),
		WriteCapabilities:         fromCapabilities(in.WriteCapabilities),
		SupportedUpstreamVersions: nonNilStrings(in.SupportedUpstreamVersions),
		CompatibilityTestPath:     in.CompatibilityTestPath,
	})
	if err != nil {
		return Connector{}, fmt.Errorf("create connector: %w", err)
	}
	return connectorFromRow(row), nil
}

// GetConnector 按 key+version 读取。
func (s *Store) GetConnector(ctx context.Context, key, version string) (Connector, error) {
	row, err := s.q.GetConnector(ctx, gen.GetConnectorParams{Key: key, Version: version})
	if err != nil {
		return Connector{}, wrapNotFound(err, "get connector")
	}
	return connectorFromRow(row), nil
}

// CreateConnection 建立 Connector 到 Service 的实际连接。
func (s *Store) CreateConnection(ctx context.Context, in Connection) (Connection, error) {
	if err := in.Validate(); err != nil {
		return Connection{}, err
	}
	row, err := s.q.CreateConnection(ctx, gen.CreateConnectionParams{
		ID:                      in.ID,
		ConnectorID:             in.ConnectorID,
		ServiceID:               in.ServiceID,
		Environment:             string(in.Environment),
		CredentialRef:           in.CredentialRef,
		TargetAllowlist:         nonNilStrings(in.TargetAllowlist),
		GrantedCapabilities:     fromCapabilities(in.GrantedCapabilities),
		KillSwitch:              in.KillSwitch,
		Status:                  string(in.Status),
		DetectedUpstreamVersion: in.DetectedUpstreamVersion,
		VersionFingerprint:      in.VersionFingerprint,
		LastVerifiedAt:          tsFromPtr(in.LastVerifiedAt),
	})
	if err != nil {
		return Connection{}, fmt.Errorf("create connection: %w", err)
	}
	return connectionFromRow(row), nil
}

// ListConnectionsByService 列出某服务的全部连接。
func (s *Store) ListConnectionsByService(ctx context.Context, serviceID uuid.UUID) ([]Connection, error) {
	rows, err := s.q.ListConnectionsByService(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, connectionFromRow(r))
	}
	return out, nil
}

// SetConnectionStatus 改变连接状态（含 Kill Switch 拉闸）。
func (s *Store) SetConnectionStatus(ctx context.Context, id uuid.UUID, status ConnectionStatus) (Connection, error) {
	if _, err := ParseConnectionStatus(string(status)); err != nil {
		return Connection{}, err
	}
	row, err := s.q.SetConnectionStatus(ctx, gen.SetConnectionStatusParams{ID: id, Status: string(status)})
	if err != nil {
		return Connection{}, wrapNotFound(err, "set connection status")
	}
	return connectionFromRow(row), nil
}
