package mailer

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type SMTPConfig struct {
	Host            string
	Port            int
	Username        string
	Password        string
	FromAddress     string
	FromName        string
	RequireSTARTTLS bool
	Timeout         time.Duration
}

type SMTPSender struct{ config SMTPConfig }

func NewSMTPSender(config SMTPConfig) (*SMTPSender, error) {
	config.Host = strings.TrimSpace(config.Host)
	config.Username = strings.TrimSpace(config.Username)
	config.FromAddress = strings.TrimSpace(config.FromAddress)
	config.FromName = strings.TrimSpace(config.FromName)
	if config.Host == "" || strings.ContainsAny(config.Host, "\r\n") || config.Port <= 0 || config.Port > 65535 {
		return nil, errors.New("invalid SMTP endpoint")
	}
	from, err := mail.ParseAddress(config.FromAddress)
	if err != nil || from.Address != config.FromAddress {
		return nil, errors.New("invalid SMTP sender address")
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	return &SMTPSender{config: config}, nil
}

func (s *SMTPSender) SendInvoiceReady(ctx context.Context, message Message) (string, error) {
	if err := message.Validate(); err != nil {
		return "", err
	}
	recipient, err := mail.ParseAddress(message.Recipient)
	if err != nil {
		return "", err
	}
	dialer := net.Dialer{Timeout: s.config.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	if err != nil {
		return "", fmt.Errorf("dial SMTP: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(s.config.Timeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, s.config.Host)
	if err != nil {
		return "", fmt.Errorf("open SMTP client: %w", err)
	}
	defer client.Close()
	if s.config.RequireSTARTTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return "", errors.New("SMTP server does not offer STARTTLS")
		}
		if err = client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return "", fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if s.config.Username != "" {
		if s.config.Password == "" {
			return "", errors.New("SMTP authorization code is empty")
		}
		if err = client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return "", fmt.Errorf("SMTP authentication: %w", err)
		}
	}
	if err = client.Mail(s.config.FromAddress); err != nil {
		return "", err
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return "", err
	}
	data, err := client.Data()
	if err != nil {
		return "", err
	}
	buffer := bufio.NewWriter(data)
	subjectText := "您的电子普票已开具"
	body := fmt.Sprintf("您的电子普票已开具。\r\n\r\n申请单号：%s\r\n请登录开票中心下载 PDF：\r\n%s\r\n\r\n本邮件不包含发票附件，请勿转发下载链接。\r\n", message.RequestNo, message.DownloadURL)
	if message.Kind == MessageSMTPTest {
		subjectText = "SoloV 发票邮件配置测试"
		body = fmt.Sprintf("这是一封发票中心邮件配置测试。\r\n\r\n测试编号：%s\r\n开票中心：%s\r\n\r\n如果您并未执行测试，请立即检查管理员账号和邮件配置。\r\n", message.RequestNo, message.DownloadURL)
	}
	subject := mime.QEncoding.Encode("UTF-8", subjectText)
	fromName := mime.QEncoding.Encode("UTF-8", s.config.FromName)
	headers := []string{"From: " + fromName + " <" + s.config.FromAddress + ">", "To: <" + recipient.Address + ">", "Subject: " + subject, "MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8", "Content-Transfer-Encoding: 8bit", "Date: " + time.Now().Format(time.RFC1123Z)}
	for _, header := range headers {
		if _, err = buffer.WriteString(header + "\r\n"); err != nil {
			return "", err
		}
	}
	if _, err = buffer.WriteString("\r\n" + body); err == nil {
		err = buffer.Flush()
	}
	closeErr := data.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = client.Quit(); err != nil {
		return "", err
	}
	return "smtp:" + message.ID, nil
}
