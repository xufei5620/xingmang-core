package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// These protocol fixtures deliberately use generated values rather than
// credential-shaped literals. They exercise the MinIO wire contract without
// contacting a provider or storing any real credential.
type s3TestSecretProvider struct {
	value secrets.SecretValue
	err   error
}

func (p s3TestSecretProvider) Resolve(context.Context, secrets.CredentialRef, string) (secrets.SecretValue, error) {
	if p.err != nil {
		return secrets.SecretValue{}, p.err
	}
	return p.value, nil
}

func (p s3TestSecretProvider) Metadata(context.Context, secrets.CredentialRef) (secrets.SecretMetadata, error) {
	return secrets.SecretMetadata{Available: p.err == nil}, p.err
}

type s3ProtocolObject struct {
	body       []byte
	versionID  string
	etag       string
	content    string
	sha256     string
	checksum   string
	kmsKeyID   string
	lockMode   string
	retainDate string
	token      string
}

type s3ProtocolFixture struct {
	t            *testing.T
	server       *httptest.Server
	mu           sync.Mutex
	objects      map[string]s3ProtocolObject
	putCount     int
	headCount    int
	getCount     int
	putHeaders   []http.Header
	lastHeadPath string
	omitVersion  bool
	redirect     bool
	wrongMeta    bool
}

func newS3ProtocolFixture(t *testing.T) *s3ProtocolFixture {
	t.Helper()
	f := &s3ProtocolFixture{t: t, objects: make(map[string]s3ProtocolObject)}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *s3ProtocolFixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirect {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
		return
	}
	if r.URL.Path == "/redirected" {
		http.Error(w, "redirected", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPut {
		f.putCount++
		f.putHeaders = append(f.putHeaders, r.Header.Clone())
		if got := r.Header.Get("If-None-Match"); got != "*" {
			f.t.Errorf("If-None-Match = %q, want *", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Fatalf("read put body: %v", err)
		}
		key := strings.TrimPrefix(r.URL.Path, "/archive-test/")
		obj := s3ProtocolObject{
			body:       append([]byte(nil), body...),
			versionID:  "version-" + strings.Repeat("v", 8),
			etag:       "etag-" + strings.Repeat("e", 8),
			content:    r.Header.Get("Content-Type"),
			sha256:     r.Header.Get("X-Amz-Meta-Xm-Sha256"),
			checksum:   r.Header.Get("X-Amz-Meta-Xm-Provider-Checksum"),
			kmsKeyID:   r.Header.Get("X-Amz-Meta-Xm-Kms-Key-Id"),
			lockMode:   r.Header.Get("X-Amz-Object-Lock-Mode"),
			retainDate: r.Header.Get("X-Amz-Object-Lock-Retain-Until-Date"),
			token:      r.Header.Get("X-Amz-Meta-Xm-Idempotency-Token"),
		}
		if f.wrongMeta {
			obj.lockMode = "GOVERNANCE"
		}
		f.objects[key] = obj
		w.Header().Set("ETag", `"`+obj.etag+`"`)
		if obj.versionID != "" && !f.omitVersion {
			w.Header().Set("x-amz-version-id", obj.versionID)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/archive-test/")
	obj, ok := f.objects[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodHead {
		f.headCount++
		f.lastHeadPath = r.URL.RequestURI()
		if version := r.URL.Query().Get("versionId"); version != "" && version != obj.versionID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.writeMetadata(w, obj)
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodGet {
		f.getCount++
		if version := r.URL.Query().Get("versionId"); version == "" || version != obj.versionID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.writeMetadata(w, obj)
		_, _ = w.Write(obj.body)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (f *s3ProtocolFixture) writeMetadata(w http.ResponseWriter, obj s3ProtocolObject) {
	w.Header().Set("ETag", `"`+obj.etag+`"`)
	w.Header().Set("Content-Length", ""+itoa(len(obj.body)))
	w.Header().Set("Content-Type", obj.content)
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.Header().Set("x-amz-version-id", obj.versionID)
	w.Header().Set("X-Amz-Server-Side-Encryption", "AES256")
	w.Header().Set("X-Amz-Object-Lock-Mode", obj.lockMode)
	w.Header().Set("X-Amz-Object-Lock-Retain-Until-Date", obj.retainDate)
	w.Header().Set("X-Amz-Meta-Xm-Sha256", obj.sha256)
	w.Header().Set("X-Amz-Meta-Xm-Provider-Checksum", obj.checksum)
	w.Header().Set("X-Amz-Meta-Xm-Kms-Key-Id", obj.kmsKeyID)
	w.Header().Set("X-Amz-Meta-Xm-Idempotency-Token", obj.token)
}

// itoa is kept local so this protocol fixture does not pull formatting into
// production code and remains obvious in request assertions.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out [32]byte
	i := len(out)
	for n > 0 {
		i--
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out[i:])
}

func s3TestCredentials(t *testing.T) secrets.SecretProvider {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"access_key": "access-" + strings.Repeat("a", 12),
		"secret_key": "secret-" + strings.Repeat("b", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s3TestSecretProvider{value: secrets.NewSecretValue(payload)}
}

func s3TestIntent(body []byte) ObjectWriteIntentV1 {
	digest := sha256Hex(body)
	return ObjectWriteIntentV1{
		OperationID:              uuidForS3Test(),
		Ordinal:                  0,
		BucketID:                 "archive-test",
		Key:                      "audit/v1/payload/sha256/" + digest + ".ndjson",
		SHA256:                   digest,
		SizeBytes:                int64(len(body)),
		ContentType:              PayloadContentType,
		EncryptionMode:           "SSE-S3",
		KMSKeyID:                 "archive-key-label",
		ObjectLockMode:           "COMPLIANCE",
		RetainUntil:              NewWireTime(time.Date(2036, 8, 30, 0, 0, 0, 0, time.UTC)),
		ProviderIdempotencyToken: "op-token-" + strings.Repeat("t", 12),
	}
}

func uuidForS3Test() uuid.UUID { return uuid.New() }

func s3TestConfig(t *testing.T, f *s3ProtocolFixture) S3StoreConfig {
	t.Helper()
	u, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return S3StoreConfig{
		Endpoint:          f.server.URL,
		EndpointAllowlist: []string{u.Host},
		AllowInsecureHTTP: true,
		BucketID:          "archive-test",
		CredentialRef:     secrets.MustCredentialRef("secret://archive/test"),
		Secrets:           s3TestCredentials(t),
		Region:            "local",
	}
}

func TestS3StoreImplementsExactCapabilities(t *testing.T) {
	var _ ObjectWriter = (*S3Store)(nil)
	var _ ExactObjectReader = (*S3Store)(nil)
}

func TestS3CredentialsRejectDuplicateAndTrailingJSON(t *testing.T) {
	if _, err := ParseS3Credentials(`{"access_key":"a","access_key":"b","secret_key":"c"}`); !errors.Is(err, ErrS3Credentials) {
		t.Fatalf("duplicate key error = %v", err)
	}
	if _, err := ParseS3Credentials(`{"access_key":"a","ACCESS_KEY":"b","secret_key":"c"}`); !errors.Is(err, ErrS3Credentials) {
		t.Fatalf("case-fold duplicate key error = %v", err)
	}
	if _, err := ParseS3Credentials(`{"access_key":"a","secret_key":"b"} {}`); !errors.Is(err, ErrS3Credentials) {
		t.Fatalf("trailing value error = %v", err)
	}
}

func TestS3PutIfAbsentUsesConditionalSSES3KnownSizeAndReadback(t *testing.T) {
	f := newS3ProtocolFixture(t)
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("protocol-payload")
	intent := s3TestIntent(body)
	got, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader(body))
	if err != nil {
		f.mu.Lock()
		t.Logf("put failure: objects=%+v headers=%v", f.objects, f.putHeaders)
		f.mu.Unlock()
		t.Fatal(err)
	}
	if got.VersionID == "" || got.SHA256 != intent.SHA256 || got.SizeBytes != int64(len(body)) {
		t.Fatalf("unexpected object version: %+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putCount != 1 {
		t.Fatalf("put count = %d, want 1", f.putCount)
	}
	h := f.putHeaders[0]
	if h.Get("If-None-Match") != "*" || h.Get("X-Amz-Server-Side-Encryption") != "AES256" {
		t.Fatalf("conditional/SSE headers missing: %v", h)
	}
	if h.Get("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id") != "" {
		t.Fatalf("AWS KMS header unexpectedly present: %v", h)
	}
	if h.Get("X-Amz-Object-Lock-Mode") != "COMPLIANCE" || h.Get("X-Amz-Meta-Xm-Sha256") != intent.SHA256 {
		t.Fatalf("lock/hash headers missing: %v", h)
	}
	if h.Get("Content-Length") != itoa(len(body)) {
		t.Fatalf("content length = %q", h.Get("Content-Length"))
	}
}

func TestS3PutIfAbsentRejectsDigestOrSizeBeforeNetwork(t *testing.T) {
	f := newS3ProtocolFixture(t)
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	intent := s3TestIntent([]byte("expected"))
	intent.SizeBytes++
	if _, err := store.PutIfAbsent(context.Background(), intent, strings.NewReader("expected")); err == nil {
		t.Fatal("size mismatch unexpectedly accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putCount != 0 {
		t.Fatalf("network put count = %d, want 0", f.putCount)
	}
}

func TestS3PutIfAbsentRejectsEmptyVersionID(t *testing.T) {
	f := newS3ProtocolFixture(t)
	f.omitVersion = true
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutIfAbsent(context.Background(), s3TestIntent([]byte("no-version")), strings.NewReader("no-version"))
	if !errors.Is(err, ErrS3VersionIDMissing) {
		t.Fatalf("error = %v, want ErrS3VersionIDMissing", err)
	}
}

func TestS3HeadAndGetRequireExactVersionAndVerifyMetadata(t *testing.T) {
	f := newS3ProtocolFixture(t)
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("exact-version")
	intent := s3TestIntent(body)
	version, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader(body))
	if err != nil {
		f.mu.Lock()
		t.Logf("put failure: objects=%+v headers=%v", f.objects, f.putHeaders)
		f.mu.Unlock()
		t.Fatal(err)
	}
	head, err := store.HeadVersion(context.Background(), version)
	if err != nil || head.VersionID != version.VersionID {
		t.Fatalf("head = %+v, err=%v", head, err)
	}
	rc, readback, err := store.GetVersion(context.Background(), version)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	gotBody, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("body = %q, err=%v", gotBody, err)
	}
	if readback.VersionID != version.VersionID || readback.SHA256 != version.SHA256 {
		t.Fatalf("readback = %+v", readback)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.lastHeadPath, "versionId=") {
		t.Fatalf("head did not carry exact version: %q", f.lastHeadPath)
	}
}

func TestS3HeadAndGetRejectUnboundBucket(t *testing.T) {
	f := newS3ProtocolFixture(t)
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	expected := s3TestIntent([]byte("bucket-bound"))
	ref := ObjectVersionV1{
		BucketID: "other-bucket", Key: expected.Key, VersionID: "version-" + strings.Repeat("v", 8),
		SHA256: expected.SHA256, SizeBytes: expected.SizeBytes, ContentType: expected.ContentType,
		ProviderChecksum: "sha256:" + expected.SHA256, ETag: "etag-" + strings.Repeat("e", 8),
		EncryptionMode: S3ArchiveEncryptionMode, KMSKeyID: expected.KMSKeyID,
		ObjectLockMode: S3ArchiveObjectLockMode, RetainUntil: expected.RetainUntil,
	}
	if _, err := store.HeadVersion(context.Background(), ref); !errors.Is(err, ErrArchiveValidation) {
		t.Fatalf("head unbound bucket error = %v", err)
	}
	if _, _, err := store.GetVersion(context.Background(), ref); !errors.Is(err, ErrArchiveValidation) {
		t.Fatalf("get unbound bucket error = %v", err)
	}
}

func TestS3RecoverPutResultFailsClosedWithoutProviderNativePrimitive(t *testing.T) {
	f := newS3ProtocolFixture(t)
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	intent := s3TestIntent([]byte("ambiguous-response"))
	f.mu.Lock()
	beforeHead, beforePut := f.headCount, f.putCount
	f.mu.Unlock()
	_, err = store.RecoverPutResult(context.Background(), intent)
	if !errors.Is(err, ErrProviderQualificationMissing) || !errors.Is(err, ErrS3RecoveryUnsupported) {
		t.Fatalf("error = %v, want ProviderQualificationMissing/S3RecoveryUnsupported", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putCount != beforePut || f.headCount != beforeHead {
		t.Fatalf("recovery made network calls: puts=%d heads=%d", f.putCount, f.headCount)
	}
}

func TestS3ReadbackRejectsProtectionMetadataMismatch(t *testing.T) {
	f := newS3ProtocolFixture(t)
	f.wrongMeta = true
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutIfAbsent(context.Background(), s3TestIntent([]byte("wrong-lock")), strings.NewReader("wrong-lock"))
	if !errors.Is(err, ErrS3MetadataMismatch) {
		t.Fatalf("error = %v, want metadata mismatch", err)
	}
}

func TestS3ConstructorRejectsEndpointAndCredentialFailures(t *testing.T) {
	f := newS3ProtocolFixture(t)
	cfg := s3TestConfig(t, f)
	cfg.EndpointAllowlist = []string{"not-" + f.server.Listener.Addr().String()}
	if _, err := NewS3Store(context.Background(), cfg); !errors.Is(err, ErrS3EndpointNotAllowed) {
		t.Fatalf("endpoint error = %v", err)
	}
	cfg = s3TestConfig(t, f)
	cfg.Secrets = nil
	if _, err := NewS3Store(context.Background(), cfg); !errors.Is(err, ErrS3Credentials) {
		t.Fatalf("credential error = %v", err)
	}
	cfg = s3TestConfig(t, f)
	cfg.CredentialRef = secrets.CredentialRef{}
	if _, err := NewS3Store(context.Background(), cfg); !errors.Is(err, ErrS3Credentials) {
		t.Fatalf("empty ref error = %v", err)
	}
}

func TestS3RejectsRedirectResponses(t *testing.T) {
	f := newS3ProtocolFixture(t)
	f.redirect = true
	store, err := NewS3Store(context.Background(), s3TestConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutIfAbsent(context.Background(), s3TestIntent([]byte("redirect")), strings.NewReader("redirect"))
	if !errors.Is(err, ErrS3Redirect) {
		t.Fatalf("error = %v, want ErrS3Redirect", err)
	}
}

func TestS3LiveQualificationProviderQualificationMissing(t *testing.T) {
	if os.Getenv("XM_REQUIRE_AUDIT_ARCHIVE_PROVIDER_QUALIFICATION") == "1" {
		t.Fatalf("%v: disposable MinIO credential ref is not configured", ErrProviderQualificationMissing)
	}
	t.Skip("ProviderQualificationMissing: live MinIO qualification requires one-time approved credential")
}
