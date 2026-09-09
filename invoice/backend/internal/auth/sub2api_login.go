package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Sub2APIAuthenticator verifies a Sub2API account password directly against
// the platform's own login endpoints. Response shapes below are read from
// K:/sub2api-src (backend/internal/handler/auth_handler.go and
// backend/internal/pkg/response/response.go) as of 2026-08-31; that tree is
// read-only reference and is never modified or vendored.
//
// Sub2API always answers with HTTP 200 and {"code":0,...} on success; on
// failure it answers with the semantic HTTP status itself (401 for wrong
// password *and* unknown email alike -- Sub2API's own AuthService.Login
// already folds both into ErrInvalidCredentials, so this client does not need
// to; 403 for an inactive account; 400 for a malformed request such as a
// non-email identifier). Any other status, a network failure, or a non-JSON
// body is treated as this service being unavailable, never as bad
// credentials, so it never counts against the caller's login-attempt budget.
type Sub2APIAuthenticator struct {
	client *http.Client
	origin string
}

func NewSub2APIAuthenticator(cfg PlatformEndpointConfig) (*Sub2APIAuthenticator, error) {
	return newSub2APIAuthenticator(cfg, nil)
}

// newSub2APIAuthenticator lets tests inject an httptest server's own client
// (e.g. server.Client()) in place of the production SSRF-safe dial context;
// see platformHTTPClient.
func newSub2APIAuthenticator(cfg PlatformEndpointConfig, base *http.Client) (*Sub2APIAuthenticator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := platformHTTPClient(cfg.BaseURL, cfg.timeout(), base)
	if err != nil {
		return nil, err
	}
	return &Sub2APIAuthenticator{client: client, origin: cfg.origin()}, nil
}

type sub2APIEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

type sub2APILoginPayload struct {
	Requires2FA bool             `json:"requires_2fa"`
	TempToken   string           `json:"temp_token"`
	User        *sub2APIUserData `json:"user"`
}

type sub2APIUserData struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

func (a *Sub2APIAuthenticator) Login(ctx context.Context, identifier, password string) (PlatformLoginResult, error) {
	// turnstile_token is deliberately omitted: this is a server-to-server
	// forwarder with no browser challenge widget to satisfy. The deployment
	// must keep Sub2API's Turnstile disabled for platform-password login to
	// work at all (see the handoff notes and GET /api/v1/settings/public).
	body, err := json.Marshal(map[string]string{"email": identifier, "password": password})
	if err != nil {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	data, err := a.call(ctx, "/api/v1/auth/login", body)
	if err != nil {
		return PlatformLoginResult{}, err
	}
	return parseSub2APILoginData(data)
}

func (a *Sub2APIAuthenticator) VerifyTwoFA(ctx context.Context, tempToken, code string) (PlatformLoginResult, error) {
	body, err := json.Marshal(map[string]string{"temp_token": tempToken, "totp_code": code})
	if err != nil {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	data, err := a.call(ctx, "/api/v1/auth/login/2fa", body)
	if err != nil {
		if errors.Is(err, ErrPlatformCredentialsInvalid) {
			return PlatformLoginResult{}, ErrPlatformTwoFAInvalid
		}
		return PlatformLoginResult{}, err
	}
	return parseSub2APILoginData(data)
}

func (a *Sub2APIAuthenticator) call(ctx context.Context, path string, body []byte) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrPlatformUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, ErrPlatformUnavailable
	}
	defer response.Body.Close()
	var envelope sub2APIEnvelope
	decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
	switch response.StatusCode {
	case http.StatusOK:
		if decodeErr != nil || envelope.Code != 0 {
			return nil, ErrPlatformUnavailable
		}
		return envelope.Data, nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrPlatformCredentialsInvalid
	default:
		return nil, ErrPlatformUnavailable
	}
}

func parseSub2APILoginData(raw json.RawMessage) (PlatformLoginResult, error) {
	var payload sub2APILoginPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	if payload.Requires2FA {
		if !looksLikeOpaqueToken(payload.TempToken) {
			return PlatformLoginResult{}, ErrPlatformUnavailable
		}
		return PlatformLoginResult{RequiresTwoFA: true, TempToken: payload.TempToken}, nil
	}
	if payload.User == nil || payload.User.ID <= 0 {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	return PlatformLoginResult{
		PlatformUserID: strconv.FormatInt(payload.User.ID, 10),
		Username:       payload.User.Username,
		Email:          payload.User.Email,
	}, nil
}
