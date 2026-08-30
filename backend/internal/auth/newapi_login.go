package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// NewAPIAuthenticator verifies a New API account password directly against
// the platform's own login endpoints. Response shapes below are read from
// K:/newapi-src (controller/user.go Login/setupLoginAtAuthVersion,
// controller/twofa.go Verify2FALogin, common/gin.go) as of 2026-08-31; that
// tree is read-only reference and is never modified or vendored.
//
// Unlike Sub2API, New API answers every request -- success or failure --
// with HTTP 200 and a JSON "success" boolean; there is no distinct status
// code or error code for wrong password vs. unknown username; New API's own
// User.ValidateAndFill already folds both into one generic message, so this
// client does not need to. Any non-200 status (a proxy/gateway/rate-limit
// layer, not the application itself), a network failure, or a non-JSON body
// is treated as this service being unavailable, never as bad credentials.
type NewAPIAuthenticator struct {
	client *http.Client
	origin string
}

func NewNewAPIAuthenticator(cfg PlatformEndpointConfig) (*NewAPIAuthenticator, error) {
	return newNewAPIAuthenticator(cfg, nil)
}

// newNewAPIAuthenticator lets tests inject an httptest server's own client
// (e.g. server.Client()) in place of the production SSRF-safe dial context;
// see platformHTTPClient.
func newNewAPIAuthenticator(cfg PlatformEndpointConfig, base *http.Client) (*NewAPIAuthenticator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := platformHTTPClient(cfg.BaseURL, cfg.timeout(), base)
	if err != nil {
		return nil, err
	}
	return &NewAPIAuthenticator{client: client, origin: cfg.origin()}, nil
}

type newAPIEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type newAPILoginPayload struct {
	Require2FA bool            `json:"require_2fa"`
	FlowToken  string          `json:"flow_token"`
	User       *newAPIUserData `json:"user"`
}

type newAPIUserData struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (a *NewAPIAuthenticator) Login(ctx context.Context, identifier, password string) (PlatformLoginResult, error) {
	body, err := json.Marshal(map[string]string{"username": identifier, "password": password})
	if err != nil {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	data, err := a.call(ctx, "/api/user/login", body)
	if err != nil {
		return PlatformLoginResult{}, err
	}
	return parseNewAPILoginData(data)
}

func (a *NewAPIAuthenticator) VerifyTwoFA(ctx context.Context, tempToken, code string) (PlatformLoginResult, error) {
	body, err := json.Marshal(map[string]string{"flow_token": tempToken, "code": code})
	if err != nil {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	data, err := a.call(ctx, "/api/user/login/2fa", body)
	if err != nil {
		if errors.Is(err, ErrPlatformCredentialsInvalid) {
			return PlatformLoginResult{}, ErrPlatformTwoFAInvalid
		}
		return PlatformLoginResult{}, err
	}
	return parseNewAPILoginData(data)
}

func (a *NewAPIAuthenticator) call(ctx context.Context, path string, body []byte) (json.RawMessage, error) {
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
	if response.StatusCode != http.StatusOK {
		return nil, ErrPlatformUnavailable
	}
	var envelope newAPIEnvelope
	if err = json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return nil, ErrPlatformUnavailable
	}
	if !envelope.Success {
		return nil, ErrPlatformCredentialsInvalid
	}
	return envelope.Data, nil
}

func parseNewAPILoginData(raw json.RawMessage) (PlatformLoginResult, error) {
	var payload newAPILoginPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	if payload.Require2FA {
		if !looksLikeOpaqueToken(payload.FlowToken) {
			return PlatformLoginResult{}, ErrPlatformUnavailable
		}
		return PlatformLoginResult{RequiresTwoFA: true, TempToken: payload.FlowToken}, nil
	}
	if payload.User == nil || payload.User.ID <= 0 {
		return PlatformLoginResult{}, ErrPlatformUnavailable
	}
	username := strings.TrimSpace(payload.User.DisplayName)
	if username == "" {
		username = payload.User.Username
	}
	return PlatformLoginResult{
		PlatformUserID: strconv.FormatInt(payload.User.ID, 10),
		Username:       username,
		Email:          payload.User.Email,
	}, nil
}
