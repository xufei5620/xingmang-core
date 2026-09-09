//go:build !linux

package document

import (
	"errors"
	"os"
)

func openScannerCapabilityNoFollow(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("capability is not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, errors.New("capability changed while opening")
	}
	return file, after, nil
}

func scannerCapabilityOwnershipOK(os.FileInfo) bool { return true }
