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
	if _, err := pool.Exec(ctx, `TRUNCATE
		core.staff_login_challenge, core.staff_totp_recovery_code, core.staff_session, core.staff_account
		CASCADE`); err != nil {
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
	created, err := store.CreateAccount(ctx, "alice", "Alice", []string{"staff", "admin"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "alice" || len(created.Roles) != 2 || !created.MustChangePassword || created.Disabled {
		t.Fatalf("created = %+v", created)
	}

	// 重复用户名应拒绝
	if _, err := store.CreateAccount(ctx, "alice", "Alice2", []string{"staff"}, hash, "tester", false); !errors.Is(err, localauth.ErrAlreadyExists) {
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

	updated, err := store.SetRoles(ctx, "alice", []string{"staff"}, "tester", false)
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
	if _, err := store.CreateAccount(ctx, "bob", "Bob", []string{"staff"}, hash, "tester", false); err != nil {
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
	acc, err := store.CreateAccount(ctx, "carol", "Carol", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}

	token, err := store.CreateSession(ctx, acc.ID, "staging", "test-agent", "127.0.0.1", time.Hour, []string{"pwd"}, nil)
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
	expiringAcc, err := store.CreateAccount(ctx, "carol-expiring", "Carol Expiring", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	expiredToken, err := store.CreateSession(ctx, expiringAcc.ID, "staging", "test-agent", "127.0.0.1", time.Hour, []string{"pwd"}, nil)
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
	token2, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour, []string{"pwd"}, nil)
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
	acc, err := store.CreateAccount(ctx, "erin", "Erin", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []time.Duration{0, -time.Hour} {
		if _, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", ttl, []string{"pwd"}, nil); !errors.Is(err, localauth.ErrInvalidInput) {
			t.Fatalf("ttl=%v 应 ErrInvalidInput, got %v", ttl, err)
		}
	}
}

func TestStoreResetPasswordRevokesSessions(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "dave", "Dave", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour, []string{"pwd"}, nil)
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

func TestStoreCreateAccountSetsMustEnrollTOTP(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")

	withTOTP, err := store.CreateAccount(ctx, "frank", "Frank", []string{"admin"}, hash, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	if !withTOTP.MustEnrollTOTP {
		t.Fatal("requiresTOTP=true 时新账号应 must_enroll_totp=true")
	}
	if withTOTP.TOTPActive() {
		t.Fatal("新账号不应已经是 TOTP 激活状态")
	}

	withoutTOTP, err := store.CreateAccount(ctx, "grace", "Grace", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	if withoutTOTP.MustEnrollTOTP {
		t.Fatal("requiresTOTP=false 时新账号不应 must_enroll_totp")
	}
}

func TestStoreSetRolesOnlyRaisesMustEnrollTOTP(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	if _, err := store.CreateAccount(ctx, "henry", "Henry", []string{"staff"}, hash, "tester", false); err != nil {
		t.Fatal(err)
	}

	// 改成需要 TOTP 的角色 -> 置真
	raised, err := store.SetRoles(ctx, "henry", []string{"admin"}, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	if !raised.MustEnrollTOTP {
		t.Fatal("改成需要 TOTP 的角色应把 must_enroll_totp 置真")
	}

	// 再改回不需要 TOTP 的角色 -> 不应被清回假（从不主动清除，只能靠 ConfirmTOTP）
	lowered, err := store.SetRoles(ctx, "henry", []string{"staff"}, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	if !lowered.MustEnrollTOTP {
		t.Fatal("改成不需要 TOTP 的角色不应清除已经置真的 must_enroll_totp")
	}
}

func TestStoreSetRolesDoesNotReRaiseWhenAlreadyEnrolled(t *testing.T) {
	pool := localAuthPool(t)
	store := localauth.NewStore(pool)
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "iris", "Iris", []string{"admin"}, hash, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := localauth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	ref := "secret://staff-totp/" + acc.ID.String()
	if _, err := store.SetTOTPSecretRef(ctx, "iris", ref, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmTOTP(ctx, "iris", ref, []string{localauth.HashRecoveryCode("AAAAABBBBB")}, "tester"); err != nil {
		t.Fatal(err)
	}
	_ = secret

	// 已激活后再次 SetRoles(requiresTOTP=true) 不该把 must_enroll_totp
	// 重新置真——已经在用的二因素不该被一次改角色重新标成"必须启用"。
	updated, err := store.SetRoles(ctx, "iris", []string{"admin"}, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MustEnrollTOTP {
		t.Fatal("已激活 TOTP 的账号改角色不该重新置位 must_enroll_totp")
	}
}

func TestStoreTOTPEnrollConfirmResetLifecycle(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "jane", "Jane", []string{"admin"}, hash, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	ref := "secret://staff-totp/" + acc.ID.String()

	pending, err := store.SetTOTPSecretRef(ctx, "jane", ref, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if pending.TOTPSecretRef != ref || pending.TOTPActive() {
		t.Fatalf("启用中状态: %+v", pending)
	}

	codes := []string{
		localauth.HashRecoveryCode("AAAAABBBBB"),
		localauth.HashRecoveryCode("CCCCCDDDDD"),
	}
	confirmed, err := store.ConfirmTOTP(ctx, "jane", ref, codes, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.TOTPActive() || confirmed.MustEnrollTOTP {
		t.Fatalf("确认后应激活且清除 must_enroll_totp: %+v", confirmed)
	}
	n, err := store.UnusedRecoveryCodeCount(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("未使用恢复码数 = %d, want 2", n)
	}

	ok, err := store.ConsumeRecoveryCode(ctx, acc.ID, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("首次消费应成功")
	}
	if ok2, err := store.ConsumeRecoveryCode(ctx, acc.ID, codes[0]); err != nil || ok2 {
		t.Fatalf("同一张恢复码不能消费两次: ok=%v err=%v", ok2, err)
	}
	n2, err := store.UnusedRecoveryCodeCount(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Fatalf("消费一张后剩余 = %d, want 1", n2)
	}

	// 重置：清空引用/激活时间，恢复码全部删除，会话吊销，must_enroll_totp 重新置真
	token, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour, []string{"pwd", "otp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reset, err := store.ResetTOTP(ctx, "jane", "admin-tester")
	if err != nil {
		t.Fatal(err)
	}
	if reset.TOTPActive() || reset.TOTPSecretRef != "" || !reset.MustEnrollTOTP {
		t.Fatalf("重置后状态: %+v", reset)
	}
	if n3, err := store.UnusedRecoveryCodeCount(ctx, acc.ID); err != nil || n3 != 0 {
		t.Fatalf("重置后恢复码应清空: n=%d err=%v", n3, err)
	}
	if _, err := store.LookupSession(ctx, token); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("重置 TOTP 应吊销既有会话, got %v", err)
	}
}

func TestStoreSetTOTPSecretRefRejectsWhenAlreadyActive(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "kevin", "Kevin", []string{"admin"}, hash, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	ref := "secret://staff-totp/" + acc.ID.String()
	if _, err := store.SetTOTPSecretRef(ctx, "kevin", ref, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmTOTP(ctx, "kevin", ref, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTOTPSecretRef(ctx, "kevin", ref, "tester"); !errors.Is(err, localauth.ErrTOTPAlreadyEnrolled) {
		t.Fatalf("已激活时重新 enroll 应 ErrTOTPAlreadyEnrolled, got %v", err)
	}
}

func TestStoreConfirmTOTPRejectsMismatchedRef(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "laura", "Laura", []string{"admin"}, hash, "tester", true)
	if err != nil {
		t.Fatal(err)
	}
	ref := "secret://staff-totp/" + acc.ID.String()
	if _, err := store.SetTOTPSecretRef(ctx, "laura", ref, "tester"); err != nil {
		t.Fatal(err)
	}
	wrongRef := "secret://staff-totp/00000000-0000-0000-0000-000000000000"
	if _, err := store.ConfirmTOTP(ctx, "laura", wrongRef, nil, "tester"); !errors.Is(err, localauth.ErrTOTPNotEnrolled) {
		t.Fatalf("引用不匹配应 ErrTOTPNotEnrolled（防 TOCTOU）, got %v", err)
	}
}

func TestStoreLoginChallengeLifecycle(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "mike", "Mike", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}

	token, err := store.CreateLoginChallenge(ctx, acc.ID, "staging", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	looked, err := store.LookupLoginChallenge(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if looked.Username != "mike" {
		t.Fatalf("looked = %+v", looked)
	}

	if _, err := store.LookupLoginChallenge(ctx, "not-a-real-token"); !errors.Is(err, localauth.ErrChallengeInvalid) {
		t.Fatalf("未知 token 应 ErrChallengeInvalid, got %v", err)
	}

	if err := store.ConsumeLoginChallenge(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupLoginChallenge(ctx, token); !errors.Is(err, localauth.ErrChallengeInvalid) {
		t.Fatalf("已消费的 token 应 ErrChallengeInvalid, got %v", err)
	}
	// 幂等：重复 consume 不报错
	if err := store.ConsumeLoginChallenge(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoginChallengeExpires(t *testing.T) {
	pool := localAuthPool(t)
	store := localauth.NewStore(pool)
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "nina", "Nina", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateLoginChallenge(ctx, acc.ID, "staging", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.staff_login_challenge SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, acc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupLoginChallenge(ctx, token); !errors.Is(err, localauth.ErrChallengeInvalid) {
		t.Fatalf("过期挑战应 ErrChallengeInvalid, got %v", err)
	}
}

func TestStoreCreateSessionWithAMRAndMFA(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "oscar", "Oscar", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	// Postgres timestamptz 只存到微秒精度；Go time.Now() 有纳秒精度，往返一趟
	// 会在亚微秒位上产生差异（与"同一时刻"无关，是存储精度截断）。截到微秒
	// 精度再比较，才是这里真正要断言的"值确实存对了"。
	mfaAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	token, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour, []string{"otp", "pwd", "pwd"}, &mfaAt)
	if err != nil {
		t.Fatal(err)
	}
	looked, err := store.LookupSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if looked.SessionMFAAt == nil || !looked.SessionMFAAt.Equal(mfaAt) {
		t.Fatalf("SessionMFAAt = %v, want %v", looked.SessionMFAAt, mfaAt)
	}
	if len(looked.SessionAMR) != 2 || looked.SessionAMR[0] != "otp" || looked.SessionAMR[1] != "pwd" {
		t.Fatalf("SessionAMR 应去重排序, got %v", looked.SessionAMR)
	}
}

func TestStoreTouchSessionMFA(t *testing.T) {
	store := localauth.NewStore(localAuthPool(t))
	ctx := context.Background()
	hash, _ := localauth.HashPassword("first-password-123")
	acc, err := store.CreateAccount(ctx, "paul", "Paul", []string{"staff"}, hash, "tester", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateSession(ctx, acc.ID, "staging", "ua", "127.0.0.1", time.Hour, []string{"pwd"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.LookupSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if before.SessionMFAAt != nil || len(before.SessionAMR) != 1 {
		t.Fatalf("初始会话不应带 mfa_at: %+v", before)
	}

	if err := store.TouchSessionMFA(ctx, token); err != nil {
		t.Fatal(err)
	}
	after, err := store.LookupSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionMFAAt == nil {
		t.Fatal("步进后应有 mfa_at")
	}
	hasOTP := false
	for _, a := range after.SessionAMR {
		if a == "otp" {
			hasOTP = true
		}
	}
	if !hasOTP {
		t.Fatalf("步进后 amr 应含 otp, got %v", after.SessionAMR)
	}

	if err := store.RevokeSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchSessionMFA(ctx, token); !errors.Is(err, localauth.ErrSessionInvalid) {
		t.Fatalf("已吊销的会话步进应 ErrSessionInvalid, got %v", err)
	}
}
