package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type ambiguousFilesystemWriter struct {
	inner   *FilesystemStore
	dropped bool
}

type unrecoverableWriter struct{}

func (unrecoverableWriter) PutIfAbsent(context.Context, ObjectWriteIntentV1, io.Reader) (ObjectVersionV1, error) {
	return ObjectVersionV1{}, ErrAmbiguousPut
}

func (unrecoverableWriter) RecoverPutResult(context.Context, ObjectWriteIntentV1) (ObjectVersionV1, error) {
	return ObjectVersionV1{}, ErrObjectNotFound
}

func (w *ambiguousFilesystemWriter) PutIfAbsent(ctx context.Context, intent ObjectWriteIntentV1, body io.Reader) (ObjectVersionV1, error) {
	object, err := w.inner.PutIfAbsent(ctx, intent, body)
	if err != nil {
		return ObjectVersionV1{}, err
	}
	if !w.dropped {
		w.dropped = true
		return ObjectVersionV1{}, ErrAmbiguousPut
	}
	return object, nil
}

func (w *ambiguousFilesystemWriter) RecoverPutResult(ctx context.Context, intent ObjectWriteIntentV1) (ObjectVersionV1, error) {
	return w.inner.RecoverPutResult(ctx, intent)
}

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

func TestAUD2QualificationProbeRecoversAmbiguousPutWithoutRepublish(t *testing.T) {
	config := ProviderQualificationConfig{
		Provider: "minio", Region: "local", BucketID: "archive-qualification",
		ObjectLockMode: "COMPLIANCE", RetentionDays: 3650,
		EncryptionMode: "SSE-S3", CredentialRef: "secret://archive/minio-qualification",
		SDKModule: ApprovedMinIOArchiveSDKModule, SDKVersion: "v7.3.0",
	}
	body := []byte("qualification")
	intent := aud2LocalIntent(body)
	intent.BucketID = "archive-qualification"
	intent.EncryptionMode = "SSE-S3"
	intent.KMSKeyID = "minio-qualification-key"
	intent.ObjectLockMode = "COMPLIANCE"
	writer, err := NewFilesystemStore(t.TempDir(), intent.BucketID)
	if err != nil {
		t.Fatal(err)
	}
	probe := &ambiguousFilesystemWriter{inner: writer}
	evidence, err := RunQualificationProbe(context.Background(), config, probe, intent, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "PASS" || evidence.RecoveryPath != "recover_put_result" || evidence.RecoveredObjectDigest == "" {
		t.Fatalf("unexpected qualification evidence: %+v", evidence)
	}
	if probe.dropped != true {
		t.Fatal("ambiguous response was not injected")
	}
}

func TestAUD2QualificationProbeRejectsUnrecoverableAmbiguousPut(t *testing.T) {
	config := ProviderQualificationConfig{Provider: "minio", Region: "local", BucketID: "archive-qualification", ObjectLockMode: "COMPLIANCE", RetentionDays: 3650, EncryptionMode: "SSE-S3", CredentialRef: "secret://archive/minio-qualification", SDKModule: ApprovedMinIOArchiveSDKModule, SDKVersion: "v7.3.0"}
	intent := aud2LocalIntent([]byte("qualification"))
	intent.BucketID = config.BucketID
	intent.EncryptionMode = config.EncryptionMode
	intent.KMSKeyID = "minio-qualification-key"
	intent.ObjectLockMode = config.ObjectLockMode
	writer := unrecoverableWriter{}
	_, err := RunQualificationProbe(context.Background(), config, writer, intent, bytes.NewReader([]byte("qualification")))
	if err == nil || !errors.Is(err, ErrProviderQualificationMissing) {
		t.Fatalf("unrecoverable ambiguous put should fail qualification: %v", err)
	}
}
