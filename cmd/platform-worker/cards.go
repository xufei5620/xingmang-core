package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// buildCardSyncer 按环境变量组装卡片同步器。
//
// 返回 nil 表示未启用（XM_CARDS_MODE 未设或为 off），调用方据此不打开
// CardSyncEnabled。**默认关闭**与 API 侧同一条纪律：这条链路会打上游，
// 一个没配好凭据就启用的环境会每 5 分钟产生一次注定失败的调用。
func buildCardSyncer(
	pool *pgxpool.Pool,
	provider secrets.SecretProvider,
	environment string,
	getenv func(string) string,
) (*cards.Syncer, error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("XM_CARDS_MODE")))
	if mode == "" || mode == "off" {
		return nil, nil
	}

	store := cards.NewPgStore(pool, environment, time.Now)

	var client infini.CardClient
	switch mode {
	case "fake":
		client = infini.NewFake()
	case "real":
		real, err := newWorkerInfiniClient(provider, getenv)
		if err != nil {
			return nil, err
		}
		client = real
	default:
		return nil, fmt.Errorf("XM_CARDS_MODE %q: 只接受 off / fake / real", mode)
	}

	grace := jobs.DefaultCardUnknownGrace
	if raw := strings.TrimSpace(getenv("XM_CARDS_UNKNOWN_GRACE")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("XM_CARDS_UNKNOWN_GRACE: %w", err)
		}
		if parsed <= 0 {
			// 宽限期为零意味着一次超时立刻惊动人，而上游的异步开卡本来就
			// 要几十秒。拒绝这种配置比默默用一个兜底值诚实。
			return nil, fmt.Errorf("XM_CARDS_UNKNOWN_GRACE 必须为正")
		}
		grace = parsed
	}

	// 流水同步默认关闭：它的调用量与卡数成正比，而上游限流阈值未知
	// （见 contracts/connectors/infini.card.v1.md 的验证清单）。
	syncTx := strings.EqualFold(strings.TrimSpace(getenv("XM_CARDS_SYNC_TRANSACTIONS")), "true")

	return cards.NewSyncer(client, store, cards.SyncOptions{
		UnknownGrace:     grace,
		SyncTransactions: syncTx,
		Now:              time.Now,
	}), nil
}

func newWorkerInfiniClient(provider secrets.SecretProvider, getenv func(string) string) (*infini.Client, error) {
	if provider == nil {
		return nil, fmt.Errorf("卡片同步 real 模式需要 SecretProvider")
	}

	baseURL := strings.TrimSpace(getenv("XM_CARDS_BASE_URL"))
	keyIDRaw := strings.TrimSpace(getenv("XM_CARDS_KEY_ID_REF"))
	secretRaw := strings.TrimSpace(getenv("XM_CARDS_SECRET_REF"))
	if baseURL == "" || keyIDRaw == "" || secretRaw == "" {
		return nil, fmt.Errorf("XM_CARDS_MODE=real 需要 XM_CARDS_BASE_URL / XM_CARDS_KEY_ID_REF / XM_CARDS_SECRET_REF")
	}

	keyIDRef, err := secrets.ParseCredentialRef(keyIDRaw)
	if err != nil {
		return nil, fmt.Errorf("XM_CARDS_KEY_ID_REF: %w", err)
	}
	secretRef, err := secrets.ParseCredentialRef(secretRaw)
	if err != nil {
		return nil, fmt.Errorf("XM_CARDS_SECRET_REF: %w", err)
	}

	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("XM_CARDS_BASE_URL %q 无法解析", baseURL)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("XM_CARDS_BASE_URL 必须是 https，当前 %q", baseURL)
	}

	return infini.NewClient(baseURL, provider, keyIDRef, secretRef, []string{u.Hostname()}), nil
}

// cardsSecretProvider 组一个能解析任意引用的文件 SecretProvider。
//
// 与 API 侧的 platformUsersSecretProvider 读同一个 XM_SECRET_ROOT 目录：
// 两个进程解析同一个引用必须拿到同一个值，否则签名会在一边通过、
// 在另一边 401。审计包一层，让「这次凭据是谁解析的」进得了审计。
func cardsSecretProvider(secretRoot, environment string, logger *slog.Logger) secrets.SecretProvider {
	if logger == nil {
		logger = slog.Default()
	}
	recorder := secrets.NewSlogRecorder(logger)
	return secrets.NewAudited(secrets.NewFileProvider(secretRoot), recorder, environment)
}
