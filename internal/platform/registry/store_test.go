package registry_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// testPool 连接测试库；未设置 XM_TEST_DATABASE_URL 时跳过（本地默认不跑，
// CI 通过 postgres service container 必跑）。
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 测试库失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx,
		"TRUNCATE core.connection, core.connector, core.service RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("清空测试数据失败: %v", err)
	}
	return pool
}

func seedService(t *testing.T, s *registry.Store) registry.Service {
	t.Helper()
	svc := registry.Service{
		ID:          uuid.New(),
		ServiceType: "sub2api",
		InstanceID:  "sub2api-prod",
		Environment: registry.EnvProduction,
		Endpoint:    "https://api.solov.cc",
		Owner:       "platform",
		Status:      registry.ServiceActive,
	}
	got, err := s.CreateService(context.Background(), svc)
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	return got
}

func seedConnector(t *testing.T, s *registry.Store) registry.Connector {
	t.Helper()
	c := registry.Connector{
		ID:                   uuid.New(),
		Key:                  "sub2api",
		Version:              "1.0.0",
		ContractVersion:      "1",
		ConnectionSchemaPath: "contracts/connectors/sub2api.connection.v1.json",
		TargetAllowlist:      []string{"api.solov.cc"},
		ReadCapabilities:     []registry.Capability{"sub2api.users.read"},
		WriteCapabilities:    []registry.Capability{"sub2api.accounts.import"},
	}
	got, err := s.CreateConnector(context.Background(), c)
	if err != nil {
		t.Fatalf("CreateConnector: %v", err)
	}
	return got
}

func TestStoreServiceRoundTrip(t *testing.T) {
	store := registry.NewStore(testPool(t))
	ctx := context.Background()

	created := seedService(t, store)
	if created.ID == uuid.Nil || created.CreatedAt.IsZero() {
		t.Fatalf("返回值缺少库侧生成字段: %+v", created)
	}

	got, err := store.GetServiceByInstance(ctx, "sub2api", "sub2api-prod")
	if err != nil {
		t.Fatalf("GetServiceByInstance: %v", err)
	}
	if got.ID != created.ID || got.Environment != registry.EnvProduction {
		t.Fatalf("读回不一致: %+v", got)
	}

	list, err := store.ListServicesByEnvironment(ctx, registry.EnvProduction)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListServicesByEnvironment = %d 条, %v", len(list), err)
	}

	empty, err := store.ListServicesByEnvironment(ctx, registry.EnvStaging)
	if err != nil || len(empty) != 0 {
		t.Fatalf("staging 应为空: %d 条, %v", len(empty), err)
	}
}

func TestStoreGetServiceNotFound(t *testing.T) {
	store := registry.NewStore(testPool(t))
	_, err := store.GetServiceByInstance(context.Background(), "sub2api", "does-not-exist")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound, got %v", err)
	}
}

func TestStoreUpdateServiceObservation(t *testing.T) {
	store := registry.NewStore(testPool(t))
	ctx := context.Background()
	created := seedService(t, store)

	at := time.Now().UTC().Truncate(time.Millisecond)
	got, err := store.UpdateServiceObservation(ctx, created.ID, "wm-42", at, registry.ServiceDegraded)
	if err != nil {
		t.Fatalf("UpdateServiceObservation: %v", err)
	}
	if got.SourceWatermark != "wm-42" || got.Status != registry.ServiceDegraded {
		t.Fatalf("观测字段未更新: %+v", got)
	}
	if got.ObservedAt == nil || !got.ObservedAt.Equal(at) {
		t.Fatalf("ObservedAt = %v, want %v", got.ObservedAt, at)
	}
}

func TestStoreRejectsInvalidBeforeHittingDB(t *testing.T) {
	store := registry.NewStore(testPool(t))
	bad := registry.Service{
		ID:          uuid.New(),
		ServiceType: "sub2api",
		InstanceID:  "sub2api-prod",
		Environment: registry.EnvProduction,
		Endpoint:    "http://insecure.example.com",
		Owner:       "platform",
		Status:      registry.ServiceActive,
	}
	if _, err := store.CreateService(context.Background(), bad); err == nil {
		t.Fatal("非法 Service 应在落库前被领域校验拒绝")
	}
}

func TestStoreConnectionRoundTripAndKillSwitch(t *testing.T) {
	store := registry.NewStore(testPool(t))
	ctx := context.Background()
	svc := seedService(t, store)
	conn := seedConnector(t, store)

	c := registry.Connection{
		ID:                  uuid.New(),
		ConnectorID:         conn.ID,
		ServiceID:           svc.ID,
		Environment:         registry.EnvProduction,
		CredentialRef:       "secret://sub2api-prod/read-only-admin",
		TargetAllowlist:     []string{"api.solov.cc"},
		GrantedCapabilities: []registry.Capability{"sub2api.users.read"},
		Status:              registry.ConnectionEnabled,
	}
	created, err := store.CreateConnection(ctx, c)
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}

	list, err := store.ListConnectionsByService(ctx, svc.ID)
	if err != nil || len(list) != 1 || list[0].CredentialRef != c.CredentialRef {
		t.Fatalf("ListConnectionsByService = %+v, %v", list, err)
	}

	killed, err := store.SetConnectionStatus(ctx, created.ID, registry.ConnectionKilled)
	if err != nil || killed.Status != registry.ConnectionKilled {
		t.Fatalf("SetConnectionStatus = %+v, %v", killed, err)
	}
}

func TestStoreConnectionWriteWithoutKillSwitchRejected(t *testing.T) {
	store := registry.NewStore(testPool(t))
	ctx := context.Background()
	svc := seedService(t, store)
	conn := seedConnector(t, store)

	c := registry.Connection{
		ID:                  uuid.New(),
		ConnectorID:         conn.ID,
		ServiceID:           svc.ID,
		Environment:         registry.EnvProduction,
		CredentialRef:       "secret://sub2api-prod/read-only-admin",
		TargetAllowlist:     []string{"api.solov.cc"},
		GrantedCapabilities: []registry.Capability{"sub2api.accounts.import"},
		Status:              registry.ConnectionEnabled,
	}
	if _, err := store.CreateConnection(ctx, c); !errors.Is(err, registry.ErrKillSwitchRequired) {
		t.Fatalf("ADR-004：写能力无 Kill Switch 必须拒绝, got %v", err)
	}
}

func TestStoreConnectionPlaintextCredentialRejectedByDB(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := registry.NewStore(pool)
	svc := seedService(t, store)
	conn := seedConnector(t, store)

	// 绕过领域校验直接插库，验证数据库 CHECK 也拦得住明文（纵深防御）。
	_, err := pool.Exec(ctx, `
		INSERT INTO core.connection (
			id, connector_id, service_id, environment, credential_ref,
			target_allowlist, granted_capabilities, kill_switch, status
		) VALUES ($1, $2, $3, 'production', 'postgres://user:pass@host/db',
			ARRAY['api.solov.cc'], ARRAY['sub2api.users.read'], '', 'enabled')`,
		uuid.New(), conn.ID, svc.ID)
	if err == nil {
		t.Fatal("数据库 CHECK 必须拒绝明文凭据（宪法 7 条纵深防御）")
	}
}
