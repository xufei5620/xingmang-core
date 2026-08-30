// Command cpa-plugin-assess evaluates a local CPA plugin evidence bundle
// against checked-in policy.  It intentionally has no network, SSH, Docker,
// plugin loader, or credential dependencies.
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

	"github.com/xufei5620/xingmang-platform/internal/platform/cpaplugin"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cpa-plugin-assess", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "", "local admission policy JSON")
	sourcesPath := flags.String("sources", "", "local store source policy JSON")
	evidencePath := flags.String("evidence", "", "local evidence bundle JSON")
	outPath := flags.String("out", "", "optional local output JSON path")
	// --output is a friendly alias retained for scripts that use the longer
	// spelling; both flags resolve to the same local-only behavior.
	flags.Var((*stringValue)(outPath), "output", "alias for --out")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *policyPath == "" || *evidencePath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "--policy and --evidence are required; all inputs must be local files")
		return 2
	}
	policyBytes, err := readLocal(*policyPath)
	if err != nil {
		fmt.Fprintf(stderr, "read policy: %v\n", err)
		return 2
	}
	policy, _, err := cpaplugin.LoadAdmissionPolicy(policyBytes)
	if err != nil {
		fmt.Fprintf(stderr, "policy rejected: %v\n", err)
		return 1
	}
	var sources []cpaplugin.PluginStoreSource
	if *sourcesPath != "" {
		sourceBytes, readErr := readLocal(*sourcesPath)
		if readErr != nil {
			fmt.Fprintf(stderr, "read sources: %v\n", readErr)
			return 2
		}
		sources, _, err = cpaplugin.LoadStoreSources(sourceBytes)
		if err != nil {
			fmt.Fprintf(stderr, "sources rejected: %v\n", err)
			return 1
		}
	}
	evidenceBytes, err := readLocal(*evidencePath)
	if err != nil {
		fmt.Fprintf(stderr, "read evidence: %v\n", err)
		return 2
	}
	bundle, err := cpaplugin.LoadEvidenceBundle(evidenceBytes)
	if err != nil {
		fmt.Fprintf(stderr, "evidence rejected: %v\n", err)
		return 1
	}
	result, err := cpaplugin.AssessOffline(policy, sources, bundle)
	if err != nil {
		fmt.Fprintf(stderr, "assessment failed: %v\n", err)
		return 1
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "encode assessment: %v\n", err)
		return 1
	}
	encoded = append(encoded, '\n')
	if *outPath != "" {
		if err := writeLocal(*outPath, encoded); err != nil {
			fmt.Fprintf(stderr, "write output: %v\n", err)
			return 2
		}
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "write assessment: %v\n", err)
		return 1
	}
	if result.Decision == "fail" {
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
	// O_EXCL prevents an assessment from overwriting an existing evidence
	// artifact. Callers can choose a fresh path for each immutable report.
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
		return errors.New("path must identify a local file, not a URL")
	}
	clean := filepath.Clean(name)
	for _, part := range strings.FieldsFunc(clean, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return errors.New("path traversal is not allowed")
		}
	}
	return nil
}
