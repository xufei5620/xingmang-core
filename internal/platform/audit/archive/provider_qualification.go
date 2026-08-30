package archive

import (
	"fmt"
	"strings"
)

const ApprovedMinIOArchiveSDKModule = "github.com/minio/minio-go/v7"

type ProviderQualificationConfig struct {
	Provider       string
	Region         string
	BucketID       string
	ObjectLockMode string
	RetentionDays  int
	EncryptionMode string
	CredentialRef  string
	SDKModule      string
	SDKVersion     string
}

type ProviderQualificationResult struct {
	Provider       string
	Region         string
	BucketID       string
	ObjectLockMode string
	RetentionDays  int
	EncryptionMode string
	SDKModule      string
	SDKVersion     string
	Status         string
}

var ErrProviderQualificationMissing = fmt.Errorf("provider qualification missing")

func ValidateProviderQualificationConfig(value ProviderQualificationConfig) error {
	if strings.ToLower(strings.TrimSpace(value.Provider)) != "minio" ||
		value.Region != "local" || !validBucketID(value.BucketID) ||
		value.ObjectLockMode != "COMPLIANCE" || value.RetentionDays != 3650 ||
		value.EncryptionMode != "SSE-S3" || value.CredentialRef != "secret://archive/minio-qualification" ||
		value.SDKModule != ApprovedMinIOArchiveSDKModule || value.SDKVersion == "" ||
		strings.EqualFold(value.SDKVersion, "latest") || !validVersion(value.SDKVersion) {
		return fmt.Errorf("%w: approved MinIO qualification config mismatch", ErrArchiveValidation)
	}
	return nil
}

func validVersion(value string) bool {
	if len(value) < 3 || value[0] != 'v' {
		return false
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func QualificationResultFor(config ProviderQualificationConfig, status string) (ProviderQualificationResult, error) {
	if err := ValidateProviderQualificationConfig(config); err != nil {
		return ProviderQualificationResult{}, err
	}
	if status != "PASS" && status != "FAIL" && status != "SKIP" {
		return ProviderQualificationResult{}, fmt.Errorf("%w: invalid status", ErrArchiveValidation)
	}
	return ProviderQualificationResult{
		Provider: config.Provider, Region: config.Region, BucketID: config.BucketID,
		ObjectLockMode: config.ObjectLockMode, RetentionDays: config.RetentionDays,
		EncryptionMode: config.EncryptionMode, SDKModule: config.SDKModule,
		SDKVersion: config.SDKVersion, Status: status,
	}, nil
}
