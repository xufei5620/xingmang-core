package archive

import "testing"

func TestAUD2ProviderQualificationConfigPinsApprovedMinIOBoundary(t *testing.T) {
	config := ProviderQualificationConfig{
		Provider: "minio", Region: "local", BucketID: "archive-qualification",
		ObjectLockMode: "COMPLIANCE", RetentionDays: 3650,
		EncryptionMode: "SSE-S3", CredentialRef: "secret://archive/minio-qualification",
		SDKModule: "github.com/minio/minio-go/v7", SDKVersion: "v7.3.0",
	}
	if err := ValidateProviderQualificationConfig(config); err != nil {
		t.Fatal(err)
	}
	config.Provider = "unknown"
	if err := ValidateProviderQualificationConfig(config); err == nil {
		t.Fatal("unknown provider unexpectedly accepted")
	}
}
