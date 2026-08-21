package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func qpdfFixtureRunner(jsonBody string, pages string) QPDFRunner {
	return func(_ context.Context, _ string, _ int64, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "--version"):
			return []byte("qpdf version 12.3.2\n"), nil
		case strings.Contains(joined, "--is-encrypted"):
			return nil, &qpdfExitStatusError{code: 2}
		case strings.Contains(joined, "--check"):
			return []byte("checking\n"), nil
		case strings.Contains(joined, "--show-npages"):
			return []byte(pages + "\n"), nil
		case strings.Contains(joined, "--json"):
			return []byte(jsonBody), nil
		default:
			return nil, errors.New("unexpected qpdf invocation")
		}
	}
}

func TestQPDFPolicyClassifiesEncryptedBeforePasswordedValidation(t *testing.T) {
	scanner := QPDFPolicyScanner{Runner: func(_ context.Context, _ string, _ int64, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "--is-encrypted") {
			return nil, nil
		}
		t.Fatalf("encrypted PDF reached a later qpdf stage: %v", args)
		return nil, nil
	}}
	if err := scanner.Scan(context.Background(), "encrypted.pdf"); !errors.Is(err, ErrUnsafePDF) {
		t.Fatalf("encrypted PDF error=%v", err)
	}
}

const staticQPDFJSON = `{
  "version": 2,
  "encrypt": {"encrypted": false},
  "attachments": {},
  "qpdf": [
    {"jsonversion": 2, "pdfversion": "1.7"},
    {"trailer": {"value": {"/Root": "1 0 R"}},
     "obj:1 0 R": {"value": {"/Type": "/Catalog", "/Pages": "2 0 R"}},
     "obj:2 0 R": {"value": {"/Type": "/Pages", "/Count": 1}}}
  ]
}`

func TestQPDFPolicyAcceptsStaticDocument(t *testing.T) {
	scanner := QPDFPolicyScanner{Runner: qpdfFixtureRunner(staticQPDFJSON, "1")}
	if err := scanner.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(context.Background(), filepath.Join(t.TempDir(), "invoice.pdf")); err != nil {
		t.Fatal(err)
	}
}

func TestQPDFPolicyRejectsActiveEncryptedAndEmbeddedContent(t *testing.T) {
	tests := map[string]string{
		"javascript":         strings.Replace(staticQPDFJSON, `"/Pages": "2 0 R"`, `"/OpenAction": "5 0 R", "/Pages": "2 0 R", "/S": "/JavaScript", "/JS": "6 0 R"`, 1),
		"encoded_javascript": strings.Replace(staticQPDFJSON, `"/Pages": "2 0 R"`, `"/S": "n:/Java#53cript", "/Pages": "2 0 R"`, 1),
		"launch":             strings.Replace(staticQPDFJSON, `"/Pages": "2 0 R"`, `"/S": "/Launch", "/Pages": "2 0 R"`, 1),
		"external_uri":       strings.Replace(staticQPDFJSON, `"/Pages": "2 0 R"`, `"/S": "/URI", "/Pages": "2 0 R"`, 1),
		"encrypted":          strings.Replace(staticQPDFJSON, `"/Root": "1 0 R"`, `"/Encrypt": "9 0 R", "/Root": "1 0 R"`, 1),
		"attachment":         strings.Replace(staticQPDFJSON, `"attachments": {}`, `"attachments": {"invoice.exe": {"filespec": "9 0 R"}}`, 1),
		"interactive_form":   strings.Replace(staticQPDFJSON, `"/Pages": "2 0 R"`, `"/AcroForm": "8 0 R", "/Pages": "2 0 R"`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			scanner := QPDFPolicyScanner{Runner: qpdfFixtureRunner(body, "1")}
			if err := scanner.Scan(context.Background(), filepath.Join(t.TempDir(), "invoice.pdf")); !errors.Is(err, ErrUnsafePDF) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestQPDFPolicyEnforcesPageAndStructureLimits(t *testing.T) {
	scanner := QPDFPolicyScanner{MaxPages: 2, Runner: qpdfFixtureRunner(staticQPDFJSON, "3")}
	if err := scanner.Scan(context.Background(), "invoice.pdf"); !errors.Is(err, ErrUnsafePDF) {
		t.Fatalf("page limit error=%v", err)
	}
	scanner = QPDFPolicyScanner{MaxJSONNodes: 2, Runner: qpdfFixtureRunner(staticQPDFJSON, "1")}
	if err := scanner.Scan(context.Background(), "invoice.pdf"); !errors.Is(err, ErrUnsafePDF) {
		t.Fatalf("node limit error=%v", err)
	}
}

func TestQPDFPolicyRejectsWrongBinaryVersion(t *testing.T) {
	scanner := QPDFPolicyScanner{Runner: func(context.Context, string, int64, ...string) ([]byte, error) {
		return []byte("qpdf version 12.2.0\n"), nil
	}}
	if err := scanner.Ping(context.Background()); !errors.Is(err, ErrQPDFUnavailable) {
		t.Fatalf("version error=%v", err)
	}
}

func TestScannerChainFailsClosedAndPreservesOrder(t *testing.T) {
	var calls []string
	chain := ScannerChain{
		ScannerFunc(func(context.Context, string) error { calls = append(calls, "clamav"); return nil }),
		ScannerFunc(func(context.Context, string) error { calls = append(calls, "qpdf"); return ErrUnsafePDF }),
		ScannerFunc(func(context.Context, string) error { calls = append(calls, "unexpected"); return nil }),
	}
	if err := chain.Scan(context.Background(), "invoice.pdf"); !errors.Is(err, ErrUnsafePDF) {
		t.Fatalf("chain error=%v", err)
	}
	if strings.Join(calls, ",") != "clamav,qpdf" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestQPDFPolicyRealBinaryWhenAvailable(t *testing.T) {
	binary, err := exec.LookPath("qpdf")
	if err != nil {
		t.Skip("qpdf is not installed on this development host")
	}
	dir := t.TempDir()
	normal := filepath.Join(dir, "normal.pdf")
	active := filepath.Join(dir, "active.pdf")
	if err = os.WriteFile(normal, minimalPDF(false), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(active, minimalPDF(true), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner := QPDFPolicyScanner{BinaryPath: binary}
	if err = scanner.Scan(context.Background(), normal); err != nil {
		t.Fatalf("normal PDF rejected: %v", err)
	}
	if err = scanner.Scan(context.Background(), active); !errors.Is(err, ErrUnsafePDF) {
		t.Fatalf("active PDF error=%v", err)
	}
}

func minimalPDF(active bool) []byte {
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> /Contents 4 0 R >>`,
		"<< /Length 0 >>\nstream\nendstream",
	}
	if active {
		objects[0] = `<< /Type /Catalog /Pages 2 0 R /OpenAction 5 0 R >>`
		objects = append(objects, `<< /Type /Action /S /JavaScript /JS (app.alert\(1\)) >>`)
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
