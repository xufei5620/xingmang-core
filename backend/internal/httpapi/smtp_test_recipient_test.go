package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
)

// smtpRecipientServer 造一个只关心「测试邮件发给谁」的 Server。envRecipient 是
// 过渡期的环境变量兜底，settingsRecipient 是管理端存进库的那个。
func smtpRecipientServer(t *testing.T, envRecipient, settingsRecipient string, sender *capturingSMTPTestSender) *Server {
	t.Helper()
	settings := smtpTestSettings("sender@example.com")
	if settingsRecipient != "" {
		current, err := settings.Get(context.Background())
		if err != nil {
			t.Fatalf("读取设置失败：%v", err)
		}
		in := adminsettings.UpdateInput{
			IssuerName: current.IssuerName, MinimumRequestMinor: current.MinimumRequestMinor,
			EligibilityStartAt: current.EligibilityStartAt,
			SMTPHost:           current.SMTPHost, SMTPPort: current.SMTPPort,
			SMTPFrom: current.SMTPFrom, SMTPFromName: current.SMTPFromName,
			SMTPStartTLS: current.SMTPStartTLS, AdminCIDRs: current.AdminCIDRs,
			SMTPTestRecipient: settingsRecipient,
		}
		actor := adminsettings.Actor{ID: "test-admin", RequestID: "req-test-recipient"}
		if _, err := settings.UpdateSMTP(context.Background(), in, adminsettings.SMTPSecretUnchanged, "", current.Revision, actor); err != nil {
			t.Fatalf("写入测试收件人失败：%v", err)
		}
	}
	return &Server{
		logger:            slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		smtpTestSender:    sender,
		smtpTestRecipient: envRecipient,
		adminSettings:     settings,
		productionAuth: &ProductionAuth{LoadUser: func(context.Context, string) (SessionUser, error) {
			return SessionUser{}, nil
		}},
		publicOrigin: "https://invoice.example",
		lastSMTPTest: make(map[string]time.Time),
		operations:   smtpAuditOperations{},
	}
}

func postSMTPTest(t *testing.T, server *Server) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/smtp/test", strings.NewReader(`{}`))
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: "admin-user"}))
	recorder := httptest.NewRecorder()
	server.testEmail(recorder, request)
	return recorder
}

// TestSettingsTestRecipientWinsOverEnvironment 是这一片的核心断言：一旦管理员
// 在后台存了收件人，环境变量就不再参与。**两个地址都非空且不同**，所以它不会
// 恒真——发错了任何一个都会红。
func TestSettingsTestRecipientWinsOverEnvironment(t *testing.T) {
	capture := &capturingSMTPTestSender{}
	server := smtpRecipientServer(t, "legacy-env@example.com", "chosen-in-admin@example.com", capture)
	if recorder := postSMTPTest(t, server); recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if capture.calls != 1 || capture.message.Recipient != "chosen-in-admin@example.com" {
		t.Fatalf("发给了错误的收件人：calls=%d recipient=%q", capture.calls, capture.message.Recipient)
	}
}

// TestEnvironmentRecipientStillUsedBeforeFirstSave 钉住过渡期：迁移 0030 只能把
// 新列建成空串（迁移读不到环境变量），所以在管理员第一次保存之前，生产必须
// 继续用环境变量那个地址，不能变成「按钮坏了」。
func TestEnvironmentRecipientStillUsedBeforeFirstSave(t *testing.T) {
	capture := &capturingSMTPTestSender{}
	server := smtpRecipientServer(t, "legacy-env@example.com", "", capture)
	if recorder := postSMTPTest(t, server); recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if capture.calls != 1 || capture.message.Recipient != "legacy-env@example.com" {
		t.Fatalf("过渡期兜底失效：calls=%d recipient=%q", capture.calls, capture.message.Recipient)
	}
}

// TestNoRecipientAnywhereFailsAtTheEndpointNotAtBoot 钉住那条从「启动即失败」
// 挪到端点的判断：两个来源都为空时，是这一个按钮明确报错，而不是整个进程起不来。
func TestNoRecipientAnywhereFailsAtTheEndpointNotAtBoot(t *testing.T) {
	capture := &capturingSMTPTestSender{}
	server := smtpRecipientServer(t, "", "", capture)
	recorder := postSMTPTest(t, server)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "TEST_EMAIL_NOT_CONNECTED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if capture.calls != 0 {
		t.Fatalf("没有收件人却发了 %d 封信", capture.calls)
	}
}

// TestSettingsResponseNeverReturnsTheRawTestRecipient 钉住本仓库既有的不变量：
// 设置响应只回遮蔽值。收件人搬进后台之后仍然如此——页面靠「输入即覆盖」编辑，
// 与 SMTP 授权码、企业微信地址是同一条纪律。
func TestSettingsResponseNeverReturnsTheRawTestRecipient(t *testing.T) {
	const raw = "chosen-in-admin@example.com"
	server := smtpRecipientServer(t, "legacy-env@example.com", raw, &capturingSMTPTestSender{})
	settings, err := server.adminSettings.Get(context.Background())
	if err != nil {
		t.Fatalf("读取设置失败：%v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	request.RemoteAddr = "203.0.113.8:1234"
	body := server.settingsResponse(settings, request)
	smtp, ok := body["smtp"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 smtp 块：%+v", body)
	}
	if smtp["test_recipient_masked"] != "cho***@example.com" {
		t.Fatalf("遮蔽值不对：%v", smtp["test_recipient_masked"])
	}
	if smtp["test_recipient_managed"] != true {
		t.Fatalf("存过之后应当标记为由后台管理：%v", smtp["test_recipient_managed"])
	}
	for key, value := range smtp {
		if text, isText := value.(string); isText && strings.Contains(text, raw) {
			t.Fatalf("响应里出现了收件人明文：smtp.%s=%q", key, text)
		}
	}
}
