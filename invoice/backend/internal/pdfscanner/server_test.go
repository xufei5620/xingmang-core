package pdfscanner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"invoice-system/backend/internal/document"
)

type recordingScanner struct {
	mu      sync.Mutex
	content string
	path    string
	scanErr error
	pingErr error
}

func (s *recordingScanner) Ping(context.Context) error { return s.pingErr }

func (s *recordingScanner) Scan(_ context.Context, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.content = string(body)
	s.path = path
	s.mu.Unlock()
	return s.scanErr
}

func TestSidecarStreamsBytesAndCleansPrivateTemporaryFile(t *testing.T) {
	server, client, scanner, cancel, result := startTestServer(t)
	defer stopTestServer(t, cancel, result)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	documentPath := filepath.Join(t.TempDir(), "invoice.pdf")
	content := "%PDF-1.7\nsidecar-test"
	if err := os.WriteFile(documentPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Scan(context.Background(), documentPath); err != nil {
		t.Fatal(err)
	}
	scanner.mu.Lock()
	scannedContent, scannedPath := scanner.content, scanner.path
	scanner.mu.Unlock()
	if scannedContent != content {
		t.Fatalf("scanned content=%q", scannedContent)
	}
	if !strings.HasPrefix(scannedPath, server.TempDirectory+string(os.PathSeparator)) {
		t.Fatalf("sidecar received non-private path %q", scannedPath)
	}
	if _, err := os.Stat(scannedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary scan file was not removed: %v", err)
	}
}

func TestSidecarRejectsUnsafePDFAndWrongCapability(t *testing.T) {
	server, client, scanner, cancel, result := startTestServer(t)
	defer stopTestServer(t, cancel, result)
	documentPath := filepath.Join(t.TempDir(), "invoice.pdf")
	if err := os.WriteFile(documentPath, []byte("%PDF-1.7\nunsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner.scanErr = document.ErrUnsafePDF
	if err := client.Scan(context.Background(), documentPath); !errors.Is(err, document.ErrUnsafePDF) {
		t.Fatalf("unsafe error=%v", err)
	}
	wrongToken := filepath.Join(filepath.Dir(server.TokenFile), "wrong-token")
	if err := os.WriteFile(wrongToken, []byte(strings.Repeat("0", 64)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	client.TokenFile = wrongToken
	if err := client.Ping(context.Background()); !errors.Is(err, document.ErrQPDFUnavailable) {
		t.Fatalf("wrong capability error=%v", err)
	}
}

func TestSidecarFailsClosedOnOversizedAndUnavailablePolicy(t *testing.T) {
	_, client, scanner, cancel, result := startTestServer(t)
	defer stopTestServer(t, cancel, result)
	documentPath := filepath.Join(t.TempDir(), "invoice.pdf")
	if err := os.WriteFile(documentPath, []byte("%PDF-1.7\nlarge"), 0o600); err != nil {
		t.Fatal(err)
	}
	client.MaxBytes = 4
	if err := client.Scan(context.Background(), documentPath); !errors.Is(err, document.ErrTooLarge) {
		t.Fatalf("oversized error=%v", err)
	}
	client.MaxBytes = document.DefaultMaxPDFBytes
	scanner.pingErr = errors.New("qpdf unavailable")
	if err := client.Ping(context.Background()); !errors.Is(err, document.ErrQPDFUnavailable) {
		t.Fatalf("unavailable error=%v", err)
	}
}

func TestHandlerAuthenticatesBoundsAndMapsPolicyResults(t *testing.T) {
	tempDirectory := t.TempDir()
	scanner := &recordingScanner{}
	server := &Server{
		TempDirectory: tempDirectory, MaxBytes: 64, Timeout: time.Second,
		Scanner: scanner, capability: strings.Repeat("a", 64), semaphore: make(chan struct{}, 1),
	}
	request := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/pdf")
		req.Header.Set("X-Invoice-Scanner-Capability", token)
		recorder := httptest.NewRecorder()
		server.handler().ServeHTTP(recorder, req)
		return recorder
	}
	if response := request(strings.Repeat("b", 64), "%PDF-1.7"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
	if response := request(strings.Repeat("a", 64), "%PDF-1.7\nclean"); response.Code != http.StatusOK {
		t.Fatalf("clean status=%d body=%s", response.Code, response.Body.String())
	}
	scanner.scanErr = document.ErrUnsafePDF
	if response := request(strings.Repeat("a", 64), "%PDF-1.7\nunsafe"); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"unsafe_pdf"`) {
		t.Fatalf("unsafe status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(strings.Repeat("a", 64), strings.Repeat("x", 65)); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", response.Code)
	}
}

func startTestServer(t *testing.T) (*Server, document.SidecarScanner, *recordingScanner, context.CancelFunc, <-chan error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// AF_UNIX is available on supported Windows versions, but Unix socket
		// file modes are not enforceable there. Production is Linux-only.
		t.Skip("scanner sidecar permission contract is Linux-only")
	}
	root := t.TempDir()
	socketDirectory := filepath.Join(root, "socket")
	tempDirectory := filepath.Join(root, "tmp")
	if err := os.Mkdir(socketDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tempDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(root, "scanner-capability")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("a", 64)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	scanner := &recordingScanner{}
	server := &Server{
		SocketPath:    filepath.Join(socketDirectory, "qpdf.sock"),
		TokenFile:     tokenFile,
		TempDirectory: tempDirectory, MaxBytes: 1 << 20,
		Timeout: 3 * time.Second, MaxConcurrent: 1, Scanner: scanner,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(server.TokenFile); err == nil {
			if _, err = os.Stat(server.SocketPath); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("scanner server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	client := document.SidecarScanner{SocketPath: server.SocketPath, TokenFile: server.TokenFile, MaxBytes: 1 << 20, Timeout: 3 * time.Second}
	return server, client, scanner, cancel, result
}

func TestCapabilityLoaderRejectsSymlinkAndWritableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix capability permissions are enforced in the Linux deployment")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("a", 64)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := document.LoadScannerCapability(link); err == nil {
		t.Fatal("symlink capability was accepted")
	}
	writable := filepath.Join(root, "writable")
	if err := os.WriteFile(writable, []byte(strings.Repeat("b", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := document.LoadScannerCapability(writable); err == nil {
		t.Fatal("writable capability was accepted")
	}
}

func stopTestServer(t *testing.T, cancel context.CancelFunc, result <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scanner server did not stop")
	}
}
