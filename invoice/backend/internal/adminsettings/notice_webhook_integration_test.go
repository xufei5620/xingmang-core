package adminsettings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/testdb"
)

// XM-INV-NOTICE-WEBHOOK-SETTING：真库上跑一遍 0028 的表与四条 SQL。
// SQL 只在真库上跑过才算数（同一轮里 CR-0006 取证脚本的 GROUP BY 就是这么
// 抓出来的）。
func TestPostgresNoticeWebhookRoundTripAndAudit(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminSettingsPool(t, pool)
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool)
	service := NewService(repo, testBox{})
	actor := Actor{ID: "admin-1", RequestID: "req-1", SourceIPHash: "hash-1"}

	// 没配过：不是错误，是"通知默认关着"。
	info, err := service.NoticeWebhook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Configured {
		t.Fatalf("干净的库不该有已配置的地址：%+v", info)
	}
	if _, err = service.NoticeWebhookForDelivery(ctx); err == nil {
		t.Fatal("没配地址时投递必须取不到")
	}

	if _, err = service.SetNoticeWebhook(ctx, testWebhook, actor); err != nil {
		t.Fatal(err)
	}
	info, err = service.NoticeWebhook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Configured || info.Fingerprint != NoticeWebhookFingerprint(testWebhook) ||
		info.UpdatedBy != "admin-1" || info.UpdatedAt.IsZero() {
		t.Fatalf("保存之后：%+v", info)
	}
	got, err := service.NoticeWebhookForDelivery(ctx)
	if err != nil || got != testWebhook {
		t.Fatalf("投递取到 %q，err=%v", got, err)
	}

	// 覆盖保存：同一行更新，不是插第二行。
	second := testWebhook + "-2"
	if _, err = service.SetNoticeWebhook(ctx, second, actor); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notice_webhook_setting`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("单例表里有 %d 行", rows)
	}
	if got, err = service.NoticeWebhookForDelivery(ctx); err != nil || got != second {
		t.Fatalf("覆盖之后投递取到 %q，err=%v", got, err)
	}

	// **库里不许有地址明文**——密文列是密文，指纹列是指纹。
	var ciphertext []byte
	var fingerprint string
	if err = pool.QueryRow(ctx, `SELECT webhook_ciphertext,fingerprint FROM notice_webhook_setting`).
		Scan(&ciphertext, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fingerprint, "qyapi") || strings.Contains(fingerprint, "key=") {
		t.Fatalf("指纹列夹带了地址：%q", fingerprint)
	}

	// 审计里也不许有地址：before/after 存的是指纹与"未配置"。
	var actions []string
	auditRows, err := pool.Query(ctx, `SELECT action,before_hash,after_hash FROM audit_events
		WHERE object_type='notice_webhook_setting' ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	for auditRows.Next() {
		var action, before, after string
		if err = auditRows.Scan(&action, &before, &after); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(before+after, "qyapi") || strings.Contains(before+after, "super-secret-value") {
			t.Fatalf("审计里夹带了地址：%s %s %s", action, before, after)
		}
		actions = append(actions, action)
	}
	auditRows.Close()
	if len(actions) != 2 || actions[0] != "admin_settings.notice_webhook.set" {
		t.Fatalf("两次保存应当有两条 set 审计：%v", actions)
	}

	// 清除：行没了，投递 fail closed，且多一条 clear 审计。
	if _, err = service.ClearNoticeWebhook(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notice_webhook_setting`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("清除之后还剩 %d 行", rows)
	}
	if _, err = service.NoticeWebhookForDelivery(ctx); err == nil {
		t.Fatal("清除之后投递必须取不到地址")
	}
	var clears int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='admin_settings.notice_webhook.clear'`).Scan(&clears); err != nil {
		t.Fatal(err)
	}
	if clears != 1 {
		t.Fatalf("清除应当留一条审计，实际 %d 条", clears)
	}
}

// 指纹列有 CHECK 约束：形状不对的值进不去。这条防的是将来有人绕过
// NoticeWebhookFingerprint 直接写库。
func TestNoticeWebhookFingerprintShapeIsEnforcedByTheDatabase(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminSettingsPool(t, pool)
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO notice_webhook_setting(
		singleton_id,webhook_ciphertext,key_version,fingerprint,updated_by)
		VALUES(1,'\x00','v1','https://qyapi.weixin.qq.com/x?key=leak','admin')`)
	if err == nil {
		t.Fatal("把地址写进指纹列必须被数据库拒绝")
	}
}
