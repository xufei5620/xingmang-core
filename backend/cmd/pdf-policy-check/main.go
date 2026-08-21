package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"invoice-system/backend/internal/document"
)

func main() {
	selfTest := flag.Bool("self-test", false, "run the real qpdf policy compatibility gate")
	flag.Parse()
	if !*selfTest || flag.NArg() != 0 {
		slog.Error("usage: invoice-pdf-policy-check --self-test")
		os.Exit(2)
	}
	if err := runSelfTest(); err != nil {
		slog.Error("qpdf policy self-test failed", "error", err)
		os.Exit(1)
	}
	slog.Info("qpdf policy self-test passed")
}

func runSelfTest() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	directory, err := os.MkdirTemp("", "invoice-qpdf-self-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	scanner := document.QPDFPolicyScanner{}
	if err = scanner.Ping(ctx); err != nil {
		return err
	}
	paths := map[string]string{}
	for _, kind := range []string{"static", "javascript", "launch", "external-uri", "attachment"} {
		path := filepath.Join(directory, kind+".pdf")
		if err = os.WriteFile(path, policyFixturePDF(kind), 0o600); err != nil {
			return err
		}
		paths[kind] = path
	}
	if err = scanner.Scan(ctx, paths["static"]); err != nil {
		return fmt.Errorf("static fixture rejected: %w", err)
	}
	for _, kind := range []string{"javascript", "launch", "external-uri", "attachment"} {
		if err = scanner.Scan(ctx, paths[kind]); !errors.Is(err, document.ErrUnsafePDF) {
			return fmt.Errorf("%s fixture was not rejected by active-content policy: %w", kind, err)
		}
	}
	encrypted := filepath.Join(directory, "encrypted.pdf")
	command := exec.CommandContext(ctx, "/usr/bin/qpdf", "--encrypt", "self-test-user", "self-test-owner", "256", "--", paths["static"], encrypted)
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		if len(output) > 512 {
			output = output[:512]
		}
		return fmt.Errorf("create encrypted fixture: %w: %s", commandErr, output)
	}
	if err = scanner.Scan(ctx, encrypted); !errors.Is(err, document.ErrUnsafePDF) {
		return fmt.Errorf("encrypted fixture was not rejected by active-content policy: %w", err)
	}
	return nil
}

func policyFixturePDF(kind string) []byte {
	catalog := `<< /Type /Catalog /Pages 2 0 R >>`
	objects := []string{
		catalog,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> /Contents 4 0 R >>`,
		"<< /Length 0 >>\nstream\nendstream",
	}
	switch kind {
	case "static":
	case "javascript":
		objects[0] = `<< /Type /Catalog /Pages 2 0 R /OpenAction 5 0 R >>`
		objects = append(objects, `<< /Type /Action /S /JavaScript /JS (app.alert\(1\)) >>`)
	case "launch":
		objects[0] = `<< /Type /Catalog /Pages 2 0 R /OpenAction 5 0 R >>`
		objects = append(objects, `<< /Type /Action /S /Launch /F (calculator.exe) >>`)
	case "external-uri":
		objects[0] = `<< /Type /Catalog /Pages 2 0 R /OpenAction 5 0 R >>`
		objects = append(objects, `<< /Type /Action /S /URI /URI (https://example.invalid/pixel) >>`)
	case "attachment":
		objects[0] = `<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names [(payload.txt) 5 0 R] >> >> >>`
		objects = append(objects,
			`<< /Type /Filespec /F (payload.txt) /EF << /F 6 0 R >> >>`,
			"<< /Type /EmbeddedFile /Length 4 >>\nstream\ntest\nendstream")
	default:
		panic("unsupported trusted self-test fixture")
	}
	var body bytes.Buffer
	body.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = body.Len()
		fmt.Fprintf(&body, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := body.Len()
	fmt.Fprintf(&body, "xref\n0 %d\n", len(objects)+1)
	body.WriteString("0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&body, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&body, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return body.Bytes()
}
