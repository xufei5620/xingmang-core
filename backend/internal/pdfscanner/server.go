package pdfscanner

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"invoice-system/backend/internal/document"
)

const (
	DefaultSocketPath    = "/scanner/qpdf.sock"
	DefaultTokenFile     = "/run/secrets/invoice_pdf_scanner_capability"
	DefaultTempDirectory = "/scan-tmp"
	defaultTimeout       = 20 * time.Second
	maxCapabilityBytes   = 64
)

type Server struct {
	SocketPath    string
	TokenFile     string
	TempDirectory string
	MaxBytes      int64
	Timeout       time.Duration
	MaxConcurrent int
	Scanner       document.Scanner

	capability string
	semaphore  chan struct{}
	listener   net.Listener
	once       sync.Once
}

func (s *Server) Run(ctx context.Context) error {
	if s.Scanner == nil {
		return errors.New("PDF policy scanner is required")
	}
	socketPath, tokenPath, tempDirectory, err := s.paths()
	if err != nil {
		return err
	}
	if err = verifyPrivateDirectory(filepath.Dir(socketPath), true); err != nil {
		return fmt.Errorf("scanner socket directory: %w", err)
	}
	if err = verifyPrivateDirectory(tempDirectory, false); err != nil {
		return fmt.Errorf("scanner temporary directory: %w", err)
	}
	s.SocketPath = socketPath
	s.TokenFile = tokenPath
	s.TempDirectory = tempDirectory
	if err = removeStaleSocket(socketPath); err != nil {
		return err
	}
	capability, err := document.LoadScannerCapability(tokenPath)
	if err != nil {
		return fmt.Errorf("load scanner capability: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on scanner socket: %w", err)
	}
	if err = os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return fmt.Errorf("secure scanner socket: %w", err)
	}
	s.capability = capability
	s.listener = listener
	maxConcurrent := s.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	s.semaphore = make(chan struct{}, maxConcurrent)
	timeout := s.timeout()
	httpServer := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       timeout,
		WriteTimeout:      timeout,
		IdleTimeout:       time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err = httpServer.Serve(listener)
	s.cleanup(socketPath)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/scan", s.scan)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		if !s.authenticated(request) {
			writeResponse(writer, http.StatusUnauthorized, "error", "unauthorized")
			return
		}
		select {
		case s.semaphore <- struct{}{}:
			defer func() { <-s.semaphore }()
		default:
			writeResponse(writer, http.StatusServiceUnavailable, "error", "busy")
			return
		}
		mux.ServeHTTP(writer, request)
	})
}

func (s *Server) authenticated(request *http.Request) bool {
	provided := request.Header.Get("X-Invoice-Scanner-Capability")
	if len(provided) != maxCapabilityBytes || len(s.capability) != maxCapabilityBytes {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.capability)) == 1
}

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	pinger, ok := s.Scanner.(interface{ Ping(context.Context) error })
	if !ok || pinger.Ping(request.Context()) != nil {
		writeResponse(writer, http.StatusServiceUnavailable, "error", "scanner_unavailable")
		return
	}
	writeResponse(writer, http.StatusOK, "clean", "")
}

func (s *Server) scan(writer http.ResponseWriter, request *http.Request) {
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = document.DefaultMaxPDFBytes
	}
	if request.ContentLength <= 0 || request.ContentLength > maxBytes || request.Header.Get("Content-Type") != "application/pdf" {
		writeResponse(writer, http.StatusRequestEntityTooLarge, "error", "invalid_request")
		return
	}
	temp, err := os.CreateTemp(strings.TrimSpace(s.TempDirectory), "invoice-scan-*.pdf")
	if err != nil {
		writeResponse(writer, http.StatusServiceUnavailable, "error", "scanner_unavailable")
		return
	}
	path := temp.Name()
	defer os.Remove(path)
	if err = temp.Chmod(0o600); err == nil {
		var copied int64
		copied, err = io.Copy(temp, io.LimitReader(request.Body, maxBytes+1))
		if err == nil && copied != request.ContentLength {
			err = errors.New("truncated scan body")
		}
		if err == nil {
			err = temp.Sync()
		}
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		writeResponse(writer, http.StatusBadRequest, "error", "invalid_request")
		return
	}
	scanCtx, cancel := context.WithTimeout(request.Context(), s.timeout())
	defer cancel()
	err = s.Scanner.Scan(scanCtx, path)
	switch {
	case err == nil:
		writeResponse(writer, http.StatusOK, "clean", "")
	case errors.Is(err, document.ErrUnsafePDF):
		writeResponse(writer, http.StatusUnprocessableEntity, "rejected", "unsafe_pdf")
	case errors.Is(err, document.ErrInvalidPDF):
		writeResponse(writer, http.StatusUnprocessableEntity, "rejected", "invalid_pdf")
	default:
		writeResponse(writer, http.StatusServiceUnavailable, "error", "scanner_unavailable")
	}
}

func (s *Server) paths() (string, string, string, error) {
	socketPath := strings.TrimSpace(s.SocketPath)
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	tokenPath := strings.TrimSpace(s.TokenFile)
	if tokenPath == "" {
		tokenPath = DefaultTokenFile
	}
	tempDirectory := strings.TrimSpace(s.TempDirectory)
	if tempDirectory == "" {
		tempDirectory = DefaultTempDirectory
	}
	for _, path := range []string{socketPath, tokenPath, tempDirectory} {
		if !filepath.IsAbs(path) {
			return "", "", "", errors.New("scanner paths must be absolute")
		}
	}
	if socketPath == tokenPath {
		return "", "", "", errors.New("scanner socket and capability paths must differ")
	}
	return socketPath, tokenPath, tempDirectory, nil
}

func (s *Server) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return defaultTimeout
}

func (s *Server) cleanup(socketPath string) {
	s.once.Do(func() {
		if s.listener != nil {
			_ = s.listener.Close()
		}
		_ = os.Remove(socketPath)
	})
}

func verifyPrivateDirectory(path string, requireSharedGroup bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	expected := os.FileMode(0o700)
	if requireSharedGroup {
		expected = 0o750
	}
	if info.Mode().Perm() != expected {
		return fmt.Errorf("directory mode must be %04o", expected)
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to replace non-socket scanner path")
	}
	return os.Remove(path)
}

func writeResponse(writer http.ResponseWriter, status int, state, code string) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Status string `json:"status"`
		Code   string `json:"code,omitempty"`
	}{Status: state, Code: code})
}
