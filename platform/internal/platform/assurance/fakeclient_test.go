package assurance

import (
	"context"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

func TestFakeClientHealthyResponsesPassAssess(t *testing.T) {
	client := NewFakeClient()
	for _, key := range PromptTemplateKeys {
		msgs, err := BuildMessages(key)
		if err != nil {
			t.Fatalf("BuildMessages(%q) = %v", key, err)
		}
		resp, err := client.Complete(context.Background(), ProbeRequest{
			TemplateKey: key, Model: "claude-sonnet-4", Messages: msgs, MaxTokens: 64,
		})
		if err != nil {
			t.Fatalf("Complete(%q) = %v, want nil", key, err)
		}
		if resp.HTTPStatus != 200 {
			t.Fatalf("Complete(%q).HTTPStatus = %d, want 200", key, resp.HTTPStatus)
		}
		out, err := Assess(AssessInput{TemplateKey: key, Model: "claude-sonnet-4", Text: resp.Text, Expected: defaultShape()})
		if err != nil {
			t.Fatalf("Assess(%q) = %v, want nil", key, err)
		}
		if out.Status != ResultStatusOK {
			t.Errorf("Assess(%q) on fake healthy response = %q, want ok (fake mode is meant to validate the full pipeline)", key, out.Status)
		}
	}
}

func TestFakeClientFailingModelSuffix(t *testing.T) {
	client := NewFakeClient()
	_, err := client.Complete(context.Background(), ProbeRequest{
		TemplateKey: TemplateMinViableRequest, Model: "gpt-4o-mini" + FakeFailingModelSuffix,
		Messages: []ProbeMessage{{Role: "user", Content: "ping"}}, MaxTokens: 8,
	})
	if err == nil {
		t.Fatal("Complete() with the fake-fail model suffix = nil error, want a deterministic failure")
	}
	if connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("KindOf(err) = %q, want unavailable", connector.KindOf(err))
	}
}

func TestFakeClientRespectsMaxTokensCap(t *testing.T) {
	client := NewFakeClient()
	resp, err := client.Complete(context.Background(), ProbeRequest{
		TemplateKey: TemplateMinViableRequest, Model: "gpt-4o", MaxTokens: 2,
	})
	if err != nil {
		t.Fatalf("Complete() = %v, want nil", err)
	}
	if resp.TokensUsed > 2 {
		t.Fatalf("TokensUsed = %d, want <= declared max_tokens (2)", resp.TokensUsed)
	}
}

func TestFakeClientHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewFakeClient()
	if _, err := client.Complete(ctx, ProbeRequest{TemplateKey: TemplateMinViableRequest}); err == nil {
		t.Fatal("Complete() on an already-cancelled context = nil error, want error")
	}
}
