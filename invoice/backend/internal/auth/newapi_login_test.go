package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestNewAPIAuthenticator(t *testing.T, handler http.HandlerFunc) *NewAPIAuthenticator {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	authenticator, err := newNewAPIAuthenticator(PlatformEndpointConfig{BaseURL: server.URL, Timeout: 5 * time.Second}, server.Client())
	if err != nil {
		t.Fatalf("newNewAPIAuthenticator: %v", err)
	}
	return authenticator
}

func TestNewAPILoginSuccess(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "message": "",
			"data": map[string]any{
				"access_token": "should-be-discarded",
				"user":         map[string]any{"id": 99, "username": "exampleuser", "display_name": "Example User", "email": "user@example.com"},
			},
		})
	})
	result, err := authenticator.Login(context.Background(), "exampleuser", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotPath != "/api/user/login" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["username"] != "exampleuser" || gotBody["password"] != "correct horse battery staple" {
		t.Fatalf("forwarded body = %+v", gotBody)
	}
	if result.RequiresTwoFA || result.PlatformUserID != "99" || result.Email != "user@example.com" || result.Username != "Example User" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestNewAPILoginPrefersUsernameWhenDisplayNameEmpty(t *testing.T) {
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"user": map[string]any{"id": 5, "username": "plainuser", "display_name": "", "email": ""}},
		})
	})
	result, err := authenticator.Login(context.Background(), "plainuser", "password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Username != "plainuser" || result.Email != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestNewAPILoginWrongPasswordAndUnknownUsernameAreIndistinguishable(t *testing.T) {
	// New API always answers HTTP 200; failure is signaled only by the
	// "success" JSON field, never a distinct status code.
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"用户名或密码错误"}`))
	})
	_, err := authenticator.Login(context.Background(), "nobody", "wrong")
	if !errors.Is(err, ErrPlatformCredentialsInvalid) {
		t.Fatalf("err = %v, want ErrPlatformCredentialsInvalid", err)
	}
}

func TestNewAPILoginTwoFAFlow(t *testing.T) {
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "message": "需要两步验证",
				"data": map[string]any{"require_2fa": true, "flow_token": "flow-token-0123456789", "expires_at": 1234567890},
			})
		case "/api/user/login/2fa":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["flow_token"] != "flow-token-0123456789" || body["code"] != "654321" {
				_, _ = w.Write([]byte(`{"success":false,"message":"验证码或备用码错误，请重试"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"user": map[string]any{"id": 11, "username": "twofauser", "display_name": "2FA User", "email": "twofa@example.com"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	first, err := authenticator.Login(context.Background(), "twofauser", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !first.RequiresTwoFA || first.TempToken != "flow-token-0123456789" {
		t.Fatalf("unexpected first-step result: %+v", first)
	}
	final, err := authenticator.VerifyTwoFA(context.Background(), first.TempToken, "654321")
	if err != nil {
		t.Fatalf("VerifyTwoFA: %v", err)
	}
	if final.RequiresTwoFA || final.PlatformUserID != "11" {
		t.Fatalf("unexpected final result: %+v", final)
	}
}

func TestNewAPIVerifyTwoFAWrongCode(t *testing.T) {
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"验证码或备用码错误，请重试"}`))
	})
	_, err := authenticator.VerifyTwoFA(context.Background(), "flow-token-0123456789", "000000")
	if !errors.Is(err, ErrPlatformTwoFAInvalid) {
		t.Fatalf("err = %v, want ErrPlatformTwoFAInvalid", err)
	}
}

func TestNewAPILoginUpstream5xxIsUnavailableNotInvalidCredentials(t *testing.T) {
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream gateway error"))
	})
	_, err := authenticator.Login(context.Background(), "user", "password")
	if !errors.Is(err, ErrPlatformUnavailable) {
		t.Fatalf("err = %v, want ErrPlatformUnavailable", err)
	}
	if errors.Is(err, ErrPlatformCredentialsInvalid) {
		t.Fatal("a non-200 status must never be classified as bad credentials")
	}
}

func TestNewAPILoginNonJSONResponseIsUnavailable(t *testing.T) {
	authenticator := newTestNewAPIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not json"))
	})
	_, err := authenticator.Login(context.Background(), "user", "password")
	if !errors.Is(err, ErrPlatformUnavailable) {
		t.Fatalf("err = %v, want ErrPlatformUnavailable", err)
	}
}
