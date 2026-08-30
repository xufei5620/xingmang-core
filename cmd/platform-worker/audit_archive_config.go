package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

// auditArchiveConfigFromEnv reads only non-secret policy and CredentialRef
// names. It never resolves a secret and it never registers a River job. The
// default is disabled; enabling the manual seam requires every approved
// provider-policy field and the disposable qualification reference.
func auditArchiveConfigFromEnv(getenv func(string) string, environment string) (jobs.AuditArchiveConfig, error) {
	cfg := jobs.DefaultAuditArchiveConfig()
	cfg.Environment = strings.TrimSpace(environment)

	if raw := strings.TrimSpace(getenv(jobs.AuditArchiveEnabledEnv)); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return jobs.AuditArchiveConfig{}, fmt.Errorf("audit archive enabled: %w", err)
		}
		cfg.Enabled = enabled
	}
	mode, err := jobs.ParseAuditArchiveMode(getenv(jobs.AuditArchiveModeEnv))
	if err != nil {
		return jobs.AuditArchiveConfig{}, err
	}
	cfg.Mode = mode
	if raw := strings.TrimSpace(getenv(jobs.AuditArchiveSchedulerEnabledEnv)); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return jobs.AuditArchiveConfig{}, fmt.Errorf("audit archive scheduler enabled: %w", err)
		}
		cfg.SchedulerEnabled = enabled
	}
	cfg.Endpoint = strings.TrimSpace(getenv(jobs.AuditArchiveEndpointEnv))
	cfg.EndpointAllowlist = parseHostAllowlist(getenv(jobs.AuditArchiveEndpointAllowlistEnv))
	cfg.BucketID = strings.TrimSpace(getenv(jobs.AuditArchiveBucketEnv))
	if value := strings.TrimSpace(getenv(jobs.AuditArchiveRegionEnv)); value != "" {
		cfg.Region = value
	}
	if value := strings.TrimSpace(getenv(jobs.AuditArchiveEncryptionEnv)); value != "" {
		cfg.EncryptionMode = value
	}
	if value := strings.TrimSpace(getenv(jobs.AuditArchiveObjectLockEnv)); value != "" {
		cfg.ObjectLockMode = value
	}
	cfg.CredentialRef = strings.TrimSpace(getenv(jobs.AuditArchiveCredentialRefEnv))
	cfg.KMSCredentialRef = strings.TrimSpace(getenv(jobs.AuditArchiveKMSCredentialRefEnv))
	cfg.QualificationCredentialRef = strings.TrimSpace(getenv(jobs.AuditArchiveQualificationCredentialRefEnv))
	cfg.SecuritySinkCredentialRef = strings.TrimSpace(getenv(jobs.AuditArchiveSecuritySinkCredentialRefEnv))
	if err := cfg.Validate(); err != nil {
		return jobs.AuditArchiveConfig{}, err
	}
	return cfg, nil
}
