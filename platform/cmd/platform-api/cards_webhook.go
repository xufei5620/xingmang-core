package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// cardWebhookProcessor 把回调端点需要的三样东西接到一起：
// 密钥解析、事件去重台账、定向刷新。
//
// 单独一个类型而不是让 httpapi 直接吃三个依赖：端点那边只该知道
// 「验签、去重、刷新」这三件事，不该知道密钥是从文件读的、刷新是同步器做的。
type cardWebhookProcessor struct {
	provider secrets.SecretProvider
	store    *cards.PgStore
	syncer   *cards.Syncer
	// notifier 为 nil 时不推送（没配 webhook 地址的部署）。
	notifier cards.Notifier
	logger   *slog.Logger
}

// WebhookSecret 解析某账号的回调密钥。
//
// 每次调用都重新解析，不缓存：与 API 密钥同一条纪律——运营在管理端轮换
// 之后，下一次回调就该用新值，而不是等进程重启。回调本来就不密集，
// 一次文件读的代价可以忽略。
func (p *cardWebhookProcessor) WebhookSecret(ctx context.Context, account string) (string, error) {
	ref, err := secrets.ParseCredentialRef(cards.WebhookSecretRefFor(account))
	if err != nil {
		return "", fmt.Errorf("回调密钥引用: %w", err)
	}
	value, err := p.provider.Resolve(ctx, ref, "infini card webhook signature")
	if err != nil {
		return "", fmt.Errorf("解析回调密钥: %w", err)
	}
	// **必须是 Reveal()，不是 String()。**
	//
	// SecretValue.String() 恒定返回 "[REDACTED]"——那是它的设计目的，
	// 防止密钥被误打进日志。拿它算 HMAC 得到的是一个对所有密钥都相同的
	// 常量，症状是「签名永远不匹配」；更坏的是那个遮蔽串不是 base64，
	// 会把诊断引向「密钥值填错了」，于是人去反复重填一个本来就正确的密钥。
	// 2026-09-05 就这样绕了三轮。
	secret := strings.TrimSpace(value.Reveal())
	if secret == "" {
		return "", fmt.Errorf("账号 %s 的回调密钥为空", account)
	}
	return secret, nil
}

func (p *cardWebhookProcessor) RecordWebhookEvent(
	ctx context.Context, account string, ev cards.WebhookEvent,
) (cards.WebhookRecord, error) {
	return p.store.RecordWebhookEvent(ctx, account, ev)
}

func (p *cardWebhookProcessor) RefreshCard(
	ctx context.Context, account, cardID string, withTransactions bool,
) error {
	return p.syncer.RefreshCard(ctx, account, cardID, withTransactions)
}

func (p *cardWebhookProcessor) MarkWebhookEventProcessed(
	ctx context.Context, account, eventID string,
) error {
	return p.store.MarkWebhookEventProcessed(ctx, account, eventID)
}

func (p *cardWebhookProcessor) RecordCardChallenge(ctx context.Context, c cards.CardChallenge) error {
	return p.store.RecordCardChallenge(ctx, c)
}

// NotifyCardEvent 尽力而为地推送。
//
// **不返回错误**：调用方（回调端点）已经把事件处理完并标记完成了，
// 此时推送失败若能改变响应，上游就会重投一个已经处理过的事件——重投会被
// 去重挡掉，推送也不会补发，只是白白让上游重试八次。丢一条推送是可接受的
// 损失，丢一次状态刷新不是。
func (p *cardWebhookProcessor) NotifyCardEvent(ctx context.Context, n cards.Notification) {
	if p.notifier == nil {
		return
	}
	if err := p.notifier.Notify(ctx, n); err != nil {
		p.logger.WarnContext(ctx, "card_notify_failed",
			"module", "platform.api", "account", n.Account, "kind", n.Kind,
			"err", err.Error())
	}
}

func (p *cardWebhookProcessor) RecordWebhookEventFailure(
	ctx context.Context, account, eventID, reason string,
) error {
	return p.store.RecordWebhookEventFailure(ctx, account, eventID, reason)
}

// buildCardWebhookProcessor 组装回调处理器。
//
// 返回 nil 表示**这条路由整个不挂载**：卡片功能没开、或者没有数据库时，
// 一个永远返回 401 的公网端点不如根本不存在——不存在的端点连被试探的
// 价值都没有。
//
// 注意这里**不检查密钥是否已配**：密钥是运营在管理端随时填的，
// 启动时没配不代表运行时也没配。密钥缺失由端点在每次请求时 fail closed。
func buildCardWebhookProcessor(
	cfg cardsConfig,
	store *cards.PgStore,
	accounts []cards.Account,
	provider secrets.SecretProvider,
	logger *slog.Logger,
) *cardWebhookProcessor {
	if cfg.Mode == cardsModeOff || store == nil || len(accounts) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	// 同步器在这里只用它的定向刷新：周期同步仍然只在 worker 里跑，
	// API 进程不注册任何周期任务。
	syncer := cards.NewSyncer(accounts, store, cards.SyncOptions{})
	return &cardWebhookProcessor{
		provider: provider, store: store, syncer: syncer, logger: logger,
		notifier: cardNotifier(provider),
	}
}

// cardNotifier 构造推送器。
//
// **地址每次调用时解析**：它是凭据（URL 里就带 key），运营在管理端轮换后
// 下一条推送就该用新值，而不是等进程重启。
//
// 用独立的引用而不是复用 alerts 那条：卡片推送里会出现 3DS 验证码，
// 而告警群通常人更多。分开两条引用，运营可以把卡片推送指到一个只有自己的
// 群；想用同一个群时把同一个地址填两遍即可。
func cardNotifier(provider secrets.SecretProvider) cards.Notifier {
	if provider == nil {
		return nil
	}
	ref, err := secrets.ParseCredentialRef(cardNotifyWebhookRef)
	if err != nil {
		return nil
	}
	return cards.NewWeComNotifier(func(ctx context.Context) (string, error) {
		value, err := provider.Resolve(ctx, ref, "card event notification")
		if err != nil {
			return "", err
		}
		endpoint := strings.TrimSpace(value.Reveal())
		if endpoint == "" {
			return "", fmt.Errorf("推送地址为空")
		}
		return endpoint, nil
	}, nil)
}

// cardWebhookOrNil 把「未装配」如实变成接口的 nil。
//
// 直接把 *cardWebhookProcessor(nil) 赋给接口字段会得到一个**非 nil 的接口值**，
// 于是路由照挂、每次请求都在 nil 指针上崩。这是 Go 里最常见的一个坑，
// 仓库里其余可选依赖（platformUsersOrNil 等）都用同一条模式挡它。
func cardWebhookOrNil(p *cardWebhookProcessor) httpapi.CardWebhookProcessor {
	if p == nil {
		return nil
	}
	return p
}
