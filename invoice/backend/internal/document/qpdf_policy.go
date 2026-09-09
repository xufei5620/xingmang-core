package document

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultQPDFBinary          = "/usr/bin/qpdf"
	defaultQPDFVersion         = "12.3.2"
	defaultQPDFTimeout         = 15 * time.Second
	defaultQPDFMaxJSONBytes    = int64(12 << 20)
	defaultQPDFMaxPages        = 50
	defaultQPDFMaxJSONNodes    = 200_000
	defaultQPDFMaxJSONDepth    = 64
	defaultQPDFDiagnosticBytes = int64(8 << 10)
)

var (
	ErrUnsafePDF          = errors.New("PDF contains prohibited active or embedded content")
	ErrQPDFUnavailable    = errors.New("qpdf structural validator is unavailable")
	errQPDFOutputTooLarge = errors.New("qpdf output exceeded the configured limit")
)

// QPDFRunner is injectable for deterministic policy tests. Production leaves
// Runner nil and executes the absolute, version-pinned qpdf binary.
type QPDFRunner func(context.Context, string, int64, ...string) ([]byte, error)

type QPDFPolicyScanner struct {
	BinaryPath      string
	ExpectedVersion string
	Timeout         time.Duration
	MaxJSONBytes    int64
	MaxPages        int
	MaxJSONNodes    int
	MaxJSONDepth    int
	Runner          QPDFRunner
}

// ScannerChain runs all scanners in order and fails closed on the first error.
type ScannerChain []Scanner

func (chain ScannerChain) Scan(ctx context.Context, path string) error {
	if len(chain) == 0 {
		return errors.New("document scanner chain is empty")
	}
	for _, scanner := range chain {
		if scanner == nil {
			return errors.New("document scanner chain contains a nil scanner")
		}
		if err := scanner.Scan(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func (s QPDFPolicyScanner) Ping(ctx context.Context) error {
	output, err := s.run(ctx, 1024, "--version")
	if err != nil {
		return fmt.Errorf("%w: version check failed", ErrQPDFUnavailable)
	}
	expected := strings.TrimSpace(s.ExpectedVersion)
	if expected == "" {
		expected = defaultQPDFVersion
	}
	fields := strings.Fields(string(output))
	for i := 0; i+1 < len(fields); i++ {
		if strings.EqualFold(fields[i], "version") && fields[i+1] == expected {
			return nil
		}
	}
	return fmt.Errorf("%w: expected qpdf %s", ErrQPDFUnavailable, expected)
}

func (s QPDFPolicyScanner) Scan(ctx context.Context, path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil || strings.TrimSpace(path) == "" {
		return ErrInvalidPDF
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultQPDFTimeout
	}
	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// qpdf intentionally returns 0 for encrypted and 2 for unencrypted files.
	// Probe before --check so a password-protected but structurally valid PDF is
	// classified as prohibited content instead of a malformed document.
	if _, probeErr := s.run(scanCtx, 1, "--is-encrypted", absolute); probeErr == nil {
		return fmt.Errorf("%w: encrypted", ErrUnsafePDF)
	} else if !isQPDFExitStatus(probeErr, 2) {
		return fmt.Errorf("%w: qpdf_encryption_probe", ErrInvalidPDF)
	}
	if _, err = s.run(scanCtx, defaultQPDFDiagnosticBytes, "--password=", "--check", absolute); err != nil {
		return fmt.Errorf("%w: qpdf_strict_validation", ErrInvalidPDF)
	}
	pageOutput, err := s.run(scanCtx, 128, "--password=", "--show-npages", absolute)
	if err != nil {
		return fmt.Errorf("%w: qpdf_page_count", ErrInvalidPDF)
	}
	pages, err := strconv.Atoi(strings.TrimSpace(string(pageOutput)))
	maxPages := s.MaxPages
	if maxPages <= 0 {
		maxPages = defaultQPDFMaxPages
	}
	if err != nil || pages <= 0 || pages > maxPages {
		return fmt.Errorf("%w: page_limit", ErrUnsafePDF)
	}
	maxJSON := s.MaxJSONBytes
	if maxJSON <= 0 {
		maxJSON = defaultQPDFMaxJSONBytes
	}
	jsonBody, err := s.run(scanCtx, maxJSON,
		"--password=", "--json=2", "--json-stream-data=none",
		"--json-key=encrypt", "--json-key=attachments", "--json-key=qpdf", absolute)
	if err != nil {
		if errors.Is(err, errQPDFOutputTooLarge) {
			return fmt.Errorf("%w: structure_size_limit", ErrUnsafePDF)
		}
		return fmt.Errorf("%w: qpdf_structure", ErrInvalidPDF)
	}
	if err = s.inspectJSON(jsonBody); err != nil {
		return err
	}
	return nil
}

func (s QPDFPolicyScanner) run(ctx context.Context, maxBytes int64, args ...string) ([]byte, error) {
	binary := strings.TrimSpace(s.BinaryPath)
	if binary == "" {
		binary = defaultQPDFBinary
	}
	if s.Runner != nil {
		return s.Runner(ctx, binary, maxBytes, args...)
	}
	if !filepath.IsAbs(binary) {
		return nil, errors.New("qpdf binary path must be absolute")
	}
	return runBoundedCommand(ctx, binary, maxBytes, args...)
}

type commandReadResult struct {
	body []byte
	err  error
}

type qpdfExitStatusError struct {
	code int
}

func (e *qpdfExitStatusError) Error() string {
	return fmt.Sprintf("qpdf exited with status %d", e.code)
}

func isQPDFExitStatus(err error, code int) bool {
	var status *qpdfExitStatusError
	return errors.As(err, &status) && status.code == code
}

func runBoundedCommand(ctx context.Context, binary string, maxBytes int64, args ...string) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("command output limit must be positive")
	}
	command := exec.CommandContext(ctx, binary, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, err
	}
	var kill sync.Once
	read := func(reader io.Reader, limit int64, results chan<- commandReadResult) {
		body, readErr := io.ReadAll(io.LimitReader(reader, limit+1))
		if int64(len(body)) > limit {
			kill.Do(func() { _ = command.Process.Kill() })
			results <- commandReadResult{err: errQPDFOutputTooLarge}
			return
		}
		results <- commandReadResult{body: body, err: readErr}
	}
	stdoutResult := make(chan commandReadResult, 1)
	stderrResult := make(chan commandReadResult, 1)
	go read(stdout, maxBytes, stdoutResult)
	go read(stderr, defaultQPDFDiagnosticBytes, stderrResult)
	out := <-stdoutResult
	diagnostic := <-stderrResult
	waitErr := command.Wait()
	if out.err != nil {
		return nil, out.err
	}
	if diagnostic.err != nil {
		return nil, diagnostic.err
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return nil, &qpdfExitStatusError{code: exitErr.ExitCode()}
		}
		return nil, errors.New("qpdf command failed")
	}
	return out.body, nil
}

func (s QPDFPolicyScanner) inspectJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("%w: invalid_qpdf_json", ErrInvalidPDF)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing_qpdf_json", ErrInvalidPDF)
	}
	object, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: invalid_qpdf_root", ErrInvalidPDF)
	}
	if collectionHasValues(object["attachments"]) {
		return fmt.Errorf("%w: embedded_attachment", ErrUnsafePDF)
	}
	if encrypted, ok := object["encrypt"].(map[string]any); ok {
		if value, ok := encrypted["encrypted"].(bool); ok && value {
			return fmt.Errorf("%w: encrypted", ErrUnsafePDF)
		}
	}
	maxNodes := s.MaxJSONNodes
	if maxNodes <= 0 {
		maxNodes = defaultQPDFMaxJSONNodes
	}
	maxDepth := s.MaxJSONDepth
	if maxDepth <= 0 {
		maxDepth = defaultQPDFMaxJSONDepth
	}
	nodes := 0
	if reason := inspectPDFNode(root, 0, maxDepth, maxNodes, &nodes); reason != "" {
		return fmt.Errorf("%w: %s", ErrUnsafePDF, reason)
	}
	return nil
}

func collectionHasValues(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	case nil:
		return false
	default:
		return true
	}
}

var prohibitedPDFNames = map[string]string{
	"/Encrypt": "encrypted", "/JS": "javascript", "/JavaScript": "javascript",
	"/Launch": "launch_action", "/OpenAction": "open_action", "/AA": "additional_action",
	"/URI": "external_uri", "/GoToR": "remote_goto", "/GoToE": "embedded_goto",
	"/SubmitForm": "form_submission", "/ImportData": "form_import", "/AcroForm": "interactive_form",
	"/XFA": "xfa", "/EmbeddedFiles": "embedded_attachment", "/EmbeddedFile": "embedded_attachment",
	"/Filespec": "file_specification", "/FileAttachment": "embedded_attachment", "/EF": "embedded_attachment",
	"/AF": "associated_file", "/Collection": "portfolio", "/RichMedia": "rich_media",
	"/Rendition": "rendition", "/Movie": "movie", "/Sound": "sound", "/3D": "three_d",
	"/Screen": "screen_annotation", "/FFilter": "external_stream_filter", "/FDecodeParms": "external_stream_filter",
	"/URL": "external_url", "/OPI": "external_prepress_reference", "/Ref": "external_reference",
	"/AlternatePresentations": "alternate_presentation", "/PresSteps": "presentation_steps", "/GoTo3DView": "three_d_action",
}

func inspectPDFNode(value any, depth, maxDepth, maxNodes int, nodes *int) string {
	(*nodes)++
	if *nodes > maxNodes {
		return "object_limit"
	}
	if depth > maxDepth {
		return "object_depth_limit"
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if reason := prohibitedPDFNameReason(key); reason != "" {
				return reason
			}
			if reason := inspectPDFNode(child, depth+1, maxDepth, maxNodes, nodes); reason != "" {
				return reason
			}
		}
	case []any:
		for _, child := range typed {
			if reason := inspectPDFNode(child, depth+1, maxDepth, maxNodes, nodes); reason != "" {
				return reason
			}
		}
	case string:
		return prohibitedPDFNameReason(typed)
	}
	return ""
}

func prohibitedPDFNameReason(value string) string {
	if strings.HasPrefix(value, "n:") {
		value = strings.TrimPrefix(value, "n:")
	}
	if !strings.HasPrefix(value, "/") {
		return ""
	}
	value = decodePDFName(value)
	return prohibitedPDFNames[value]
}

func decodePDFName(value string) string {
	var output strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '#' && i+2 < len(value) {
			decoded, err := hex.DecodeString(value[i+1 : i+3])
			if err == nil {
				output.WriteByte(decoded[0])
				i += 2
				continue
			}
		}
		output.WriteByte(value[i])
	}
	return output.String()
}
