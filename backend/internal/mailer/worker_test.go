package mailer

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryRepo struct {
	messages     []Message
	sent, failed map[string]string
}

func (r *memoryRepo) Claim(context.Context, int, time.Time) ([]Message, error) {
	out := append([]Message(nil), r.messages...)
	r.messages = nil
	return out, nil
}
func (r *memoryRepo) MarkSent(_ context.Context, id, provider string, _ time.Time) error {
	r.sent[id] = provider
	return nil
}
func (r *memoryRepo) MarkFailed(_ context.Context, id, code string, _ time.Time) error {
	r.failed[id] = code
	return nil
}

type fakeSender struct {
	calls int
	err   error
}

func (s *fakeSender) SendInvoiceReady(context.Context, Message) (string, error) {
	s.calls++
	return "provider-1", s.err
}

func TestWorkerIsIdempotencyRepositoryDrivenAndValidatesRecipient(t *testing.T) {
	repo := &memoryRepo{messages: []Message{{ID: "mail-1", Recipient: "user@example.com", RequestNo: "INV-1", DownloadURL: "https://invoice.example/records?request_id=request-1"}, {ID: "mail-2", Recipient: "bad\r\nBcc:x@example.com", RequestNo: "INV-2", DownloadURL: "https://invoice.example/records?request_id=request-2"}}, sent: map[string]string{}, failed: map[string]string{}}
	sender := &fakeSender{}
	worker := Worker{Repository: repo, Sender: sender}
	count, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || sender.calls != 1 {
		t.Fatalf("count=%d calls=%d", count, sender.calls)
	}
	if repo.sent["mail-1"] != "provider-1" || repo.failed["mail-2"] != "VALIDATION_FAILED" {
		t.Fatalf("sent=%v failed=%v", repo.sent, repo.failed)
	}
	count, err = worker.RunOnce(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("second run count=%d err=%v", count, err)
	}
}

func TestWorkerRecordsDeliveryFailureWithoutMarkingSent(t *testing.T) {
	repo := &memoryRepo{messages: []Message{{ID: "mail-1", Recipient: "user@example.com", RequestNo: "INV-1", DownloadURL: "https://invoice.example/records?request_id=request-1"}}, sent: map[string]string{}, failed: map[string]string{}}
	sender := &fakeSender{err: errors.New("smtp unavailable")}
	worker := Worker{Repository: repo, Sender: sender}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.failed["mail-1"] != "DELIVERY_FAILED" || len(repo.sent) != 0 {
		t.Fatalf("sent=%v failed=%v", repo.sent, repo.failed)
	}
}

func TestMessageRejectsBearerOrUnexpectedLinkParameters(t *testing.T) {
	message := Message{ID: "mail-1", Recipient: "user@example.com", RequestNo: "INV-1",
		DownloadURL: "https://invoice.example/records?request_id=request-1&token=secret"}
	if err := message.Validate(); err == nil {
		t.Fatal("invoice email link accepted a bearer token parameter")
	}
}
