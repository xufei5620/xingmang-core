package main

import (
	"context"
	"fmt"
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
	secret := strings.TrimSpace(value.String())
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
) *cardWebhookProcessor {
	if cfg.Mode == cardsModeOff || store == nil || len(accounts) == 0 {
		return nil
	}
	// 同步器在这里只用它的定向刷新：周期同步仍然只在 worker 里跑，
	// API 进程不注册任何周期任务。
	syncer := cards.NewSyncer(accounts, store, cards.SyncOptions{})
	return &cardWebhookProcessor{provider: provider, store: store, syncer: syncer}
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
