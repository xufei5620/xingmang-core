package server_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/server"
)

// 本文件跑在真库上：库层的部分不变量只在 SQL 里（服务器资产的三条状态/
// 计费周期 CHECK、jsonb 数组约束、金额与币种成对出现的 CHECK、外键 RESTRICT
// 与几处唯一索引），用内存假货复刻只会测到假货。

const testEnv = "production"

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
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// server_service_note 对 server_asset 是 ON DELETE RESTRICT，
	// server_asset 对 server_supplier 同样是 RESTRICT——TRUNCATE 要求
	// 一次列全所有引用方，漏掉任何一张这条语句会直接报错。
	if _, err := pool.Exec(ctx,
		"TRUNCATE core.server_service_note, core.server_domain, "+
			"core.server_asset, core.server_supplier"); err != nil {
		t.Fatalf("清空服务器登记簿失败: %v", err)
	}
	return pool
}

func testStore(t *testing.T) *server.Store {
	t.Helper()
	return server.NewStore(testPool(t))
}

func mustCreateSupplier(t *testing.T, s *server.Store, name string) server.Supplier {
	t.Helper()
	out, err := s.CreateSupplier(context.Background(), server.Supplier{Name: name, Environment: testEnv})
	if err != nil {
		t.Fatalf("CreateSupplier: %v", err)
	}
	return out
}

func mustCreateAsset(t *testing.T, s *server.Store, in server.Asset) server.Asset {
	t.Helper()
	if in.Environment == "" {
		in.Environment = testEnv
	}
	out, err := s.CreateAsset(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	return out
}

func TestAssetCreateGetUpdateRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	supplier := mustCreateSupplier(t, s, "Vultr")

	vcpu, mem, disk := 4, 8, 160
	cost := int64(9990)
	expires := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	created := mustCreateAsset(t, s, server.Asset{
		Hostname:              "srv-sin-01",
		IPAddresses:           []string{"203.0.113.9", "10.0.0.5"},
		Datacenter:            "SIN",
		SupplierID:            supplier.ID,
		VCPU:                  &vcpu,
		MemoryGB:              &mem,
		DiskGB:                &disk,
		Purpose:               "sub2api relay",
		Status:                server.AssetActive,
		MonthlyCostMinorUnits: &cost,
		Currency:              "USD",
		BillingCycle:          server.BillingMonthly,
		ExpiresAt:             expires,
		Notes:                 "primary relay node",
	})

	if created.ID == uuid.Nil {
		t.Fatal("创建后应有非零 ID")
	}
	if len(created.IPAddresses) != 2 || created.IPAddresses[0] != "203.0.113.9" {
		t.Fatalf("IP 数组往返不一致: %v", created.IPAddresses)
	}
	if !created.ExpiresAt.Equal(expires) {
		t.Fatalf("到期日往返不一致: %v, want %v", created.ExpiresAt, expires)
	}
	if created.MonthlyCostMinorUnits == nil || *created.MonthlyCostMinorUnits != 9990 {
		t.Fatalf("月付成本往返不一致: %v", created.MonthlyCostMinorUnits)
	}

	got, err := s.GetAsset(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if got.Hostname != created.Hostname {
		t.Fatalf("Get 与 Create 不一致")
	}

	// 整行替换：把 IP 换成一个，成本清空（模拟「续费信息还没定」）。
	// Environment 必须带上：它是身份边界（宪法 15 条），不进 UPDATE SQL 的
	// SET 列表（不可编辑），但 Asset.Validate 仍要求非空——真实调用路径
	// （assetSetHandler）总是从 Principal 带出这个值，这里手工模拟同样要带。
	updated, err := s.UpdateAsset(ctx, server.Asset{
		ID:          created.ID,
		Hostname:    "srv-sin-01",
		IPAddresses: []string{"203.0.113.9"},
		Status:      server.AssetActive,
		Environment: testEnv,
	})
	if err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if len(updated.IPAddresses) != 1 {
		t.Fatalf("更新后 IP 数组应只剩 1 个, got %v", updated.IPAddresses)
	}
	if updated.MonthlyCostMinorUnits != nil {
		t.Fatalf("更新未带金额字段应清空为未登记, got %v", *updated.MonthlyCostMinorUnits)
	}
	if updated.SupplierID != uuid.Nil {
		t.Fatalf("更新未带供应商应清空, got %v", updated.SupplierID)
	}
}

func TestAssetSetStatusIsIndependentFromFullUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	created := mustCreateAsset(t, s, server.Asset{Hostname: "srv-retire-me", Status: server.AssetActive})

	after, err := s.SetAssetStatus(ctx, created.ID, server.AssetRetired)
	if err != nil {
		t.Fatalf("SetAssetStatus: %v", err)
	}
	if after.Status != server.AssetRetired {
		t.Fatalf("status = %s, want retired", after.Status)
	}
	if after.Hostname != created.Hostname {
		t.Fatal("退役不该动其它字段")
	}
}

func TestAssetEnvironmentHostnameMustBeUnique(t *testing.T) {
	s := testStore(t)
	mustCreateAsset(t, s, server.Asset{Hostname: "srv-dup", Status: server.AssetActive})
	_, err := s.CreateAsset(context.Background(), server.Asset{
		Hostname: "srv-dup", Status: server.AssetActive, Environment: testEnv,
	})
	if err == nil {
		t.Fatal("同环境重复主机名应被库层唯一索引拒绝")
	}
}

func TestAssetSupplierForeignKeyIsEnforced(t *testing.T) {
	s := testStore(t)
	_, err := s.CreateAsset(context.Background(), server.Asset{
		Hostname: "srv-bad-supplier", Status: server.AssetActive,
		Environment: testEnv, SupplierID: uuid.New(),
	})
	if err == nil {
		t.Fatal("指向不存在的 supplier_id 应被外键拒绝")
	}
}

func TestSupplierCreateGetUpdateListRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	created := mustCreateSupplier(t, s, "Vultr")

	got, err := s.GetSupplier(ctx, created.ID)
	if err != nil || got.Name != "Vultr" {
		t.Fatalf("GetSupplier: %v, got %+v", err, got)
	}

	updated, err := s.UpdateSupplier(ctx, server.Supplier{
		ID: created.ID, Name: "Vultr Cloud", Website: "https://vultr.com", Environment: testEnv,
	})
	if err != nil {
		t.Fatalf("UpdateSupplier: %v", err)
	}
	if updated.Website != "https://vultr.com" {
		t.Fatalf("website = %q", updated.Website)
	}

	list, err := s.ListSuppliersByEnvironment(ctx, testEnv)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSuppliersByEnvironment: %v, len=%d", err, len(list))
	}
}

func TestServerDomainCreateGetUpdateListRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	expires := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	created, err := s.CreateServerDomain(ctx, server.ServerDomain{
		DomainName: "console.example.com", Registrar: "GoDaddy",
		CertSource: server.CertACME, CertExpiresAt: expires, Environment: testEnv,
	})
	if err != nil {
		t.Fatalf("CreateServerDomain: %v", err)
	}
	if !created.CertExpiresAt.Equal(expires) {
		t.Fatalf("证书到期日往返不一致: %v", created.CertExpiresAt)
	}

	got, err := s.GetServerDomain(ctx, created.ID)
	if err != nil || got.DomainName != "console.example.com" {
		t.Fatalf("GetServerDomain: %v, got %+v", err, got)
	}

	list, err := s.ListServerDomainsByEnvironment(ctx, testEnv)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListServerDomainsByEnvironment: %v, len=%d", err, len(list))
	}
}

func TestServerDomainEnvironmentNameMustBeUnique(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateServerDomain(ctx, server.ServerDomain{
		DomainName: "dup.example.com", Environment: testEnv,
	}); err != nil {
		t.Fatalf("首次登记不该失败: %v", err)
	}
	if _, err := s.CreateServerDomain(ctx, server.ServerDomain{
		DomainName: "dup.example.com", Environment: testEnv,
	}); err == nil {
		t.Fatal("同环境重复域名应被库层唯一索引拒绝")
	}
}

func TestServiceNoteCreateUpdateDeleteAndListByServer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	asset := mustCreateAsset(t, s, server.Asset{Hostname: "srv-with-services", Status: server.AssetActive})

	port := 8080
	created, err := s.CreateServiceNote(ctx, server.ServiceNote{
		ServerID: asset.ID, ServiceName: "api", ServiceKind: server.ServiceContainer, Port: &port,
	})
	if err != nil {
		t.Fatalf("CreateServiceNote: %v", err)
	}

	byServer, err := s.ListServiceNotesByServer(ctx, asset.ID)
	if err != nil || len(byServer) != 1 {
		t.Fatalf("ListServiceNotesByServer: %v, len=%d", err, len(byServer))
	}

	byEnv, err := s.ListServiceNotesByEnvironment(ctx, testEnv)
	if err != nil || len(byEnv) != 1 {
		t.Fatalf("ListServiceNotesByEnvironment（经 JOIN server_asset）: %v, len=%d", err, len(byEnv))
	}

	// ServerID 必须带上：换服务器不是这个 Action 的语义（迁移注释：要改归属
	// 只能删了重登），UPDATE SQL 不写它，但 Validate 仍要求非空——真实调用
	// 路径（serviceNoteSetHandler）总是从已读到的 before 行带出这个值。
	newPort := 9090
	updated, err := s.UpdateServiceNote(ctx, server.ServiceNote{
		ID: created.ID, ServerID: asset.ID, ServiceName: "api-v2", ServiceKind: server.ServiceSystemd, Port: &newPort,
	})
	if err != nil {
		t.Fatalf("UpdateServiceNote: %v", err)
	}
	if updated.ServiceName != "api-v2" || updated.ServerID != asset.ID {
		t.Fatalf("更新后不该丢失归属或改错字段: %+v", updated)
	}

	if err := s.DeleteServiceNote(ctx, created.ID); err != nil {
		t.Fatalf("DeleteServiceNote: %v", err)
	}
	if _, err := s.GetServiceNote(ctx, created.ID); err == nil {
		t.Fatal("删除后应查不到")
	}
	// 删不到（已经删过一次）应返回 ErrNotFound 而不是静默成功
	if err := s.DeleteServiceNote(ctx, created.ID); err == nil {
		t.Fatal("重复删除应返回 ErrNotFound")
	}
}

func TestServiceNoteServerForeignKeyIsEnforced(t *testing.T) {
	s := testStore(t)
	_, err := s.CreateServiceNote(context.Background(), server.ServiceNote{
		ServerID: uuid.New(), ServiceName: "ghost", ServiceKind: server.ServiceProcess,
	})
	if err == nil {
		t.Fatal("指向不存在的 server_id 应被外键拒绝")
	}
}

func TestServiceNoteServerNameMustBeUniquePerServer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	asset := mustCreateAsset(t, s, server.Asset{Hostname: "srv-dup-service", Status: server.AssetActive})
	if _, err := s.CreateServiceNote(ctx, server.ServiceNote{
		ServerID: asset.ID, ServiceName: "api", ServiceKind: server.ServiceContainer,
	}); err != nil {
		t.Fatalf("首次登记不该失败: %v", err)
	}
	if _, err := s.CreateServiceNote(ctx, server.ServiceNote{
		ServerID: asset.ID, ServiceName: "api", ServiceKind: server.ServiceContainer,
	}); err == nil {
		t.Fatal("同一台服务器下重复服务名应被库层唯一索引拒绝")
	}
}
