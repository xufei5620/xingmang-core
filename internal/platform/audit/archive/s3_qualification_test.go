package archive

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

const (
	minIOQualificationRunEnv     = "XM_RUN_AUDIT_ARCHIVE_MINIO_QUALIFICATION"
	minIOQualificationRequireEnv = "XM_REQUIRE_AUDIT_ARCHIVE_PROVIDER_QUALIFICATION"
	minIOQualificationEnvPrefix  = "XM_AUDIT_QUAL_SECRET_"
)

// TestS3LiveQualificationDisposableMinIO is intentionally opt-in. It owns a
// random Compose project/volume and never targets xingmang-launch. When the
// require flag is set, inability to start the disposable provider is a hard
// failure rather than a silent skip.
func TestS3LiveQualificationDisposableMinIO(t *testing.T) {
	require := os.Getenv(minIOQualificationRequireEnv) == "1"
	run := require || os.Getenv(minIOQualificationRunEnv) == "1"
	if !run {
		t.Skip("ProviderQualificationMissing: set XM_RUN_AUDIT_ARCHIVE_MINIO_QUALIFICATION=1 for disposable MinIO qualification")
	}
	err := runDisposableMinIOQualification(t)
	if err == nil {
		return
	}
	if errors.Is(err, ErrProviderQualificationMissing) && !require {
		t.Skip("ProviderQualificationMissing: " + qualificationErrorText(err))
	}
	t.Fatal(qualificationErrorText(err))
}

func TestReadMinIOImagePinUsesRepositoryLock(t *testing.T) {
	pin, err := readMinIOImagePin()
	if err != nil {
		t.Fatal(err)
	}
	if pin.image != "docker.io/minio/minio:RELEASE.2025-04-22T22-12-26Z" ||
		pin.digest != "sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e" {
		t.Fatalf("unexpected MinIO image pin: image=%q digest=%q", pin.image, pin.digest)
	}
}

func TestQualificationComposeDelegatesEphemeralPortToDocker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := writeQualificationCompose(path, "docker.io/minio/minio@sha256:"+strings.Repeat("a", 64), "run-id", "archive-data"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, `127.0.0.1::9000`) || strings.Contains(text, `127.0.0.1:9000:9000`) {
		t.Fatalf("compose must use Docker-assigned loopback port: %s", text)
	}
}

func qualificationErrorText(err error) string {
	if err == nil {
		return ""
	}
	// Docker errors can contain command-line environment details. Keep the
	// evidence projection deliberately short and secret-free.
	return strings.TrimSpace(err.Error())
}

type minIOImagePin struct {
	image  string
	digest string
}

func runDisposableMinIOQualification(t *testing.T) (retErr error) {
	t.Helper()
	if err := validateQualificationEnvironment(); err != nil {
		return err
	}
	pin, err := readMinIOImagePin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	imageRef := pin.image + "@" + pin.digest
	if err := ensureQualificationImage(ctx, imageRef, pin.digest); err != nil {
		return err
	}

	runID := randomQualificationID("run")
	project := randomQualificationID("xm-aud2-q")
	volumeKey := "archive-data"
	port := 0
	tmpRoot, err := os.MkdirTemp("", "xm-aud2-minio-")
	if err != nil {
		return fmt.Errorf("%w: temporary directory: %v", ErrProviderQualificationMissing, err)
	}
	defer os.RemoveAll(tmpRoot)
	composePath := filepath.Join(tmpRoot, "compose.yaml")
	rootUser := randomSecret("root")
	rootPassword := randomSecret("password")
	kmsSecret := randomKMSSecret()
	if err := writeQualificationCompose(composePath, imageRef, runID, volumeKey); err != nil {
		return err
	}
	// Compose receives generated credentials only through process environment;
	// they are never put in the repository, command arguments, logs, or evidence.
	t.Setenv("XM_AUD2_MINIO_ROOT_USER", rootUser)
	t.Setenv("XM_AUD2_MINIO_ROOT_PASSWORD", rootPassword)
	t.Setenv("XM_AUD2_MINIO_KMS_SECRET_KEY", kmsSecret)
	started := false
	defer func() {
		if !started {
			return
		}
		if err := teardownQualification(ctx, composePath, project, runID, port); retErr == nil && err != nil {
			retErr = err
		}
	}()

	if _, err := runDocker(ctx, "compose", "-p", project, "-f", composePath, "config", "--quiet"); err != nil {
		return fmt.Errorf("%w: compose config: %v", ErrProviderQualificationMissing, err)
	}
	if err := assertNoQualificationResources(ctx, project, runID); err != nil {
		return err
	}
	// From this point the harness owns the random project even if Compose
	// creates only a partial container before returning an error.
	started = true
	if _, err := runDocker(ctx, "compose", "-p", project, "-f", composePath, "up", "-d", "minio"); err != nil {
		return fmt.Errorf("%w: compose start: %v", ErrProviderQualificationMissing, err)
	}
	port, err = qualificationPort(ctx, composePath, project)
	if err != nil {
		return err
	}
	if err := waitQualificationReady(ctx, port); err != nil {
		return err
	}
	containerID, err := qualificationContainerID(ctx, composePath, project)
	if err != nil {
		return err
	}
	if err := verifyQualificationContainer(ctx, composePath, containerID, project, runID, pin.digest, port); err != nil {
		return err
	}

	// Map the generated root credential through the approved CredentialRef and
	// ConventionEnvProvider; no adapter path reads these environment variables
	// directly.
	credentialRef := secrets.MustCredentialRef("secret://archive/minio-qualification")
	payload, err := json.Marshal(S3Credentials{AccessKey: rootUser, SecretKey: rootPassword})
	if err != nil {
		return fmt.Errorf("%w: credential payload", ErrProviderQualificationMissing)
	}
	envName, err := secrets.ConventionEnvName(minIOQualificationEnvPrefix, credentialRef)
	if err != nil {
		return fmt.Errorf("%w: credential mapping", ErrProviderQualificationMissing)
	}
	t.Setenv(envName, string(payload))
	provider, err := secrets.NewConventionEnvProvider(minIOQualificationEnvPrefix, []string{"archive"}, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("%w: secret provider: %v", ErrProviderQualificationMissing, err)
	}
	bucket := "aud2q" + randomSecret("bucket")[:18]
	dropper := &qualificationResponseDropTransport{base: http.DefaultTransport}
	store, err := NewS3Store(ctx, S3StoreConfig{
		Endpoint:          fmt.Sprintf("http://127.0.0.1:%d", port),
		EndpointAllowlist: []string{fmt.Sprintf("127.0.0.1:%d", port)},
		AllowInsecureHTTP: true,
		BucketID:          bucket,
		Region:            "local",
		CredentialRef:     credentialRef,
		Secrets:           provider,
		HTTPClient:        &http.Client{Transport: dropper},
	})
	if err != nil {
		return fmt.Errorf("%w: adapter construction: %v", ErrProviderQualificationMissing, err)
	}
	if err := qualifyBucket(ctx, store.client, bucket); err != nil {
		return err
	}
	body := []byte("aud2-minio-qualification")
	intent := s3QualificationIntent(body, bucket)
	dropper.Arm()
	_, putErr := store.PutIfAbsent(ctx, intent, strings.NewReader(string(body)))
	if putErr == nil {
		return fmt.Errorf("%w: response-loss fault was not observed", ErrProviderQualificationFailed)
	}
	// The provider committed the object before the transport dropped the
	// response. Recovery must use exact-key ListObjectVersions and then an
	// exact VersionID HEAD; no second PUT is allowed.
	recovered, err := store.RecoverPutResult(ctx, intent)
	if err != nil {
		return fmt.Errorf("%w: ambiguous recovery: %v", ErrProviderQualificationFailed, err)
	}
	if dropper.PutAttempts() != 1 {
		return fmt.Errorf("%w: response-loss path issued %d PUTs", ErrProviderQualificationFailed, dropper.PutAttempts())
	}
	rc, version, err := store.GetVersion(ctx, recovered)
	if err != nil {
		return fmt.Errorf("%w: exact get: %v", ErrProviderQualificationMissing, err)
	}
	readback, readErr := io.ReadAll(rc)
	_ = rc.Close()
	if readErr != nil || string(readback) != string(body) || version.VersionID != recovered.VersionID {
		return fmt.Errorf("%w: exact body/version readback", ErrProviderQualificationFailed)
	}
	return nil
}

func validateQualificationEnvironment() error {
	for _, name := range []string{"DOCKER_HOST", "COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return fmt.Errorf("%w: external %s override is forbidden", ErrProviderQualificationMissing, name)
		}
	}
	if contextName := strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")); contextName != "" && contextName != "default" {
		return fmt.Errorf("%w: Docker context must be default", ErrProviderQualificationMissing)
	}
	return nil
}

func readMinIOImagePin() (minIOImagePin, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return minIOImagePin{}, fmt.Errorf("%w: locate repository", ErrProviderQualificationMissing)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".."))
	lock, err := os.ReadFile(filepath.Join(root, "VERSIONS.lock"))
	if err != nil {
		return minIOImagePin{}, fmt.Errorf("%w: read VERSIONS.lock", ErrProviderQualificationMissing)
	}
	imageMatch := regexp.MustCompile(`(?m)^minio\.image\s*=\s*(\S+)\s*$`).FindSubmatch(lock)
	digestMatch := regexp.MustCompile(`(?m)^minio\.digest\s*=\s*(sha256:[0-9a-f]{64})\s*$`).FindSubmatch(lock)
	if len(imageMatch) != 2 || len(digestMatch) != 2 {
		return minIOImagePin{}, fmt.Errorf("%w: VERSIONS.lock lacks exact MinIO image/digest", ErrProviderQualificationMissing)
	}
	return minIOImagePin{image: string(imageMatch[1]), digest: string(digestMatch[1])}, nil
}

func ensureQualificationImage(ctx context.Context, imageRef, digest string) error {
	// Registry access is an external qualification prerequisite. Bound it
	// separately from the full harness so an unavailable registry cannot hold
	// the test (or a Docker layer lock) for four minutes.
	imageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := runDocker(imageCtx, "image", "inspect", imageRef, "--format", "{{json .RepoDigests}}")
	if err != nil {
		if _, pullErr := runDocker(imageCtx, "pull", imageRef); pullErr != nil {
			return fmt.Errorf("%w: pinned MinIO image unavailable", ErrProviderQualificationMissing)
		}
		output, err = runDocker(imageCtx, "image", "inspect", imageRef, "--format", "{{json .RepoDigests}}")
	}
	if err != nil || !strings.Contains(output, "@"+digest) {
		return fmt.Errorf("%w: MinIO RepoDigest does not match VERSIONS.lock", ErrProviderQualificationMissing)
	}
	return nil
}

func writeQualificationCompose(path, imageRef, runID, volumeKey string) error {
	content := fmt.Sprintf(`services:
  minio:
    image: %s
    command: ["server", "/data", "--address", ":9000"]
    environment:
      MINIO_ROOT_USER: ${XM_AUD2_MINIO_ROOT_USER}
      MINIO_ROOT_PASSWORD: ${XM_AUD2_MINIO_ROOT_PASSWORD}
      MINIO_KMS_SECRET_KEY: ${XM_AUD2_MINIO_KMS_SECRET_KEY}
    ports:
      - "127.0.0.1::9000"
    volumes:
      - %s:/data
    labels:
      com.xingmang.audit-qualification: "true"
      com.xingmang.audit-qualification.run-id: "%s"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/ready"]
      interval: 1s
      timeout: 2s
      retries: 30
volumes:
  %s:
    labels:
      com.xingmang.audit-qualification: "true"
      com.xingmang.audit-qualification.run-id: "%s"
`, imageRef, volumeKey, runID, volumeKey, runID)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("%w: write compose fixture", ErrProviderQualificationMissing)
	}
	return nil
}

func assertNoQualificationResources(ctx context.Context, project, runID string) error {
	checks := [][]string{
		{"ps", "-aq", "--filter", "label=com.docker.compose.project=" + project},
		{"volume", "ls", "-q", "--filter", "label=com.xingmang.audit-qualification.run-id=" + runID},
		{"network", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project},
	}
	for _, args := range checks {
		output, err := runDocker(ctx, args...)
		if err != nil {
			return fmt.Errorf("%w: preflight resource check", ErrProviderQualificationMissing)
		}
		if strings.TrimSpace(output) != "" {
			return fmt.Errorf("%w: random qualification labels already exist", ErrProviderQualificationMissing)
		}
	}
	return nil
}

func qualificationContainerID(ctx context.Context, composePath, project string) (string, error) {
	output, err := runDocker(ctx, "compose", "-p", project, "-f", composePath, "ps", "-q", "minio")
	if err != nil || strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("%w: discover MinIO container", ErrProviderQualificationMissing)
	}
	return strings.TrimSpace(output), nil
}

func qualificationPort(ctx context.Context, composePath, project string) (int, error) {
	output, err := runDocker(ctx, "compose", "-p", project, "-f", composePath, "port", "minio", "9000")
	if err != nil {
		return 0, fmt.Errorf("%w: discover assigned loopback port", ErrProviderQualificationMissing)
	}
	host, portText, splitErr := net.SplitHostPort(strings.TrimSpace(output))
	if splitErr != nil || host != "127.0.0.1" {
		return 0, fmt.Errorf("%w: assigned port is not loopback-only", ErrProviderQualificationMissing)
	}
	port, parseErr := strconv.Atoi(portText)
	if parseErr != nil || port <= 1024 || port > 65535 {
		return 0, fmt.Errorf("%w: assigned port is not ephemeral", ErrProviderQualificationMissing)
	}
	return port, nil
}

func verifyQualificationContainer(ctx context.Context, composePath, containerID, project, runID, digest string, port int) error {
	labels, err := runDocker(ctx, "inspect", "--format", "{{json .Config.Labels}}", containerID)
	if err != nil {
		return fmt.Errorf("%w: inspect labels", ErrProviderQualificationMissing)
	}
	var labelMap map[string]string
	if json.Unmarshal([]byte(labels), &labelMap) != nil || labelMap["com.docker.compose.project"] != project || labelMap["com.xingmang.audit-qualification.run-id"] != runID {
		return fmt.Errorf("%w: container labels mismatch", ErrProviderQualificationMissing)
	}
	repoDigests, err := runDocker(ctx, "inspect", "--format", "{{json .RepoDigests}}", containerID)
	if err != nil || !strings.Contains(repoDigests, "@"+digest) {
		// Container inspect may expose image ID rather than RepoDigests; inspect
		// the image object as the authoritative digest source.
		imageID, imageErr := runDocker(ctx, "inspect", "--format", "{{.Image}}", containerID)
		if imageErr != nil {
			return fmt.Errorf("%w: inspect image", ErrProviderQualificationMissing)
		}
		repoDigests, imageErr = runDocker(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", strings.TrimSpace(imageID))
		if imageErr != nil || !strings.Contains(repoDigests, "@"+digest) {
			return fmt.Errorf("%w: container RepoDigest mismatch", ErrProviderQualificationMissing)
		}
	}
	imageID, err := runDocker(ctx, "inspect", "--format", "{{.Image}}", containerID)
	if err != nil {
		return fmt.Errorf("%w: inspect image id", ErrProviderQualificationMissing)
	}
	imageInfo, err := runDocker(ctx, "image", "inspect", "--format", "{{json .}}", strings.TrimSpace(imageID))
	if err != nil {
		return fmt.Errorf("%w: inspect image platform", ErrProviderQualificationMissing)
	}
	var platform struct {
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
	}
	if json.Unmarshal([]byte(imageInfo), &platform) != nil || platform.OS != "linux" || platform.Architecture == "" {
		return fmt.Errorf("%w: MinIO container platform mismatch", ErrProviderQualificationMissing)
	}
	portOutput, err := runDocker(ctx, "compose", "-p", project, "-f", composePath, "port", "minio", "9000")
	if err != nil {
		return fmt.Errorf("%w: inspect loopback port", ErrProviderQualificationMissing)
	}
	host, discoveredPort, splitErr := net.SplitHostPort(strings.TrimSpace(portOutput))
	if splitErr != nil || host != "127.0.0.1" || discoveredPort != fmt.Sprintf("%d", port) {
		return fmt.Errorf("%w: port mapping is not loopback random port", ErrProviderQualificationMissing)
	}
	return nil
}

func waitQualificationReady(ctx context.Context, port int) error {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/minio/health/ready", port)
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: MinIO readiness timeout", ErrProviderQualificationMissing)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func qualifyBucket(ctx context.Context, client *minio.Client, bucket string) error {
	if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "local", ObjectLocking: true}); err != nil {
		return fmt.Errorf("%w: create lock bucket", ErrProviderQualificationMissing)
	}
	if err := client.EnableVersioning(ctx, bucket); err != nil {
		return fmt.Errorf("%w: enable versioning", ErrProviderQualificationMissing)
	}
	versioning, err := client.GetBucketVersioning(ctx, bucket)
	if err != nil || !versioning.Enabled() {
		return fmt.Errorf("%w: versioning is not enabled", ErrProviderQualificationFailed)
	}
	mode := minio.Compliance
	days := uint(3650)
	unit := minio.Days
	if err := client.SetBucketObjectLockConfig(ctx, bucket, &mode, &days, &unit); err != nil {
		return fmt.Errorf("%w: configure object lock", ErrProviderQualificationMissing)
	}
	gotMode, gotDays, gotUnit, err := client.GetBucketObjectLockConfig(ctx, bucket)
	if err != nil || gotMode == nil || *gotMode != minio.Compliance || gotDays == nil || *gotDays != days || gotUnit == nil || *gotUnit != minio.Days {
		return fmt.Errorf("%w: object lock configuration mismatch", ErrProviderQualificationFailed)
	}
	return nil
}

func s3QualificationIntent(body []byte, bucket string) ObjectWriteIntentV1 {
	retain := time.Now().UTC().Add(3650 * 24 * time.Hour).Truncate(time.Second)
	digest := sha256Hex(body)
	return ObjectWriteIntentV1{
		OperationID:              uuidForQualification(),
		Ordinal:                  0,
		BucketID:                 bucket,
		Key:                      "audit/v1/qualification/sha256/" + digest + ".ndjson",
		SHA256:                   digest,
		SizeBytes:                int64(len(body)),
		ContentType:              PayloadContentType,
		EncryptionMode:           S3ArchiveEncryptionMode,
		KMSKeyID:                 "minio-sse-s3-label",
		ObjectLockMode:           S3ArchiveObjectLockMode,
		RetainUntil:              NewWireTime(retain),
		ProviderIdempotencyToken: "qualification-" + randomSecret("token")[:16],
	}
}

func uuidForQualification() uuid.UUID { return uuid.New() }

func randomQualificationID(prefix string) string {
	return strings.ToLower(prefix + "-" + randomSecret("id")[:16])
}

func randomSecret(prefix string) string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(raw[:])
}

func randomKMSSecret() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return "qualification-key:" + base64.StdEncoding.EncodeToString(raw[:])
}

func runDocker(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// qualificationResponseDropTransport injects the one fault that cannot be
// represented by an httptest protocol fixture: the provider has committed the
// object but the caller loses the PUT response (and therefore VersionID).
type qualificationResponseDropTransport struct {
	base  http.RoundTripper
	armed atomic.Bool
	puts  atomic.Int32
}

func (t *qualificationResponseDropTransport) Arm() { t.armed.Store(true) }

func (t *qualificationResponseDropTransport) PutAttempts() int32 { return t.puts.Load() }

func (t *qualificationResponseDropTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.armed.Load() && req.Method == http.MethodPut && t.puts.CompareAndSwap(0, 1) {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return nil, errors.New("qualification response dropped after provider commit")
	}
	return resp, nil
}

func teardownQualification(ctx context.Context, composePath, project, runID string, port int) error {
	if _, err := runDocker(ctx, "compose", "-p", project, "-f", composePath, "down", "--volumes", "--remove-orphans"); err != nil {
		return fmt.Errorf("%w: qualification teardown", ErrProviderQualificationMissing)
	}
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", "label=com.docker.compose.project=" + project},
		{"volume", "ls", "-q", "--filter", "label=com.xingmang.audit-qualification.run-id=" + runID},
		{"network", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project},
	} {
		output, err := runDocker(ctx, args...)
		if err != nil || strings.TrimSpace(output) != "" {
			return fmt.Errorf("%w: qualification resources remain", ErrProviderQualificationMissing)
		}
	}
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 750*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: qualification port remains open", ErrProviderQualificationMissing)
	}
	return nil
}
