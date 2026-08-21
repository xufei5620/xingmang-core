package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultPDFScannerSocket    = "/scanner/qpdf.sock"
	defaultPDFScannerTokenFile = "/run/secrets/invoice_pdf_scanner_capability"
	defaultPDFScannerTimeout   = 20 * time.Second
	maxScannerResponseBytes    = int64(4 << 10)
)

// SidecarScanner sends quarantined plaintext to the isolated PDF policy
// scanner over a Unix socket. It never sends a host path: the sidecar receives
// only a bounded byte stream and cannot mount or address document storage.
type SidecarScanner struct {
	SocketPath string
	TokenFile  string
	MaxBytes   int64
	Timeout    time.Duration
}

type scannerResponse struct {
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

func (s SidecarScanner) Scan(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open PDF for isolated scan: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat PDF for isolated scan: %w", err)
	}
	maxBytes := s.maxBytes()
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return ErrInvalidPDF
	}
	if info.Size() > maxBytes {
		return ErrTooLarge
	}
	return s.request(ctx, http.MethodPost, "/v1/scan", file, info.Size())
}

func (s SidecarScanner) Ping(ctx context.Context) error {
	return s.request(ctx, http.MethodGet, "/healthz", nil, 0)
}

func (s SidecarScanner) request(ctx context.Context, method, endpoint string, body io.Reader, size int64) error {
	socketPath := strings.TrimSpace(s.SocketPath)
	if socketPath == "" {
		socketPath = defaultPDFScannerSocket
	}
	tokenFile := strings.TrimSpace(s.TokenFile)
	if tokenFile == "" {
		tokenFile = defaultPDFScannerTokenFile
	}
	if !strings.HasPrefix(socketPath, "/") || !strings.HasPrefix(tokenFile, "/") {
		return fmt.Errorf("%w: scanner socket and token paths must be absolute", ErrQPDFUnavailable)
	}
	token, err := LoadScannerCapability(tokenFile)
	if err != nil {
		return fmt.Errorf("%w: capability unavailable", ErrQPDFUnavailable)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultPDFScannerTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		MaxIdleConns:           0,
		IdleConnTimeout:        time.Second,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 4 << 10,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: min(timeout, 3*time.Second)}).DialContext(dialCtx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(requestCtx, method, "http://scanner"+endpoint, body)
	if err != nil {
		return fmt.Errorf("%w: create scanner request", ErrQPDFUnavailable)
	}
	request.Header.Set("X-Invoice-Scanner-Capability", token)
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.ContentLength = size
		request.Header.Set("Content-Type", "application/pdf")
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return fmt.Errorf("%w: sidecar request failed", ErrQPDFUnavailable)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxScannerResponseBytes+1))
	if readErr != nil || int64(len(payload)) > maxScannerResponseBytes {
		return fmt.Errorf("%w: invalid sidecar response", ErrQPDFUnavailable)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var result scannerResponse
	if err = decoder.Decode(&result); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: malformed sidecar response", ErrQPDFUnavailable)
	}
	switch {
	case response.StatusCode == http.StatusOK && result.Status == "clean":
		return nil
	case response.StatusCode == http.StatusUnprocessableEntity && result.Status == "rejected" && result.Code == "unsafe_pdf":
		return ErrUnsafePDF
	case response.StatusCode == http.StatusUnprocessableEntity && result.Status == "rejected" && result.Code == "invalid_pdf":
		return ErrInvalidPDF
	default:
		return fmt.Errorf("%w: sidecar status %d", ErrQPDFUnavailable, response.StatusCode)
	}
}

func (s SidecarScanner) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultMaxPDFBytes
}

// LoadScannerCapability reads the deployment-generated sidecar capability
// without following links and verifies the opened inode is the one inspected.
// Production mounts this file read-only into both containers; it never lives
// in the qpdf-writable socket volume.
func LoadScannerCapability(path string) (string, error) {
	file, info, err := openScannerCapabilityNoFollow(path)
	if err != nil {
		return "", errors.New("invalid scanner capability file")
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128 {
		return "", errors.New("invalid scanner capability file")
	}
	if mode := info.Mode().Perm(); mode != 0o400 && mode != 0o440 {
		return "", errors.New("scanner capability file must have mode 0400 or 0440")
	}
	if !scannerCapabilityOwnershipOK(info) {
		return "", errors.New("scanner capability file has unsafe ownership")
	}
	body, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(body))
	if len(value) != 64 {
		return "", errors.New("invalid scanner capability")
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", errors.New("invalid scanner capability")
		}
	}
	return value, nil
}
