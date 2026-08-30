package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func validManualArchiveConfig() AuditArchiveConfig {
	return AuditArchiveConfig{
		Enabled:                    true,
		Mode:                       AuditArchiveModeManual,
		Environment:                "staging",
		Provider:                   "minio",
		Endpoint:                   "http://127.0.0.1:19000",
		EndpointAllowlist:          []string{"127.0.0.1:19000"},
		BucketID:                   "audit-archive-staging",
		Region:                     "local",
		EncryptionMode:             "SSE-S3",
		ObjectLockMode:             "COMPLIANCE",
		RetentionDays:              3650,
		CredentialRef:              "secret://archive/minio-runtime",
		KMSCredentialRef:           "secret://archive/minio-kms",
		QualificationCredentialRef: "secret://archive/minio-qualification",
	}
}

func TestAuditArchiveDefaultsAreDisabledAndNotScheduled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.AuditArchive.Enabled {
		t.Fatal("audit archive must be disabled by default")
	}
	if cfg.AuditArchive.Mode != AuditArchiveModeDisabled {
		t.Fatalf("mode = %q, want disabled", cfg.AuditArchive.Mode)
	}
	if cfg.AuditArchive.SchedulerEnabled {
		t.Fatal("archive scheduler must remain disabled until R2-10 and DB roles are proven")
	}
	if AuditArchivePeriodicRegistrationAllowed() {
		t.Fatal("archive periodic registration must be gated")
	}
}

func TestAuditArchiveManualContractFreezesDisabledSchedulerAndCredentialRefs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jobs", "audit-archive-manual.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Kind                string `json:"kind"`
		Mode                string `json:"mode"`
		DefaultEnabled      bool   `json:"default_enabled"`
		SchedulerRegistered bool   `json:"scheduler_registered"`
		CredentialRefs      []struct {
			Ref string `json:"ref"`
		} `json:"credential_refs"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Kind != AuditArchiveJobKind || contract.Mode != string(AuditArchiveModeManual) || contract.DefaultEnabled || contract.SchedulerRegistered {
		t.Fatalf("manual contract drifted: %+v", contract)
	}
	refs := make(map[string]bool, len(contract.CredentialRefs))
	for _, ref := range contract.CredentialRefs {
		refs[ref.Ref] = true
	}
	for _, want := range []string{
		"secret://archive/minio-runtime",
		"secret://archive/minio-kms",
		"secret://archive/minio-qualification",
		"secret://archive/security-sink",
	} {
		if !refs[want] {
			t.Fatalf("contract missing CredentialRef %q", want)
		}
	}
}

func TestAuditArchiveConfigRejectsScheduledModeBeforeGates(t *testing.T) {
	cfg := validManualArchiveConfig()
	cfg.Mode = AuditArchiveModeScheduled
	cfg.Enabled = true
	if err := cfg.Validate(); !errors.Is(err, ErrAuditArchiveSchedulerGated) {
		t.Fatalf("scheduled mode error = %v, want ErrAuditArchiveSchedulerGated", err)
	}
}

func TestAuditArchiveConfigRequiresExplicitManualPolicy(t *testing.T) {
	cfg := validManualArchiveConfig()
	cfg.Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("manual mode with an explicit off switch should be accepted: %v", err)
	}

	cfg = validManualArchiveConfig()
	cfg.Mode = AuditArchiveModeDisabled
	cfg.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabled disabled mode must fail closed")
	}

	cfg = DefaultAuditArchiveConfig()
	cfg.CredentialRef = "not-a-ref"
	if err := cfg.Validate(); err == nil {
		t.Fatal("malformed CredentialRef must fail even while archive is disabled")
	}
}

func TestAuditArchiveConfigRejectsChangedRetentionPolicy(t *testing.T) {
	cfg := validManualArchiveConfig()
	cfg.RetentionDays = 3649
	if err := cfg.Validate(); err == nil {
		t.Fatal("archive retention must stay fixed at the approved 3650 days")
	}
}

func TestAuditArchiveConfigAcceptsLoopbackManualFixture(t *testing.T) {
	cfg := validManualArchiveConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid manual fixture rejected: %v", err)
	}
}

func TestAuditArchiveConfigRejectsProductionManualUntilLifecycleApproval(t *testing.T) {
	cfg := validManualArchiveConfig()
	cfg.Environment = "production"
	if err := cfg.Validate(); !errors.Is(err, ErrAuditArchiveProductionGated) {
		t.Fatalf("production manual error = %v, want ErrAuditArchiveProductionGated", err)
	}
}

func TestAuditArchiveManualTriggerValidatesDigestAndInvokesInjectedRunner(t *testing.T) {
	cfg := validManualArchiveConfig()
	var got AuditArchiveRequest
	runner := ArchiveRunnerFunc(func(_ context.Context, request AuditArchiveRequest) error {
		got = request
		return nil
	})
	trigger, err := NewManualArchiveTrigger(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := trigger.Trigger(context.Background(), AuditArchiveRequest{ApprovalEnvelopeSHA256: "not-a-digest"}); err == nil {
		t.Fatal("invalid approval digest must be rejected")
	}
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := trigger.Trigger(context.Background(), AuditArchiveRequest{ApprovalEnvelopeSHA256: digest}); err != nil {
		t.Fatalf("valid manual trigger failed: %v", err)
	}
	if got.ApprovalEnvelopeSHA256 != digest {
		t.Fatalf("runner request digest = %q, want %q", got.ApprovalEnvelopeSHA256, digest)
	}
}

func TestAuditArchiveManualTriggerNeverRunsWhenDisabled(t *testing.T) {
	cfg := DefaultAuditArchiveConfig()
	calls := 0
	trigger, err := NewManualArchiveTrigger(cfg, ArchiveRunnerFunc(func(context.Context, AuditArchiveRequest) error {
		calls++
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = trigger.Trigger(context.Background(), AuditArchiveRequest{ApprovalEnvelopeSHA256: strings.Repeat("a", 64)})
	if !errors.Is(err, ErrAuditArchiveDisabled) {
		t.Fatalf("disabled trigger error = %v, want ErrAuditArchiveDisabled", err)
	}
	if calls != 0 {
		t.Fatalf("disabled trigger invoked runner %d times", calls)
	}
	cfg = validManualArchiveConfig()
	cfg.Enabled = false
	trigger, err = NewManualArchiveTrigger(cfg, ArchiveRunnerFunc(func(context.Context, AuditArchiveRequest) error {
		calls++
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := trigger.Trigger(context.Background(), AuditArchiveRequest{ApprovalEnvelopeSHA256: strings.Repeat("a", 64)}); !errors.Is(err, ErrAuditArchiveDisabled) {
		t.Fatalf("manual off trigger error = %v, want disabled", err)
	}
	if calls != 0 {
		t.Fatalf("manual off trigger invoked runner %d times", calls)
	}
}

func TestAuditArchiveManualTriggerRequiresRunner(t *testing.T) {
	trigger, err := NewManualArchiveTrigger(validManualArchiveConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = trigger.Trigger(context.Background(), AuditArchiveRequest{ApprovalEnvelopeSHA256: strings.Repeat("b", 64)})
	if !errors.Is(err, ErrAuditArchiveRunnerUnavailable) {
		t.Fatalf("missing runner error = %v, want ErrAuditArchiveRunnerUnavailable", err)
	}
}

func TestAuditArchiveArgsHaveManualOnlyUniquenessAndAreNotManifestJobs(t *testing.T) {
	args := AuditArchiveArgs{ApprovalEnvelopeSHA256: strings.Repeat("c", 64)}
	if got := args.Kind(); got != AuditArchiveJobKind {
		t.Fatalf("kind = %q, want %q", got, AuditArchiveJobKind)
	}
	opts := args.InsertOpts()
	if opts.Queue != QueueMaintenance || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Fatalf("manual insert options = %+v", opts)
	}
	for _, spec := range RegisteredPeriodicJobSpecs() {
		if spec.ID == AuditArchiveJobKind {
			t.Fatal("manual archive must not be added to periodic JobManifest")
		}
	}
}

func TestAuditArchiveWorkerInvokesRunnerWithoutSchedulerRegistration(t *testing.T) {
	const digest = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	var calls int
	trigger, err := NewManualArchiveTrigger(validManualArchiveConfig(), ArchiveRunnerFunc(func(_ context.Context, request AuditArchiveRequest) error {
		if request.ApprovalEnvelopeSHA256 != digest {
			t.Fatalf("runner digest = %q", request.ApprovalEnvelopeSHA256)
		}
		calls++
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	worker := NewManualArchiveWorker(trigger, nil, "staging")
	job := &river.Job[AuditArchiveArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Kind: AuditArchiveJobKind, Queue: QueueMaintenance, Attempt: 1, MaxAttempts: 1},
		Args:   AuditArchiveArgs{ApprovalEnvelopeSHA256: digest},
	}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatalf("worker failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
}

func TestAuditArchiveWorkerFailsClosedForNilTriggerOrJob(t *testing.T) {
	worker := NewManualArchiveWorker(nil, nil, "staging")
	if err := worker.Work(context.Background(), nil); !errors.Is(err, ErrAuditArchiveRunnerUnavailable) {
		t.Fatalf("nil trigger error = %v, want runner unavailable", err)
	}
	trigger, err := NewManualArchiveTrigger(validManualArchiveConfig(), ArchiveRunnerFunc(func(context.Context, AuditArchiveRequest) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	worker = NewManualArchiveWorker(trigger, nil, "staging")
	if err := worker.Work(context.Background(), nil); !errors.Is(err, ErrAuditArchiveRequestInvalid) {
		t.Fatalf("nil job error = %v, want request invalid", err)
	}
}

func TestAuditArchiveWorkerHonorsCancelledContextBeforeRunner(t *testing.T) {
	calls := 0
	trigger, err := NewManualArchiveTrigger(validManualArchiveConfig(), ArchiveRunnerFunc(func(context.Context, AuditArchiveRequest) error {
		calls++
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	worker := NewManualArchiveWorker(trigger, nil, "staging")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job := &river.Job[AuditArchiveArgs]{Args: AuditArchiveArgs{ApprovalEnvelopeSHA256: strings.Repeat("e", 64)}}
	if err := worker.Work(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled worker error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("cancelled worker invoked runner %d times", calls)
	}
}

func TestEnqueueManualArchiveRejectsScheduledInsert(t *testing.T) {
	var inserted bool
	inserter := ManualArchiveInserterFunc(func(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
		inserted = true
		return &rivertype.JobInsertResult{}, nil
	})
	cfg := validManualArchiveConfig()
	cfg.Mode = AuditArchiveModeScheduled
	_, err := EnqueueManualArchive(context.Background(), inserter, cfg, AuditArchiveRequest{ApprovalEnvelopeSHA256: strings.Repeat("d", 64)})
	if !errors.Is(err, ErrAuditArchiveSchedulerGated) {
		t.Fatalf("enqueue error = %v, want scheduler gate", err)
	}
	if inserted {
		t.Fatal("scheduled-gated enqueue reached River")
	}
}

func TestEnqueueManualArchiveHonorsExplicitOffSwitch(t *testing.T) {
	inserted := false
	inserter := ManualArchiveInserterFunc(func(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
		inserted = true
		return &rivertype.JobInsertResult{}, nil
	})
	cfg := validManualArchiveConfig()
	cfg.Enabled = false
	_, err := EnqueueManualArchive(context.Background(), inserter, cfg, AuditArchiveRequest{ApprovalEnvelopeSHA256: strings.Repeat("f", 64)})
	if !errors.Is(err, ErrAuditArchiveDisabled) {
		t.Fatalf("off switch error = %v, want disabled", err)
	}
	if inserted {
		t.Fatal("off switch reached River inserter")
	}
}

func TestEnqueueManualArchiveCarriesOnlyEnvelopeDigest(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var gotArgs AuditArchiveArgs
	inserter := ManualArchiveInserterFunc(func(_ context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
		if opts == nil || opts.Queue != QueueMaintenance || opts.ScheduledAt.IsZero() == false {
			t.Fatalf("manual enqueue must not schedule or change queue: %+v", opts)
		}
		var ok bool
		gotArgs, ok = args.(AuditArchiveArgs)
		if !ok {
			t.Fatalf("unexpected args type %T", args)
		}
		return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 9, Kind: AuditArchiveJobKind}}, nil
	})
	result, err := EnqueueManualArchive(context.Background(), inserter, validManualArchiveConfig(), AuditArchiveRequest{ApprovalEnvelopeSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Job == nil || result.Job.Kind != AuditArchiveJobKind || gotArgs.ApprovalEnvelopeSHA256 != digest {
		t.Fatalf("manual enqueue result/args = %+v / %+v", result, gotArgs)
	}
}
