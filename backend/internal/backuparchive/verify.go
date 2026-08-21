package backuparchive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMaxEntries   = 100_000
	DefaultMaxTotalSize = int64(100 << 30)
	DefaultMaxFileSize  = int64(1 << 30)
	DefaultMaxPathBytes = 1024
)

type Limits struct {
	MaxEntries   int
	MaxTotalSize int64
	MaxFileSize  int64
	MaxPathBytes int
}

type Report struct {
	Entries   int
	Files     int
	TotalSize int64
}

// VerifyTar permits only ordinary files and directories. It rejects all link,
// device, FIFO and socket records, PAX/xattr metadata, duplicate or unsafe
// paths, and archives that exceed bounded resource limits.
func VerifyTar(reader io.Reader, configured Limits) (Report, error) {
	limits := normalizeLimits(configured)
	tape := tar.NewReader(reader)
	seen := make(map[string]struct{})
	var report Report
	for {
		header, err := tape.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, fmt.Errorf("read tar header: %w", err)
		}
		report.Entries++
		if report.Entries > limits.MaxEntries {
			return Report{}, errors.New("archive entry limit exceeded")
		}
		name, err := safeArchivePath(header.Name, limits.MaxPathBytes)
		if err != nil {
			return Report{}, err
		}
		if _, duplicate := seen[name]; duplicate {
			return Report{}, fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = struct{}{}
		if len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return Report{}, fmt.Errorf("extended PAX/xattr metadata is prohibited for %q", name)
		}
		if header.Linkname != "" {
			return Report{}, fmt.Errorf("archive link target is prohibited for %q", name)
		}
		if header.Mode&0o7000 != 0 {
			return Report{}, fmt.Errorf("special permission bits are prohibited for %q", name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return Report{}, fmt.Errorf("directory %q has non-zero content", name)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > limits.MaxFileSize || report.TotalSize > limits.MaxTotalSize-header.Size {
				return Report{}, fmt.Errorf("archive content limit exceeded at %q", name)
			}
			report.Files++
			report.TotalSize += header.Size
			if _, err = io.CopyN(io.Discard, tape, header.Size); err != nil {
				return Report{}, fmt.Errorf("truncated archive member %q: %w", name, err)
			}
		default:
			return Report{}, fmt.Errorf("archive member %q has prohibited type %d", name, header.Typeflag)
		}
	}
	if report.Entries == 0 {
		return Report{}, errors.New("archive is empty")
	}
	return report, nil
}

func VerifyTarFile(filename string, limits Limits) (Report, error) {
	info, err := os.Lstat(filename)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Report{}, errors.New("archive must be a regular non-symlink file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Report{}, errors.New("archive changed while opening")
	}
	return VerifyTar(file, limits)
}

func safeArchivePath(value string, maxBytes int) (string, error) {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.Contains(value, "\\") {
		return "", errors.New("archive contains an invalid path")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("archive path contains a control character")
		}
	}
	if strings.HasPrefix(value, "/") {
		return "", errors.New("archive path must be relative")
	}
	trimmed := strings.TrimPrefix(value, "./")
	for _, component := range strings.Split(trimmed, "/") {
		if component == ".." {
			return "", errors.New("archive path contains a parent traversal component")
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "" {
		cleaned = "."
	}
	if path.IsAbs(cleaned) || cleaned != "." && (cleaned == ".." || strings.HasPrefix(cleaned, "../")) {
		return "", errors.New("archive path escapes the restore root")
	}
	return cleaned, nil
}

func normalizeLimits(configured Limits) Limits {
	if configured.MaxEntries <= 0 {
		configured.MaxEntries = DefaultMaxEntries
	}
	if configured.MaxTotalSize <= 0 {
		configured.MaxTotalSize = DefaultMaxTotalSize
	}
	if configured.MaxFileSize <= 0 {
		configured.MaxFileSize = DefaultMaxFileSize
	}
	if configured.MaxPathBytes <= 0 {
		configured.MaxPathBytes = DefaultMaxPathBytes
	}
	return configured
}
