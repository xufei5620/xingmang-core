package auth

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func validLogoutClaims(fixture *oidcFixture) map[string]any {
	return map[string]any{
		"iss": fixture.server.URL, "sub": "user-123", "sid": "provider-session-1",
		"aud": "invoice-web", "iat": fixture.now.Unix(), "exp": fixture.now.Add(5 * time.Minute).Unix(),
		"jti":    "logout-jti-1",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	}
}

func TestRPInitiatedLogoutURLUsesDiscoveredEndpointAndExactReturn(t *testing.T) {
	fixture := newOIDCFixture(t)
	client := fixture.client(t)
	raw, err := client.RPInitiatedLogoutURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != fixture.server.Listener.Addr().String() || parsed.Path != "/logout" ||
		parsed.Query().Get("client_id") != "invoice-web" || parsed.Query().Get("post_logout_redirect_uri") != fixture.server.URL+"/" {
		t.Fatalf("unsafe RP logout URL: %s", raw)
	}
	unsafeClient := *client
	unsafeClient.config.PostLogoutRedirectURL = "https://attacker.example/"
	if _, err = unsafeClient.RPInitiatedLogoutURL(); !errors.Is(err, ErrLogoutUnsupported) {
		t.Fatalf("cross-origin post-logout redirect accepted: %v", err)
	}
}

func TestBackchannelLogoutTokenStrictVerification(t *testing.T) {
	fixture := newOIDCFixture(t)
	client := fixture.client(t)
	raw, err := signFixtureJWT(fixture.key, validLogoutClaims(fixture))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := client.VerifyBackchannelLogout(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Issuer != fixture.server.URL || verified.Subject != "user-123" || verified.SessionID != "provider-session-1" || verified.TokenID != "logout-jti-1" {
		t.Fatalf("verified logout=%+v", verified)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
		wrong  bool
	}{
		{"nonce_present", func(c map[string]any) { c["nonce"] = "forbidden" }, false},
		{"wrong_issuer", func(c map[string]any) { c["iss"] = "https://attacker.example" }, false},
		{"wrong_audience", func(c map[string]any) { c["aud"] = "other-client" }, false},
		{"missing_jti", func(c map[string]any) { delete(c, "jti") }, false},
		{"missing_iat", func(c map[string]any) { delete(c, "iat") }, false},
		{"stale_iat", func(c map[string]any) { c["iat"] = fixture.now.Add(-time.Hour).Unix() }, false},
		{"long_expiry", func(c map[string]any) { c["exp"] = fixture.now.Add(time.Hour).Unix() }, false},
		{"missing_subject_and_sid", func(c map[string]any) { delete(c, "sub"); delete(c, "sid") }, false},
		{"event_not_empty", func(c map[string]any) {
			c["events"] = map[string]any{backchannelLogoutEvent: map[string]any{"x": true}}
		}, false},
		{"event_null", func(c map[string]any) {
			c["events"] = map[string]any{backchannelLogoutEvent: nil}
		}, false},
		{"extra_event", func(c map[string]any) {
			c["events"] = map[string]any{backchannelLogoutEvent: map[string]any{}, "other": map[string]any{}}
		}, false},
		{"wrong_signature", func(map[string]any) {}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := validLogoutClaims(fixture)
			test.mutate(claims)
			key := fixture.key
			if test.wrong {
				key = fixture.wrongKey
			}
			token, signErr := signFixtureJWT(key, claims)
			if signErr != nil {
				t.Fatal(signErr)
			}
			if _, verifyErr := client.VerifyBackchannelLogout(context.Background(), token); !errors.Is(verifyErr, ErrInvalidLogoutToken) {
				t.Fatalf("error=%v", verifyErr)
			}
		})
	}
}
