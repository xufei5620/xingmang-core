package jobs

import (
	"context"
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
    endpoint         text NOT NULL DEFAULT '',
    target_allowlist text[] NOT NULL DEFAULT '{}',
    credential_ref   text NOT NULL DEFAULT '',
    version          integer NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at       timestamptz NOT NULL,
    updated_by       text NOT NULL,
    PRIMARY KEY (platform, environment)
)`); err != nil {
		t.Fatal(err)
	}

	// environment 有 FK 到 core.environment，只能用已登记环境（development/
	// staging/production 三选一）——platform(2) x environment(3) 的键空间
	// 小到不可能靠“挑一个没人用过的值”换隔离；其他包（如
	// internal/platform/credentials 的 TestConnectorConfigSetAndList）
	// 同样会写 ('sub2api','staging')，且不保证在它之后清干净。所以本用例
	// 既要在开跑前把自己要用的键位清空一遍（不依赖表原本是空的），也要在
	// 结束时清理，两头都做才是真正的自包含。
	environment := "staging"
	cleanup := func(ctx context.Context) error {
		_, err := pool.Exec(ctx,
			`DELETE FROM core.connector_config WHERE platform IN ('sub2api', 'newapi') AND environment = $1`,
			environment)
		return err
	}
	if err := cleanup(ctx); err != nil {
		t.Fatalf("预清理接入配置行: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := cleanup(cleanupCtx); err != nil {
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
	// newapi 行只给必填列（platform/environment/mode/updated_at/updated_by）：
	// endpoint/target_allowlist/credential_ref/version 都吃迁移里的 DEFAULT
	// （''/{}'/''/1），一列都不会是 NULL——migrations/000020 给这四列都上了
	// NOT NULL DEFAULT，updated_at/updated_by 则是 NOT NULL 无默认值，必须
	// 显式给。COALESCE 仍然保留在读取侧：它读到的是「默认值」而不是
	// 「NULL」，这条 SELECT 对两者一视同仁，不必因为库层已经堵死 NULL
	// 就删掉这层防御。
	newapiUpdatedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO core.connector_config (platform, environment, mode, updated_at, updated_by)
VALUES ('newapi', $1, 'fake', $2, 'itest')`, environment, newapiUpdatedAt); err != nil {
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
		len(row.TargetAllowlist) != 0 || row.Version != 1 || !row.UpdatedAt.Equal(newapiUpdatedAt) {
		t.Fatalf("未填的可空列应读成 DEFAULT 值: %+v", row)
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
