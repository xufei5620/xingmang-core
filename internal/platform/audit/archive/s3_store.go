package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// The S3 adapter is deliberately narrower than minio.Client.  Only the
// ObjectWriter and ExactObjectReader methods below are exposed; callers cannot
// accidentally obtain Delete, List, latest-object, or overwrite capabilities.
const (
	S3ArchiveEncryptionMode = "SSE-S3"
	S3ArchiveObjectLockMode = "COMPLIANCE"

	s3CredentialPurpose = "audit archive object store"
)

var (
	ErrS3Config              = errors.New("s3 archive configuration invalid")
	ErrS3Credentials         = errors.New("s3 archive credentials unavailable")
	ErrS3EndpointNotAllowed  = errors.New("s3 archive endpoint not allowlisted")
	ErrS3Redirect            = errors.New("s3 archive redirect rejected")
	ErrS3VersionIDMissing    = errors.New("s3 archive version id missing")
	ErrS3MetadataMismatch    = errors.New("s3 archive object metadata mismatch")
	ErrS3RecoveryUnsupported = errors.New("s3 archive provider-native recovery unavailable")
)

// S3Credentials is the redacted-secret payload resolved through a
// secrets.SecretProvider.  The value is never stored in S3StoreConfig or
// returned from the adapter.  SecretProvider implementations should store this
// JSON object under the approved CredentialRef, for example:
// {"access_key":"…","secret_key":"…","session_token":"…"}.
type S3Credentials struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token,omitempty"`
}

// S3StoreConfig contains non-secret endpoint and credential references for the
// approved self-hosted MinIO archive. EndpointAllowlist entries are exact host
// names (or host:port); wildcards are intentionally unsupported. HTTP is
// accepted only when AllowInsecureHTTP is explicitly true for disposable
// protocol fixtures. Production configuration must use HTTPS.
type S3StoreConfig struct {
	Endpoint          string
	EndpointAllowlist []string
	AllowInsecureHTTP bool
	BucketID          string
	Region            string
	CredentialRef     secrets.CredentialRef
	Secrets           secrets.SecretProvider
	HTTPClient        *http.Client
}

// S3Store implements the narrow archive object-store capabilities against a
// versioned MinIO/S3 endpoint. It always uses path-style bucket addressing so
// the endpoint host remains the one explicitly allowlisted by configuration.
type S3Store struct {
	client   *minio.Client
	bucketID string
}

var _ ObjectWriter = (*S3Store)(nil)
var _ ExactObjectReader = (*S3Store)(nil)

// NewS3Store validates the endpoint/allowlist and resolves credentials once at
// construction. The adapter never reads environment variables or files
// directly; all credential material comes through the injected SecretProvider.
func NewS3Store(ctx context.Context, cfg S3StoreConfig) (*S3Store, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	endpoint, err := parseS3Endpoint(cfg.Endpoint, cfg.AllowInsecureHTTP, cfg.EndpointAllowlist)
	if err != nil {
		return nil, err
	}
	if !validBucketID(cfg.BucketID) || cfg.BucketID == "local-fixture" {
		return nil, fmt.Errorf("%w: bucket", ErrS3Config)
	}
	if cfg.Secrets == nil || cfg.CredentialRef.IsZero() {
		return nil, ErrS3Credentials
	}
	secret, err := cfg.Secrets.Resolve(ctx, cfg.CredentialRef, s3CredentialPurpose)
	if err != nil {
		// Deliberately do not include the ref/value: this error crosses a
		// provider boundary and must not expose credential details.
		return nil, fmt.Errorf("%w: provider resolve", ErrS3Credentials)
	}
	parsed, err := ParseS3Credentials(secret.Reveal())
	if err != nil {
		return nil, fmt.Errorf("%w: malformed payload", ErrS3Credentials)
	}

	transport := http.RoundTripper(http.DefaultTransport)
	if cfg.HTTPClient != nil && cfg.HTTPClient.Transport != nil {
		transport = cfg.HTTPClient.Transport
	}
	transport = &s3EndpointTransport{
		base:       transport,
		allowHosts: normalizeS3Allowlist(cfg.EndpointAllowlist),
		scheme:     endpoint.Scheme,
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(parsed.AccessKey, parsed.SecretKey, parsed.SessionToken),
		Secure:       endpoint.Scheme == "https",
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupPath,
		Transport:    transport,
		// A retry can create a second ambiguous Put. Recovery is an explicit
		// content-addressed HEAD path, so disable SDK retries here.
		MaxRetries: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: client", ErrS3Config)
	}
	return &S3Store{
		client:   client,
		bucketID: cfg.BucketID,
	}, nil
}

// ParseS3Credentials parses the exact JSON payload expected from a
// SecretProvider. Unknown fields, trailing values, empty fields and control
// characters are rejected so a malformed secret cannot silently downgrade to
// anonymous access.
func ParseS3Credentials(raw string) (S3Credentials, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || len(raw) > 16<<10 {
		return S3Credentials{}, ErrS3Credentials
	}
	if err := rejectCredentialDuplicateJSONKeys(raw); err != nil {
		return S3Credentials{}, ErrS3Credentials
	}
	var parsed S3Credentials
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return S3Credentials{}, ErrS3Credentials
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return S3Credentials{}, ErrS3Credentials
	}
	if !validS3CredentialPart(parsed.AccessKey) || !validS3CredentialPart(parsed.SecretKey) ||
		(parsed.SessionToken != "" && !validS3CredentialPart(parsed.SessionToken)) {
		return S3Credentials{}, ErrS3Credentials
	}
	canonical, err := json.Marshal(parsed)
	if err != nil || string(canonical) != raw {
		return S3Credentials{}, ErrS3Credentials
	}
	return parsed, nil
}

// rejectDuplicateJSONKeys performs a token-level pass because encoding/json
// otherwise silently keeps the last value for duplicate object members. The
// credentials payload is intentionally a single object; nested objects/arrays
// are rejected by the typed decode below, but the scanner still tracks their
// member names so duplicates cannot be smuggled through an ignored field.
func rejectCredentialDuplicateJSONKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return ErrS3Credentials
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return ErrS3Credentials
		}
		canonicalKey := strings.ToLower(key)
		if _, duplicate := seen[canonicalKey]; duplicate {
			return ErrS3Credentials
		}
		seen[canonicalKey] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if closeDelim, ok := closing.(json.Delim); !ok || closeDelim != '}' {
		return ErrS3Credentials
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrS3Credentials
	}
	return nil
}

func validS3CredentialPart(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

type s3EndpointTransport struct {
	base       http.RoundTripper
	allowHosts map[string]struct{}
	scheme     string
}

func (t *s3EndpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.URL.Scheme != t.scheme || !s3HostAllowed(req.URL.Host, req.URL.Hostname(), t.allowHosts) {
		return nil, ErrS3EndpointNotAllowed
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, ErrS3Redirect
	}
	return resp, nil
}

func parseS3Endpoint(raw string, allowHTTP bool, allowlist []string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: endpoint empty", ErrS3Config)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: endpoint shape", ErrS3Config)
	}
	u.Path = ""
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("%w: endpoint scheme", ErrS3Config)
	}
	if u.Scheme == "http" && !allowHTTP {
		return nil, ErrS3EndpointNotAllowed
	}
	if !s3HostAllowed(u.Host, u.Hostname(), normalizeS3Allowlist(allowlist)) {
		return nil, ErrS3EndpointNotAllowed
	}
	return u, nil
}

func normalizeS3Allowlist(raw []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" || strings.ContainsAny(item, "*/\\") {
			continue
		}
		if strings.Contains(item, "://") {
			if parsed, err := url.Parse(item); err == nil {
				item = parsed.Host
			}
		}
		item = strings.TrimSuffix(strings.ToLower(item), ".")
		if item != "" {
			allowed[item] = struct{}{}
		}
	}
	return allowed
}

func s3HostAllowed(host, hostname string, allowlist map[string]struct{}) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if _, ok := allowlist[host]; ok {
		return true
	}
	if _, ok := allowlist[hostname]; ok {
		return true
	}
	return false
}

func (s *S3Store) validateIntent(intent ObjectWriteIntentV1) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("%w: nil store", ErrS3Config)
	}
	if err := ValidateObjectWriteIntent(intent); err != nil {
		return err
	}
	if intent.BucketID != s.bucketID || intent.EncryptionMode != S3ArchiveEncryptionMode ||
		intent.ObjectLockMode != S3ArchiveObjectLockMode || intent.KMSKeyID == "" {
		return fmt.Errorf("%w: approved protection mismatch", ErrArchiveValidation)
	}
	if strings.EqualFold(intent.ProviderIdempotencyToken, "latest") {
		return fmt.Errorf("%w: latest token", ErrArchiveValidation)
	}
	return nil
}

func readS3Body(ctx context.Context, body io.Reader, expectedSize int64, expectedSHA string) ([]byte, error) {
	if body == nil || expectedSize < 0 || expectedSize > MaxObjectWriteBytes {
		return nil, fmt.Errorf("%w: body", ErrArchiveValidation)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limited := io.LimitReader(body, expectedSize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != expectedSize || sha256Hex(raw) != expectedSHA {
		return nil, fmt.Errorf("%w: body digest/size", ErrArchiveValidation)
	}
	return raw, nil
}

func (s *S3Store) PutIfAbsent(ctx context.Context, intent ObjectWriteIntentV1, body io.Reader) (ObjectVersionV1, error) {
	if err := s.validateIntent(intent); err != nil {
		return ObjectVersionV1{}, err
	}
	raw, err := readS3Body(ctx, body, intent.SizeBytes, intent.SHA256)
	if err != nil {
		return ObjectVersionV1{}, err
	}
	retain, err := ParseWireTime(intent.RetainUntil)
	if err != nil {
		return ObjectVersionV1{}, fmt.Errorf("%w: retain date", ErrArchiveValidation)
	}
	options := minio.PutObjectOptions{
		ContentType:          intent.ContentType,
		ServerSideEncryption: encrypt.NewSSE(), // SSE-S3 / AES256; never AWS KMS.
		Mode:                 minio.Compliance,
		RetainUntilDate:      retain.UTC(),
		DisableMultipart:     true,
		// The size is known and the body is already hashed. Disable streaming
		// SigV4 chunks so the wire Content-Length/body remain the exact bytes
		// represented by the intent (and so a provider cannot reinterpret a
		// chunk-signature stream as payload data).
		DisableContentSha256: true,
		SendContentMd5:       true,
		UserMetadata: map[string]string{
			"xm-sha256":            intent.SHA256,
			"xm-provider-checksum": "sha256:" + intent.SHA256,
			"xm-kms-key-id":        intent.KMSKeyID,
			"xm-idempotency-token": intent.ProviderIdempotencyToken,
			"xm-retain-until":      string(intent.RetainUntil),
		},
	}
	// MinIO's conditional-match extension emits If-None-Match: * and is
	// preserved by the signer. A 412 is handled by the content-addressed
	// recovery path below; no second Put is attempted.
	options.SetMatchETagExcept("*")
	info, err := s.client.PutObject(ctx, s.bucketID, intent.Key, bytes.NewReader(raw), intent.SizeBytes, options)
	if err != nil {
		mapped := mapS3Error(err)
		if errors.Is(mapped, ErrObjectConflict) {
			return s.RecoverPutResult(ctx, intent)
		}
		return ObjectVersionV1{}, mapped
	}
	if strings.TrimSpace(info.VersionID) == "" || strings.EqualFold(info.VersionID, "null") || strings.EqualFold(info.VersionID, "latest") {
		return ObjectVersionV1{}, ErrS3VersionIDMissing
	}
	candidate := ObjectVersionV1{
		BucketID: intent.BucketID, Key: intent.Key, VersionID: info.VersionID,
		SHA256: intent.SHA256, SizeBytes: intent.SizeBytes, ContentType: intent.ContentType,
		ProviderChecksum: "sha256:" + intent.SHA256, ETag: strings.Trim(info.ETag, `"`),
		EncryptionMode: S3ArchiveEncryptionMode, KMSKeyID: intent.KMSKeyID,
		ObjectLockMode: S3ArchiveObjectLockMode, RetainUntil: intent.RetainUntil,
	}
	if candidate.ETag == "" {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	return s.headVersion(ctx, candidate, intent.ProviderIdempotencyToken)
}

// RecoverPutResult is intentionally fail-closed until the approved MinIO
// qualification names a provider-native intent-token result lookup. A plain
// S3 HEAD without VersionID is a latest-object discovery operation and cannot
// prove which version was created after an ambiguous PUT; using it here would
// violate the ExactObjectReader contract. The method therefore performs no
// network call and reports ProviderQualificationMissing until that primitive is
// supplied in a separately approved follow-up.
func (s *S3Store) RecoverPutResult(ctx context.Context, intent ObjectWriteIntentV1) (ObjectVersionV1, error) {
	if err := s.validateIntent(intent); err != nil {
		return ObjectVersionV1{}, err
	}
	if err := contextErr(ctx); err != nil {
		return ObjectVersionV1{}, err
	}
	return ObjectVersionV1{}, fmt.Errorf("%w: %w", ErrProviderQualificationMissing, ErrS3RecoveryUnsupported)
}

func (s *S3Store) HeadVersion(ctx context.Context, expected ObjectVersionV1) (ObjectVersionV1, error) {
	if s == nil || s.client == nil {
		return ObjectVersionV1{}, fmt.Errorf("%w: nil store", ErrS3Config)
	}
	if expected.BucketID != s.bucketID {
		return ObjectVersionV1{}, fmt.Errorf("%w: bucket is not bound to store", ErrArchiveValidation)
	}
	if err := ValidateObjectVersion(expected); err != nil {
		return ObjectVersionV1{}, err
	}
	if strings.TrimSpace(expected.VersionID) == "" || strings.EqualFold(expected.VersionID, "null") || strings.EqualFold(expected.VersionID, "latest") {
		return ObjectVersionV1{}, ErrS3VersionIDMissing
	}
	return s.headVersion(ctx, expected, "")
}

func (s *S3Store) headVersion(ctx context.Context, expected ObjectVersionV1, expectedToken string) (ObjectVersionV1, error) {
	if s == nil || s.client == nil {
		return ObjectVersionV1{}, fmt.Errorf("%w: nil store", ErrS3Config)
	}
	if expected.BucketID != s.bucketID {
		return ObjectVersionV1{}, fmt.Errorf("%w: bucket is not bound to store", ErrArchiveValidation)
	}
	if err := ValidateObjectVersion(expected); err != nil {
		return ObjectVersionV1{}, err
	}
	if expected.EncryptionMode != S3ArchiveEncryptionMode || expected.ObjectLockMode != S3ArchiveObjectLockMode || expected.KMSKeyID == "" {
		return ObjectVersionV1{}, fmt.Errorf("%w: approved protection mismatch", ErrArchiveValidation)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectVersionV1{}, err
	}
	info, err := s.client.StatObject(ctx, expected.BucketID, expected.Key, minio.StatObjectOptions{VersionID: expected.VersionID})
	if err != nil {
		return ObjectVersionV1{}, mapS3Error(err)
	}
	actual, err := objectVersionFromInfo(info, expected)
	if err != nil {
		return ObjectVersionV1{}, err
	}
	if expectedToken != "" && info.UserMetadata["xm-idempotency-token"] != expectedToken && info.Metadata.Get("X-Amz-Meta-Xm-Idempotency-Token") != expectedToken {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	return actual, nil
}

func objectVersionFromInfo(info minio.ObjectInfo, expected ObjectVersionV1) (ObjectVersionV1, error) {
	if info.VersionID == "" || strings.EqualFold(info.VersionID, "null") || strings.EqualFold(info.VersionID, "latest") {
		return ObjectVersionV1{}, ErrS3VersionIDMissing
	}
	if info.VersionID != expected.VersionID || info.Key != expected.Key || info.Size != expected.SizeBytes || strings.Trim(info.ETag, `"`) != strings.Trim(expected.ETag, `"`) {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	if strings.TrimSpace(info.ContentType) != strings.TrimSpace(expected.ContentType) {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	metadata := info.Metadata
	if metadata.Get("X-Amz-Server-Side-Encryption") != "AES256" {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	if metadata.Get("X-Amz-Object-Lock-Mode") != S3ArchiveObjectLockMode {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	if metadata.Get("X-Amz-Meta-Xm-Sha256") != expected.SHA256 ||
		metadata.Get("X-Amz-Meta-Xm-Provider-Checksum") != expected.ProviderChecksum ||
		metadata.Get("X-Amz-Meta-Xm-Kms-Key-Id") != expected.KMSKeyID {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	actualRetain, err := parseS3RetentionHeader(metadata.Get("X-Amz-Object-Lock-Retain-Until-Date"))
	if err != nil || string(NewWireTime(actualRetain)) != string(expected.RetainUntil) {
		return ObjectVersionV1{}, ErrS3MetadataMismatch
	}
	return ObjectVersionV1{
		BucketID: expected.BucketID, Key: expected.Key, VersionID: info.VersionID,
		SHA256: expected.SHA256, SizeBytes: info.Size, ContentType: info.ContentType,
		ProviderChecksum: expected.ProviderChecksum, ETag: strings.Trim(info.ETag, `"`),
		EncryptionMode: S3ArchiveEncryptionMode, KMSKeyID: expected.KMSKeyID,
		ObjectLockMode: S3ArchiveObjectLockMode, RetainUntil: expected.RetainUntil,
		RowCount: expected.RowCount,
	}, nil
}

func parseS3RetentionHeader(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, ErrS3MetadataMismatch
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func (s *S3Store) GetVersion(ctx context.Context, expected ObjectVersionV1) (io.ReadCloser, ObjectVersionV1, error) {
	if s == nil || s.client == nil {
		return nil, ObjectVersionV1{}, fmt.Errorf("%w: nil store", ErrS3Config)
	}
	if expected.BucketID != s.bucketID {
		return nil, ObjectVersionV1{}, fmt.Errorf("%w: bucket is not bound to store", ErrArchiveValidation)
	}
	if err := ValidateObjectVersion(expected); err != nil {
		return nil, ObjectVersionV1{}, err
	}
	if strings.TrimSpace(expected.VersionID) == "" || strings.EqualFold(expected.VersionID, "null") || strings.EqualFold(expected.VersionID, "latest") {
		return nil, ObjectVersionV1{}, ErrS3VersionIDMissing
	}
	verified, err := s.HeadVersion(ctx, expected)
	if err != nil {
		return nil, ObjectVersionV1{}, err
	}
	object, err := s.client.GetObject(ctx, expected.BucketID, expected.Key, minio.GetObjectOptions{VersionID: expected.VersionID})
	if err != nil {
		return nil, ObjectVersionV1{}, mapS3Error(err)
	}
	// Force the lazy SDK object to expose the exact GET response metadata before
	// any bytes are returned. A provider that ignores versionId or redirects to
	// another version is rejected at this boundary.
	getInfo, statErr := object.Stat()
	if statErr != nil {
		_ = object.Close()
		return nil, ObjectVersionV1{}, mapS3Error(statErr)
	}
	if _, verifyErr := objectVersionFromInfo(getInfo, verified); verifyErr != nil {
		_ = object.Close()
		return nil, ObjectVersionV1{}, verifyErr
	}
	raw, readErr := io.ReadAll(io.LimitReader(object, verified.SizeBytes+1))
	closeErr := object.Close()
	if readErr != nil {
		return nil, ObjectVersionV1{}, mapS3Error(readErr)
	}
	if closeErr != nil {
		return nil, ObjectVersionV1{}, mapS3Error(closeErr)
	}
	if int64(len(raw)) != verified.SizeBytes || sha256Hex(raw) != verified.SHA256 {
		return nil, ObjectVersionV1{}, fmt.Errorf("%w: body readback", ErrS3MetadataMismatch)
	}
	return io.NopCloser(bytes.NewReader(raw)), verified, nil
}

func mapS3Error(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrS3EndpointNotAllowed) || errors.Is(err, ErrS3Redirect) || errors.Is(err, ErrS3VersionIDMissing) || errors.Is(err, ErrS3MetadataMismatch) {
		return err
	}
	response := minio.ToErrorResponse(err)
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return ErrS3Redirect
	}
	if response.StatusCode == http.StatusNotFound {
		return ErrObjectNotFound
	}
	if response.StatusCode == http.StatusPreconditionFailed || response.StatusCode == http.StatusConflict {
		return ErrObjectConflict
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrS3Credentials
	}
	return err
}
