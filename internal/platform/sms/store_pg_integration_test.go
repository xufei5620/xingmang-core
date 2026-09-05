package sms

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore 的集成测试：SQL 只有对着真实 Postgres 跑过才算数。
//
// 迁移 000040 给 sms_resource 加了五列、新建了 sms_email、重建了
// sms_operation 的 kind CHECK——这三样在编译期一个都看不出来：列名写错、
// 参数位错、CHECK 漏了新 kind 全是运行时才炸。
//
// 用 scripts/dev/worktree-testdb.sh 起本 worktree 专属的库：
//
//	eval "$(scripts/dev/worktree-testdb.sh)"
//	go test ./internal/platform/sms/ -run TestPgStore -count=1
const testEnvironment = "development"

func pgStore(t *testing.T) *PgStore {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过 PgStore 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{
		"sms.sms_code", "sms.sms_operation", "sms.sms_resource", "sms.sms_order",
		"sms.sms_email", "sms.provider_status",
	} {
		if _, err := pool.Exec(context.Background(), "TRUNCATE "+table+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	return NewPgStore(pool, testEnvironment, func() time.Time { return testNow })
}

// 资源的五个官方新字段要能写进去、读回来；同步传空时**保留原值**。
func TestPgStoreResourceKeepsOfficialFieldsAcrossSync(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()

	id, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-1", Phone: "79990000001", PhoneMask: "****0001",
		Service: "go", Country: "12", Status: "1",
		Operator: "mts", PriceText: "0.35", VerificationType: "sms", Subtype: SubtypeRent, CountryPhoneCode: "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 一次不带新字段的同步（比如列表接口不回它们）。
	if _, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-1", Status: "6",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetResource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "6" {
		t.Errorf("status 应更新为 6, got %q", got.Status)
	}
	if got.Operator != "mts" || got.PriceText != "0.35" || got.VerificationType != "sms" ||
		got.Subtype != SubtypeRent || got.CountryPhoneCode != "7" {
		t.Errorf("官方字段被同步冲掉了: %+v", got)
	}
}

// 邮箱：按 (provider, external_id) 幂等；value 一旦收到不会被后续同步冲成空。
func TestPgStoreEmailUpsertKeepsValue(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()

	id, err := store.UpsertEmail(ctx, Email{
		Provider: ProviderHero, ExternalID: "9", Site: "example.com", Email: "a@x",
		Status: "WAIT", CostText: "0.20", Currency: 840, UpstreamDate: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 收到验证内容。
	if _, err := store.UpsertEmail(ctx, Email{Provider: ProviderHero, ExternalID: "9", Status: "SUCCESS", Value: "123456"}); err != nil {
		t.Fatal(err)
	}
	// 再来一次不带 value 的列表同步。
	id2, err := store.UpsertEmail(ctx, Email{Provider: ProviderHero, ExternalID: "9", Status: "SUCCESS"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("同一邮箱应保持本地 UUID: %s vs %s", id, id2)
	}
	got, err := store.GetEmail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "123456" || got.Status != "SUCCESS" || got.Site != "example.com" || got.Currency != 840 {
		t.Errorf("email = %+v", got)
	}
	list, err := store.ListEmails(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err = %v", list, err)
	}
}

// 62 没有邮箱接码：CHECK 要挡住误写。
func TestPgStoreEmailRejectsSMS62(t *testing.T) {
	store := pgStore(t)
	if _, err := store.UpsertEmail(context.Background(), Email{Provider: ProviderSMS62, ExternalID: "x"}); err == nil {
		t.Fatal("62 的邮箱行必须被 CHECK 挡住")
	}
}

// 新 kind 要能进台账（CHECK 重建过），并能带 email_id。
func TestPgStoreOperationAcceptsNewKinds(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	emailID, err := store.UpsertEmail(ctx, Email{Provider: ProviderHero, ExternalID: "9", Status: "WAIT"})
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{KindRent, KindEmailPurchase, KindEmailCancel, KindEmailReorder, KindFavoriteSet, KindFavoriteRemove} {
		op := Operation{
			ID: "00000000-0000-0000-0000-00000000000" + string(rune('1'+i)), Provider: ProviderHero, Kind: kind,
			RequestHash: "h" + kind, ParamsSummary: kind, StartedAt: testNow, UpdatedAt: testNow, EmailID: emailID,
		}
		if err := store.PrepareOperation(ctx, op); err != nil {
			t.Fatalf("kind %s 进不了台账: %v", kind, err)
		}
	}
}
