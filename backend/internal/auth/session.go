package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrSessionInvalid = errors.New("session is invalid, expired or revoked")
	ErrSessionBinding = errors.New("session client binding does not match")
)

type ClientBinding struct {
	IPHash        string
	UserAgentHash string
}

type Session struct {
	ID              string
	FamilyID        string
	UserID          string
	Issuer          string     `json:"-"`
	Subject         string     `json:"-"`
	TokenHash       string     `json:"-"`
	CSRFHash        string     `json:"-"`
	ProviderSIDHash string     `json:"-"`
	Roles           []string   `json:"-"`
	ACR             string     `json:"-"`
	AMR             []string   `json:"-"`
	AuthTime        time.Time  `json:"-"`
	MFAAt           *time.Time `json:"-"`
	ClientIPHash    string     `json:"-"`
	UserAgentHash   string     `json:"-"`
	// Platform and PlatformUserID are non-empty only for a platform-password
	// login (see platform_login.go). They are always re-derived from the
	// joined invoice_users row, never trusted from a caller-supplied value --
	// see PostgresSessionStore in postgres.go.
	Platform       Platform `json:"-"`
	PlatformUserID string   `json:"-"`
	// DisplayName is the platform's own captured account username
	// (Principal.DisplayName, sourced from PlatformLoginResult.Username),
	// empty for an OIDC session. Unlike Platform/PlatformUserID above it is
	// NOT re-derived from invoice_users (which has no such column -- CR-0003
	// makes identity strictly per-platform-login, and this belongs to the
	// session, not the user record); PostgresSessionStore stores it
	// encrypted in auth_sessions.display_name_ciphertext and decrypts it
	// back into this plaintext field on every load.
	DisplayName       string `json:"-"`
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RotatedFrom       string
	RevokedAt         *time.Time
	RevokedReason     string
}

type SessionStore interface {
	Create(ctx context.Context, session Session) error
	AuthenticateAndTouch(ctx context.Context, tokenHash string, binding ClientBinding, now time.Time, idleTTL time.Duration) (Session, error)
	Rotate(ctx context.Context, oldTokenHash, expectedSessionID string, next Session, now time.Time) (Session, error)
	RevokeToken(ctx context.Context, tokenHash string, now time.Time, reason string) (Session, error)
	RevokeFamily(ctx context.Context, familyID string, now time.Time, reason string) (int64, error)
	RevokeUser(ctx context.Context, userID string, now time.Time, reason string) (int64, error)
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

type SessionConfig struct {
	CookieName  string
	IdleTTL     time.Duration
	AbsoluteTTL time.Duration
	SameSite    http.SameSite
}

func (c SessionConfig) withDefaults() SessionConfig {
	if c.CookieName == "" {
		c.CookieName = "__Host-invoice_session"
	}
	if c.IdleTTL == 0 {
		c.IdleTTL = 8 * time.Hour
	}
	if c.AbsoluteTTL == 0 {
		c.AbsoluteTTL = 24 * time.Hour
	}
	if c.SameSite == 0 {
		c.SameSite = http.SameSiteLaxMode
	}
	return c
}

func (c SessionConfig) Validate() error {
	c = c.withDefaults()
	if !strings.HasPrefix(c.CookieName, "__Host-") || strings.ContainsAny(c.CookieName, ";=, \t\r\n\x00") {
		return errors.New("session cookie name must use a valid __Host- prefix")
	}
	if c.IdleTTL < 5*time.Minute || c.IdleTTL > 24*time.Hour {
		return errors.New("session idle TTL must be between 5 minutes and 24 hours")
	}
	if c.AbsoluteTTL < c.IdleTTL || c.AbsoluteTTL > 7*24*time.Hour {
		return errors.New("session absolute TTL must be at least idle TTL and at most 7 days")
	}
	if c.SameSite != http.SameSiteLaxMode && c.SameSite != http.SameSiteStrictMode {
		return errors.New("session cookie SameSite must be Lax or Strict")
	}
	return nil
}

type SessionManager struct {
	store  SessionStore
	config SessionConfig
	audit  SecurityAuditSink
	now    func() time.Time
}

func NewSessionManager(store SessionStore, config SessionConfig, audit SecurityAuditSink) (*SessionManager, error) {
	if store == nil || audit == nil {
		return nil, errors.New("session store and security audit sink are required")
	}
	config = config.withDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &SessionManager{store: store, config: config, audit: audit, now: time.Now}, nil
}

type IssueSessionInput struct {
	UserID    string
	Principal Principal
	Binding   ClientBinding
	MFAAt     *time.Time
	RequestID string
}

type RotateSessionInput struct {
	Token             string
	ExpectedSessionID string
	Principal         Principal
	Binding           ClientBinding
	MFAAt             *time.Time
	RequestID         string
}

type SessionCredentials struct {
	Session   Session `json:"-"`
	Token     string  `json:"-"`
	CSRFToken string  `json:"-"`
}

func (m *SessionManager) Issue(ctx context.Context, input IssueSessionInput) (SessionCredentials, error) {
	if strings.TrimSpace(input.UserID) == "" || input.Principal.Issuer == "" || input.Principal.Subject == "" || !validRequestID(input.RequestID) {
		return SessionCredentials{}, errors.New("session user, verified identity and request ID are required")
	}
	token, csrf, err := newSessionSecrets()
	if err != nil {
		return SessionCredentials{}, err
	}
	now := m.now().UTC()
	if err = validateSessionMFA(input.Principal, input.MFAAt, now); err != nil {
		return SessionCredentials{}, err
	}
	session := Session{
		ID: randomUUIDv4(), FamilyID: randomUUIDv4(), UserID: input.UserID,
		Issuer: input.Principal.Issuer, Subject: input.Principal.Subject,
		TokenHash: sha256Hex(token), CSRFHash: sha256Hex(csrf),
		ProviderSIDHash: optionalHash(input.Principal.ProviderSID),
		// append onto a non-nil empty slice: auth_sessions.roles/amr are
		// TEXT[] NOT NULL, and pgx encodes a nil []string as SQL NULL (the
		// column DEFAULT does not apply to an explicit column list). OIDC
		// principals always carry roles, but platform-password principals
		// have none -- a nil here broke the first real platform login in
		// production (SQLSTATE 23502) after the claim fix let it reach
		// session issuance.
		Roles: append([]string{}, input.Principal.Roles...), ACR: input.Principal.ACR, AMR: append([]string{}, input.Principal.AMR...),
		AuthTime: input.Principal.AuthTime, MFAAt: copyTime(input.MFAAt),
		ClientIPHash: input.Binding.IPHash, UserAgentHash: input.Binding.UserAgentHash,
		Platform: input.Principal.Platform, PlatformUserID: input.Principal.PlatformUserID,
		DisplayName: input.Principal.DisplayName,
		CreatedAt:   now, LastSeenAt: now, IdleExpiresAt: now.Add(m.config.IdleTTL), AbsoluteExpiresAt: now.Add(m.config.AbsoluteTTL),
	}
	if err = validateSessionRecord(session); err != nil {
		return SessionCredentials{}, err
	}
	if err = m.store.Create(ctx, session); err != nil {
		return SessionCredentials{}, err
	}
	if err = m.record(ctx, SecurityAuditEvent{ActorType: "user", ActorID: input.UserID, Action: "auth.session.issue", ObjectType: "auth_session", ObjectID: session.ID, RequestID: input.RequestID, Severity: SeverityInfo}); err != nil {
		_, _ = m.store.RevokeToken(ctx, session.TokenHash, now, "audit write failed")
		return SessionCredentials{}, err
	}
	return SessionCredentials{Session: session, Token: token, CSRFToken: csrf}, nil
}

func (m *SessionManager) Authenticate(ctx context.Context, token string, binding ClientBinding) (Session, error) {
	if !validOpaqueToken(token) {
		return Session{}, ErrSessionInvalid
	}
	session, err := m.store.AuthenticateAndTouch(ctx, sha256Hex(token), binding, m.now().UTC(), m.config.IdleTTL)
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
	return session, nil
}

func (m *SessionManager) Rotate(ctx context.Context, input RotateSessionInput) (SessionCredentials, error) {
	if !validOpaqueToken(input.Token) || strings.TrimSpace(input.ExpectedSessionID) == "" || !validRequestID(input.RequestID) {
		return SessionCredentials{}, ErrSessionInvalid
	}
	token, csrf, err := newSessionSecrets()
	if err != nil {
		return SessionCredentials{}, err
	}
	now := m.now().UTC()
	if err = validateSessionMFA(input.Principal, input.MFAAt, now); err != nil {
		return SessionCredentials{}, err
	}
	next := Session{
		ID: randomUUIDv4(), Issuer: input.Principal.Issuer, Subject: input.Principal.Subject,
		TokenHash: sha256Hex(token), CSRFHash: sha256Hex(csrf),
		ProviderSIDHash: optionalHash(input.Principal.ProviderSID),
		// append onto a non-nil empty slice: auth_sessions.roles/amr are
		// TEXT[] NOT NULL, and pgx encodes a nil []string as SQL NULL (the
		// column DEFAULT does not apply to an explicit column list). OIDC
		// principals always carry roles, but platform-password principals
		// have none -- a nil here broke the first real platform login in
		// production (SQLSTATE 23502) after the claim fix let it reach
		// session issuance.
		Roles: append([]string{}, input.Principal.Roles...), ACR: input.Principal.ACR, AMR: append([]string{}, input.Principal.AMR...),
		AuthTime: input.Principal.AuthTime, MFAAt: copyTime(input.MFAAt),
		ClientIPHash: input.Binding.IPHash, UserAgentHash: input.Binding.UserAgentHash,
		Platform: input.Principal.Platform, PlatformUserID: input.Principal.PlatformUserID,
		DisplayName: input.Principal.DisplayName,
		CreatedAt:   now, LastSeenAt: now, IdleExpiresAt: now.Add(m.config.IdleTTL),
	}
	next, err = m.store.Rotate(ctx, sha256Hex(input.Token), input.ExpectedSessionID, next, now)
	if err != nil {
		return SessionCredentials{}, ErrSessionInvalid
	}
	if err = m.record(ctx, SecurityAuditEvent{ActorType: "user", ActorID: next.UserID, Action: "auth.session.rotate", ObjectType: "auth_session", ObjectID: next.ID, RequestID: input.RequestID, Severity: SeverityInfo}); err != nil {
		_, _ = m.store.RevokeToken(ctx, next.TokenHash, now, "audit write failed")
		return SessionCredentials{}, err
	}
	return SessionCredentials{Session: next, Token: token, CSRFToken: csrf}, nil
}

func (m *SessionManager) RevokeToken(ctx context.Context, token, reason, requestID string) error {
	if !validOpaqueToken(token) || strings.TrimSpace(reason) == "" || len(reason) > 500 || !validRequestID(requestID) {
		return errors.New("valid session token and bounded revocation reason are required")
	}
	session, err := m.store.RevokeToken(ctx, sha256Hex(token), m.now().UTC(), reason)
	if err != nil {
		return ErrSessionInvalid
	}
	return m.record(ctx, SecurityAuditEvent{ActorType: "user", ActorID: session.UserID, Action: "auth.session.revoke", ObjectType: "auth_session", ObjectID: session.ID, RequestID: requestID, Reason: reason, Severity: SeverityNotice})
}

func (m *SessionManager) RevokeFamily(ctx context.Context, familyID, actorID, reason, requestID string) (int64, error) {
	if strings.TrimSpace(familyID) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" || len(reason) > 500 || !validRequestID(requestID) {
		return 0, errors.New("session family, actor and bounded reason are required")
	}
	count, err := m.store.RevokeFamily(ctx, familyID, m.now().UTC(), reason)
	if err == nil {
		err = m.record(ctx, SecurityAuditEvent{ActorType: "admin", ActorID: actorID, Action: "auth.session_family.revoke", ObjectType: "auth_session_family", ObjectID: familyID, RequestID: requestID, Reason: reason, Severity: SeverityWarning})
	}
	return count, err
}

func (m *SessionManager) RevokeUser(ctx context.Context, userID, actorID, reason, requestID string) (int64, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" || len(reason) > 500 || !validRequestID(requestID) {
		return 0, errors.New("user, actor and bounded reason are required")
	}
	count, err := m.store.RevokeUser(ctx, userID, m.now().UTC(), reason)
	if err == nil {
		err = m.record(ctx, SecurityAuditEvent{ActorType: "admin", ActorID: actorID, Action: "auth.user_sessions.revoke", ObjectType: "invoice_user", ObjectID: userID, RequestID: requestID, Reason: reason, Severity: SeverityWarning})
	}
	return count, err
}

func (m *SessionManager) SessionCookie(token string, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: m.config.CookieName, Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), Secure: true, HttpOnly: true, SameSite: m.config.SameSite}
}

func (m *SessionManager) ClearSessionCookie() *http.Cookie {
	return &http.Cookie{Name: m.config.CookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, HttpOnly: true, SameSite: m.config.SameSite}
}

// OIDCFlowCookie is first-party, short-lived and HttpOnly. SameSite=Lax allows
// the top-level callback navigation while still working for same-site embeds.
func OIDCFlowCookie(browserBinding string, expires time.Time) (*http.Cookie, error) {
	if !validOpaqueToken(browserBinding) {
		return nil, errors.New("invalid OIDC browser binding")
	}
	return &http.Cookie{Name: "__Host-invoice_oidc_flow", Value: browserBinding, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}, nil
}

func ClearOIDCFlowCookie() *http.Cookie {
	return &http.Cookie{Name: "__Host-invoice_oidc_flow", Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func newSessionSecrets() (string, string, error) {
	token, err := randomURLToken(32)
	if err != nil {
		return "", "", err
	}
	csrf, err := randomURLToken(32)
	return token, csrf, err
}

func validateSessionRecord(session Session) error {
	if session.ID == "" || session.FamilyID == "" || session.UserID == "" || session.Issuer == "" || session.Subject == "" || len(session.TokenHash) != 64 || len(session.CSRFHash) != 64 {
		return errors.New("session identity or secret hashes are invalid")
	}
	if len(session.Issuer) > 2048 || len(session.Subject) > 512 || hasControl(session.Issuer) || hasControl(session.Subject) || len(session.ACR) > 512 || hasControl(session.ACR) || strings.TrimSpace(session.ACR) != session.ACR {
		return errors.New("session identity claims are invalid")
	}
	if session.ProviderSIDHash != "" && len(session.ProviderSIDHash) != 64 || session.ClientIPHash != "" && len(session.ClientIPHash) != 64 || session.UserAgentHash != "" && len(session.UserAgentHash) != 64 {
		return errors.New("session optional secret hashes are invalid")
	}
	if (session.Platform == "") != (session.PlatformUserID == "") {
		return errors.New("session platform and platform user ID must be set together")
	}
	if session.Platform != "" && !session.Platform.Valid() {
		return errors.New("session platform is invalid")
	}
	if len(session.PlatformUserID) > 512 || hasControl(session.PlatformUserID) || strings.TrimSpace(session.PlatformUserID) != session.PlatformUserID {
		return errors.New("session platform user ID is invalid")
	}
	if len(session.DisplayName) > 512 || hasControl(session.DisplayName) || strings.TrimSpace(session.DisplayName) != session.DisplayName {
		return errors.New("session display name is invalid")
	}
	if err := validateSessionClaimSet(session.Roles, 100); err != nil {
		return err
	}
	if err := validateSessionClaimSet(session.AMR, 32); err != nil {
		return err
	}
	if session.CreatedAt.IsZero() || session.LastSeenAt.Before(session.CreatedAt) || !session.IdleExpiresAt.After(session.CreatedAt) || !session.AbsoluteExpiresAt.After(session.CreatedAt) || session.IdleExpiresAt.After(session.AbsoluteExpiresAt) {
		return errors.New("session timestamps are invalid")
	}
	return nil
}

func validateSessionClaimSet(values []string, limit int) error {
	if len(values) > limit {
		return errors.New("session claim set is too large")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 512 || hasControl(value) {
			return errors.New("session claim set contains an invalid value")
		}
	}
	return nil
}

func validRequestID(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && len(value) <= 512 && !hasControl(value)
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func optionalHash(value string) string {
	if value == "" {
		return ""
	}
	return sha256Hex(value)
}

func validateSessionMFA(principal Principal, mfaAt *time.Time, now time.Time) error {
	if mfaAt == nil {
		return nil
	}
	if principal.AuthTime.IsZero() || mfaAt.Before(principal.AuthTime.Add(-defaultClockSkew)) || mfaAt.After(now.Add(defaultClockSkew)) {
		return ErrMFAStepUpRequired
	}
	return nil
}

func randomUUIDv4() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	raw := hex.EncodeToString(value[:])
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32]
}

func validUUIDString(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	raw := strings.ReplaceAll(value, "-", "")
	_, err := hex.DecodeString(raw)
	return err == nil
}

type MemorySessionStore struct {
	mu      sync.Mutex
	byToken map[string]Session
	byID    map[string]string
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{byToken: make(map[string]Session), byID: make(map[string]string)}
}

func (m *MemorySessionStore) Create(_ context.Context, session Session) error {
	if err := validateSessionRecord(session); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byToken[session.TokenHash]; exists {
		return errors.New("session token collision")
	}
	if _, exists := m.byID[session.ID]; exists {
		return errors.New("session ID collision")
	}
	session = cloneSession(session)
	m.byToken[session.TokenHash] = session
	m.byID[session.ID] = session.TokenHash
	return nil
}

func (m *MemorySessionStore) AuthenticateAndTouch(_ context.Context, tokenHash string, binding ClientBinding, now time.Time, idleTTL time.Duration) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.byToken[tokenHash]
	if !ok || session.RevokedAt != nil || !session.IdleExpiresAt.After(now) || !session.AbsoluteExpiresAt.After(now) || !bindingMatches(session, binding) {
		return Session{}, ErrSessionInvalid
	}
	session.LastSeenAt = now
	session.IdleExpiresAt = now.Add(idleTTL)
	if session.IdleExpiresAt.After(session.AbsoluteExpiresAt) {
		session.IdleExpiresAt = session.AbsoluteExpiresAt
	}
	m.byToken[tokenHash] = session
	return cloneSession(session), nil
}

func (m *MemorySessionStore) Rotate(_ context.Context, oldTokenHash, expectedSessionID string, next Session, now time.Time) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.byToken[oldTokenHash]
	if !ok || old.ID != expectedSessionID || old.RevokedAt != nil || !old.IdleExpiresAt.After(now) || !old.AbsoluteExpiresAt.After(now) || old.Issuer != next.Issuer || old.Subject != next.Subject || old.Platform != next.Platform || old.PlatformUserID != next.PlatformUserID {
		return Session{}, ErrSessionInvalid
	}
	if _, exists := m.byToken[next.TokenHash]; exists {
		return Session{}, errors.New("session token collision")
	}
	revokedAt := now
	old.RevokedAt = &revokedAt
	old.RevokedReason = "rotated"
	m.byToken[oldTokenHash] = old
	next.FamilyID = old.FamilyID
	next.UserID = old.UserID
	next.AbsoluteExpiresAt = old.AbsoluteExpiresAt
	if next.IdleExpiresAt.After(next.AbsoluteExpiresAt) {
		next.IdleExpiresAt = next.AbsoluteExpiresAt
	}
	next.RotatedFrom = old.ID
	if err := validateSessionRecord(next); err != nil {
		return Session{}, err
	}
	m.byToken[next.TokenHash] = cloneSession(next)
	m.byID[next.ID] = next.TokenHash
	return cloneSession(next), nil
}

func (m *MemorySessionStore) RevokeToken(_ context.Context, tokenHash string, now time.Time, reason string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.byToken[tokenHash]
	if !ok || session.RevokedAt != nil {
		return Session{}, ErrSessionInvalid
	}
	session.RevokedAt = copyTime(&now)
	session.RevokedReason = reason
	m.byToken[tokenHash] = session
	return cloneSession(session), nil
}

func (m *MemorySessionStore) RevokeFamily(_ context.Context, familyID string, now time.Time, reason string) (int64, error) {
	return m.revokeWhere(now, reason, func(session Session) bool { return session.FamilyID == familyID }), nil
}

func (m *MemorySessionStore) RevokeUser(_ context.Context, userID string, now time.Time, reason string) (int64, error) {
	return m.revokeWhere(now, reason, func(session Session) bool { return session.UserID == userID }), nil
}

func (m *MemorySessionStore) revokeWhere(now time.Time, reason string, matches func(Session) bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for hash, session := range m.byToken {
		if session.RevokedAt == nil && matches(session) {
			session.RevokedAt = copyTime(&now)
			session.RevokedReason = reason
			m.byToken[hash] = session
			count++
		}
	}
	return count
}

func (m *MemorySessionStore) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Match the PostgreSQL self-FK semantics: an active rotation leaf retains
	// every predecessor until the complete chain is cleanup-eligible.
	retained := make(map[string]struct{}, len(m.byToken))
	for _, session := range m.byToken {
		if !sessionCleanupEligible(session, before) {
			retained[session.ID] = struct{}{}
		}
	}
	for changed := true; changed; {
		changed = false
		for id := range retained {
			hash, ok := m.byID[id]
			if !ok {
				continue
			}
			parentID := m.byToken[hash].RotatedFrom
			if parentID == "" {
				continue
			}
			if _, ok = retained[parentID]; !ok {
				retained[parentID] = struct{}{}
				changed = true
			}
		}
	}
	var count int64
	for hash, session := range m.byToken {
		_, keep := retained[session.ID]
		if sessionCleanupEligible(session, before) && !keep {
			delete(m.byToken, hash)
			delete(m.byID, session.ID)
			count++
		}
	}
	return count, nil
}

func sessionCleanupEligible(session Session, before time.Time) bool {
	return session.AbsoluteExpiresAt.Before(before) || session.RevokedAt != nil && session.RevokedAt.Before(before)
}

func bindingMatches(session Session, binding ClientBinding) bool {
	if session.ClientIPHash != "" && !secureEqualHex(session.ClientIPHash, binding.IPHash) {
		return false
	}
	return session.UserAgentHash == "" || secureEqualHex(session.UserAgentHash, binding.UserAgentHash)
}

func cloneSession(session Session) Session {
	session.Roles = append([]string(nil), session.Roles...)
	session.AMR = append([]string(nil), session.AMR...)
	session.MFAAt = copyTime(session.MFAAt)
	session.RevokedAt = copyTime(session.RevokedAt)
	return session
}
