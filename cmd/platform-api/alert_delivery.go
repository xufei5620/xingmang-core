package main

import (
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
)

// alertDeliveryStatusFromEnv reports whether alert notification channels
// are configured, for the ops overview page (XM-OPS0) -- presence only,
// never the value itself (constitution clause 7 / ADR-014).
//
// This reads the same XM_ALERT_TELEGRAM_BOT_REF / XM_ALERT_TELEGRAM_CHAT_ID
// / XM_ALERT_WEBHOOK_URL env var names cmd/platform-worker/config.go reads
// to build the worker's actual notifier (see AlertNotifierConfig in
// internal/platform/jobs/alert_evaluate.go) -- platform-api does not build
// a notifier or read any credential value, it only checks presence.
//
// Why env vars instead of a DB-backed source: platform-api reads no other
// XM_SUB2API_*/XM_NEWAPI_*/XM_ALERT_* config today (its config.go is
// deliberately scoped to non-worker concerns; connector effective-mode is
// the one exception, and that already goes through core.connector_config
// via the credentials store precisely because operators can flip it from
// the admin UI at runtime). Alert channel configuration has no such runtime
// admin UI yet -- it is deploy-time env config on both processes, normally
// sourced from the same .env in whatever compose/deploy setup runs them
// together, so reading the same three names here is not a second decision,
// just a second read of the same input. If that assumption stops holding
// (e.g. the two processes get independently configured env in some
// deployment), the honest fix is to have the worker persist these two
// booleans as an ops observation instead -- flagged as a follow-up in this
// slice's handoff rather than solved speculatively here.
func alertDeliveryStatusFromEnv(getenv func(string) string) httpapi.AlertDeliveryStatus {
	telegramBotRef := strings.TrimSpace(getenv("XM_ALERT_TELEGRAM_BOT_REF"))
	telegramChatID := strings.TrimSpace(getenv("XM_ALERT_TELEGRAM_CHAT_ID"))
	webhookURL := strings.TrimSpace(getenv("XM_ALERT_WEBHOOK_URL"))
	return httpapi.AlertDeliveryStatus{
		TelegramConfigured: telegramBotRef != "" && telegramChatID != "",
		WebhookConfigured:  webhookURL != "",
	}
}
