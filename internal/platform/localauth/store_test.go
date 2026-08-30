package localauth_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
)

func localAuthPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := pool.Exec(ctx, "TRUNCATE core.staff_session, core.staff_account"); err != nil {
		t.Fatalf("清空员工账号表: %v", err)
	}
	return pool
}

func TestStoreAccountRoundTrip(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()

	hash, err := localauth.HashPassword("first-password-123")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateAccount(ctx, "alice", "Alice", []string{"staff", "admin"}, hash, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "alice" || len(created.Roles) != 2 || !created.MustChangePassword || created.Disabled {
		t.Fatalf("created = %+v", created)
	}

	// 重复用户名应拒绝
	if _, err := store.CreateAccount(ctx, "alice", "Alice2", []string{"staff"}, hash, "tester"); !errors.Is(err, localauth.ErrAlreadyExists) {
		t.Fatalf("重复用户名应 ErrAlreadyExists, got %v", err)
	}

	got, err := store.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Fatalf("got = %+v", got)
	}

	if _, err := store.GetByUsername(ctx, "no-such-user"); !errors.Is(err, localauth.ErrNotFound) {
		t.Fatalf("未登记用户应 ErrNotFound, got %v", err)
	}

	updated, err := store.SetRoles(ctx, "alice", []string{"staff"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Roles) != 1 || updated.Roles[0] != "staff" {
		t.Fatalf("updated roles = %v", updated.Roles)
	}

	list, err := store.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Username != "alice" {
		t.Fatalf("list = %+v", list)
	}
}

func TestStoreLockoutAfterFiveFailures(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	if _, err := store.CreateAccount(ctx, "bob", "Bob", []string{"staff"}, hash, "tester"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		if err := store.RecordFailure(ctx, "bob"); err != nil {
			t.Fatal(err)
		}
	}
	notYetLocked, err := store.GetByUsername(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if notYetLocked.Locked(time.Now().UTC()) {
		t.Fatal("4 次失败不该锁定")
	}

	if err := store.RecordFailure(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	locked, err := store.GetByUsername(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !locked.Locked(time.Now().UTC()) {
		t.Fatal("第 5 次失败应锁定账号")
	}
	if locked.FailedAttempts != 0 {
		t.Fatalf("锁定后失败计数应清零, got %d", locked.FailedAttempts)
	}

	if err := store.ResetFailures(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	reset, err := store.GetByUsername(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if reset.Locked(time.Now().UTC()) || reset.LastLoginAt == nil {
		t.Fatalf("ResetFailures 后应解锁并记录 last_login_at, got %+v", reset)
	}
}

func TestStoreSessionLifecycle(t *testing.T) {
	pool := localAuthPool(t)
	store := localauth.NewStore(pool)
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "carol", "Carol", []string{"staff"}, hash, "tester")
	if err != nil {
		t.Fatal(err)
	}

	token, err := store.CreateSession(ctx, acc.ID, "staging", "test-agent", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("token 不应为空")
	}

	looked, err := store.LookupSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if looked.Username != "carol" {
		t.Fatalf("looked = %+v", looked)
	}

	if _, err := store.LookupSession(ctx, "not-a-real-token"); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("不存在的 token 应 ErrSessionInvalid, got %v", err)
	}

	// 过期会话查不到。CreateSession 拒绝 ttl<=0（对负数 TTL 的拒绝本身就是
	// 一条值得测的仓储行为，见下面 TestStoreCreateSessionRejectsNonPositiveTTL），
	// 所以这里用一个专门账号 + 正常 TTL 创建后，直接用测试自己的 pool 把
	// expires_at 拨回过去，模拟"时间已经流逝到过期"而不是"创建时就非法"。
	expiringAcc, err := store.CreateAccount(ctx, "carol-expiring", "Carol Expiring", []string{"staff"}, hash, "tester")
	if err != nil {
		t.Fatal(err)
	}
	expiredToken, err := store.CreateSession(ctx, expiringAcc.ID, "staging", "test-agent", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.staff_session SET expires_at = now() - interval '1 hour' WHERE account_id = $1`, expiringAcc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupSession(ctx, expiredToken); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("过期会话应 ErrSessionInvalid, got %v", err)
	}

	if err := store.RevokeSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupSession(ctx, token); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("吊销后应 ErrSessionInvalid, got %v", err)
	}

	// SetDisabled(true) 应吊销全部会话
	token2, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDisabled(ctx, "carol", true, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupSession(ctx, token2); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("停用账号应吊销其全部会话, got %v", err)
	}
}

func TestStoreCreateSessionRejectsNonPositiveTTL(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "erin", "Erin", []string{"staff"}, hash, "tester")
	if err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []time.Duration{0, -time.Hour} {
		if _, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", ttl); !errors.Is(err, localauth.ErrInvalidInput) {
			t.Fatalf("ttl=%v 应 ErrInvalidInput, got %v", ttl, err)
		}
	}
}

func TestStoreResetPasswordRevokesSessions(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "dave", "Dave", []string{"staff"}, hash, "tester")
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	newHash, _ := localauth.HashPassword("second-password-456")
	updated, err := store.ResetPassword(ctx, "dave", newHash, true, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.MustChangePassword {
		t.Fatal("重置密码应设置 must_change_password")
	}
	if _, err := store.LookupSession(ctx, token); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("重置密码应吊销既有会话, got %v", err)
	}
}
