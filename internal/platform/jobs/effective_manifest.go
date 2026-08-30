package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EffectiveJobSpec is the deployment-resolved view of one static JobSpec.
// It contains only non-secret values that must agree across replicas.
type EffectiveJobSpec struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Queue           string `json:"queue"`
	Enabled         bool   `json:"enabled"`
	RunOnStart      bool   `json:"run_on_start"`
	IntervalSeconds int64  `json:"interval_seconds"`
	Ownership       string `json:"ownership"`
	CatchUp         string `json:"catch_up"`
	SideEffectClass string `json:"side_effect_class"`
}

// EffectiveJobManifest is the canonical, per-deployment contract used by the
// later fleet-identity slice. Jobs are always sorted by ID byte order.
type EffectiveJobManifest struct {
	ContractVersion int                `json:"contract_version"`
	Environment     string             `json:"environment"`
	WorkerClusterID string             `json:"worker_cluster_id"`
	RiverSchema     string             `json:"river_schema"`
	Jobs            []EffectiveJobSpec `json:"jobs"`
}

// BuildEffectiveManifest resolves a static JobManifest against non-secret
// worker configuration and returns the canonical SHA-256 digest. It does not
// connect to River/PostgreSQL or inspect credentials.
func BuildEffectiveManifest(cfg Config, manifest JobManifest) (EffectiveJobManifest, string, error) {
	if err := validateJobManifest(manifest); err != nil {
		return EffectiveJobManifest{}, "", err
	}
	if err := validateManifestIdentity("environment", cfg.Environment); err != nil {
		return EffectiveJobManifest{}, "", err
	}
	if err := validateManifestIdentity("worker cluster id", cfg.WorkerClusterID); err != nil {
		return EffectiveJobManifest{}, "", err
	}
	if err := validateManifestIdentity("River schema", cfg.RiverSchema); err != nil {
		return EffectiveJobManifest{}, "", err
	}
	if cfg.Environment == "production" && productionRunID(cfg) != "" {
		return EffectiveJobManifest{}, "", fmt.Errorf("effective job manifest: production RunID must be empty")
	}

	// Resolve only the schedule/enable fields needed by the six registered jobs.
	// Calling normalized preserves the existing zero-value defaults without
	// changing any runtime registration behavior.
	cfg = cfg.normalized()
	jobs := make([]EffectiveJobSpec, 0, len(manifest.Jobs))
	for _, row := range manifest.Jobs {
		enabled, runOnStart, interval, expectedSchedule, err := effectiveJobConfig(cfg, row.ID)
		if err != nil {
			return EffectiveJobManifest{}, "", err
		}
		if row.ScheduleConfig != expectedSchedule {
			return EffectiveJobManifest{}, "", fmt.Errorf("effective job manifest: %q schedule source %q does not match %q", row.ID, row.ScheduleConfig, expectedSchedule)
		}
		if interval <= 0 {
			return EffectiveJobManifest{}, "", fmt.Errorf("effective job manifest: %q interval must be positive", row.ID)
		}
		if interval < time.Second || interval%time.Second != 0 {
			return EffectiveJobManifest{}, "", fmt.Errorf("effective job manifest: %q interval %s must be whole seconds and at least one second", row.ID, interval)
		}
		jobs = append(jobs, EffectiveJobSpec{
			ID: row.ID, Kind: row.Kind, Queue: row.Queue,
			Enabled: enabled, RunOnStart: runOnStart,
			IntervalSeconds: int64(interval / time.Second),
			Ownership:       string(row.Ownership), CatchUp: row.CatchUp,
			SideEffectClass: row.SideEffectClass,
		})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	effective := EffectiveJobManifest{
		ContractVersion: manifest.Version,
		Environment:     cfg.Environment, WorkerClusterID: cfg.WorkerClusterID,
		RiverSchema: cfg.RiverSchema, Jobs: jobs,
	}
	canonical, err := json.Marshal(effective)
	if err != nil {
		return EffectiveJobManifest{}, "", fmt.Errorf("effective job manifest: canonical encode: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return effective, hex.EncodeToString(digest[:]), nil
}

func effectiveJobConfig(cfg Config, id string) (enabled, runOnStart bool, interval time.Duration, schedule string, err error) {
	switch id {
	case HeartbeatJobKind:
		return true, cfg.HeartbeatRunOnStart, cfg.HeartbeatInterval, "HEARTBEAT_INTERVAL", nil
	case Sub2APISyncJobKind:
		return cfg.Sub2APISyncEnabled, cfg.Sub2APISyncRunOnStart, cfg.Sub2APISyncInterval, "XM_SUB2API_SYNC_INTERVAL", nil
	case NewAPISyncJobKind:
		return cfg.NewAPISyncEnabled, cfg.NewAPISyncRunOnStart, cfg.NewAPISyncInterval, "XM_NEWAPI_SYNC_INTERVAL", nil
	case FinanceCollectJobKind:
		return cfg.FinanceCollectEnabled, cfg.FinanceCollectRunOnStart, cfg.FinanceCollectInterval, "XM_FINANCE_COLLECT_INTERVAL", nil
	case RetentionJobKind:
		return cfg.RetentionEnabled, cfg.RetentionRunOnStart, cfg.RetentionInterval, "XM_RETENTION_INTERVAL", nil
	case AlertEvaluateJobKind:
		return cfg.AlertEvaluateEnabled, cfg.AlertEvaluateRunOnStart, cfg.AlertEvaluateInterval, "XM_ALERT_EVALUATE_INTERVAL", nil
	case CPASyncJobKind:
		return cfg.CPASyncEnabled, cfg.CPASyncRunOnStart, cfg.CPASyncInterval, "XM_CPA_SYNC_INTERVAL", nil
	default:
		return false, false, 0, "", fmt.Errorf("effective job manifest: unknown job %q", id)
	}
}

func productionRunID(cfg Config) string {
	for _, value := range []string{cfg.HeartbeatRunID, cfg.Sub2APISyncRunID, cfg.NewAPISyncRunID, cfg.FinanceCollectRunID, cfg.RetentionRunID, cfg.AlertEvaluateRunID, cfg.CPASyncRunID} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validateManifestIdentity(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("effective job manifest: %s must be non-empty and trimmed", name)
	}
	if len(value) > 127 {
		return fmt.Errorf("effective job manifest: %s exceeds 127 bytes", name)
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.') {
			return fmt.Errorf("effective job manifest: %s contains invalid character", name)
		}
	}
	if !isASCIIAlphaNumeric(value[0]) || !isASCIIAlphaNumeric(value[len(value)-1]) {
		return fmt.Errorf("effective job manifest: %s must start and end with an ASCII letter or digit", name)
	}
	return nil
}

func isASCIIAlphaNumeric(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}
