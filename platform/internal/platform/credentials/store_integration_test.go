package credentials_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func credentialPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 测试库: %v", err)
	}
	t.Cleanup(pool.Close)

	// **两头都清**（XM-DBTEST-FIX0）。
	//
	// 只在开跑前清的话，本包会给共享测试库留下 ('sub2api','staging') 那一行
	// ——实测确认过：跑完本包，`core.connector_config` 从 0 变 1。
	// `internal/platform/jobs/connector_config_integration_test.go` 的注释
	// 点名过这件事：「其他包（如 internal/platform/credentials 的
	// TestConnectorConfigSetAndList）同样会写 ('sub2api','staging')，且不保证
	// 在它之后清干净」，那一片因此不得不自己两头设防。
	//
	// 每个包都自己防一遍是行得通的，但它把「表是干净的」从**不变量**降成
	// 了「谁记得防谁就没事」——2026-09-02/03 已经真撞过一次。这里补上收尾，
	// 让不变量重新成立，新写的测试不必再各自设防。
	//
	// 用 t.Cleanup 而不是 defer：credentialPool 是被每个用例各自调用的，
	// t.Cleanup 挂在调用它的那个用例上，逐个用例收尾比整包收一次更干净。
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "TRUNCATE core.credential_ref, core.connector_config"); err != nil {
			t.Errorf("收尾清空凭据表: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	return pool
}

func TestStoreUpsertRotateRevokeRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := credentials.NewStore(credentialPool(t), root)
	ctx := context.Background()
	ref := secrets.MustCredentialRef("secret://sub2api-prod/read-token")
	provider := secrets.NewFileProvider(root)

	// 轮换要求已有行
	if _, err := store.Rotate(ctx, ref, secrets.NewSecretValue([]byte("test-value-1")), "staging", "alice"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("未登记的轮换应 ErrNotFound, got %v", err)
	}

	first, err := store.Upsert(ctx, ref, secrets.NewSecretValue([]byte("test-value-1")), "staging", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if first.Before != nil || first.After.Version != 1 || !first.After.Available || first.After.Revoked() {
		t.Fatalf("新建 = %#v", first)
	}
	if first.After.Fingerprint != credentials.Fingerprint(secrets.NewSecretValue([]byte("test-value-1"))) {
		t.Fatalf("指纹不符: %q", first.After.Fingerprint)
	}
	if v, err := provider.Resolve(ctx, ref, "test"); err != nil || v.Reveal() != "test-value-1" {
		t.Fatalf("FileProvider 读回 = %q, %v", v.Reveal(), err)
	}

	second, err := store.Rotate(ctx, ref, secrets.NewSecretValue([]byte("test-value-2")), "staging", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if second.Before == nil || second.Before.Version != 1 || second.After.Version != 2 ||
		second.After.UpdatedBy != "bob" || second.After.Fingerprint == second.Before.Fingerprint {
		t.Fatalf("轮换 = %#v", second)
	}
	if v, err := provider.Resolve(ctx, ref, "test"); err != nil || v.Reveal() != "test-value-2" {
		t.Fatalf("轮换后 FileProvider 读回 = %q, %v", v.Reveal(), err)
	}

	// 跨环境改写被拒
	if _, err := store.Upsert(ctx, ref, secrets.NewSecretValue([]byte("test-value-3")), "production", "eve"); !errors.Is(err, credentials.ErrEnvironmentMismatch) {
		t.Fatalf("跨环境 Upsert 应 ErrEnvironmentMismatch, got %v", err)
	}
	if _, err := store.Revoke(ctx, ref, "production", "eve", "试探"); !errors.Is(err, credentials.ErrEnvironmentMismatch) {
		t.Fatalf("跨环境 Revoke 应 ErrEnvironmentMismatch, got %v", err)
	}
	if v, err := provider.Resolve(ctx, ref, "test"); err != nil || v.Reveal() != "test-value-2" {
		t.Fatalf("被拒的写不该动文件: %q, %v", v.Reveal(), err)
	}

	revoked, err := store.Revoke(ctx, ref, "staging", "carol", "上游泄漏")
	if err != nil {
		t.Fatal(err)
	}
	if revoked.After.RevokedAt == nil || revoked.After.Available || revoked.After.Version != 2 ||
		revoked.Before == nil || !revoked.Before.Available {
		t.Fatalf("吊销 = %#v", revoked)
	}
	if _, err := provider.Resolve(ctx, ref, "test"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("吊销后 FileProvider 应 ErrNotFound, got %v", err)
	}

	items, err := store.List(ctx, "staging")
	if err != nil || len(items) != 1 {
		t.Fatalf("List = %#v, %v", items, err)
	}
	if !items[0].Revoked() || items[0].Available || items[0].Version != 2 || items[0].Scope != "sub2api-prod" {
		t.Fatalf("List 项 = %#v", items[0])
	}
	if other, err := store.List(ctx, "production"); err != nil || len(other) != 0 {
		t.Fatalf("其他环境应为空: %#v, %v", other, err)
	}

	// 吊销后重新登记：版本继续递增、吊销标记清除、文件回来
	again, err := store.Upsert(ctx, ref, secrets.NewSecretValue([]byte("test-value-4")), "staging", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if again.After.Version != 3 || again.After.Revoked() || !again.After.Available {
		t.Fatalf("重新登记 = %#v", again.After)
	}
}

func TestStoreNeverStoresTheValue(t *testing.T) {
	root := t.TempDir()
	pool := credentialPool(t)
	store := credentials.NewStore(pool, root)
	ctx := context.Background()
	const marker = "SECRET-MARKER-3b7e"
	if _, err := store.Upsert(ctx, secrets.MustCredentialRef("secret://newapi/readonly-token"),
		secrets.NewSecretValue([]byte(marker)), "staging", "alice"); err != nil {
		t.Fatal(err)
	}
	var hits int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM core.credential_ref AS r WHERE to_jsonb(r)::text LIKE '%' || $1 || '%'",
		marker).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatal("数据库行里出现了凭据值")
	}
}

func TestConnectorConfigSetAndList(t *testing.T) {
	store := credentials.NewStore(credentialPool(t), t.TempDir())
	ctx := context.Background()

	before, after, err := store.SetConnectorConfig(ctx, credentials.ConnectorConfig{
		Platform: "sub2api", Environment: "staging", Mode: credentials.ModeFake,
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if before != nil || after.Version != 1 || after.Mode != "fake" || after.TargetAllowlist == nil {
		t.Fatalf("新建 = %#v / %#v", before, after)
	}

	before, after, err = store.SetConnectorConfig(ctx, credentials.ConnectorConfig{
		Platform: "sub2api", Environment: "staging", Mode: credentials.ModeReal,
		Endpoint: "https://api.solov.cc", TargetAllowlist: []string{"api.solov.cc"},
		CredentialRef: "secret://sub2api-prod/read-token",
	}, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if before == nil || before.Version != 1 || after.Version != 2 || after.Mode != "real" ||
		after.UpdatedBy != "bob" || len(after.TargetAllowlist) != 1 {
		t.Fatalf("更新 = %#v / %#v", before, after)
	}

	if _, _, err := store.SetConnectorConfig(ctx, credentials.ConnectorConfig{
		Platform: "newapi", Environment: "staging", Mode: credentials.ModeReal,
	}, "bob"); !errors.Is(err, credentials.ErrInvalidConnectorConfig) {
		t.Fatalf("三项缺失的 real 应被拒, got %v", err)
	}

	items, err := store.ListConnectorConfigs(ctx, "staging")
	if err != nil || len(items) != 1 || items[0].Platform != "sub2api" || items[0].Version != 2 {
		t.Fatalf("List = %#v, %v", items, err)
	}
	if other, err := store.ListConnectorConfigs(ctx, "production"); err != nil || len(other) != 0 {
		t.Fatalf("其他环境应为空: %#v, %v", other, err)
	}
}
