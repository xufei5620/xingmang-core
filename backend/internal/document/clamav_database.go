package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultClamAVMaxUpdateAge = 48 * time.Hour

type ClamAVDatabaseScanner struct {
	Root         string
	MaxUpdateAge time.Duration
	Now          func() time.Time
}

func (scanner ClamAVDatabaseScanner) Scan(context.Context, string) error {
	now := time.Now().UTC()
	if scanner.Now != nil {
		now = scanner.Now().UTC()
	}
	return CheckClamAVDatabaseFreshness(scanner.Root, scanner.MaxUpdateAge, now)
}

// CheckClamAVDatabaseFreshness verifies that the API's read-only view contains
// the baseline database and a recently written daily signature database.
// main.cvd itself is intentionally not age-limited because upstream publishes
// it infrequently. freshclam.dat is not an update freshness signal: FreshClam
// can successfully download and load a new daily.* database without rewriting
// that implementation-owned state file.
func CheckClamAVDatabaseFreshness(root string, maxUpdateAge time.Duration, now time.Time) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("ClamAV database root must be an absolute clean path")
	}
	if maxUpdateAge <= 0 {
		maxUpdateAge = DefaultClamAVMaxUpdateAge
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	mainPath, err := firstSafeDatabaseFile(root, "main.cvd", "main.cld")
	if err != nil {
		return fmt.Errorf("ClamAV main signature database: %w", err)
	}
	_ = mainPath
	dailyPath, err := newestSafeDatabaseFile(root, "daily.cvd", "daily.cld")
	if err != nil {
		return fmt.Errorf("ClamAV daily signature database: %w", err)
	}
	info, statErr := os.Stat(dailyPath)
	if statErr != nil {
		return fmt.Errorf("ClamAV daily signatures: %w", statErr)
	}
	age := now.Sub(info.ModTime())
	if age < -5*time.Minute || age > maxUpdateAge {
		return errors.New("ClamAV daily signatures are stale")
	}
	return nil
}

func firstSafeDatabaseFile(root string, names ...string) (string, error) {
	for _, name := range names {
		if strings.ContainsAny(name, `/\\`) {
			return "", errors.New("invalid database filename")
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
			return "", errors.New("database file is missing or unsafe")
		}
		return path, nil
	}
	return "", errors.New("database file is missing")
}

func newestSafeDatabaseFile(root string, names ...string) (string, error) {
	var selected string
	var selectedAt time.Time
	for _, name := range names {
		if strings.ContainsAny(name, `/\\`) {
			return "", errors.New("invalid database filename")
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
			return "", errors.New("database file is missing or unsafe")
		}
		if selected == "" || info.ModTime().After(selectedAt) {
			selected = path
			selectedAt = info.ModTime()
		}
	}
	if selected == "" {
		return "", errors.New("database file is missing")
	}
	return selected, nil
}
