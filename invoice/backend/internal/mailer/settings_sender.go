package mailer

import (
	"context"
	"errors"
	"strings"
	"time"

	"invoice-system/backend/internal/adminsettings"
)

type SMTPSettingsSource interface {
	Get(context.Context) (adminsettings.Settings, error)
	SMTPSecretForDelivery(context.Context) (string, error)
}

// SettingsSender resolves the latest non-secret SMTP settings and write-only
// authorization code for every delivery. Rotation therefore does not require
// restarting the worker, and the credential never enters an API response.
type SettingsSender struct {
	Source  SMTPSettingsSource
	Timeout time.Duration
}

func (s SettingsSender) SendInvoiceReady(ctx context.Context, message Message) (string, error) {
	if s.Source == nil {
		return "", errors.New("SMTP settings source is not configured")
	}
	settings, err := s.Source.Get(ctx)
	if err != nil {
		return "", err
	}
	if !settings.SMTPSecretConfigured {
		return "", adminsettings.ErrSecretMissing
	}
	secret, err := s.Source.SMTPSecretForDelivery(ctx)
	if err != nil {
		return "", err
	}
	config := SMTPConfig{
		Host: settings.SMTPHost, Port: settings.SMTPPort,
		Username: settings.SMTPFrom, Password: secret,
		FromAddress: settings.SMTPFrom, FromName: settings.SMTPFromName,
		RequireSTARTTLS: settings.SMTPStartTLS, Timeout: s.Timeout,
	}
	if strings.TrimSpace(config.Password) == "" {
		return "", adminsettings.ErrSecretMissing
	}
	sender, err := NewSMTPSender(config)
	if err != nil {
		return "", smtpFailureAt("config", err)
	}
	return sender.SendInvoiceReady(ctx, message)
}

var _ Sender = SettingsSender{}
