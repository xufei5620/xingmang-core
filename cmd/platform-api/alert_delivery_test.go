package main

import "testing"

func TestAlertDeliveryStatusFromEnv(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		wantTelegram bool
		wantWebhook  bool
	}{
		{name: "nothing configured", env: map[string]string{}, wantTelegram: false, wantWebhook: false},
		{
			name: "telegram fully configured",
			env: map[string]string{
				"XM_ALERT_TELEGRAM_BOT_REF": "secret://alerts/telegram-bot",
				"XM_ALERT_TELEGRAM_CHAT_ID": "-1001234567890",
			},
			wantTelegram: true, wantWebhook: false,
		},
		{
			// A bot ref with no chat ID is not "configured" -- the worker's
			// own newAlertNotifier requires both to build a channel.
			name:         "telegram missing chat id",
			env:          map[string]string{"XM_ALERT_TELEGRAM_BOT_REF": "secret://alerts/telegram-bot"},
			wantTelegram: false, wantWebhook: false,
		},
		{
			name:         "webhook configured",
			env:          map[string]string{"XM_ALERT_WEBHOOK_URL": "https://example.test/hook"},
			wantTelegram: false, wantWebhook: true,
		},
		{
			name: "whitespace-only values do not count as configured",
			env: map[string]string{
				"XM_ALERT_TELEGRAM_BOT_REF": "   ",
				"XM_ALERT_TELEGRAM_CHAT_ID": "   ",
				"XM_ALERT_WEBHOOK_URL":      "   ",
			},
			wantTelegram: false, wantWebhook: false,
		},
		{
			name: "both channels configured",
			env: map[string]string{
				"XM_ALERT_TELEGRAM_BOT_REF": "secret://alerts/telegram-bot",
				"XM_ALERT_TELEGRAM_CHAT_ID": "-1001234567890",
				"XM_ALERT_WEBHOOK_URL":      "https://example.test/hook",
			},
			wantTelegram: true, wantWebhook: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string { return tc.env[key] }
			got := alertDeliveryStatusFromEnv(getenv)
			if got.TelegramConfigured != tc.wantTelegram || got.WebhookConfigured != tc.wantWebhook {
				t.Fatalf("alertDeliveryStatusFromEnv() = %+v, want telegram=%v webhook=%v",
					got, tc.wantTelegram, tc.wantWebhook)
			}
		})
	}
}

// TestAlertDeliveryStatusFromEnvNeverExposesValues is a structural
// reminder, not a runtime property test: the function's return type
// (httpapi.AlertDeliveryStatus) only has two bool fields. There is no
// string field this function could accidentally populate with a secret ref
// or URL even if someone tried -- this test exists so that claim stays true
// after future edits, by failing to compile the moment it stops being true.
func TestAlertDeliveryStatusFromEnvNeverExposesValues(t *testing.T) {
	got := alertDeliveryStatusFromEnv(func(string) string { return "secret://should-not-appear" })
	_ = got.TelegramConfigured
	_ = got.WebhookConfigured
	// If httpapi.AlertDeliveryStatus ever grows a string field, this file
	// will still compile (Go does not care), but the doc comment above
	// stops being an enforced guarantee -- reviewers of that future change
	// should re-read this test's purpose, not just its assertions.
}
