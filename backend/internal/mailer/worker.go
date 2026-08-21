package mailer

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

type Message struct {
	ID          string
	Kind        string
	Recipient   string
	RequestNo   string
	DownloadURL string
	CreatedAt   time.Time
}

const (
	MessageInvoiceReady = "invoice_ready"
	MessageSMTPTest     = "smtp_test"
)

func (m Message) Validate() error {
	if m.Kind == "" {
		m.Kind = MessageInvoiceReady
	}
	if m.Kind != MessageInvoiceReady && m.Kind != MessageSMTPTest {
		return errors.New("unsupported email message kind")
	}
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.RequestNo) == "" {
		return errors.New("message identity is required")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(m.Recipient))
	if err != nil || address.Address != strings.TrimSpace(m.Recipient) {
		return errors.New("verified recipient is invalid")
	}
	if strings.ContainsAny(m.RequestNo, "\r\n") {
		return errors.New("request number contains invalid characters")
	}
	parsed, err := url.Parse(m.DownloadURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Path != "/records" {
		return errors.New("invoice link must be the absolute HTTPS records UI route")
	}
	query := parsed.Query()
	requestIDs := query["request_id"]
	if len(query) != 1 || len(requestIDs) != 1 || strings.TrimSpace(requestIDs[0]) == "" ||
		len(requestIDs[0]) > 128 || strings.ContainsAny(requestIDs[0], "\r\n\x00") {
		return errors.New("invoice link must contain only one bounded request_id")
	}
	return nil
}

type Sender interface {
	SendInvoiceReady(context.Context, Message) (providerMessageID string, err error)
}

type Repository interface {
	Claim(context.Context, int, time.Time) ([]Message, error)
	MarkSent(context.Context, string, string, time.Time) error
	MarkFailed(context.Context, string, string, time.Time) error
}

type Worker struct {
	Repository Repository
	Sender     Sender
	BatchSize  int
	Now        func() time.Time
}

func (w Worker) RunOnce(ctx context.Context) (int, error) {
	if w.Repository == nil || w.Sender == nil {
		return 0, errors.New("mailer worker is not configured")
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	batch := w.BatchSize
	if batch <= 0 || batch > 100 {
		batch = 20
	}
	messages, err := w.Repository.Claim(ctx, batch, now())
	if err != nil {
		return 0, fmt.Errorf("claim email outbox: %w", err)
	}
	processed := 0
	for _, message := range messages {
		if err = message.Validate(); err != nil {
			_ = w.Repository.MarkFailed(ctx, message.ID, "VALIDATION_FAILED", now())
			processed++
			continue
		}
		providerID, sendErr := w.Sender.SendInvoiceReady(ctx, message)
		if sendErr != nil {
			_ = w.Repository.MarkFailed(ctx, message.ID, "DELIVERY_FAILED", now().Add(5*time.Minute))
			processed++
			continue
		}
		if err = w.Repository.MarkSent(ctx, message.ID, providerID, now()); err != nil {
			return processed, fmt.Errorf("mark email sent: %w", err)
		}
		processed++
	}
	return processed, nil
}
