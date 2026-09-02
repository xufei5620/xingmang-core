package assurance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

func newTestRealClient(t *testing.T, srv *httptest.Server) *RealClient {
	t.Helper()
	host := strings.TrimPrefix(srv.URL, "https://")
	host = strings.TrimPrefix(host, "http://")
	client, err := NewRealClient(RealClientConfig{
		TargetHost: host, Token: "test-token", HTTPClient: srv.Client(), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRealClient() = %v, want nil", err)
	}
	return client
}

func TestRealClientSuccessParsesChoicesAndUsage(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want Bearer test-token", got)
		}
		if r.URL.Path != chatCompletionsPath {
			t.Errorf("path = %q, want %q", r.URL.Path, chatCompletionsPath)
		}
		var body chatCompletionRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Model != "claude-sonnet-4" || body.Stream {
			t.Errorf("request body = %+v, unexpected", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "我是 Claude"}}},
			"usage":   map[string]any{"completion_tokens": 12},
		})
	}))
	defer srv.Close()

	client := newTestRealClient(t, srv)
	resp, err := client.Complete(context.Background(), ProbeRequest{
		Model: "claude-sonnet-4", Messages: []ProbeMessage{{Role: "user", Content: "ping"}}, MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("Complete() = %v, want nil", err)
	}
	if resp.Text != "我是 Claude" || resp.TokensUsed != 12 || resp.HTTPStatus != 200 {
		t.Fatalf("resp = %+v, unexpected", resp)
	}
	if resp.Measured {
		t.Fatal("non-streaming RealClient must never report a measured first-token latency")
	}
}

func TestRealClientMapsAuthErrors(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := newTestRealClient(t, srv)
	_, err := client.Complete(context.Background(), ProbeRequest{Model: "m", MaxTokens: 1})
	if connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("KindOf(err) = %q, want auth", connector.KindOf(err))
	}
}

func TestRealClientMapsRateLimit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	client := newTestRealClient(t, srv)
	_, err := client.Complete(context.Background(), ProbeRequest{Model: "m", MaxTokens: 1})
	if connector.KindOf(err) != connector.KindRateLimited {
		t.Fatalf("KindOf(err) = %q, want rate_limited", connector.KindOf(err))
	}
}

func TestRealClientMapsUpstream5xxAsUnavailable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	client := newTestRealClient(t, srv)
	_, err := client.Complete(context.Background(), ProbeRequest{Model: "m", MaxTokens: 1})
	if connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("KindOf(err) = %q, want unavailable", connector.KindOf(err))
	}
}

func TestRealClientMapsMalformedJSONAsBadResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	client := newTestRealClient(t, srv)
	_, err := client.Complete(context.Background(), ProbeRequest{Model: "m", MaxTokens: 1})
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("KindOf(err) = %q, want bad_response", connector.KindOf(err))
	}
}

func TestRealClientMapsEmptyChoicesAsBadResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer srv.Close()
	client := newTestRealClient(t, srv)
	_, err := client.Complete(context.Background(), ProbeRequest{Model: "m", MaxTokens: 1})
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("KindOf(err) = %q, want bad_response", connector.KindOf(err))
	}
}

func TestRealClientTimeoutSurfacesContextError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer srv.Close()
	client := newTestRealClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Complete(ctx, ProbeRequest{Model: "m", MaxTokens: 1})
	if err == nil {
		t.Fatal("Complete() over a timed-out context = nil error, want error")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want context.DeadlineExceeded (so the caller can classify status=timeout)", err)
	}
}

func TestNewRealClientRejectsEmptyHostOrToken(t *testing.T) {
	if _, err := NewRealClient(RealClientConfig{TargetHost: "", Token: "t"}); err == nil {
		t.Fatal("NewRealClient() with empty host = nil error, want error")
	}
	if _, err := NewRealClient(RealClientConfig{TargetHost: "h", Token: ""}); err == nil {
		t.Fatal("NewRealClient() with empty token = nil error, want error")
	}
}
