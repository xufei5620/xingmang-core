package mailer

import "testing"

func TestNewSMTPSenderRejectsUnsafeConfiguration(t *testing.T) {
	for _, config := range []SMTPConfig{{Host: "", Port: 587, FromAddress: "invoice@example.com"}, {Host: "smtp.example.com\r\nX: y", Port: 587, FromAddress: "invoice@example.com"}, {Host: "smtp.example.com", Port: 587, FromAddress: "bad\r\nBcc:x@example.com"}} {
		if _, err := NewSMTPSender(config); err == nil {
			t.Fatalf("accepted unsafe config: %+v", config)
		}
	}
}

func TestQQSMTPConfigurationUsesAuthorizationCodeField(t *testing.T) {
	sender, err := NewSMTPSender(SMTPConfig{Host: "smtp.qq.com", Port: 587, Username: "invoice@qq.com", Password: "authorization-code", FromAddress: "invoice@qq.com", FromName: "发票中心", RequireSTARTTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if sender.config.Password != "authorization-code" || !sender.config.RequireSTARTTLS {
		t.Fatal("SMTP configuration changed")
	}
}
