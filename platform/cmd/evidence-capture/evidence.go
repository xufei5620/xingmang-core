package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// evidenceFile is one file this tool will write into an evidence directory.
type evidenceFile struct {
	Name string
	Data []byte
}

// timestampDir renders a UTC timestamp the same way this tool names every
// evidence run directory: compact, sortable, filesystem-safe on both
// POSIX and Windows (no colons) - docs/evidence/users-real/<platform>/<this>/.
func timestampDir(now time.Time) string {
	return now.UTC().Format("20060102T150405Z")
}

// defaultEvidenceRoot is where capture/reqlog evidence lands unless --out
// overrides it. Both subcommands share the docs/evidence/users-real/ root
// so a reviewer only has one place to look.
const defaultEvidenceRoot = "docs/evidence/users-real"

// computeSums returns the sha256 hex digest of every file, keyed by name.
func computeSums(files []evidenceFile) map[string]string {
	sums := make(map[string]string, len(files))
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		sums[f.Name] = hex.EncodeToString(sum[:])
	}
	return sums
}

// sumsFileContent renders a standard coreutils-style `sha256sum` checksum
// file (`<hex>␠␠<name>` per line, names sorted) covering files. This exact
// format - not a "key: value" style label right next to a hex blob - is
// deliberate: it is what sha256sum/gitleaks/every other tool already
// expects to see and not flag as a suspected credential.
func sumsFileContent(files []evidenceFile, sums map[string]string) []byte {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[name], name)
	}
	return []byte(b.String())
}

// writeEvidenceDir persists files under outDir, refusing to overwrite an
// existing directory - every run gets its own fresh timestamp directory
// (timestampDir), so an existing directory at the target path means a
// caller reused a timestamp or a path collision, either of which is a bug
// worth stopping for rather than silently merging into.
func writeEvidenceDir(outDir string, files []evidenceFile) error {
	if _, err := os.Stat(outDir); err == nil {
		return fmt.Errorf("evidence-capture: refusing to overwrite existing directory %s", outDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("evidence-capture: failed to check %s: %w", outDir, err)
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("evidence-capture: failed to create %s: %w", outDir, err)
	}
	for _, f := range files {
		path := filepath.Join(outDir, f.Name)
		if err := os.WriteFile(path, f.Data, 0o640); err != nil {
			return fmt.Errorf("evidence-capture: failed to write %s: %w", path, err)
		}
	}
	return nil
}
