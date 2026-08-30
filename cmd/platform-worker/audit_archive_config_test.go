package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

func TestConfigFromEnvDefaultsAuditArchiveToDisabled(t *testing.T) {
	values := map[string]string{"ENVIRONMENT": "staging"}
	cfg, err := configFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditArchive.Enabled || cfg.AuditArchive.Mode != jobs.AuditArchiveModeDisabled {
		t.Fatalf("archive default = %+v, want disabled", cfg.AuditArchive)
	}
	if cfg.AuditArchive.SchedulerEnabled {
		t.Fatal("archive scheduler must default to disabled")
	}
}

func TestConfigFromEnvReadsManualArchivePolicyWithoutSecrets(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":                                  "staging",
		jobs.AuditArchiveEnabledEnv:                    "true",
		jobs.AuditArchiveModeEnv:                       "manual",
		jobs.AuditArchiveEndpointEnv:                   "http://127.0.0.1:19000",
		jobs.AuditArchiveEndpointAllowlistEnv:          "127.0.0.1:19000",
		jobs.AuditArchiveBucketEnv:                     "audit-archive-staging",
		jobs.AuditArchiveCredentialRefEnv:              "secret://archive/minio-runtime",
		jobs.AuditArchiveKMSCredentialRefEnv:           "secret://archive/minio-kms",
		jobs.AuditArchiveQualificationCredentialRefEnv: "secret://archive/minio-qualification",
	}
	cfg, err := configFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AuditArchive.Enabled || cfg.AuditArchive.Mode != jobs.AuditArchiveModeManual {
		t.Fatalf("archive config = %+v", cfg.AuditArchive)
	}
	if cfg.AuditArchive.CredentialRef != "secret://archive/minio-runtime" ||
		cfg.AuditArchive.KMSCredentialRef != "secret://archive/minio-kms" {
		t.Fatalf("credential refs were not preserved as references: %+v", cfg.AuditArchive)
	}
}

func TestConfigFromEnvAllowsExplicitArchiveOffSwitchWithoutProviderDetails(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":               "staging",
		jobs.AuditArchiveModeEnv:    "manual",
		jobs.AuditArchiveEnabledEnv: "false",
	}
	cfg, err := configFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditArchive.Enabled || cfg.AuditArchive.Mode != jobs.AuditArchiveModeManual {
		t.Fatalf("explicit off archive config = %+v", cfg.AuditArchive)
	}
}

func TestConfigFromEnvRejectsArchiveSchedulerAttempt(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":                        "staging",
		jobs.AuditArchiveEnabledEnv:          "true",
		jobs.AuditArchiveModeEnv:             "scheduled",
		jobs.AuditArchiveSchedulerEnabledEnv: "true",
	}
	_, err := configFromEnv(func(name string) string { return values[name] })
	if !errors.Is(err, jobs.ErrAuditArchiveSchedulerGated) {
		t.Fatalf("scheduler error = %v, want gate", err)
	}
}

func TestConfigFromEnvRejectsInvalidArchivePolicy(t *testing.T) {
	base := map[string]string{
		"ENVIRONMENT":                                  "staging",
		jobs.AuditArchiveEnabledEnv:                    "true",
		jobs.AuditArchiveModeEnv:                       "manual",
		jobs.AuditArchiveEndpointEnv:                   "http://127.0.0.1:19000",
		jobs.AuditArchiveEndpointAllowlistEnv:          "127.0.0.1:19000",
		jobs.AuditArchiveBucketEnv:                     "audit-archive-staging",
		jobs.AuditArchiveCredentialRefEnv:              "secret://archive/minio-runtime",
		jobs.AuditArchiveKMSCredentialRefEnv:           "secret://archive/minio-kms",
		jobs.AuditArchiveQualificationCredentialRefEnv: "secret://archive/minio-qualification",
	}
	for name, value := range map[string]string{
		jobs.AuditArchiveEndpointAllowlistEnv: "*",
		jobs.AuditArchiveKMSCredentialRefEnv:  "not-a-ref",
	} {
		values := make(map[string]string, len(base)+1)
		for key, item := range base {
			values[key] = item
		}
		values[name] = value
		if _, err := configFromEnv(func(key string) string { return values[key] }); err == nil {
			t.Fatalf("%s=%q should be rejected", name, value)
		}
	}
}

func TestConfigFromEnvNeverReadsArchiveSecretValues(t *testing.T) {
	var asked []string
	values := map[string]string{"ENVIRONMENT": "staging"}
	if _, err := configFromEnv(func(name string) string {
		asked = append(asked, name)
		return values[name]
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range asked {
		if strings.Contains(strings.ToUpper(name), "TOKEN") || strings.Contains(strings.ToUpper(name), "PASSWORD") || strings.Contains(strings.ToUpper(name), "SECRET_KEY") {
			t.Fatalf("config parser must not read secret values directly: %s", name)
		}
	}
}
