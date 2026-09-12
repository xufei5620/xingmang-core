package main

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// The platform API uses NewFileProvider: <secret-root>/staff-totp/<UUID>.
// The flat spelling remains allowed only to clean earlier owned fixtures.
var staffSecretName = regexp.MustCompile(`^staff-secrets/(?:staff-totp/|staff-totp__)[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var fixtureSQLName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,80}\.sql$`)

func allowedShredFile(name string) bool {
	switch name {
	case "private.json", "guard.json", "provider-guard.json", "seed-guard.json", "staff-password", "sub2api-password", "newapi-password", "expected-email", "provider-key.pem", "public-ca.pem", "provider-cert.pem", "summary-platform.json", "summary-invoice.json":
		return true
	}
	return staffSecretName.MatchString(name) || fixtureSQLName.MatchString(name)
}

type fixtureWiper interface {
	WriteAt([]byte, int64) (int, error)
	Sync() error
}

func wipeFixtureFile(f fixtureWiper, size int64, entropy io.Reader) error {
	if size < 0 || size > 64<<20 {
		return errors.New("fixture file exceeds wipe limit")
	}
	buffer := make([]byte, 64<<10)
	for pass := 0; pass < 4; pass++ {
		for offset := int64(0); offset < size; {
			count := int64(len(buffer))
			if size-offset < count {
				count = size - offset
			}
			part := buffer[:count]
			if pass < 3 {
				if _, err := io.ReadFull(entropy, part); err != nil {
					return errors.New("fixture random overwrite failed")
				}
			} else {
				clear(part)
			}
			written, err := f.WriteAt(part, offset)
			if err != nil || written != len(part) {
				return errors.New("fixture overwrite failed")
			}
			offset += count
		}
		if err := f.Sync(); err != nil {
			return errors.New("fixture overwrite sync failed")
		}
	}
	return nil
}

type openedFixture struct {
	name string
	file *os.File
	info fs.FileInfo
}

// shredFixtures only handles the caller-owned private tmpfs mount contents.
// The outer operator proves actual Docker ownership, tmpfs and absence of
// production mounts. Four synced passes are process-level overwrite evidence;
// this does not claim physical erasure guarantees for arbitrary storage.
func shredFixtures(directory string, g fixtureGuard, guardPath string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || filepath.Dir(directory) == directory {
		return errors.New("fixture wipe requires an absolute non-root directory")
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil || canonical != directory {
		return errors.New("fixture wipe refuses linked directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("fixture wipe root is not an ordinary directory")
	}
	if err = validateGuard(g, net.JoinHostPort(g.BindIP, "8443"), g.Owner, g.Project); err != nil {
		return err
	}
	if !filepath.IsAbs(guardPath) || filepath.Clean(guardPath) != guardPath {
		return errors.New("fixture guard must be absolute")
	}
	relative, err := filepath.Rel(directory, guardPath)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.Base(relative) != relative || (relative != "guard.json" && relative != "provider-guard.json" && relative != "seed-guard.json") {
		return errors.New("fixture wipe guard must be inside its owned mount")
	}
	var diskGuard fixtureGuard
	if err = readJSONFile(guardPath, false, &diskGuard); err != nil {
		return err
	}
	if !reflect.DeepEqual(g, diskGuard) {
		return errors.New("fixture wipe owner guard mismatch")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("fixture wipe root open failed")
	}
	defer root.Close()
	var names []string
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("fixture inventory failed")
		}
		if name == "." {
			return nil
		}
		stat, err := root.Lstat(filepath.FromSlash(name))
		if err != nil {
			return errors.New("fixture inventory metadata unavailable")
		}
		if stat.Mode()&os.ModeSymlink != 0 {
			return errors.New("fixture wipe refuses links")
		}
		if stat.IsDir() {
			if name != "staff-secrets" && name != "staff-secrets/staff-totp" {
				return errors.New("fixture wipe refuses unknown directory")
			}
			return nil
		}
		if !stat.Mode().IsRegular() || !allowedShredFile(name) || stat.Size() > 64<<20 {
			return errors.New("fixture wipe refuses unknown or unbounded file")
		}
		// Unix hard links can escape a directory without using symlinks. Refuse
		// them where the host exposes a link count; the operator owns the tmpfs.
		sys := reflect.ValueOf(stat.Sys())
		if sys.Kind() == reflect.Pointer {
			sys = sys.Elem()
		}
		if sys.IsValid() && sys.Kind() == reflect.Struct {
			nlink := sys.FieldByName("Nlink")
			if nlink.IsValid() && nlink.CanUint() && nlink.Uint() > 1 {
				return errors.New("fixture wipe refuses multiply linked file")
			}
		}
		names = append(names, filepath.FromSlash(name))
		return nil
	})
	if err != nil {
		return err
	}
	// Preserve the owner guard until all other files have been erased, so a
	// partial I/O failure retains ownership evidence for a guarded retry.
	sort.Slice(names, func(i, j int) bool {
		if names[i] == relative {
			return false
		}
		if names[j] == relative {
			return true
		}
		return names[i] < names[j]
	})
	opened := make([]openedFixture, 0, len(names))
	defer func() {
		for _, f := range opened {
			_ = f.file.Close()
		}
	}()
	// Open and recheck the complete allowlist before the first overwrite. Root
	// APIs reject escape through replaced ancestors; SameFile catches races.
	for _, name := range names {
		before, err := root.Lstat(name)
		if err != nil || !before.Mode().IsRegular() {
			return errors.New("fixture changed during inventory")
		}
		f, err := root.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			return errors.New("fixture could not be opened for overwrite")
		}
		actual, err := f.Stat()
		if err != nil || !os.SameFile(before, actual) {
			f.Close()
			return errors.New("fixture changed before overwrite")
		}
		opened = append(opened, openedFixture{name: name, file: f, info: actual})
	}
	for _, f := range opened {
		if err = wipeFixtureFile(f.file, f.info.Size(), rand.Reader); err != nil {
			return err
		}
		after, err := root.Lstat(f.name)
		if err != nil || !os.SameFile(f.info, after) {
			return errors.New("fixture path changed during overwrite")
		}
		if err = f.file.Close(); err != nil {
			return errors.New("fixture close failed")
		}
		if err = root.Remove(f.name); err != nil {
			return errors.New("fixture unlink failed")
		}
	}
	for _, name := range []string{"staff-secrets/staff-totp", "staff-secrets"} {
		if _, err = root.Stat(name); err == nil {
			if err = root.Remove(name); err != nil {
				return errors.New("fixture staff directory not empty")
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return errors.New("fixture directory check failed")
		}
	}
	return nil
}
