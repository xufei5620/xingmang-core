package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/mailer"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
)

var (
	ErrOIDCEmailUnverified = errors.New("OIDC delivery email is not verified")
	ErrEvidenceRequired    = errors.New("independent payment evidence is required")
	ErrIssuerNotConfigured = errors.New("invoice issuer is not configured")
)

type SettingsProvider interface {
	Get(context.Context) (adminsettings.Settings, error)
}

type Options struct {
	MinimumRequestMinor int64
	PublicBaseURL       string
	// DownloadBaseURL is kept as a temporary configuration alias. Both values
	// represent the public UI origin; generated mail never links to a bearer API.
	DownloadBaseURL               string
	EmailTemplateVersion          string
	SourceEconomicHeartbeatMaxAge time.Duration
	SourceEconomicWatermarkMaxAge time.Duration
	SourceIdentitiesMaxAge        time.Duration
	// SourceEconomicRescanActivityMaxAge (XM-INV-AGENT-RESTART-GRACE part A)
	// is independently optional: zero disables the readiness grace entirely
	// (an ECONOMIC_WATERMARK_STALE finding is never downgraded), which is
	// always safe -- it is not part of the SourceEconomic*/SourceIdentities
	// all-or-nothing bundle checked below.
	SourceEconomicRescanActivityMaxAge time.Duration
}

type Service struct {
	store                              *postgresstore.Store
	keys                               securefields.Keyring
	settings                           SettingsProvider
	minimumRequestMinor                atomic.Int64
	publicBaseURL                      *url.URL
	emailTemplateVersion               string
	sourceEconomicHeartbeatMaxAge      time.Duration
	sourceEconomicWatermarkMaxAge      time.Duration
	sourceIdentitiesMaxAge             time.Duration
	sourceEconomicRescanActivityMaxAge time.Duration
	now                                func() time.Time
}

func NewService(store *postgresstore.Store, keys securefields.Keyring, settings SettingsProvider, options Options) (*Service, error) {
	if store == nil {
		return nil, errors.New("postgres store is required")
	}
	if err := keys.Validate(); err != nil {
		return nil, fmt.Errorf("profile encryption keyring: %w", err)
	}
	if settings == nil {
		return nil, errors.New("persistent admin settings provider is required")
	}
	minimum := options.MinimumRequestMinor
	if minimum == 0 {
		minimum = domain.MinimumRequestMinor
	}
	if minimum < domain.MinimumRequestMinor {
		return nil, domain.ErrMinimumAmount
	}
	baseValue := strings.TrimSpace(options.PublicBaseURL)
	if baseValue == "" {
		baseValue = strings.TrimSpace(options.DownloadBaseURL)
	}
	base, err := url.Parse(baseValue)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("public base URL must be an absolute HTTPS origin without credentials, path, query or fragment")
	}
	base.Path = ""
	template := strings.TrimSpace(options.EmailTemplateVersion)
	if template == "" {
		template = "invoice-ready-v1"
	}
	economicHeartbeatMaxAge := options.SourceEconomicHeartbeatMaxAge
	economicWatermarkMaxAge := options.SourceEconomicWatermarkMaxAge
	identitiesMaxAge := options.SourceIdentitiesMaxAge
	configuredFreshnessValues := 0
	for _, value := range []time.Duration{economicHeartbeatMaxAge, economicWatermarkMaxAge, identitiesMaxAge} {
		if value != 0 {
			configuredFreshnessValues++
		}
	}
	if configuredFreshnessValues != 0 && configuredFreshnessValues != 3 ||
		economicHeartbeatMaxAge != 0 && (economicHeartbeatMaxAge < 30*time.Second || economicHeartbeatMaxAge > 24*time.Hour ||
			economicWatermarkMaxAge < 30*time.Second || economicWatermarkMaxAge > 24*time.Hour ||
			identitiesMaxAge < 30*time.Second || identitiesMaxAge > 24*time.Hour) {
		return nil, errors.New("source freshness thresholds must be between 30 seconds and 24 hours")
	}
	economicRescanActivityMaxAge := options.SourceEconomicRescanActivityMaxAge
	if economicRescanActivityMaxAge != 0 && (economicRescanActivityMaxAge < 30*time.Second || economicRescanActivityMaxAge > 24*time.Hour) {
		return nil, errors.New("source economic rescan activity threshold must be between 30 seconds and 24 hours")
	}
	service := &Service{
		store: store, keys: keys, settings: settings, publicBaseURL: base,
		emailTemplateVersion: template, sourceEconomicHeartbeatMaxAge: economicHeartbeatMaxAge,
		sourceEconomicWatermarkMaxAge: economicWatermarkMaxAge,
		sourceIdentitiesMaxAge:        identitiesMaxAge, sourceEconomicRescanActivityMaxAge: economicRescanActivityMaxAge,
		now: func() time.Time { return time.Now().UTC() },
	}
	service.minimumRequestMinor.Store(minimum)
	return service, nil
}

func (s *Service) sourceFreshnessPolicy() postgresstore.SourceFreshnessPolicy {
	return postgresstore.SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge:      s.sourceEconomicHeartbeatMaxAge,
		EconomicWatermarkMaxAge:      s.sourceEconomicWatermarkMaxAge,
		IdentitiesMaxAge:             s.sourceIdentitiesMaxAge,
		EconomicRescanActivityMaxAge: s.sourceEconomicRescanActivityMaxAge,
		Now:                          s.now().UTC(),
	}
}

func (s *Service) MinimumRequestMinor() int64 { return s.minimumRequestMinor.Load() }

func (s *Service) SetMinimumRequestMinor(value int64) error {
	if value < domain.MinimumRequestMinor {
		return domain.ErrMinimumAmount
	}
	s.minimumRequestMinor.Store(value)
	return nil
}

type auditContextKey struct{}

func WithAuditActor(ctx context.Context, actor postgresstore.AuditActor) context.Context {
	return context.WithValue(ctx, auditContextKey{}, actor)
}

func auditActor(ctx context.Context, actorType, actorID, reason string) postgresstore.AuditActor {
	actor, _ := ctx.Value(auditContextKey{}).(postgresstore.AuditActor)
	if actor.Type == "" {
		actor.Type = actorType
	}
	if actor.ID == "" {
		actor.ID = actorID
	}
	if actor.Reason == "" {
		actor.Reason = reason
	}
	return actor
}

type OIDCIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Status        string
}

type CurrentUser struct {
	ID            string `json:"id"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified"`
	Status        string `json:"status"`
	// OIDCIssuer/OIDCSubject are the invoice_user's stored canonical
	// identity pair. Session issuance guards on this exact pair, so a
	// login that lands on a pre-existing identity (platform-password
	// claim of a source-projected SSO user) must issue its session with
	// these values, not the login principal's synthetic pair. Never
	// serialized to API responses.
	OIDCIssuer  string `json:"-"`
	OIDCSubject string `json:"-"`
}

func (s *Service) GetCurrentUser(ctx context.Context, userID string) (CurrentUser, error) {
	record, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return CurrentUser{}, err
	}
	out := CurrentUser{ID: record.ID, Status: record.Status, OIDCIssuer: record.OIDCIssuer, OIDCSubject: record.OIDCSubject}
	verified, err := s.store.GetLatestVerifiedEmail(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return CurrentUser{}, err
	}
	plaintext, decryptErr := s.keys.Decrypt(verified.EmailCiphertext,
		verifiedEmailAAD(userID, verified.NormalizedEmailHMAC))
	if decryptErr != nil {
		return CurrentUser{}, fmt.Errorf("decrypt current user email: %w", decryptErr)
	}
	out.Email, err = canonicalEmail(string(plaintext))
	if err != nil {
		return CurrentUser{}, err
	}
	out.EmailVerified = true
	return out, nil
}

func (s *Service) EnsureUser(ctx context.Context, identity OIDCIdentity) (postgresstore.UserRecord, error) {
	issuer := strings.TrimRight(strings.TrimSpace(identity.Issuer), "/")
	subject := strings.TrimSpace(identity.Subject)
	if issuer == "" || subject == "" {
		return postgresstore.UserRecord{}, errors.New("OIDC issuer and subject are required")
	}
	email := ""
	var ciphertext []byte
	var err error
	if identity.EmailVerified {
		email, err = canonicalEmail(identity.Email)
		if err != nil {
			return postgresstore.UserRecord{}, err
		}
		ciphertext, err = s.keys.Encrypt([]byte(email), userEmailAAD(issuer, subject))
		if err != nil {
			return postgresstore.UserRecord{}, err
		}
	}
	record := postgresstore.UserRecord{
		OIDCIssuer: issuer, OIDCSubject: subject, Status: identity.Status,
		EmailCiphertext: ciphertext, EmailVerified: identity.EmailVerified,
	}
	saved, err := s.store.EnsureUserAndSyncOIDCEmail(ctx, record, func(principalID string) (*postgresstore.VerifiedEmailRecord, error) {
		if !identity.EmailVerified {
			return nil, nil
		}
		hash, hashErr := s.keys.BlindIndex("verified-email/"+principalID, email)
		if hashErr != nil {
			return nil, hashErr
		}
		verifiedCiphertext, encryptErr := s.keys.Encrypt([]byte(email), verifiedEmailAAD(principalID, hash))
		if encryptErr != nil {
			return nil, encryptErr
		}
		return &postgresstore.VerifiedEmailRecord{
			PrincipalID: principalID, EmailCiphertext: verifiedCiphertext,
			NormalizedEmailHMAC: hash, VerificationSource: "oidc", VerifiedAt: s.now(),
		}, nil
	}, auditActor(ctx, "oidc", subject, "OIDC identity and verified email synchronized"))
	if err != nil {
		return postgresstore.UserRecord{}, err
	}
	if err = s.wakeDependency(ctx, "invoice_oidc_user", "", issuer+"\n"+subject); err != nil {
		return postgresstore.UserRecord{}, err
	}
	return saved, nil
}

// RegisterChallengeVerifiedEmail is a trusted application boundary: callers
// may invoke it only after the email challenge service has consumed a
// single-use verification token.  SaveProfile never calls it and never trusts
// domain.InvoiceProfile.EmailVerified.
func (s *Service) RegisterChallengeVerifiedEmail(ctx context.Context, principalID, email string) error {
	canonical, err := canonicalEmail(email)
	if err != nil {
		return err
	}
	_, err = s.registerVerifiedEmail(ctx, principalID, canonical, "email_challenge",
		auditActor(ctx, "email_challenge", principalID, "email challenge completed"))
	return err
}

func (s *Service) registerVerifiedEmail(ctx context.Context, principalID, email, source string, actor postgresstore.AuditActor) (postgresstore.VerifiedEmailRecord, error) {
	hash, err := s.keys.BlindIndex("verified-email/"+principalID, email)
	if err != nil {
		return postgresstore.VerifiedEmailRecord{}, err
	}
	ciphertext, err := s.keys.Encrypt([]byte(email), verifiedEmailAAD(principalID, hash))
	if err != nil {
		return postgresstore.VerifiedEmailRecord{}, err
	}
	return s.store.RegisterVerifiedEmail(ctx, postgresstore.VerifiedEmailRecord{
		PrincipalID: principalID, EmailCiphertext: ciphertext,
		NormalizedEmailHMAC: hash, VerificationSource: source, VerifiedAt: s.now(),
	}, actor)
}

func (s *Service) IsEmailVerified(ctx context.Context, principalID, email string) (bool, error) {
	canonical, err := canonicalEmail(email)
	if err != nil {
		return false, nil
	}
	hash, err := s.keys.BlindIndex("verified-email/"+principalID, canonical)
	if err != nil {
		return false, err
	}
	return s.store.IsEmailVerified(ctx, principalID, hash)
}

func (s *Service) RevokeVerifiedEmail(ctx context.Context, principalID, email string) error {
	canonical, err := canonicalEmail(email)
	if err != nil {
		return err
	}
	hash, err := s.keys.BlindIndex("verified-email/"+principalID, canonical)
	if err != nil {
		return err
	}
	return s.store.RevokeVerifiedEmail(ctx, principalID, hash,
		auditActor(ctx, "admin", principalID, "verified delivery email revoked"))
}

func (s *Service) BindExternalAccount(ctx context.Context, record postgresstore.ExternalAccountRecord) (postgresstore.ExternalAccountRecord, error) {
	if record.ExternalSubjectHMAC == "" && record.BindingMethod == "platform_password_login" {
		// external_accounts carries UNIQUE NULLS NOT DISTINCT
		// (source_instance_id, external_subject_hmac): at most ONE row per
		// source may hold a NULL subject HMAC. Platform-password bindings
		// have no OIDC provider subject, so the first such binding used up
		// that slot and the second real user's first login failed with
		// SQLSTATE 23505 (found by the RC55 production canary). Give each
		// platform binding a deterministic blind index in its own
		// namespace -- distinct from the projection's
		// "external-oidc/<source>" namespace so a password binding can
		// never collide with an OIDC-subject binding.
		subjectHMAC, err := s.keys.BlindIndex("external-platform/"+record.SourceInstanceID, record.ExternalUserID)
		if err != nil {
			return postgresstore.ExternalAccountRecord{}, fmt.Errorf("derive platform binding subject index: %w", err)
		}
		record.ExternalSubjectHMAC = subjectHMAC
	}
	saved, err := s.store.BindExternalAccount(ctx, record,
		auditActor(ctx, "user", record.PrincipalID, "external account binding verified"))
	if err != nil {
		return saved, err
	}
	if record.BindingMethod == "platform_password_login" {
		// Source facts for a never-bound platform user park as
		// parked_identity waiting on this account binding, with only a slow
		// time-based retry backstop. The projection pipeline fires this wake
		// when IT creates a binding (source_processor); a platform-password
		// login that creates the binding must fire the same wake, or the
		// user's parked usage/credit facts stay invisible until the backstop.
		if wakeErr := s.wakeDependency(ctx, "source_external_account", record.SourceInstanceID, record.ExternalUserID); wakeErr != nil {
			return saved, fmt.Errorf("wake parked source facts for new platform binding: %w", wakeErr)
		}
	}
	return saved, nil
}

// WakeSourceAccountFacts requeues source facts parked on this external
// account's binding (dependency kind source_external_account). The
// projection pipeline and a first-ever password bind fire this wake when
// they CREATE the binding, but an account bound before the wake existed --
// or whose login lands on the claim path, which never re-binds -- would
// otherwise leave its parked cutover/usage facts frozen forever, because
// parked_identity events are excluded from the periodic sweep by design.
// Firing it on every claim login is an idempotent single UPDATE.
func (s *Service) WakeSourceAccountFacts(ctx context.Context, sourceInstanceID, externalUserID string) error {
	return s.wakeDependency(ctx, "source_external_account", sourceInstanceID, externalUserID)
}

// ClaimPlatformIdentity backfills a pre-existing invoice_user's platform/
// platform_user_id columns for a platform-password login that landed on an
// identity a source-projection pipeline already created and bound (see
// postgresstore.Store.ClaimPlatformIdentity for the full rationale). The
// caller must compare the returned stored values against what it asked to
// claim -- a mismatch means this invoice_user already belongs to a different
// platform identity and the login must be rejected.
func (s *Service) ClaimPlatformIdentity(ctx context.Context, userID, platform, platformUserID string) (storedPlatform, storedPlatformUserID string, err error) {
	return s.store.ClaimPlatformIdentity(ctx, userID, platform, platformUserID,
		auditActor(ctx, "platform", userID, "platform password login claimed a pre-existing projected identity"))
}

func (s *Service) ListExternalAccounts(ctx context.Context, principalID string, platform domain.SourceType) ([]postgresstore.ConnectedSourceAccount, error) {
	return s.store.ListExternalAccounts(ctx, principalID, platform)
}

// GetExternalAccountBySourceUser is a read-only passthrough (see
// ListExternalAccounts above for the same pattern): platform-password login
// uses it to check whether a source-projection pipeline already bound this
// external account to an invoice_user before deciding whether to claim that
// existing identity or provision a new one.
func (s *Service) GetExternalAccountBySourceUser(ctx context.Context, sourceInstanceID, externalUserID string) (postgresstore.ExternalAccountRecord, error) {
	return s.store.GetExternalAccountBySourceUser(ctx, sourceInstanceID, externalUserID)
}

type FundingObservation struct {
	Lot                                                                      domain.FundingLot
	ExternalUserID                                                           string
	EventKind                                                                string
	ExternalEventID                                                          string
	SchemaVersion                                                            string
	Payload                                                                  []byte
	SourceCreatedAt                                                          time.Time
	SourceUpdatedAt                                                          time.Time
	SourceSequence                                                           int64
	EligibilityKind                                                          domain.EligibilityKind
	CashServiceUnits, WalletUnitCode, CutoverManifestHash, ConfigurationHash string
	CausalDomain, CausalOrder, SourceCursor, BatchID, ScanCycleID            string
	StreamWatermarkAt                                                        time.Time
}

type VerifiedSourceBatchEvent struct {
	EventID     string
	EntityType  string
	Operation   string
	PayloadHash string
	Payload     []byte
	ObservedAt  time.Time
}

type VerifiedSourceBatch struct {
	SchemaVersion        string
	SourceInstanceID     string
	StreamID             string
	BatchID              string
	Sequence             int64
	BodyHash             string
	PreviousBatchHash    string
	SigningKeyID         string
	SourceRuntimeVersion string
	SourceAgentVersion   string
	SourceCapturedAt     time.Time
	ProjectionStatus     string
	StreamWatermarkAt    time.Time
	SourceCursor         string
	ScanCeilingAt        time.Time
	ScanCeilingCursor    string
	ScanCycleID          string
	ScanComplete         bool
	ScanSnapshotID       string
	ScanSnapshotRowCount int64
	Events               []VerifiedSourceBatchEvent
}

type SourceBatchAck struct {
	Accepted         bool   `json:"accepted"`
	SourceInstanceID string `json:"source_instance_id"`
	StreamID         string `json:"stream_id"`
	BatchID          string `json:"batch_id"`
	Sequence         int64  `json:"sequence"`
	AcceptedRecords  int    `json:"accepted_records"`
	Duplicate        bool   `json:"duplicate"`
}

func (s *Service) AcceptSourceBatch(ctx context.Context, authenticatedSourceID string, batch VerifiedSourceBatch) (SourceBatchAck, error) {
	result, err := s.CommitVerifiedSourceBatch(ctx, authenticatedSourceID, batch)
	if err != nil {
		return SourceBatchAck{}, err
	}
	return SourceBatchAck{
		Accepted: true, SourceInstanceID: batch.SourceInstanceID, StreamID: batch.StreamID, BatchID: batch.BatchID,
		Sequence: result.Sequence, AcceptedRecords: result.AcceptedRecords, Duplicate: result.Duplicate,
	}, nil
}

// CommitVerifiedSourceBatch is downstream of the mTLS + Ed25519 verifier.
// authenticatedSourceID comes only from the client certificate mapping and
// must match the signed body before anything reaches PostgreSQL.
func (s *Service) CommitVerifiedSourceBatch(ctx context.Context, authenticatedSourceID string, batch VerifiedSourceBatch) (postgresstore.SourceBatchResult, error) {
	if strings.TrimSpace(authenticatedSourceID) == "" || authenticatedSourceID != batch.SourceInstanceID {
		return postgresstore.SourceBatchResult{}, domain.ErrForbidden
	}
	events := make([]postgresstore.SourceBatchEvent, len(batch.Events))
	for i, event := range batch.Events {
		ciphertext, err := s.keys.Encrypt(event.Payload,
			ingestEventAAD(batch.SourceInstanceID, batch.StreamID, event.EventID, event.PayloadHash))
		if err != nil {
			return postgresstore.SourceBatchResult{}, err
		}
		events[i] = postgresstore.SourceBatchEvent{
			EventID: event.EventID, EntityType: event.EntityType, Operation: event.Operation, PayloadHash: event.PayloadHash,
			PayloadCiphertext: ciphertext, ObservedAt: event.ObservedAt,
		}
	}
	return s.store.CommitSourceBatch(ctx, postgresstore.SourceBatchInput{
		SchemaVersion:    batch.SchemaVersion,
		SourceInstanceID: batch.SourceInstanceID, StreamID: batch.StreamID,
		BatchID: batch.BatchID, Sequence: batch.Sequence, BodyHash: batch.BodyHash,
		PreviousBatchHash: batch.PreviousBatchHash, SigningKeyID: batch.SigningKeyID,
		SourceRuntimeVersion: batch.SourceRuntimeVersion, SourceAgentVersion: batch.SourceAgentVersion,
		SourceCapturedAt: batch.SourceCapturedAt, ProjectionStatus: batch.ProjectionStatus,
		StreamWatermarkAt: batch.StreamWatermarkAt, SourceCursor: batch.SourceCursor,
		ScanCeilingAt: batch.ScanCeilingAt, ScanCeilingCursor: batch.ScanCeilingCursor,
		ScanCycleID: batch.ScanCycleID, ScanComplete: batch.ScanComplete,
		ScanSnapshotID: batch.ScanSnapshotID, ScanSnapshotRowCount: batch.ScanSnapshotRowCount,
		Events: events, Actor: auditActor(ctx, "source_connector", authenticatedSourceID, "verified source batch durably accepted"),
		// XM-INV-SCAN-CYCLE-SUPERSEDE: reuse the exact same activity-freshness
		// budget the readiness endpoint already grants an active rescan
		// (sourceFreshnessPolicy's EconomicRescanActivityMaxAge, derived in
		// backend/cmd/api/runtime.go) so "still looks like a live rescan"
		// answers the same question here (should a competing scan cycle id be
		// superseded) as it does there (should /readyz tolerate a
		// stale-looking watermark).
		Now: s.now(), StaleActiveScanCycleMaxAge: s.sourceEconomicRescanActivityMaxAge,
	})
}

func (s *Service) ObserveFundingLot(ctx context.Context, input FundingObservation) (postgresstore.ObservationResult, error) {
	var encrypted []byte
	var err error
	if len(input.Payload) > 0 {
		aad := sourceEventAAD(input.Lot.SourceInstanceID, input.EventKind, input.ExternalEventID, input.Lot.SourceRevision)
		encrypted, err = s.keys.Encrypt(input.Payload, aad)
		if err != nil {
			return postgresstore.ObservationResult{}, err
		}
	}
	return s.store.ObserveFundingLot(ctx, postgresstore.SourceObservation{
		Lot: input.Lot, ExternalUserID: input.ExternalUserID, EventKind: input.EventKind,
		ExternalEventID: input.ExternalEventID, SchemaVersion: input.SchemaVersion,
		PayloadCiphertext: encrypted, SourceCreatedAt: input.SourceCreatedAt,
		SourceUpdatedAt: input.SourceUpdatedAt, SourceSequence: input.SourceSequence,
		EligibilityKind: input.EligibilityKind, CashServiceUnits: input.CashServiceUnits,
		WalletUnitCode: input.WalletUnitCode, CutoverManifestHash: input.CutoverManifestHash,
		ConfigurationHash: input.ConfigurationHash, CausalDomain: input.CausalDomain,
		CausalOrder: input.CausalOrder, SourceCursor: input.SourceCursor,
		BatchID: input.BatchID, ScanCycleID: input.ScanCycleID, StreamWatermarkAt: input.StreamWatermarkAt,
	}, auditActor(ctx, "source_connector", input.Lot.SourceInstanceID, "signed source observation ingested"))
}

func (s *Service) SaveProfile(ctx context.Context, profile domain.InvoiceProfile) (domain.InvoiceProfile, error) {
	user, err := s.store.GetUser(ctx, profile.PrincipalID)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	if user.Status != "active" {
		return domain.InvoiceProfile{}, domain.ErrForbidden
	}
	requestedEmail, err := canonicalEmail(profile.Email)
	if err != nil {
		return domain.InvoiceProfile{}, ErrOIDCEmailUnverified
	}
	verified, err := s.IsEmailVerified(ctx, profile.PrincipalID, requestedEmail)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	if !verified {
		return domain.InvoiceProfile{}, ErrOIDCEmailUnverified
	}
	profile.Email = requestedEmail
	profile.EmailVerified = true
	if err = profile.Validate(); err != nil {
		return domain.InvoiceProfile{}, err
	}
	expectedRevision := profile.Revision
	if profile.ID == "" {
		profile.ID, err = randomUUID()
		if err != nil {
			return domain.InvoiceProfile{}, err
		}
		if expectedRevision != 0 {
			return domain.InvoiceProfile{}, domain.ErrVersionConflict
		}
	}
	record, err := s.encryptProfile(profile)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	record, err = s.store.SaveProfileCAS(ctx, record, true, expectedRevision,
		auditActor(ctx, "user", profile.PrincipalID, "invoice profile saved"))
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	profile.Revision = record.Revision
	profile.CreatedAt = record.CreatedAt
	profile.UpdatedAt = record.UpdatedAt
	return profile, nil
}

func (s *Service) ListProfiles(ctx context.Context, principalID string) ([]domain.InvoiceProfile, error) {
	records, err := s.store.ListProfileRecords(ctx, principalID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.InvoiceProfile, 0, len(records))
	for _, record := range records {
		profile, decryptErr := s.decryptProfile(record)
		if decryptErr != nil {
			return nil, decryptErr
		}
		out = append(out, profile)
	}
	return out, nil
}

func (s *Service) ListFundingLots(ctx context.Context, principalID string, platform domain.SourceType) ([]domain.FundingLot, error) {
	lots, err := s.store.ListFundingLots(ctx, principalID, platform)
	if err != nil {
		return nil, err
	}
	// Mock/unit services intentionally omit source freshness. Production
	// startup always supplies all three bounded ages and takes the fail-closed path
	// below; keeping the test-only configuration absent avoids fabricating five
	// healthy signed streams in non-source workflow tests.
	if s.sourceEconomicHeartbeatMaxAge <= 0 || s.sourceEconomicWatermarkMaxAge <= 0 || s.sourceIdentitiesMaxAge <= 0 {
		return lots, nil
	}
	report, err := s.store.SourceHealth(ctx, s.sourceFreshnessPolicy())
	if err != nil {
		return nil, fmt.Errorf("load source readiness for funding lots: %w", err)
	}
	ready := make(map[string]bool)
	seen := make(map[string]int)
	for _, stream := range report.Items {
		if _, exists := ready[stream.SourceInstanceID]; !exists {
			ready[stream.SourceInstanceID] = true
		}
		seen[stream.SourceInstanceID]++
		ready[stream.SourceInstanceID] = ready[stream.SourceInstanceID] && stream.Ready
	}
	for i := range lots {
		if seen[lots[i].SourceInstanceID] != 5 || !ready[lots[i].SourceInstanceID] {
			// This is a response-only status. It does not persist a financial
			// freeze, but it makes the list agree with Submit/Confirm's five-stream
			// freshness gate instead of presenting a stale amount as actionable.
			lots[i].EligibilityStatus = "source_unavailable"
		}
	}
	return lots, nil
}

func (s *Service) Submit(ctx context.Context, input ledger.SubmitInput, platform domain.SourceType) (domain.InvoiceRequest, error) {
	user, err := s.store.GetUser(ctx, input.PrincipalID)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	if user.Status != "active" {
		return domain.InvoiceRequest{}, domain.ErrForbidden
	}
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("load invoice policy: %w", err)
	}
	if settings.MinimumRequestMinor < domain.MinimumRequestMinor {
		return domain.InvoiceRequest{}, domain.ErrMinimumAmount
	}
	s.minimumRequestMinor.Store(settings.MinimumRequestMinor)
	profileRecord, err := s.store.GetProfile(ctx, input.PrincipalID, input.ProfileID)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	profile, err := s.decryptProfile(profileRecord)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	verified, err := s.IsEmailVerified(ctx, input.PrincipalID, profile.Email)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	if !profile.EmailVerified || !verified {
		return domain.InvoiceRequest{}, ErrOIDCEmailUnverified
	}
	snapshot := domain.SnapshotProfile(profile)
	body, err := json.Marshal(snapshot)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	encryptedSnapshot, err := s.keys.Encrypt(body, requestSnapshotAAD(input.PrincipalID, input.IdempotencyKey))
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	allocations := make([]postgresstore.AllocationInput, len(input.Allocations))
	for i, allocation := range input.Allocations {
		allocations[i] = postgresstore.AllocationInput{FundingLotID: allocation.FundingLotID, AmountMinor: allocation.AmountMinor}
	}
	request, err := s.store.Submit(ctx, postgresstore.SubmitInput{
		PrincipalID: input.PrincipalID, ProfileID: input.ProfileID,
		SourceInstanceID: input.SourceInstanceID, IdempotencyKey: input.IdempotencyKey,
		ProfileSnapshotCiphertext: encryptedSnapshot, IssuerCode: "default",
		Allocations: allocations, MinimumRequestMinor: settings.MinimumRequestMinor,
		Freshness: s.sourceFreshnessPolicy(), Platform: platform,
		Actor: auditActor(ctx, "user", input.PrincipalID, "invoice request submitted"),
	})
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	request.Profile = snapshot
	return request, nil
}

func (s *Service) Cancel(ctx context.Context, principalID, requestID string, expectedVersion int64, platform domain.SourceType) (domain.InvoiceRequest, error) {
	record, err := s.store.CancelRequest(ctx, principalID, requestID, expectedVersion, platform,
		auditActor(ctx, "user", principalID, "invoice request cancelled"))
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	return s.hydrateRequest(record)
}

func (s *Service) Review(ctx context.Context, adminID, requestID, action, note string, expectedVersion int64) (domain.InvoiceRequest, error) {
	record, err := s.store.ReviewRequest(ctx, adminID, requestID, action, note, expectedVersion,
		auditActor(ctx, "admin", adminID, "invoice request reviewed"))
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	return s.hydrateRequest(record)
}

func (s *Service) BeginManualIssue(ctx context.Context, adminID, requestID string, expectedVersion int64) (domain.InvoiceRequest, error) {
	record, err := s.store.BeginManualIssue(ctx, adminID, requestID, expectedVersion,
		auditActor(ctx, "admin", adminID, "manual issue started"))
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	return s.hydrateRequest(record)
}

type IssueSnapshot struct {
	IssuerName               string    `json:"issuer_name"`
	IssuerCode               string    `json:"issuer_code"`
	ServiceItem              string    `json:"service_item"`
	SettingsRevision         int64     `json:"settings_revision"`
	EligibilityStartAt       time.Time `json:"eligibility_start_at"`
	EligibilityPolicyVersion int64     `json:"eligibility_policy_version"`
	ConfirmedAt              time.Time `json:"confirmed_at"`
}

func (s *Service) ConfirmManualIssue(ctx context.Context, adminID, requestID string, expectedVersion int64) (domain.InvoiceRequest, error) {
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("load issuer settings: %w", err)
	}
	issuerName := strings.TrimSpace(settings.IssuerName)
	if !adminsettings.IsIssuerConfigured(issuerName) || settings.Revision <= 0 || settings.ServiceItem != domain.FixedServiceItem {
		return domain.InvoiceRequest{}, ErrIssuerNotConfigured
	}
	if settings.EligibilityStartAt.IsZero() || settings.EligibilityPolicyVersion <= 0 {
		return domain.InvoiceRequest{}, domain.ErrInvalidState
	}
	snapshot := IssueSnapshot{
		IssuerName: issuerName, IssuerCode: "default", ServiceItem: domain.FixedServiceItem,
		SettingsRevision: settings.Revision, EligibilityStartAt: settings.EligibilityStartAt,
		EligibilityPolicyVersion: settings.EligibilityPolicyVersion, ConfirmedAt: s.now(),
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	ciphertext, err := s.keys.Encrypt(body, issueSnapshotAAD(requestID))
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	record, err := s.store.ConfirmManualIssue(ctx, postgresstore.ConfirmIssueInput{
		AdminID: adminID, RequestID: requestID, ExpectedVersion: expectedVersion,
		IssuerSettingRevision: settings.Revision, IssueSnapshotCiphertext: ciphertext,
		Freshness: s.sourceFreshnessPolicy(),
		Actor:     auditActor(ctx, "admin", adminID, "manual issue confirmed with immutable issuer snapshot"),
	})
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	return s.hydrateRequest(record)
}

func (s *Service) GetIssueSnapshot(ctx context.Context, adminID, requestID string) (IssueSnapshot, error) {
	record, err := s.store.GetRequestRecord(ctx, "", requestID, true, "")
	if err != nil {
		return IssueSnapshot{}, err
	}
	if record.IssuerSettingRevision <= 0 || len(record.IssueSnapshotCiphertext) == 0 {
		return IssueSnapshot{}, domain.ErrNotFound
	}
	body, err := s.keys.Decrypt(record.IssueSnapshotCiphertext, issueSnapshotAAD(requestID))
	if err != nil {
		return IssueSnapshot{}, err
	}
	var snapshot IssueSnapshot
	if err = json.Unmarshal(body, &snapshot); err != nil {
		return IssueSnapshot{}, err
	}
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return IssueSnapshot{}, fmt.Errorf("load eligibility policy settings: %w", err)
	}
	if err = validateIssueSnapshot(snapshot, record.IssuerSettingRevision, settings); err != nil {
		return IssueSnapshot{}, err
	}
	_ = adminID // authorization is enforced by the calling admin edge.
	return snapshot, nil
}

func validateIssueSnapshot(snapshot IssueSnapshot, storedRevision int64, settings adminsettings.Settings) error {
	if !adminsettings.IsIssuerConfigured(snapshot.IssuerName) {
		return ErrIssuerNotConfigured
	}
	if snapshot.SettingsRevision != storedRevision || snapshot.ServiceItem != domain.FixedServiceItem ||
		!snapshot.EligibilityStartAt.UTC().Equal(settings.EligibilityStartAt.UTC()) ||
		snapshot.EligibilityPolicyVersion <= 0 ||
		snapshot.EligibilityPolicyVersion != settings.EligibilityPolicyVersion {
		return domain.ErrConflict
	}
	return nil
}

func (s *Service) AttachDocument(ctx context.Context, adminID string, document domain.InvoiceDocument, expectedVersion int64) (domain.InvoiceRequest, domain.InvoiceDocument, domain.EmailOutbox, error) {
	record, err := s.store.GetRequestRecord(ctx, "", document.RequestID, true, "")
	if err != nil {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	request, err := s.hydrateRequest(record)
	if err != nil {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	verified, err := s.IsEmailVerified(ctx, request.PrincipalID, request.Profile.Email)
	if err != nil {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	if !verified {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, ErrOIDCEmailUnverified
	}
	recipientHash, err := s.keys.BlindIndex("invoice-delivery-email", request.Profile.Email)
	if err != nil {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	actor := auditActor(ctx, "admin", adminID, "issued PDF attached")
	actor.ID = adminID
	record, saved, outbox, err := s.store.AttachDocument(ctx, postgresstore.AttachDocumentInput{
		Document: document, ExpectedVersion: expectedVersion, RecipientHash: recipientHash,
		TemplateVersion: s.emailTemplateVersion, Actor: actor,
	})
	if err != nil {
		return domain.InvoiceRequest{}, domain.InvoiceDocument{}, domain.EmailOutbox{}, err
	}
	request, err = s.hydrateRequest(record)
	return request, saved, outbox, err
}

func (s *Service) GetDocumentForRequest(ctx context.Context, principalID, requestID string, platform domain.SourceType) (domain.InvoiceDocument, error) {
	return s.store.GetDocumentForRequest(ctx, principalID, requestID, platform)
}

func (s *Service) GetDocumentForRequestAsAdmin(ctx context.Context, requestID string) (domain.InvoiceDocument, error) {
	return s.store.GetDocumentForRequestAsAdmin(ctx, requestID)
}

type InvoiceDeliveryStatus struct {
	DocumentID    string    `json:"document_id"`
	InvoiceNumber string    `json:"invoice_number"`
	IssuedAt      time.Time `json:"issued_at"`
	SizeBytes     int64     `json:"size_bytes"`
	MIME          string    `json:"mime"`
	EmailStatus   string    `json:"email_status"`
	EmailAttempts int       `json:"email_attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
}

func (s *Service) GetInvoiceDeliveryState(ctx context.Context, principalID, requestID string, admin bool, platform domain.SourceType) (postgresstore.InvoiceDeliveryState, error) {
	return s.store.GetInvoiceDeliveryState(ctx, principalID, requestID, admin, platform)
}

// GetInvoiceDeliveryStatus is not reachable from any HTTP handler (unlike
// GetInvoiceDeliveryState, it is not part of OperationsService); unscoped
// (XM-INV-PLATFORM-SCOPE does not apply -- nothing calls this with a
// platform-scoped session).
func (s *Service) GetInvoiceDeliveryStatus(ctx context.Context, principalID, requestID string, admin bool) (InvoiceDeliveryStatus, error) {
	state, err := s.store.GetInvoiceDeliveryState(ctx, principalID, requestID, admin, "")
	if err != nil {
		return InvoiceDeliveryStatus{}, err
	}
	return InvoiceDeliveryStatus{
		DocumentID: state.Document.ID, InvoiceNumber: state.Document.InvoiceNumber,
		IssuedAt: state.Document.IssuedAt, SizeBytes: state.Document.SizeBytes,
		MIME: state.Document.MIME, EmailStatus: state.Outbox.Status,
		EmailAttempts: state.Outbox.Attempts, NextAttemptAt: state.Outbox.NextAttemptAt,
	}, nil
}

func (s *Service) RequeueEmail(ctx context.Context, requestID, adminID, reason string) (domain.EmailOutbox, error) {
	record, err := s.store.GetRequestRecord(ctx, "", requestID, true, "")
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	request, err := s.hydrateRequest(record)
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	verified, err := s.IsEmailVerified(ctx, request.PrincipalID, request.Profile.Email)
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	if !verified {
		return domain.EmailOutbox{}, ErrOIDCEmailUnverified
	}
	return s.store.RequeueEmail(ctx, requestID, adminID, reason,
		auditActor(ctx, "admin", adminID, "invoice email manually requeued"))
}

// RecordAdminAudit stores only a bounded action/object and coarse outcome.
// Callers must never pass an email address, SMTP error text or credential.
func (s *Service) RecordAdminAudit(ctx context.Context, adminID, action, objectID, requestID, outcome string) error {
	return s.store.RecordAdminOperationalAudit(ctx, adminID, action, objectID, requestID, outcome)
}

func (s *Service) ListRequests(ctx context.Context, principalID string, admin bool, platform domain.SourceType) ([]domain.InvoiceRequest, error) {
	limit := 200
	if admin {
		limit = 100
	}
	records, err := s.store.ListRequestRecords(ctx, principalID, admin, limit, platform)
	if err != nil {
		return nil, err
	}
	out := make([]domain.InvoiceRequest, 0, len(records))
	for _, record := range records {
		request, hydrateErr := s.hydrateRequest(record)
		if hydrateErr != nil {
			return nil, hydrateErr
		}
		out = append(out, request)
	}
	return out, nil
}

func (s *Service) GetRequest(ctx context.Context, principalID, requestID string, admin bool, platform domain.SourceType) (domain.InvoiceRequest, error) {
	record, err := s.store.GetRequestRecord(ctx, principalID, requestID, admin, platform)
	if err != nil {
		return domain.InvoiceRequest{}, err
	}
	return s.hydrateRequest(record)
}

type RequestPageQuery struct {
	PrincipalID       string
	Admin             bool
	Limit             int
	BeforeSubmittedAt time.Time
	BeforeID          string
	Statuses          []domain.RequestStatus
	SourceInstanceID  string
	// Platform scopes the page to one platform's requests (XM-INV-PLATFORM-SCOPE).
	// Empty means unscoped; always sourced from the session, never from an
	// admin-supplied filter (admin sessions never carry a platform).
	Platform domain.SourceType
}

type RequestPage struct {
	Items                 []domain.InvoiceRequest
	HasMore               bool
	NextBeforeSubmittedAt time.Time
	NextBeforeID          string
}

func (s *Service) ListRequestsPage(ctx context.Context, in RequestPageQuery) (RequestPage, error) {
	page, err := s.store.ListRequestRecordsPage(ctx, postgresstore.RequestPageQuery{
		PrincipalID: in.PrincipalID, Admin: in.Admin, Limit: in.Limit,
		BeforeSubmittedAt: in.BeforeSubmittedAt, BeforeID: in.BeforeID,
		Statuses: in.Statuses, SourceInstanceID: in.SourceInstanceID,
		Platform: in.Platform,
	})
	if err != nil {
		return RequestPage{}, err
	}
	out := RequestPage{
		Items: make([]domain.InvoiceRequest, 0, len(page.Items)), HasMore: page.HasMore,
		NextBeforeSubmittedAt: page.NextBeforeSubmittedAt, NextBeforeID: page.NextBeforeID,
	}
	for _, record := range page.Items {
		request, hydrateErr := s.hydrateRequest(record)
		if hydrateErr != nil {
			return RequestPage{}, hydrateErr
		}
		out.Items = append(out.Items, request)
	}
	return out, nil
}

// VerifyNewAPIPayment deliberately requires the evidence-bearing method below.
// Keeping the unsafe legacy signature from silently verifying money prevents a
// future interface adapter from dropping the evidence reference again.
func (s *Service) VerifyNewAPIPayment(context.Context, string, int64, string) (domain.FundingLot, error) {
	return domain.FundingLot{}, ErrEvidenceRequired
}

func (s *Service) VerifyNewAPIPaymentWithEvidence(ctx context.Context, adminID, lotID string, paidMinor int64, currency, evidenceReference string) (domain.FundingLot, error) {
	if strings.TrimSpace(evidenceReference) == "" {
		return domain.FundingLot{}, ErrEvidenceRequired
	}
	return s.reviewNewAPIPayment(ctx, adminID, lotID, "verify", paidMinor, currency, evidenceReference, "管理员已独立核验支付凭证")
}

// ApplyNewAPIManualCap records manual refund/freeze evidence and atomically
// lowers a previously verified New API lot. The shared funding reduction
// transaction releases unissued reservations and opens refund/red-letter cases
// for issued exposure. Any old dual-review decision is invalidated; restoring
// a non-zero net amount requires a fresh two-person review below this ceiling.
func (s *Service) ApplyNewAPIManualCap(ctx context.Context, adminID, lotID string, newCapMinor int64, evidenceReference, reason string) (domain.FundingLot, error) {
	adminID = strings.TrimSpace(adminID)
	evidenceReference = strings.TrimSpace(evidenceReference)
	reason = strings.TrimSpace(reason)
	if adminID == "" || lotID == "" || newCapMinor < 0 || evidenceReference == "" || reason == "" ||
		len(evidenceReference) > 2048 || len(reason) > 500 ||
		strings.ContainsAny(evidenceReference, "\r\n\x00") || strings.ContainsAny(reason, "\x00") {
		return domain.FundingLot{}, ErrEvidenceRequired
	}
	lot, err := s.store.GetFundingLot(ctx, lotID)
	if err != nil {
		return domain.FundingLot{}, err
	}
	if lot.SourceType != domain.SourceNewAPI ||
		(lot.Verification != domain.VerificationVerified && lot.Verification != domain.VerificationFrozen) ||
		newCapMinor > lot.CurrentCapMinor {
		return domain.FundingLot{}, domain.ErrInvalidState
	}
	if lot.PrincipalID == adminID {
		return domain.FundingLot{}, domain.ErrForbidden
	}
	externalUserID, err := s.store.ExternalUserIDForFundingLot(ctx, lotID)
	if err != nil {
		return domain.FundingLot{}, err
	}
	tuple := fmt.Sprintf("%s\n%d\n%s\n%s", lotID, newCapMinor, evidenceReference, reason)
	sum := sha256.Sum256([]byte(tuple))
	tupleHash := hex.EncodeToString(sum[:])
	payload, err := json.Marshal(map[string]any{
		"new_cap_minor": newCapMinor, "evidence_reference": evidenceReference,
		"reason": reason, "recorded_by": adminID,
	})
	if err != nil {
		return domain.FundingLot{}, err
	}
	revision := "manual-newapi-cap:" + tupleHash
	externalEventID := "manual-newapi-cap:" + tupleHash
	ciphertext, err := s.keys.Encrypt(payload,
		sourceEventAAD(lot.SourceInstanceID, "refund", externalEventID, revision))
	if err != nil {
		return domain.FundingLot{}, err
	}
	now := s.now().UTC()
	lot.CurrentCapMinor = newCapMinor
	lot.Verification = domain.VerificationFrozen
	lot.SourceStatus = "MANUAL_REFUND_OR_FREEZE"
	lot.SourceRevision = revision
	lot.ObservedAt = now
	lot.UpdatedAt = now
	result, err := s.store.ObserveFundingLot(ctx, postgresstore.SourceObservation{
		Lot: lot, ExternalUserID: externalUserID, EventKind: "refund",
		ExternalEventID: externalEventID, SchemaVersion: "manual-newapi-cap-v1",
		PayloadCiphertext: ciphertext, SourceUpdatedAt: now,
		NewAPIManualCeilingMinor: &newCapMinor,
	}, auditActor(ctx, "admin", adminID, "New API manual cap evidence sha256:"+tupleHash))
	if err != nil {
		return domain.FundingLot{}, err
	}
	return result.Lot, nil
}

func (s *Service) RejectNewAPIPaymentWithEvidence(ctx context.Context, adminID, lotID, evidenceReference, reason string) (domain.FundingLot, error) {
	return s.reviewNewAPIPayment(ctx, adminID, lotID, "reject", 0, "", evidenceReference, reason)
}

func (s *Service) FreezeNewAPIPaymentWithEvidence(ctx context.Context, adminID, lotID, evidenceReference, reason string) (domain.FundingLot, error) {
	return s.reviewNewAPIPayment(ctx, adminID, lotID, "freeze", 0, "", evidenceReference, reason)
}

func (s *Service) reviewNewAPIPayment(ctx context.Context, adminID, lotID, action string, paidMinor int64, currency, evidenceReference, reason string) (domain.FundingLot, error) {
	evidenceReference = strings.TrimSpace(evidenceReference)
	reason = strings.TrimSpace(reason)
	if evidenceReference == "" || len(evidenceReference) > 2048 || strings.ContainsAny(evidenceReference, "\r\n\x00") || strings.TrimSpace(adminID) == "" {
		return domain.FundingLot{}, ErrEvidenceRequired
	}
	if reason == "" || len(reason) > 500 || strings.ContainsAny(reason, "\r\n\x00") {
		return domain.FundingLot{}, errors.New("bounded payment review reason is required")
	}
	sum := sha256.Sum256([]byte(evidenceReference))
	hash := hex.EncodeToString(sum[:])
	ciphertext, err := s.keys.Encrypt([]byte(evidenceReference), paymentEvidenceAAD(lotID, action, hash))
	if err != nil {
		return domain.FundingLot{}, err
	}
	reasonCiphertext, err := s.keys.Encrypt([]byte(reason), paymentReviewReasonAAD(lotID, action, hash))
	if err != nil {
		return domain.FundingLot{}, err
	}
	reasonSum := sha256.Sum256([]byte(reason))
	return s.store.ReviewNewAPIPaymentCandidate(ctx, postgresstore.ReviewPaymentCandidateInput{
		LotID: lotID, Action: action, PaidMinor: paidMinor, Currency: currency,
		Evidence:         postgresstore.PaymentEvidence{Hash: hash, Ciphertext: ciphertext},
		ReasonCiphertext: reasonCiphertext,
		ReasonHash:       hex.EncodeToString(reasonSum[:]),
		Actor:            auditActor(ctx, "admin", adminID, "New API payment candidate manually reviewed"),
	})
}

func (s *Service) ListPaymentCandidatesPage(ctx context.Context, in postgresstore.PaymentCandidatePageQuery) (postgresstore.PaymentCandidatePage, error) {
	return s.store.ListPaymentCandidatesPage(ctx, in)
}

func (s *Service) ListRefundCasesPage(ctx context.Context, in postgresstore.RefundCasePageQuery) (postgresstore.RefundCasePage, error) {
	return s.store.ListRefundCasesPage(ctx, in)
}

func (s *Service) ListEligibilityFreezesPage(ctx context.Context, in postgresstore.EligibilityFreezePageQuery) (postgresstore.EligibilityFreezePage, error) {
	return s.store.ListEligibilityFreezesPage(ctx, in)
}

// ListEligibilityLedgerPage: XM-INV-USER-LEDGER-QUERY (design section 3(E)),
// a plain passthrough to the store -- see postgresstore.eligibility_ledger.go
// for the read-only query itself.
func (s *Service) ListEligibilityLedgerPage(ctx context.Context, in postgresstore.EligibilityLedgerPageQuery) (postgresstore.EligibilityLedgerPage, error) {
	return s.store.ListEligibilityLedgerPage(ctx, in)
}

func (s *Service) ResolveEligibilityFreeze(ctx context.Context, adminID, freezeID string, expectedVersion int64, evidenceReference, note string) (postgresstore.EligibilityFreeze, error) {
	adminID = strings.TrimSpace(adminID)
	freezeID = strings.TrimSpace(freezeID)
	evidenceReference = strings.TrimSpace(evidenceReference)
	note = strings.TrimSpace(note)
	if adminID == "" || freezeID == "" || expectedVersion <= 0 || evidenceReference == "" || note == "" || len(evidenceReference) > 2048 || len(note) > 2000 || strings.ContainsAny(evidenceReference, "\r\n\x00") || strings.ContainsAny(note, "\x00") {
		return postgresstore.EligibilityFreeze{}, errors.New("administrator, version, bounded evidence reference and resolution note are required")
	}
	evidenceSum := sha256.Sum256([]byte(evidenceReference))
	evidenceHash := hex.EncodeToString(evidenceSum[:])
	noteSum := sha256.Sum256([]byte(note))
	noteHash := hex.EncodeToString(noteSum[:])
	evidenceCiphertext, err := s.keys.Encrypt([]byte(evidenceReference), eligibilityFreezeEvidenceAAD(freezeID, evidenceHash))
	if err != nil {
		return postgresstore.EligibilityFreeze{}, err
	}
	noteCiphertext, err := s.keys.Encrypt([]byte(note), eligibilityFreezeNoteAAD(freezeID, evidenceHash))
	if err != nil {
		return postgresstore.EligibilityFreeze{}, err
	}
	return s.store.ResolveEligibilityFreeze(ctx, postgresstore.ResolveEligibilityFreezeInput{FreezeID: freezeID, ExpectedVersion: expectedVersion,
		EvidenceHash: evidenceHash, EvidenceCiphertext: evidenceCiphertext, NoteHash: noteHash, NoteCiphertext: noteCiphertext,
		FreshnessPolicy: s.sourceFreshnessPolicy(), Actor: auditActor(ctx, "admin", adminID, "eligibility freeze safely resolved")})
}

type ServiceUnitSummary struct {
	ServiceUnits string `json:"service_units"`
	UnitCode     string `json:"unit_code"`
}

type UserEligibilitySummary struct {
	SourceInstanceID  string             `json:"source_instance_id"`
	SourceType        domain.SourceType  `json:"source_type"`
	SourceName        string             `json:"source_name"`
	BindingStatus     string             `json:"binding_status"`
	EligibilityStatus string             `json:"status"`
	Currency          string             `json:"currency"`
	AvailableMinor    int64              `json:"available_minor"`
	ConsumedMinor     int64              `json:"consumed_minor"`
	UnconsumedMinor   int64              `json:"unconsumed_minor"`
	ReservedMinor     int64              `json:"reserved_minor"`
	IssuedMinor       int64              `json:"issued_minor"`
	Legacy            ServiceUnitSummary `json:"legacy_noninvoiceable"`
	NonCash           ServiceUnitSummary `json:"noncash"`
	Reasons           []string           `json:"reasons"`
}

func (s *Service) ListUserEligibilitySummaries(ctx context.Context, principalID string, platform domain.SourceType) ([]UserEligibilitySummary, error) {
	base, err := s.store.ListEligibilitySummaries(ctx, strings.TrimSpace(principalID), platform)
	if err != nil {
		return nil, err
	}
	report, err := s.store.SourceHealth(ctx, s.sourceFreshnessPolicy())
	if err != nil {
		return nil, err
	}
	ready := map[string]bool{}
	seen := map[string]int{}
	for _, stream := range report.Items {
		if _, ok := ready[stream.SourceInstanceID]; !ok {
			ready[stream.SourceInstanceID] = true
		}
		seen[stream.SourceInstanceID]++
		ready[stream.SourceInstanceID] = ready[stream.SourceInstanceID] && stream.Ready
	}
	out := make([]UserEligibilitySummary, 0, len(base))
	for _, item := range base {
		reasons := []string{}
		blocked := false
		if item.BindingStatus != "verified" {
			reasons = append(reasons, "BINDING_NOT_VERIFIED")
			blocked = true
		}
		if item.HasOpenFreeze || item.EligibilityStatus == "frozen" {
			reasons = append(reasons, "ACCOUNT_FROZEN")
			blocked = true
		}
		if item.EligibilityStatus == "not_invoiceable_pending_reconciliation" {
			// XM-INV-ELIG-AUTO-RECONCILE: distinct from ACCOUNT_FROZEN -- no
			// admin queue entry exists for this account, and it clears
			// itself automatically once the ledger reconciles.
			reasons = append(reasons, "PENDING_RECONCILIATION")
			blocked = true
		}
		if item.ProjectionPending || item.EligibilityStatus == "syncing" {
			reasons = append(reasons, "PROJECTION_PENDING")
			blocked = true
		}
		if seen[item.SourceInstanceID] != 5 || !ready[item.SourceInstanceID] {
			reasons = append(reasons, "SOURCE_NOT_READY")
			blocked = true
		}
		available := item.AvailableMinor
		if blocked {
			available = 0
		}
		if !blocked && available == 0 {
			reasons = append(reasons, "NO_CONSUMED_CASH")
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "READY")
		}
		out = append(out, UserEligibilitySummary{SourceInstanceID: item.SourceInstanceID, SourceType: item.SourceType, SourceName: item.SourceName,
			BindingStatus: item.BindingStatus, EligibilityStatus: item.EligibilityStatus, Currency: "CNY", AvailableMinor: available,
			ConsumedMinor: item.ConsumedMinor, UnconsumedMinor: item.UnconsumedMinor, ReservedMinor: item.ReservedMinor, IssuedMinor: item.IssuedMinor,
			Legacy: ServiceUnitSummary{ServiceUnits: item.LegacyServiceUnits, UnitCode: item.UnitCode}, NonCash: ServiceUnitSummary{ServiceUnits: item.NonCashServiceUnits, UnitCode: item.UnitCode}, Reasons: reasons})
	}
	return out, nil
}

func (s *Service) ResolveRefundCase(ctx context.Context, adminID, caseID, resolutionStatus, evidenceReference, note string) (postgresstore.RefundCase, error) {
	evidenceReference = strings.TrimSpace(evidenceReference)
	note = strings.TrimSpace(note)
	if strings.TrimSpace(adminID) == "" || evidenceReference == "" || note == "" || len(evidenceReference) > 2048 || len(note) > 2000 || strings.ContainsAny(note, "\x00") {
		return postgresstore.RefundCase{}, errors.New("administrator, bounded evidence reference and resolution note are required")
	}
	sum := sha256.Sum256([]byte(evidenceReference))
	hash := hex.EncodeToString(sum[:])
	evidenceCiphertext, err := s.keys.Encrypt([]byte(evidenceReference), refundEvidenceAAD(caseID, resolutionStatus, hash))
	if err != nil {
		return postgresstore.RefundCase{}, err
	}
	noteCiphertext, err := s.keys.Encrypt([]byte(note), refundNoteAAD(caseID, resolutionStatus, hash))
	if err != nil {
		return postgresstore.RefundCase{}, err
	}
	noteSum := sha256.Sum256([]byte(note))
	return s.store.ResolveRefundCase(ctx, postgresstore.ResolveRefundCaseInput{
		CaseID: caseID, ResolutionStatus: resolutionStatus, EvidenceHash: hash,
		EvidenceCiphertext: evidenceCiphertext, NoteCiphertext: noteCiphertext,
		NoteHash: hex.EncodeToString(noteSum[:]),
		Actor:    auditActor(ctx, "admin", adminID, "refund case manually resolved"),
	})
}

type RefundResolution struct {
	Case              postgresstore.RefundCase `json:"case"`
	EvidenceReference string                   `json:"evidence_reference"`
	Note              string                   `json:"note"`
}

func (s *Service) GetRefundCaseResolution(ctx context.Context, caseID string) (RefundResolution, error) {
	record, err := s.store.GetRefundCaseResolution(ctx, caseID)
	if err != nil {
		return RefundResolution{}, err
	}
	evidence, err := s.keys.Decrypt(record.EvidenceCiphertext,
		refundEvidenceAAD(record.Case.ID, record.Case.Status, record.Case.ResolutionEvidenceHash))
	if err != nil {
		return RefundResolution{}, err
	}
	note, err := s.keys.Decrypt(record.NoteCiphertext,
		refundNoteAAD(record.Case.ID, record.Case.Status, record.Case.ResolutionEvidenceHash))
	if err != nil {
		return RefundResolution{}, err
	}
	return RefundResolution{Case: record.Case, EvidenceReference: string(evidence), Note: string(note)}, nil
}

func (s *Service) hydrateRequest(record postgresstore.RequestRecord) (domain.InvoiceRequest, error) {
	body, err := s.keys.Decrypt(record.ProfileSnapshotCiphertext,
		requestSnapshotAAD(record.Request.PrincipalID, record.IdempotencyKey))
	if err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("decrypt invoice profile snapshot: %w", err)
	}
	var snapshot domain.ProfileSnapshot
	if err = json.Unmarshal(body, &snapshot); err != nil {
		return domain.InvoiceRequest{}, fmt.Errorf("decode invoice profile snapshot: %w", err)
	}
	record.Request.Profile = snapshot
	return record.Request, nil
}

func (s *Service) Claim(ctx context.Context, limit int, now time.Time) ([]mailer.Message, error) {
	claims, err := s.store.ClaimOutbox(ctx, limit, now)
	if err != nil {
		return nil, err
	}
	out := make([]mailer.Message, 0, len(claims))
	for _, claim := range claims {
		message := mailer.Message{
			ID: claim.ClaimID, RequestNo: claim.RequestNo, CreatedAt: claim.CreatedAt,
			DownloadURL: s.downloadURL(claim.RequestID),
		}
		body, decryptErr := s.keys.Decrypt(claim.ProfileSnapshotCiphertext,
			requestSnapshotAAD(claim.PrincipalID, claim.IdempotencyKey))
		if decryptErr == nil {
			var snapshot domain.ProfileSnapshot
			if json.Unmarshal(body, &snapshot) == nil {
				verified, verifyErr := s.IsEmailVerified(ctx, claim.PrincipalID, snapshot.Email)
				if verifyErr != nil {
					return nil, verifyErr
				}
				if verified {
					message.Recipient = snapshot.Email
				}
			}
		}
		out = append(out, message)
	}
	return out, nil
}

func (s *Service) MarkSent(ctx context.Context, claimID, providerMessageID string, now time.Time) error {
	return s.store.MarkOutboxSent(ctx, claimID, providerMessageID, now)
}

func (s *Service) MarkFailed(ctx context.Context, claimID, errorCode string, nextAttemptAt time.Time) error {
	return s.store.MarkOutboxFailed(ctx, claimID, errorCode, nextAttemptAt)
}

func (s *Service) downloadURL(requestID string) string {
	copy := *s.publicBaseURL
	copy.Path = path.Join("/", "records")
	copy.RawQuery = url.Values{"request_id": []string{requestID}}.Encode()
	return copy.String()
}

func canonicalEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(address.Address, value) {
		return "", errors.New("email must be a plain mailbox address")
	}
	return strings.ToLower(address.Address), nil
}

func randomUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
