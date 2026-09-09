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

func newTestSub2APIAuthenticator(t *testing.T, handler http.HandlerFunc) *Sub2APIAuthenticator {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	authenticator, err := newSub2APIAuthenticator(PlatformEndpointConfig{BaseURL: server.URL, Timeout: 5 * time.Second}, server.Client())
	if err != nil {
		t.Fatalf("newSub2APIAuthenticator: %v", err)
	}
	return authenticator
}

func TestSub2APILoginSuccess(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	authenticator := newTestSub2APIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "message": "success",
			"data": map[string]any{
				"access_token": "jwt-should-be-discarded",
				"user":         map[string]any{"id": 42, "email": "user@example.com", "username": "exampleuser"},
			},
		})
	})
	result, err := authenticator.Login(context.Background(), "user@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotPath != "/api/v1/auth/login" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["email"] != "user@example.com" || gotBody["password"] != "correct horse battery staple" {
		t.Fatalf("forwarded body = %+v", gotBody)
	}
	if result.RequiresTwoFA || result.PlatformUserID != "42" || result.Email != "user@example.com" || result.Username != "exampleuser" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSub2APILoginWrongPasswordAndUnknownEmailAreIndistinguishable(t *testing.T) {
	authenticator := newTestSub2APIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 401, "message": "invalid email or password", "reason": "INVALID_CREDENTIALS",
		})
	})
	_, err := authenticator.Login(context.Background(), "nobody@example.com", "wrong")
	if !errors.Is(err, ErrPlatformCredentialsInvalid) {
		t.Fatalf("err = %v, want ErrPlatformCredentialsInvalid", err)
	}
}

func TestSub2APILoginTwoFAFlow(t *testing.T) {
	var loginCalls, twoFACalls int
	authenticator := newTestSub2APIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "success",
				"data": map[string]any{"requires_2fa": true, "temp_token": "temp-token-0123456789", "user_email_masked": "u***@example.com"},
			})
		case "/api/v1/auth/login/2fa":
			twoFACalls++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["temp_token"] != "temp-token-0123456789" || body["totp_code"] != "123456" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "message": "invalid or expired 2FA session"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "success",
				"data": map[string]any{"user": map[string]any{"id": 7, "email": "twofa@example.com", "username": "twofauser"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	first, err := authenticator.Login(context.Background(), "twofa@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !first.RequiresTwoFA || first.TempToken != "temp-token-0123456789" {
		t.Fatalf("unexpected first-step result: %+v", first)
	}
	final, err := authenticator.VerifyTwoFA(context.Background(), first.TempToken, "123456")
	if err != nil {
		t.Fatalf("VerifyTwoFA: %v", err)
	}
	if final.RequiresTwoFA || final.PlatformUserID != "7" {
		t.Fatalf("unexpected final result: %+v", final)
	}
	if loginCalls != 1 || twoFACalls != 1 {
		t.Fatalf("loginCalls=%d twoFACalls=%d", loginCalls, twoFACalls)
	}
}

func TestSub2APIVerifyTwoFAWrongCode(t *testing.T) {
	authenticator := newTestSub2APIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "message": "invalid or expired 2FA session"})
	})
	_, err := authenticator.VerifyTwoFA(context.Background(), "temp-token-0123456789", "000000")
	if !errors.Is(err, ErrPlatformTwoFAInvalid) {
		t.Fatalf("err = %v, want ErrPlatformTwoFAInvalid", err)
	}
}

func TestSub2APILoginUpstream5xxIsUnavailableNotInvalidCredentials(t *testing.T) {
	authenticator := newTestSub2APIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"message":"internal error"}`))
	})
	_, err := authenticator.Login(context.Background(), "user@example.com", "password")
	if !errors.Is(err, ErrPlatformUnavailable) {
		t.Fatalf("err = %v, want ErrPlatformUnavailable", err)
	}
	if errors.Is(err, ErrPlatformCredentialsInvalid) {
		t.Fatal("a 5xx must never be classified as bad credentials")
	}
}

func TestSub2APILoginNonJSONResponseIsUnavailable(t *testing.T) {
	authenticator := newTestSub2APIAuthenticator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not json</html>"))
	})
	_, err := authenticator.Login(context.Background(), "user@example.com", "password")
	if !errors.Is(err, ErrPlatformUnavailable) {
		t.Fatalf("err = %v, want ErrPlatformUnavailable", err)
	}
}
