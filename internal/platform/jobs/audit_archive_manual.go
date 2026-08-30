package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Audit archive runtime wiring is intentionally manual-only for now. R2-10
// cluster ownership and the DB role split are not yet proven on release, so no
// archive job is registered in NewClient and this kind is not part of the
// periodic JobManifest. A future activation must change this contract and add
// the two independent gate proofs before introducing a scheduler.
type AuditArchiveMode string

const (
	AuditArchiveModeDisabled  AuditArchiveMode = "disabled"
	AuditArchiveModeManual    AuditArchiveMode = "manual"
	AuditArchiveModeScheduled AuditArchiveMode = "scheduled"
)

const (
	AuditArchiveJobKind = "audit_archive_manual"

	// Environment variable names are exported so cmd/platform-worker and
	// contract tests share one spelling. Values are always policy/refs, never
	// secret material.
	AuditArchiveEnabledEnv                    = "XM_AUDIT_ARCHIVE_ENABLED"
	AuditArchiveModeEnv                       = "XM_AUDIT_ARCHIVE_MODE"
	AuditArchiveSchedulerEnabledEnv           = "XM_AUDIT_ARCHIVE_SCHEDULER_ENABLED"
	AuditArchiveEndpointEnv                   = "XM_AUDIT_ARCHIVE_ENDPOINT"
	AuditArchiveEndpointAllowlistEnv          = "XM_AUDIT_ARCHIVE_ENDPOINT_ALLOWLIST"
	AuditArchiveBucketEnv                     = "XM_AUDIT_ARCHIVE_BUCKET"
	AuditArchiveRegionEnv                     = "XM_AUDIT_ARCHIVE_REGION"
	AuditArchiveEncryptionEnv                 = "XM_AUDIT_ARCHIVE_ENCRYPTION"
	AuditArchiveObjectLockEnv                 = "XM_AUDIT_ARCHIVE_OBJECT_LOCK"
	AuditArchiveCredentialRefEnv              = "XM_AUDIT_ARCHIVE_CREDENTIAL_REF"
	AuditArchiveKMSCredentialRefEnv           = "XM_AUDIT_ARCHIVE_KMS_CREDENTIAL_REF"
	AuditArchiveQualificationCredentialRefEnv = "XM_AUDIT_ARCHIVE_QUALIFICATION_CREDENTIAL_REF"
	AuditArchiveSecuritySinkCredentialRefEnv  = "XM_AUDIT_ARCHIVE_SECURITY_SINK_CREDENTIAL_REF"

	DefaultAuditArchiveRegion        = "local"
	DefaultAuditArchiveProvider      = "minio"
	DefaultAuditArchiveEncryption    = "SSE-S3"
	DefaultAuditArchiveObjectLock    = "COMPLIANCE"
	DefaultAuditArchiveRetentionDays = 3650
	// Deprecated singular alias kept for callers that mirror the old plan prose.
	DefaultAuditArchiveRetentionDay = DefaultAuditArchiveRetentionDays
	auditArchiveMaxAttempts         = 3
)

var (
	ErrAuditArchiveConfig            = errors.New("audit archive configuration invalid")
	ErrAuditArchiveSchedulerGated    = errors.New("audit archive scheduler requires R2-10 and DB role gates")
	ErrAuditArchiveProductionGated   = errors.New("audit archive production activation requires lifecycle approval")
	ErrAuditArchiveDisabled          = errors.New("audit archive is disabled")
	ErrAuditArchiveRunnerUnavailable = errors.New("audit archive manual runner unavailable")
	ErrAuditArchiveRequestInvalid    = errors.New("audit archive manual request invalid")
)

// AuditArchiveConfig contains non-secret runtime policy. CredentialRef values
// are references only; the worker never resolves or stores credential material.
// The default is disabled and cannot be changed by omission of a field.
type AuditArchiveConfig struct {
	Enabled           bool
	Mode              AuditArchiveMode
	SchedulerEnabled  bool
	Environment       string
	Provider          string
	Endpoint          string
	EndpointAllowlist []string
	BucketID          string
	Region            string
	EncryptionMode    string
	ObjectLockMode    string
	RetentionDays     int

	// CredentialRef is the object-writer access reference. KMSCredentialRef is
	// the independent MinIO KMS secret reference. Both are names, never values.
	CredentialRef              string
	KMSCredentialRef           string
	QualificationCredentialRef string
	SecuritySinkCredentialRef  string
}

// DefaultAuditArchiveConfig returns a safe, inactive configuration. Policy
// values are still pinned so a manual fixture cannot silently choose a weaker
// provider policy when it is explicitly enabled.
func DefaultAuditArchiveConfig() AuditArchiveConfig {
	return AuditArchiveConfig{
		Mode:           AuditArchiveModeDisabled,
		Provider:       DefaultAuditArchiveProvider,
		Region:         DefaultAuditArchiveRegion,
		EncryptionMode: DefaultAuditArchiveEncryption,
		ObjectLockMode: DefaultAuditArchiveObjectLock,
		RetentionDays:  DefaultAuditArchiveRetentionDay,
	}
}

func ParseAuditArchiveMode(raw string) (AuditArchiveMode, error) {
	mode := AuditArchiveMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		return AuditArchiveModeDisabled, nil
	}
	switch mode {
	case AuditArchiveModeDisabled, AuditArchiveModeManual, AuditArchiveModeScheduled:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: unsupported mode %q", ErrAuditArchiveConfig, mode)
	}
}

func (c AuditArchiveConfig) normalized() AuditArchiveConfig {
	defaults := DefaultAuditArchiveConfig()
	if strings.TrimSpace(string(c.Mode)) == "" {
		c.Mode = defaults.Mode
	}
	c.Mode = AuditArchiveMode(strings.ToLower(strings.TrimSpace(string(c.Mode))))
	if strings.TrimSpace(c.Provider) == "" {
		c.Provider = defaults.Provider
	}
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if strings.TrimSpace(c.Region) == "" {
		c.Region = defaults.Region
	}
	c.Region = strings.TrimSpace(c.Region)
	if strings.TrimSpace(c.EncryptionMode) == "" {
		c.EncryptionMode = defaults.EncryptionMode
	}
	c.EncryptionMode = strings.ToUpper(strings.TrimSpace(c.EncryptionMode))
	if strings.TrimSpace(c.ObjectLockMode) == "" {
		c.ObjectLockMode = defaults.ObjectLockMode
	}
	c.ObjectLockMode = strings.ToUpper(strings.TrimSpace(c.ObjectLockMode))
	if c.RetentionDays == 0 {
		c.RetentionDays = defaults.RetentionDays
	}
	c.Environment = strings.ToLower(strings.TrimSpace(c.Environment))
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.BucketID = strings.TrimSpace(c.BucketID)
	c.CredentialRef = strings.TrimSpace(c.CredentialRef)
	c.KMSCredentialRef = strings.TrimSpace(c.KMSCredentialRef)
	c.QualificationCredentialRef = strings.TrimSpace(c.QualificationCredentialRef)
	c.SecuritySinkCredentialRef = strings.TrimSpace(c.SecuritySinkCredentialRef)
	allowlist := make([]string, 0, len(c.EndpointAllowlist))
	seen := make(map[string]struct{}, len(c.EndpointAllowlist))
	for _, raw := range c.EndpointAllowlist {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			continue
		}
		if _, found := seen[host]; found {
			continue
		}
		seen[host] = struct{}{}
		allowlist = append(allowlist, host)
	}
	c.EndpointAllowlist = allowlist
	return c
}

// Validate applies the approved MinIO/Object Lock policy and the activation
// gates. In particular, scheduled mode is always rejected until the separate
// R2-10 and DB-role evidence is merged; setting an env flag cannot bypass it.
func (c AuditArchiveConfig) Validate() error {
	c = c.normalized()
	if c.SchedulerEnabled {
		return ErrAuditArchiveSchedulerGated
	}
	if err := validateArchiveCredentialRefShapes(c); err != nil {
		return err
	}
	switch c.Mode {
	case AuditArchiveModeDisabled:
		if c.Enabled {
			return fmt.Errorf("%w: disabled mode cannot be enabled", ErrAuditArchiveConfig)
		}
		return nil
	case AuditArchiveModeScheduled:
		return ErrAuditArchiveSchedulerGated
	case AuditArchiveModeManual:
		if !c.Enabled {
			// An explicit off switch is safe even when an operator leaves the
			// desired mode staged. Do not require endpoint/credential details or
			// attempt any provider work while disabled.
			return nil
		}
		if c.Environment == "production" {
			return ErrAuditArchiveProductionGated
		}
		if c.Environment != "development" && c.Environment != "staging" && c.Environment != "test" {
			return fmt.Errorf("%w: manual archive environment must be development, staging, or test", ErrAuditArchiveConfig)
		}
		if c.Provider != DefaultAuditArchiveProvider || c.Region != DefaultAuditArchiveRegion {
			return fmt.Errorf("%w: provider/region must remain minio/local", ErrAuditArchiveConfig)
		}
		if c.EncryptionMode != DefaultAuditArchiveEncryption ||
			c.ObjectLockMode != DefaultAuditArchiveObjectLock ||
			c.RetentionDays != DefaultAuditArchiveRetentionDay {
			return fmt.Errorf("%w: approved protection policy is SSE-S3/COMPLIANCE/3650 days", ErrAuditArchiveConfig)
		}
		if err := validateArchiveEndpoint(c.Endpoint, c.EndpointAllowlist, c.Environment); err != nil {
			return err
		}
		if !validArchiveBucketID(c.BucketID) {
			return fmt.Errorf("%w: bucket id is invalid", ErrAuditArchiveConfig)
		}
		refs := []string{c.CredentialRef, c.KMSCredentialRef, c.QualificationCredentialRef, c.SecuritySinkCredentialRef}
		seen := make(map[string]struct{}, len(refs))
		for index, raw := range refs {
			if raw == "" {
				if index < 2 {
					return fmt.Errorf("%w: required CredentialRef is missing", ErrAuditArchiveConfig)
				}
				continue
			}
			ref, _ := secrets.ParseCredentialRef(raw)
			canonical := ref.String()
			if _, duplicate := seen[canonical]; duplicate {
				return fmt.Errorf("%w: credential references must be distinct", ErrAuditArchiveConfig)
			}
			seen[canonical] = struct{}{}
		}
		if c.KMSCredentialRef != "secret://archive/minio-kms" {
			return fmt.Errorf("%w: KMS CredentialRef must be secret://archive/minio-kms", ErrAuditArchiveConfig)
		}
		if c.QualificationCredentialRef != "secret://archive/minio-qualification" {
			return fmt.Errorf("%w: qualification CredentialRef must be secret://archive/minio-qualification", ErrAuditArchiveConfig)
		}
		if c.SecuritySinkCredentialRef != "" && c.SecuritySinkCredentialRef != "secret://archive/security-sink" {
			return fmt.Errorf("%w: security-sink CredentialRef is not approved", ErrAuditArchiveConfig)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrAuditArchiveConfig, c.Mode)
	}
}

func validateArchiveCredentialRefShapes(c AuditArchiveConfig) error {
	refs := []struct {
		label string
		raw   string
	}{
		{label: "object-writer", raw: c.CredentialRef},
		{label: "KMS", raw: c.KMSCredentialRef},
		{label: "qualification", raw: c.QualificationCredentialRef},
		{label: "security-sink", raw: c.SecuritySinkCredentialRef},
	}
	for _, item := range refs {
		label, raw := item.label, item.raw
		if raw == "" {
			continue
		}
		if _, err := secrets.ParseCredentialRef(raw); err != nil {
			return fmt.Errorf("%w: %s CredentialRef: %v", ErrAuditArchiveConfig, label, err)
		}
	}
	return nil
}

func validateArchiveEndpoint(raw string, allowlist []string, environment string) error {
	if strings.TrimSpace(raw) == "" || len(allowlist) == 0 {
		return fmt.Errorf("%w: endpoint and exact allowlist are required", ErrAuditArchiveConfig)
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: endpoint must be an authority URL", ErrAuditArchiveConfig)
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || environment == "production" || !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("%w: non-TLS endpoint is allowed only for loopback fixtures", ErrAuditArchiveConfig)
		}
	}
	host := strings.ToLower(u.Host)
	matched := false
	for _, allowed := range allowlist {
		if allowed == "" || strings.ContainsAny(allowed, "/\\*?[]") {
			return fmt.Errorf("%w: endpoint allowlist must contain exact hosts", ErrAuditArchiveConfig)
		}
		if allowed == host || (u.Port() == "" && allowed == strings.ToLower(u.Hostname())) {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("%w: endpoint host is not in exact allowlist", ErrAuditArchiveConfig)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validArchiveBucketID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || strings.Contains(value, "..") || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

// AuditArchivePeriodicRegistrationAllowed is deliberately a constant false
// while the release lacks both independent scheduler activation proofs.
func AuditArchivePeriodicRegistrationAllowed() bool { return false }

// AuditArchiveRequest references an externally approved signed envelope by
// digest. Raw envelope bytes, paths, credentials and CLI overrides never enter
// River arguments; the injected runner resolves and verifies the envelope.
type AuditArchiveRequest struct {
	ApprovalEnvelopeSHA256 string `json:"approval_envelope_sha256"`
}

func (r AuditArchiveRequest) Validate() error {
	if len(r.ApprovalEnvelopeSHA256) != 64 {
		return ErrAuditArchiveRequestInvalid
	}
	for _, ch := range r.ApprovalEnvelopeSHA256 {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ErrAuditArchiveRequestInvalid
		}
	}
	return nil
}

// ArchiveRunner is the AUD3/AUD5 lifecycle implementation seam. It must verify
// the signed envelope, Kill Switch and exact object protocol; this slice only
// makes a safe manual hand-off possible and does not upload or read anything.
type ArchiveRunner interface {
	Run(context.Context, AuditArchiveRequest) error
}

type ArchiveRunnerFunc func(context.Context, AuditArchiveRequest) error

func (f ArchiveRunnerFunc) Run(ctx context.Context, request AuditArchiveRequest) error {
	if f == nil {
		return ErrAuditArchiveRunnerUnavailable
	}
	return f(ctx, request)
}

type ManualArchiveTrigger struct {
	config AuditArchiveConfig
	runner ArchiveRunner
}

func NewManualArchiveTrigger(config AuditArchiveConfig, runner ArchiveRunner) (*ManualArchiveTrigger, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &ManualArchiveTrigger{config: config.normalized(), runner: runner}, nil
}

func (t *ManualArchiveTrigger) Trigger(ctx context.Context, request AuditArchiveRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil {
		return ErrAuditArchiveRunnerUnavailable
	}
	if err := t.config.Validate(); err != nil {
		if errors.Is(err, ErrAuditArchiveConfig) || errors.Is(err, ErrAuditArchiveSchedulerGated) || errors.Is(err, ErrAuditArchiveProductionGated) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrAuditArchiveConfig, err)
	}
	if t.config.Mode == AuditArchiveModeDisabled || !t.config.Enabled {
		return ErrAuditArchiveDisabled
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if t.runner == nil {
		return ErrAuditArchiveRunnerUnavailable
	}
	return t.runner.Run(ctx, request)
}

// AuditArchiveArgs are manually enqueued arguments. They intentionally do not
// implement a periodic manifest entry or a schedule interval.
type AuditArchiveArgs struct {
	ApprovalEnvelopeSHA256 string `json:"approval_envelope_sha256"`
}

func (AuditArchiveArgs) Kind() string { return AuditArchiveJobKind }

func (AuditArchiveArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: auditArchiveMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}

type ManualArchiveWorker struct {
	river.WorkerDefaults[AuditArchiveArgs]
	trigger     *ManualArchiveTrigger
	logger      *slog.Logger
	environment string
}

func NewManualArchiveWorker(trigger *ManualArchiveTrigger, logger *slog.Logger, environment string) *ManualArchiveWorker {
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment = strings.TrimSpace(environment)
	if environment == "" {
		environment = "unknown"
	}
	return &ManualArchiveWorker{trigger: trigger, logger: logger, environment: environment}
}

func (w *ManualArchiveWorker) Work(ctx context.Context, job *river.Job[AuditArchiveArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w == nil || w.trigger == nil {
		return ErrAuditArchiveRunnerUnavailable
	}
	if job == nil {
		return ErrAuditArchiveRequestInvalid
	}
	err := w.trigger.Trigger(ctx, AuditArchiveRequest{ApprovalEnvelopeSHA256: job.Args.ApprovalEnvelopeSHA256})
	logger := w.logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment := strings.TrimSpace(w.environment)
	if environment == "" {
		environment = "unknown"
	}
	attrs := []slog.Attr{
		slog.String("event", "job_completed"),
		slog.String("module", "platform.jobs"),
		slog.String("environment", environment),
		slog.String("principal_id", "worker:platform"),
		slog.String("job_kind", AuditArchiveJobKind),
		slog.String("queue", QueueMaintenance),
		slog.Bool("success", err == nil),
		slog.String("error_code", auditArchiveErrorCode(err)),
	}
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "job_completed", attrs...)
		return err
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "job_completed", attrs...)
	return nil
}

func auditArchiveErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAuditArchiveDisabled):
		return "audit_archive_disabled"
	case errors.Is(err, ErrAuditArchiveSchedulerGated):
		return "audit_archive_scheduler_gated"
	case errors.Is(err, ErrAuditArchiveRunnerUnavailable):
		return "audit_archive_runner_unavailable"
	case errors.Is(err, ErrAuditArchiveRequestInvalid):
		return "audit_archive_request_invalid"
	default:
		return "audit_archive_manual_failed"
	}
}

// ManualArchiveInserter is the narrow River capability needed by an explicit
// operator command. It keeps the worker seam testable without exposing a
// database pool or allowing arbitrary job kinds/options.
type ManualArchiveInserter interface {
	Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

type ManualArchiveInserterFunc func(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)

func (f ManualArchiveInserterFunc) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if f == nil {
		return nil, ErrAuditArchiveRunnerUnavailable
	}
	return f(ctx, args, opts)
}

func EnqueueManualArchive(ctx context.Context, inserter ManualArchiveInserter, config AuditArchiveConfig, request AuditArchiveRequest) (*rivertype.JobInsertResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	normalized := config.normalized()
	if normalized.Mode == AuditArchiveModeDisabled || !normalized.Enabled {
		return nil, ErrAuditArchiveDisabled
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if inserter == nil {
		return nil, ErrAuditArchiveRunnerUnavailable
	}
	args := AuditArchiveArgs{ApprovalEnvelopeSHA256: request.ApprovalEnvelopeSHA256}
	opts := args.InsertOpts()
	return inserter.Insert(ctx, args, &opts)
}

// Compile-time assertions make accidental widening of the manual capability
// visible during review while preserving the absence of periodic registration.
var _ river.JobArgs = AuditArchiveArgs{}
var _ river.JobArgsWithInsertOpts = AuditArchiveArgs{}
var _ river.Worker[AuditArchiveArgs] = (*ManualArchiveWorker)(nil)
