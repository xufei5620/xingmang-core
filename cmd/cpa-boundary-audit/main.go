// Command cpa-boundary-audit evaluates a CPA boundary contract against an
// explicitly supplied local, redacted evidence bundle. It has no network,
// SSH, Docker, browser, or credential-provider capability by construction.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/cpaboundary"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cpa-boundary-audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	boundaryPath := flags.String("boundary", "", "local boundary contract JSON")
	evidencePath := flags.String("evidence", "", "local redacted evidence JSON")
	outPath := flags.String("out", "", "optional local report JSON path")
	flags.Var((*stringValue)(outPath), "output", "alias for --out")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *boundaryPath == "" || *evidencePath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "--boundary and --evidence are required; all inputs must be local files")
		return 2
	}
	boundaryBytes, err := readLocal(*boundaryPath)
	if err != nil {
		fmt.Fprintf(stderr, "read boundary: %v\n", err)
		return 2
	}
	boundary, _, err := cpaboundary.LoadBoundary(boundaryBytes)
	if err != nil {
		fmt.Fprintf(stderr, "boundary rejected: %v\n", err)
		return 1
	}
	evidenceBytes, err := readLocal(*evidencePath)
	if err != nil {
		fmt.Fprintf(stderr, "read evidence: %v\n", err)
		return 2
	}
	evidence, err := cpaboundary.LoadEvidenceBundle(evidenceBytes)
	if err != nil {
		fmt.Fprintf(stderr, "evidence rejected: %v\n", err)
		return 1
	}
	report, err := cpaboundary.AuditOffline(boundary, evidence)
	if err != nil {
		fmt.Fprintf(stderr, "audit failed: %v\n", err)
		return 1
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 1
	}
	encoded = append(encoded, '\n')
	if *outPath != "" {
		if err := writeLocal(*outPath, encoded); err != nil {
			fmt.Fprintf(stderr, "write report: %v\n", err)
			return 2
		}
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "write report: %v\n", err)
		return 1
	}
	if report.Decision != "pass" {
		return 1
	}
	return 0
}

type stringValue string

func (s *stringValue) String() string { return string(*s) }
func (s *stringValue) Set(value string) error {
	*s = stringValue(value)
	return nil
}

func readLocal(name string) ([]byte, error) {
	if err := validateLocalPath(name); err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular local file")
	}
	return os.ReadFile(name)
}

func writeLocal(name string, data []byte) error {
	if err := validateLocalPath(name); err != nil {
		return err
	}
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func validateLocalPath(name string) error {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "://") || strings.HasPrefix(name, `\\`) || strings.HasPrefix(name, `//`) {
		return errors.New("path must identify a local file, not a URL or UNC path")
	}
	// Reject traversal before filepath.Clean can erase the evidence.
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return errors.New("path traversal is not allowed")
		}
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == "" {
		return errors.New("path must name a file")
	}
	return nil
}
