package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// OwnershipMode describes which actor owns a periodic job slot. R210-1 only
// admits the River-backed cluster singleton mode; a different scheduler or a
// per-replica mode requires a new contract version and review.
type OwnershipMode string

const OwnershipClusterSingleton OwnershipMode = "cluster_singleton"

const (
	jobManifestVersion      = 1
	jobManifestScheduler    = "river-oss-postgres-leader-v0.45"
	jobManifestClusterModel = "one-environment-per-database-schema"
	jobManifestOwnerProcess = "platform-worker"
	jobManifestExecution    = "at_least_once"
	jobManifestCatchUp      = "at_most_one_immediate"
	jobManifestFenceLeader  = "river_leader/default"
	jobManifestFenceUnique  = "args+queue+period"
)

// JobSpec is the immutable, non-secret contract for one periodic River job.
// ScheduleConfig and RunOnStartSource name configuration sources rather than
// carrying deployment values; those values are resolved into EffectiveJobSpec.
type JobSpec struct {
	ID                  string        `json:"id"`
	Kind                string        `json:"kind"`
	Queue               string        `json:"queue"`
	OwnerProcess        string        `json:"owner_process"`
	Ownership           OwnershipMode `json:"ownership"`
	ScheduleConfig      string        `json:"schedule_config"`
	RunOnStartSource    string        `json:"run_on_start_source"`
	CatchUp             string        `json:"catch_up"`
	EnqueueFences       []string      `json:"enqueue_fences"`
	UniqueStates        []string      `json:"unique_states"`
	Execution           string        `json:"execution"`
	SideEffectClass     string        `json:"side_effect_class"`
	IdempotencyEvidence string        `json:"idempotency_evidence"`
}

// JobManifest is the versioned static contract consumed by the worker and
// later by the signed fleet/effective-manifest layers.
type JobManifest struct {
	Version      int       `json:"version"`
	Scheduler    string    `json:"scheduler"`
	ClusterModel string    `json:"cluster_model"`
	Jobs         []JobSpec `json:"jobs"`
}

// RegisteredPeriodicJobSpecs returns a defensive copy of the six periodic
// jobs currently registered by NewClient. Callers cannot mutate the package's
// registry through the returned slices.
func RegisteredPeriodicJobSpecs() []JobSpec {
	rows := []JobSpec{
		{
			ID: HeartbeatJobKind, Kind: HeartbeatJobKind, Queue: QueueMaintenance,
			OwnerProcess: jobManifestOwnerProcess, Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "HEARTBEAT_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.HeartbeatRunOnStart",
			CatchUp: jobManifestCatchUp, EnqueueFences: manifestFences(), UniqueStates: manifestUniqueStates(),
			Execution: jobManifestExecution, SideEffectClass: "audit_append",
			IdempotencyEvidence: "audit chain append is keyed by the worker heartbeat event and remains at-least-once",
		},
		{
			ID: Sub2APISyncJobKind, Kind: Sub2APISyncJobKind, Queue: QueueMaintenance,
			OwnerProcess: jobManifestOwnerProcess, Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_SUB2API_SYNC_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.Sub2APISyncRunOnStart",
			CatchUp: jobManifestCatchUp, EnqueueFences: manifestFences(), UniqueStates: manifestUniqueStates(),
			Execution: jobManifestExecution, SideEffectClass: "upstream_read_then_db_transaction",
			IdempotencyEvidence: "latest+sample atomic transaction; retry remains upstream-read attempt",
		},
		{
			ID: NewAPISyncJobKind, Kind: NewAPISyncJobKind, Queue: QueueMaintenance,
			OwnerProcess: jobManifestOwnerProcess, Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_NEWAPI_SYNC_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.NewAPISyncRunOnStart",
			CatchUp: jobManifestCatchUp, EnqueueFences: manifestFences(), UniqueStates: manifestUniqueStates(),
			Execution: jobManifestExecution, SideEffectClass: "upstream_read_then_db_transaction",
			IdempotencyEvidence: "latest+sample atomic transaction; retry remains upstream-read attempt",
		},
		{
			ID: FinanceCollectJobKind, Kind: FinanceCollectJobKind, Queue: QueueMaintenance,
			OwnerProcess: jobManifestOwnerProcess, Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_FINANCE_COLLECT_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.FinanceCollectRunOnStart",
			CatchUp: jobManifestCatchUp, EnqueueFences: manifestFences(), UniqueStates: manifestUniqueStates(),
			Execution: jobManifestExecution, SideEffectClass: "upstream_read_then_finance_transaction",
			IdempotencyEvidence: "daily cost and profit rows are upserted in bounded transactions; retry is recorded as another read attempt",
		},
		{
			ID: RetentionJobKind, Kind: RetentionJobKind, Queue: QueueMaintenance,
			OwnerProcess: jobManifestOwnerProcess, Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_RETENTION_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.RetentionRunOnStart",
			CatchUp: jobManifestCatchUp, EnqueueFences: manifestFences(), UniqueStates: manifestUniqueStates(),
			Execution: jobManifestExecution, SideEffectClass: "bounded_delete",
			IdempotencyEvidence: "cutoff-bounded batched delete; repeating a batch cannot expand its deletion range",
		},
		{
			ID: AlertEvaluateJobKind, Kind: AlertEvaluateJobKind, Queue: QueueMaintenance,
			OwnerProcess: jobManifestOwnerProcess, Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_ALERT_EVALUATE_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.AlertEvaluateRunOnStart",
			CatchUp: jobManifestCatchUp, EnqueueFences: manifestFences(), UniqueStates: manifestUniqueStates(),
			Execution: jobManifestExecution, SideEffectClass: "reconcile_then_notification",
			IdempotencyEvidence: "alert fingerprint reconciliation is persisted before notification; retry reuses the same finding identity",
		},
	}
	return cloneJobSpecs(rows)
}

func manifestFences() []string { return []string{jobManifestFenceLeader, jobManifestFenceUnique} }

// Keep the contract's wire order stable and human-readable. It represents the
// same state set as rivertype.UniqueOptsByStateDefault; Args use River's typed
// order directly when constructing InsertOpts.
func manifestUniqueStates() []string {
	return []string{"available", "completed", "pending", "running", "retryable", "scheduled"}
}

// LoadJobManifest strictly decodes and validates the frozen contract. The
// returned string is the lowercase SHA-256 of canonical JSON bytes produced by
// encoding/json over the validated struct, not a digest of caller formatting.
func LoadJobManifest(raw []byte) (JobManifest, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return JobManifest{}, "", errors.New("job manifest: empty document")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest JobManifest
	if err := decoder.Decode(&manifest); err != nil {
		return JobManifest{}, "", fmt.Errorf("job manifest: decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return JobManifest{}, "", errors.New("job manifest: trailing JSON value")
		}
		return JobManifest{}, "", fmt.Errorf("job manifest: trailing data: %w", err)
	}
	if err := validateJobManifest(manifest); err != nil {
		return JobManifest{}, "", err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return JobManifest{}, "", fmt.Errorf("job manifest: canonical encode: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return manifest, hex.EncodeToString(digest[:]), nil
}

func validateJobManifest(manifest JobManifest) error {
	if manifest.Version != jobManifestVersion {
		return fmt.Errorf("job manifest: unsupported version %d", manifest.Version)
	}
	if manifest.Scheduler != jobManifestScheduler {
		return fmt.Errorf("job manifest: unsupported scheduler %q", manifest.Scheduler)
	}
	if manifest.ClusterModel != jobManifestClusterModel {
		return fmt.Errorf("job manifest: unsupported cluster model %q", manifest.ClusterModel)
	}
	expected := RegisteredPeriodicJobSpecs()
	if len(manifest.Jobs) != len(expected) {
		return fmt.Errorf("job manifest: jobs count %d, want exactly %d", len(manifest.Jobs), len(expected))
	}
	expectedByID := make(map[string]JobSpec, len(expected))
	for _, row := range expected {
		expectedByID[row.ID] = row
	}
	seenIDs := make(map[string]struct{}, len(manifest.Jobs))
	seenKinds := make(map[string]struct{}, len(manifest.Jobs))
	for i, got := range manifest.Jobs {
		if strings.TrimSpace(got.ID) == "" {
			return fmt.Errorf("job manifest: jobs[%d] has empty id", i)
		}
		if _, ok := seenIDs[got.ID]; ok {
			return fmt.Errorf("job manifest: duplicate id %q", got.ID)
		}
		seenIDs[got.ID] = struct{}{}
		if _, ok := seenKinds[got.Kind]; ok {
			return fmt.Errorf("job manifest: duplicate kind %q", got.Kind)
		}
		seenKinds[got.Kind] = struct{}{}
		want, ok := expectedByID[got.ID]
		if !ok {
			return fmt.Errorf("job manifest: unregistered periodic job %q", got.ID)
		}
		if got.Kind != want.Kind {
			return fmt.Errorf("job manifest: %q kind %q does not match registered kind %q", got.ID, got.Kind, want.Kind)
		}
		if err := validateJobSpec(i, got, want); err != nil {
			return err
		}
	}
	for _, want := range expected {
		if _, ok := seenIDs[want.ID]; !ok {
			return fmt.Errorf("job manifest: missing registered periodic job %q", want.ID)
		}
	}
	return nil
}

func validateJobSpec(index int, got, want JobSpec) error {
	if got.OwnerProcess != jobManifestOwnerProcess || got.Ownership != OwnershipClusterSingleton ||
		got.CatchUp != jobManifestCatchUp || got.Execution != jobManifestExecution {
		return fmt.Errorf("job manifest: jobs[%d] has unsupported ownership/catch-up/execution", index)
	}
	if got.Queue != want.Queue || got.ScheduleConfig != want.ScheduleConfig || got.RunOnStartSource != want.RunOnStartSource {
		return fmt.Errorf("job manifest: %q runtime source/queue differs from registered contract", got.ID)
	}
	if !equalStrings(got.EnqueueFences, want.EnqueueFences) {
		return fmt.Errorf("job manifest: %q enqueue fences differ", got.ID)
	}
	if !equalStrings(got.UniqueStates, want.UniqueStates) {
		return fmt.Errorf("job manifest: %q unique states differ", got.ID)
	}
	if strings.TrimSpace(got.SideEffectClass) == "" || strings.TrimSpace(got.IdempotencyEvidence) == "" {
		return fmt.Errorf("job manifest: %q must declare side effect and idempotency evidence", got.ID)
	}
	if got.SideEffectClass != want.SideEffectClass {
		return fmt.Errorf("job manifest: %q side effect class %q differs from registered %q", got.ID, got.SideEffectClass, want.SideEffectClass)
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneJobSpecs(in []JobSpec) []JobSpec {
	out := make([]JobSpec, len(in))
	for i, row := range in {
		out[i] = row
		out[i].EnqueueFences = append([]string(nil), row.EnqueueFences...)
		out[i].UniqueStates = append([]string(nil), row.UniqueStates...)
	}
	return out
}

func registeredPeriodicJobSpec(id string) (JobSpec, bool) {
	for _, row := range RegisteredPeriodicJobSpecs() {
		if row.ID == id {
			return row, true
		}
	}
	return JobSpec{}, false
}
