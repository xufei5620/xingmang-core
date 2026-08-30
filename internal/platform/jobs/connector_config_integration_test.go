package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPgConnectorConfigSourceIntegration 在真库上跑那条唯一的手写 SELECT：
// 列的类型（text[] / int / timestamptz）与 NULL 处理只有真库才验得出来。
//
// 表由 XM-CRED0 的迁移片创建；本用例在表不存在时按同一份契约建出来
// （CREATE TABLE IF NOT EXISTS），所以在还没迁移的库上也能跑。
// environment 用带纳秒后缀的唯一值，不会碰到任何真实配置行。
func TestPgConnectorConfigSourceIntegration(t *testing.T) {
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set XM_TEST_DATABASE_URL (CI) or XM_RUN_INTEGRATION=1 with a local DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS core`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS core.connector_config (
    platform         text NOT NULL CHECK (platform IN ('sub2api', 'newapi')),
    environment      text NOT NULL,
    mode             text NOT NULL CHECK (mode IN ('fake', 'real')),
    endpoint         text,
    target_allowlist text[],
    credential_ref   text,
    version          int,
    updated_at       timestamptz,
    updated_by       text,
    PRIMARY KEY (platform, environment)
)`); err != nil {
		t.Fatal(err)
	}

	environment := fmt.Sprintf("itest-connector-config-%d", time.Now().UnixNano())
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM core.connector_config WHERE environment = $1`, environment); err != nil {
			t.Errorf("清理接入配置行: %v", err)
		}
	}()

	updatedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO core.connector_config
    (platform, environment, mode, endpoint, target_allowlist, credential_ref, version, updated_at, updated_by)
VALUES ('sub2api', $1, 'real', 'https://api.example.test', ARRAY['api.example.test', 'Alt.Example.Test'],
        'secret://sub2api-prod/read-token', 4, $2, 'itest')`,
		environment, updatedAt); err != nil {
		t.Fatal(err)
	}
	// newapi 行故意把可空列全留 NULL：COALESCE 必须把它们读成空值而不是报错。
	if _, err := pool.Exec(ctx, `
INSERT INTO core.connector_config (platform, environment, mode)
VALUES ('newapi', $1, 'fake')`, environment); err != nil {
		t.Fatal(err)
	}

	source := NewPgConnectorConfigSource(pool)
	row, err := source.Get(ctx, ConnectorPlatformSub2API, environment)
	if err != nil {
		t.Fatalf("Get sub2api: %v", err)
	}
	if row == nil || row.Platform != "sub2api" || row.Environment != environment || row.Mode != "real" ||
		row.Endpoint != "https://api.example.test" || row.CredentialRef != "secret://sub2api-prod/read-token" ||
		row.Version != 4 || !row.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("sub2api 行读回不一致: %+v", row)
	}
	if len(row.TargetAllowlist) != 2 || row.TargetAllowlist[0] != "api.example.test" || row.TargetAllowlist[1] != "Alt.Example.Test" {
		t.Fatalf("text[] 应原样读回（归一化在工厂里做）: %v", row.TargetAllowlist)
	}

	row, err = source.Get(ctx, ConnectorPlatformNewAPI, environment)
	if err != nil {
		t.Fatalf("Get newapi: %v", err)
	}
	if row == nil || row.Mode != "fake" || row.Endpoint != "" || row.CredentialRef != "" ||
		len(row.TargetAllowlist) != 0 || row.Version != 0 || !row.UpdatedAt.IsZero() {
		t.Fatalf("NULL 列应读成空值: %+v", row)
	}

	// 没有这一行 = (nil, nil)，不是错误。
	if row, err := source.Get(ctx, ConnectorPlatformSub2API, environment+"-none"); err != nil || row != nil {
		t.Fatalf("缺行应返回 (nil, nil): %+v, %v", row, err)
	}

	// 30s 缓存：库里改了行，缓存期内读到的仍是旧版本——切换生效的上限就是这个 TTL。
	if _, err := pool.Exec(ctx,
		`UPDATE core.connector_config SET version = 5 WHERE platform = 'sub2api' AND environment = $1`,
		environment); err != nil {
		t.Fatal(err)
	}
	if row, err := source.Get(ctx, ConnectorPlatformSub2API, environment); err != nil || row.Version != 4 {
		t.Fatalf("缓存期内应仍是旧版本: %+v, %v", row, err)
	}
	fresh := NewPgConnectorConfigSource(pool)
	if row, err := fresh.Get(ctx, ConnectorPlatformSub2API, environment); err != nil || row.Version != 5 {
		t.Fatalf("新实例应读到新版本: %+v, %v", row, err)
	}
}
