package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
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
var ErrAmbiguousPut = errors.New("archive_ambiguous_put")

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

// QualificationEvidence is a sanitized, provider-neutral result. It contains
// digests and protocol facts only; credentials, endpoint hosts and payload bytes
// are intentionally absent.
type QualificationEvidence struct {
	Provider              string
	Region                string
	SDKModule             string
	SDKVersion            string
	ObjectLockMode        string
	EncryptionMode        string
	IntentDigest          string
	RecoveredObjectDigest string
	RecoveryPath          string
	Status                string
}

// RunQualificationProbe exercises the approved ambiguous-Put contract against a
// supplied writer. A writer may return ErrAmbiguousPut after committing a body;
// recovery must return the exact object version without a second Put/List/latest
// operation. The caller can inject a disposable MinIO/HTTP fixture or the local
// filesystem fixture; this helper never opens an endpoint itself.
func RunQualificationProbe(ctx context.Context, config ProviderQualificationConfig, writer ObjectWriter,
	intent ObjectWriteIntentV1, body io.Reader) (QualificationEvidence, error) {
	if err := ValidateProviderQualificationConfig(config); err != nil {
		return QualificationEvidence{}, err
	}
	if writer == nil || body == nil {
		return QualificationEvidence{}, fmt.Errorf("%w: qualification writer/body", ErrArchiveValidation)
	}
	if err := ValidateObjectWriteIntent(intent); err != nil {
		return QualificationEvidence{}, err
	}
	intentDigest, err := OperationIntentObjectDigest(intent)
	if err != nil {
		return QualificationEvidence{}, err
	}
	object, putErr := writer.PutIfAbsent(ctx, intent, body)
	recoveryPath := "put"
	if putErr != nil {
		if !errors.Is(putErr, ErrAmbiguousPut) {
			return QualificationEvidence{}, putErr
		}
		recoveryPath = "recover_put_result"
		object, err = writer.RecoverPutResult(ctx, intent)
		if err != nil {
			return QualificationEvidence{}, fmt.Errorf("%w: ambiguous put recovery: %v", ErrProviderQualificationMissing, err)
		}
	}
	if !objectMatchesIntent(object, intent) {
		return QualificationEvidence{}, fmt.Errorf("%w: recovered object does not match intent", ErrProviderQualificationMissing)
	}
	objectDigest, err := ObjectVersionDigest(object)
	if err != nil {
		return QualificationEvidence{}, err
	}
	return QualificationEvidence{
		Provider: config.Provider, Region: config.Region, SDKModule: config.SDKModule,
		SDKVersion: config.SDKVersion, ObjectLockMode: config.ObjectLockMode,
		EncryptionMode: config.EncryptionMode, IntentDigest: intentDigest,
		RecoveredObjectDigest: objectDigest, RecoveryPath: recoveryPath, Status: "PASS",
	}, nil
}

// OperationIntentObjectDigest hashes the object intent itself, excluding the
// approval envelope and provider credentials. It is suitable for sanitized
// qualification evidence, not as a replacement for the signed operation digest.
func OperationIntentObjectDigest(value ObjectWriteIntentV1) (string, error) {
	if err := ValidateObjectWriteIntent(value); err != nil {
		return "", err
	}
	return sha256Hex([]byte(value.OperationID.String() + "\x00" + value.Key + "\x00" + value.SHA256)), nil
}
