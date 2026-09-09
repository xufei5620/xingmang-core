package mailer

import (
	"context"
	"errors"
	"testing"

	"invoice-system/backend/internal/adminsettings"
)

type fakeSMTPSettingsSource struct {
	settings adminsettings.Settings
	secret   string
	err      error
}

func (f fakeSMTPSettingsSource) Get(context.Context) (adminsettings.Settings, error) {
	return f.settings, f.err
}

func (f fakeSMTPSettingsSource) SMTPSecretForDelivery(context.Context) (string, error) {
	return f.secret, f.err
}

func TestSettingsSenderFailsClosedWithoutCredential(t *testing.T) {
	sender := SettingsSender{Source: fakeSMTPSettingsSource{settings: adminsettings.Settings{
		SMTPHost: "smtp.qq.com", SMTPPort: 587, SMTPFrom: "invoice@qq.com",
		SMTPFromName: "发票中心", SMTPStartTLS: true,
	}}}
	_, err := sender.SendInvoiceReady(context.Background(), Message{})
	if !errors.Is(err, adminsettings.ErrSecretMissing) {
		t.Fatalf("missing credential error=%v", err)
	}
}

func TestSettingsSenderRequiresSource(t *testing.T) {
	_, err := (SettingsSender{}).SendInvoiceReady(context.Background(), Message{})
	if err == nil {
		t.Fatal("sender without settings source was accepted")
	}
}
