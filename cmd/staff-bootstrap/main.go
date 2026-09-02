// Command staff-bootstrap creates the first local-login (XM-LOGIN) staff
// account exactly once. It is a Platform Lifecycle Operation (constitution
// clause 3), not an HTTP/API write path: it exists because the very first
// admin account cannot be created through the staff.account.create Action
// —— that Action requires an already-authenticated staff.manage principal,
// and before this command runs there is no local account at all.
//
// Re-running with the same username is idempotent: if the account already
// exists, it prints "exists" and exits 0 without touching the row (roles or
// password drift afterwards is handled by staff.account.set_roles /
// staff.account.reset_password, not by this command).
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

var getenv = os.Getenv

// bootstrapRoles 是首个账号获得的角色集合：admin（全权，含 XM-LOGIN 新加的
// staff.manage / credential.manage / connector.manage，见
// oidcauth.DefaultRoleScopeMap 第 6 条）、credential-admin（与 admin 的授予
// 并存，专门角色的最小面语义不受影响）、staff（基础只读）。三个都是
// oidcauth.DefaultRoleScopeMap 里已经存在的角色名，不需要额外配置就能翻译
// 出 scope。
var bootstrapRoles = []string{"admin", "credential-admin", "staff"}

const generatedPasswordLen = 20

// generatedAlphabet 去掉容易混淆的字符（l/I/1/O/0）。与
// internal/platform/localauth 里的字母表一致，但那是未导出常量，
// 跨包不可复用，这里独立声明一份。
const generatedAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func main() {
	flag.Parse()
	command := flag.Arg(0)
	if command == "" {
		command = "up"
	}
	if command == "version" {
		fmt.Println(buildinfo.String())
		return
	}
	if command != "up" {
		fmt.Fprintln(os.Stderr, "用法: staff-bootstrap [up|version]")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil)).With(
		slog.String("service", "staff-bootstrap"),
		slog.String("build", buildinfo.String()),
	)
	ctx := context.Background()

	username := strings.TrimSpace(getenv("XM_STAFF_BOOTSTRAP_USERNAME"))
	if username == "" {
		logger.Error("缺少 XM_STAFF_BOOTSTRAP_USERNAME")
		os.Exit(2)
	}

	dsn, err := databaseURLFromEnv(ctx, getenv, logger)
	if err != nil {
		logger.Error("数据库连接配置无效", slog.Any("err", err))
		os.Exit(2)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("数据库连接池创建失败", slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()

	store := localauth.NewStore(pool)

	_, err = store.GetByUsername(ctx, username)
	switch {
	case err == nil:
		// 幂等：账号已存在就什么都不做，不核对角色/密码是否与本次入参一致
		// ——bootstrap 只负责"从无到有"。
		fmt.Println("exists")
		return
	case errors.Is(err, localauth.ErrNotFound):
		// 继续走下面的创建流程
	default:
		logger.Error("查询账号失败", slog.Any("err", err))
		os.Exit(1)
	}

	password := strings.TrimSpace(getenv("XM_STAFF_BOOTSTRAP_PASSWORD"))
	generated := false
	if password == "" {
		generated = true
		password, err = generatePassword()
		if err != nil {
			logger.Error("生成初始密码失败", slog.Any("err", err))
			os.Exit(1)
		}
	}
	if err := localauth.ValidatePasswordPolicy(password); err != nil {
		logger.Error("XM_STAFF_BOOTSTRAP_PASSWORD 不满足口令策略", slog.Any("err", err))
		os.Exit(2)
	}
	hash, err := localauth.HashPassword(password)
	if err != nil {
		logger.Error("口令哈希失败", slog.Any("err", err))
		os.Exit(1)
	}

	// bootstrapRoles 固定含 "admin"，而 admin 角色持有 staff.manage
	// （oidcauth.DefaultRoleScopeMap）——按 XM-AUTH-TOTP0 的强制范围裁定
	// （CR-0006 正文："仅限持有 staff.manage ... 的账号"），第一个账号同样
	// 必须在下次登录后启用 TOTP，不因为是 bootstrap 账号而豁免：must_enroll_totp
	// 只是把人引导去启用页（RequireAuth 的软重定向，与 must_change_password
	// 同一语义），从不在后端层面拒绝登录或锁死账号，因此不存在"第一个账号被
	// 自己的安全策略锁在门外"的风险（见 docs/handoffs/slices/XM-AUTH-TOTP0.md
	// 对这条取舍的完整说明）。
	acc, err := store.CreateAccount(ctx, username, username, bootstrapRoles, hash, "lifecycle:staff-bootstrap", true)
	if err != nil {
		logger.Error("创建账号失败", slog.Any("err", err))
		os.Exit(1)
	}

	// 只输出可核对的非敏感事实；DSN、CredentialRef 均不输出。
	fmt.Printf("username=%s roles=%s must_change_password=%v\n",
		acc.Username, strings.Join(acc.Roles, ","), acc.MustChangePassword)
	if generated {
		// 明文密码只在这一行、这一次输出——运维应立即转交本人并清除终端
		// 历史；must_change_password=true（CreateAccount 的固定默认值）会
		// 强制下次登录改密，这行输出因此只是一次性的引导凭据。
		fmt.Printf("initial_password=%s\n", password)
	}
}

func generatePassword() (string, error) {
	buf := make([]byte, generatedPasswordLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, generatedPasswordLen)
	for i, v := range buf {
		out[i] = generatedAlphabet[int(v)%len(generatedAlphabet)]
	}
	return string(out), nil
}

// databaseURLFromEnv 与 cmd/runway-threshold-bootstrap 的同名函数是同一套
// 纪律（刻意保持同构，未抽公共包——两个 bootstrap 命令都很小，各自持有一份
// 比引入一个只有两个调用方的共享包更直接）：非开发环境或已显式配置
// DATABASE_PASSWORD_REF 时，密码只能来自 CredentialRef；DSN 内联密码、
// ?password= 查询参数一律拒绝。
func databaseURLFromEnv(ctx context.Context, read func(string) string, logger *slog.Logger) (string, error) {
	raw := strings.TrimSpace(read("DATABASE_URL"))
	if raw == "" {
		raw = strings.TrimSpace(read("XM_DATABASE_URL"))
	}
	if raw == "" {
		return "", fmt.Errorf("DATABASE_URL is required")
	}
	environment := strings.TrimSpace(read("ENVIRONMENT"))
	refText := strings.TrimSpace(read("DATABASE_PASSWORD_REF"))
	if err := pgdsn.Validate(raw, environment != "development" || refText != ""); err != nil {
		return "", err
	}
	if refText == "" {
		return raw, nil
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return "", err
	}
	provider, err := secrets.NewEnvProvider(map[string]string{ref.String(): "DATABASE_PASSWORD"}, secrets.WithLookup(func(name string) (string, bool) {
		value := read(name)
		return value, value != ""
	}))
	if err != nil {
		return "", err
	}
	if logger == nil {
		logger = slog.Default()
	}
	audited := secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment)
	value, err := audited.Resolve(secrets.WithCaller(ctx, "lifecycle:staff-bootstrap"), ref, "staff bootstrap database connection")
	if err != nil {
		return "", err
	}
	return pgdsn.WithPassword(raw, value.Reveal())
}
