package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 账号 id 要拼进环境变量名，字符集与 API 侧同一条规矩。
var workerAccountIDPattern = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

// buildCardSyncer 按环境变量组装卡片同步器。
//
// 返回 nil 表示未启用（XM_CARDS_MODE 未设或为 off），调用方据此不打开
// CardSyncEnabled。**默认关闭**与 API 侧同一条纪律：这条链路会打上游，
// 一个没配好凭据就启用的环境会每 5 分钟产生一次注定失败的调用。
//
// 账号清单与 API 侧读同一批变量：两个进程必须解析出**同一组账号**，
// 否则会出现「API 能开卡的账号，worker 不认识」——那笔操作的不确定态
// 永远收敛不了。
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
	if mode != "fake" && mode != "real" {
		return nil, fmt.Errorf("XM_CARDS_MODE %q: 只接受 off / fake / real", mode)
	}

	baseURL := strings.TrimSpace(getenv("XM_CARDS_BASE_URL"))
	ids, err := workerAccountIDs(getenv("XM_CARDS_ACCOUNTS"))
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("XM_CARDS_MODE=%s 但 XM_CARDS_ACCOUNTS 为空", mode)
	}

	accounts := make([]cards.Account, 0, len(ids))
	for _, id := range ids {
		var client infini.CardClient
		switch mode {
		case "fake":
			client = infini.NewFake()
		case "real":
			realClient, err := workerInfiniClient(baseURL, id, provider)
			if err != nil {
				return nil, fmt.Errorf("账号 %s: %w", id, err)
			}
			client = realClient
		}
		// 同步器不做限额判定（它不发起花钱操作），所以不读上限。
		accounts = append(accounts, cards.Account{ID: id, Client: client})
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

	syncTx := syncTransactionsEnabled(getenv("XM_CARDS_SYNC_TRANSACTIONS"))

	store := cards.NewPgStore(pool, environment, time.Now)
	// 提现的收敛也挂在这一轮：上游受理后是异步上链的，提现响应只回一个
	// 受理确认，没有这一步页面上的状态会永远停在「已提交」。
	//
	// 这里造的 WithdrawService **不带提现额度**（accounts 里没有
	// WithdrawLimits）——worker 只查状态回写，不发起提现，而额度是发起时
	// 的闸。给它一份额度反而会让人以为 worker 也能提现。
	withdraw := cards.NewWithdrawService(accounts, store, time.Now)
	return cards.NewSyncer(accounts, store, cards.SyncOptions{
		UnknownGrace:     grace,
		SyncTransactions: syncTx,
		Withdrawals:      withdraw,
		Now:              time.Now,
	}), nil
}

// syncTransactionsEnabled 决定要不要在每轮同步里拉流水。**默认开启。**
//
// 它原本默认关闭，理由是「调用量与卡数成正比，而上游限流阈值未知」。
// 那个未知在 2026-09-05 消失了：实测 600 次/分钟/密钥。按 250 张卡、
// 5 分钟一轮算是 50 次/分钟，占预算的 8%；而卡状态刷新同期改成了批量
// （250 张卡从 250 次调用降到 3 次），恰好把原先被它占掉的预算让了出来。
//
// 回调（XM-CARD4）已经能秒级推进单卡流水，所以这一轮周期同步是**兜底**：
// 补回调丢掉的、以及回调里没有的历史。兜底默认开着才有意义。
//
// 无法识别的值按开启处理，因为这个开关的两个方向不对称：多同步一轮的
// 代价是几十次调用，少同步的代价是流水长期缺失而没有任何报错。
// 只有显式的 false 才关闭。
func syncTransactionsEnabled(raw string) bool {
	return !strings.EqualFold(strings.TrimSpace(raw), "false")
}

func workerAccountIDs(raw string) ([]string, error) {
	var ids []string
	for _, part := range strings.Split(raw, ",") {
		id := strings.ToUpper(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if !workerAccountIDPattern.MatchString(id) {
			return nil, fmt.Errorf("账号 id %q 非法：只接受大写字母、数字与下划线", id)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func workerInfiniClient(
	baseURL, id string,
	provider secrets.SecretProvider,
) (*infini.Client, error) {
	if provider == nil {
		return nil, fmt.Errorf("real 模式需要 SecretProvider")
	}

	if baseURL == "" {
		return nil, fmt.Errorf("real 模式需要 XM_CARDS_BASE_URL")
	}
	// 引用由账号 id 推出，与 API 侧同一条推导——两边必须落在同一个 scope，
	// 否则管理端写进一个目录、worker 读另一个目录。
	keyIDRaw, secretRaw := cards.CredentialRefsFor(id)

	keyIDRef, err := secrets.ParseCredentialRef(keyIDRaw)
	if err != nil {
		return nil, fmt.Errorf("KEY_ID_REF: %w", err)
	}
	secretRef, err := secrets.ParseCredentialRef(secretRaw)
	if err != nil {
		return nil, fmt.Errorf("SECRET_REF: %w", err)
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
