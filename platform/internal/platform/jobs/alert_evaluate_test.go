package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func alertTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func alertTestSecrets(t *testing.T) secrets.SecretProvider {
	t.Helper()
	p, err := secrets.NewEnvProvider(
		map[string]string{"secret://alerts/telegram-bot": "XM_TEST_TOKEN"},
		secrets.WithLookup(func(string) (string, bool) { return "token-value", true }),
	)
	if err != nil {
		t.Fatalf("构造 Provider: %v", err)
	}
	return p
}

// alertWeComTestSecrets 是企微渠道专用的 SecretProvider——与 alertTestSecrets
// 是两个独立实例，照 AlertNotifierConfig 里 Secrets / WeComSecrets 分开装配
// 的设计（cmd/platform-worker 里两者分别走 env-only 与文件优先链）。
func alertWeComTestSecrets(t *testing.T) secrets.SecretProvider {
	t.Helper()
	p, err := secrets.NewEnvProvider(
		map[string]string{"secret://alerts/wecom-webhook": "XM_TEST_WECOM_WEBHOOK"},
		secrets.WithLookup(func(string) (string, bool) {
			return "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-only", true
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider: %v", err)
	}
	return p
}

// TestNewAlertNotifierFailsClosedOnMisconfiguration 是本文件的重点。
//
// 与 XM-0022 的 sub2api 配置刻意相反：那条链路配错时每轮都会往看板写一条
// 说得清原因的 SyncFailed 观测，故障是**可见**的。告警链路没有这个性质——
// 一个因为 URL 拼错而没建起来的渠道，唯一的痕迹是启动时一行日志，
// 之后每条告警都会静静地不投递，而运维会以为自己配好了。
func TestNewAlertNotifierFailsClosedOnMisconfiguration(t *testing.T) {
	cases := map[string]AlertNotifierConfig{
		"Bot Token 引用拼错": {
			TelegramBotRef: "not-a-credential-ref",
			TelegramChatID: "-100",
			Secrets:        alertTestSecrets(t),
		},
		"只配了 ref 没配 chat_id": {
			TelegramBotRef: "secret://alerts/telegram-bot",
			Secrets:        alertTestSecrets(t),
		},
		"只配了 chat_id 没配 ref": {
			TelegramChatID: "-100",
			Secrets:        alertTestSecrets(t),
		},
		"配了 ref 却没有 SecretProvider": {
			TelegramBotRef: "secret://alerts/telegram-bot",
			TelegramChatID: "-100",
		},
		"Webhook 不是 https": {
			WebhookURL: "http://example.com/hook",
		},
		"Webhook 地址不合法": {
			WebhookURL: "://broken",
		},
		"企微 Webhook 引用拼错": {
			WeComWebhookRef: "not-a-credential-ref",
			WeComSecrets:    alertWeComTestSecrets(t),
		},
		"配了企微 ref 却没有 SecretProvider": {
			WeComWebhookRef: "secret://alerts/wecom-webhook",
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			cfg.Logger = alertTestLogger()
			if _, err := newAlertNotifier(cfg); err == nil {
				t.Fatal("配错必须让进程起不来，而不是静默地少一个渠道")
			}
		})
	}
}

// TestNewAlertNotifierWithNothingConfiguredIsAllowed：一个渠道都没配是
// 允许的（本地开发、刚起的环境），但结果必须是一个**空**的 MultiNotifier，
// 由 Reconciler 每轮打 warn「仅落库未投递」。
func TestNewAlertNotifierWithNothingConfiguredIsAllowed(t *testing.T) {
	n, err := newAlertNotifier(AlertNotifierConfig{Logger: alertTestLogger()})
	if err != nil {
		t.Fatalf("没配渠道不该是错误: %v", err)
	}
	if n.Len() != 0 {
		t.Fatalf("渠道数 = %d, want 0", n.Len())
	}
	if err := n.Notify(context.Background(), alerts.Alert{}); !errors.Is(err, alerts.ErrNoNotifier) {
		t.Fatalf("空渠道应返回 ErrNoNotifier，实际 %v", err)
	}
}

// TestNewAlertNotifierBuildsConfiguredChannels：配齐了就真的建出来。
func TestNewAlertNotifierBuildsConfiguredChannels(t *testing.T) {
	n, err := newAlertNotifier(AlertNotifierConfig{
		TelegramBotRef:  "secret://alerts/telegram-bot",
		TelegramChatID:  "-1001234567890",
		WebhookURL:      "https://ops.example.com/hooks/alerts",
		Secrets:         alertTestSecrets(t),
		WeComWebhookRef: "secret://alerts/wecom-webhook",
		WeComSecrets:    alertWeComTestSecrets(t),
		Logger:          alertTestLogger(),
	})
	if err != nil {
		t.Fatalf("newAlertNotifier: %v", err)
	}
	if n.Len() != 3 {
		t.Fatalf("渠道数 = %d, want 3", n.Len())
	}
	names := strings.Join(n.Names(), ",")
	for _, want := range []string{"telegram", "webhook", "wecom"} {
		if !strings.Contains(names, want) {
			t.Fatalf("渠道名 = %q，缺 %q", names, want)
		}
	}
}

// TestNewAlertNotifierBuildsWeComChannelAlone：企微渠道可以独立于
// Telegram/Webhook 单独配置——三个渠道互不依赖。
func TestNewAlertNotifierBuildsWeComChannelAlone(t *testing.T) {
	n, err := newAlertNotifier(AlertNotifierConfig{
		WeComWebhookRef: "secret://alerts/wecom-webhook",
		WeComSecrets:    alertWeComTestSecrets(t),
		Logger:          alertTestLogger(),
	})
	if err != nil {
		t.Fatalf("newAlertNotifier: %v", err)
	}
	if n.Len() != 1 {
		t.Fatalf("渠道数 = %d, want 1", n.Len())
	}
	if names := strings.Join(n.Names(), ","); names != "wecom" {
		t.Fatalf("渠道名 = %q, want wecom", names)
	}
}

// TestAlertEvaluateArgsUniqueness：多副本部署时同一个周期只该评估一次，
// 否则同一条告警会被投递 N 次（照 XM-0022 的唯一性模式）。
func TestAlertEvaluateArgsUniqueness(t *testing.T) {
	opts := AlertEvaluateArgs{}.InsertOpts()
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Fatalf("唯一性应按参数 + 队列: %+v", opts.UniqueOpts)
	}
	if opts.UniqueOpts.ByPeriod != DefaultAlertEvaluateInterval {
		t.Fatalf("唯一性周期 = %s, want %s", opts.UniqueOpts.ByPeriod, DefaultAlertEvaluateInterval)
	}
	if opts.Queue != QueueMaintenance {
		t.Fatalf("队列 = %s, want %s", opts.Queue, QueueMaintenance)
	}
	if opts.MaxAttempts != alertEvaluateMaxAttempts {
		t.Fatalf("最大尝试 = %d", opts.MaxAttempts)
	}
	// 括号是必需的：if 条件里的复合字面量会与语句块的左花括号产生歧义。
	if kind := (AlertEvaluateArgs{}).Kind(); kind != AlertEvaluateJobKind {
		t.Fatalf("kind = %q", kind)
	}
}

// TestAlertEvaluateWorkerRequiresReconciler：没装配 Reconciler 时明确报错，
// 而不是静默地什么都不做——一个「跑着但什么都不评估」的告警任务，
// 在日志里看起来和正常运行一模一样。
func TestAlertEvaluateWorkerRequiresReconciler(t *testing.T) {
	w := NewAlertEvaluateWorker(AlertEvaluateOptions{
		Logger: alertTestLogger(), Environment: "development",
	})
	if err := w.Work(context.Background(), nil); err == nil {
		t.Fatal("没有 Reconciler 时应报错")
	}
}

// TestAlertConfigDefaults：默认就跑告警（规格 §22.2 的退出条件之一是
// 「能触发一条真实告警」，一个默认关闭的告警系统在需要它那天多半还是关的）。
func TestAlertConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.AlertEvaluateEnabled {
		t.Fatal("默认应启用告警评估")
	}
	if cfg.AlertEvaluateInterval != DefaultAlertEvaluateInterval {
		t.Fatalf("默认评估周期 = %s", cfg.AlertEvaluateInterval)
	}
	if !cfg.AlertEvaluateRunOnStart {
		t.Fatal("默认应在启动时先评估一次")
	}
	if cfg.AlertBalanceThresholdMinorUnits != alerts.DefaultBalanceThresholdMinorUnits {
		t.Fatalf("默认余额阈值 = %d", cfg.AlertBalanceThresholdMinorUnits)
	}
	// 三个投递渠道变量默认都空：不假装配好了。
	if cfg.AlertTelegramBotRef != "" || cfg.AlertTelegramChatID != "" || cfg.AlertWebhookURL != "" {
		t.Fatal("投递渠道不该有默认值")
	}
}

// TestAlertConfigNormalizationAndValidation：零值阈值与非法周期的兜底。
func TestAlertConfigNormalizationAndValidation(t *testing.T) {
	// 零阈值等于「永不触发」，但看起来像配了一个阈值。
	cfg := DefaultConfig()
	cfg.Environment = "development"
	cfg.AlertBalanceThresholdMinorUnits = 0
	got := cfg.normalized()
	if got.AlertBalanceThresholdMinorUnits != alerts.DefaultBalanceThresholdMinorUnits {
		t.Fatalf("零阈值应回落到默认值，实际 %d", got.AlertBalanceThresholdMinorUnits)
	}

	// 低于 River 一秒下限的周期必须被拒绝。
	tooFast := DefaultConfig()
	tooFast.Environment = "development"
	tooFast.AlertEvaluateInterval = 100 * time.Millisecond
	if err := tooFast.normalized().validate(); err == nil {
		t.Fatal("低于一秒的评估周期应被拒绝")
	}

	// RunID 只服务于集成测试隔离，生产配上它等于给每个副本发一张免签。
	prod := DefaultConfig()
	prod.Environment = "production"
	prod.Sub2APIMode = Sub2APIModeReal
	prod.AlertEvaluateRunID = "isolated"
	if err := prod.normalized().validate(); err == nil {
		t.Fatal("生产环境不该允许 AlertEvaluateRunID")
	}
}
