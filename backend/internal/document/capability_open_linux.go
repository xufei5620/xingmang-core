//go:build linux

package document

import (
	"os"
	"syscall"
)

func openScannerCapabilityNoFollow(path string) (*os.File, os.FileInfo, error) {
	descriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func scannerCapabilityOwnershipOK(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	switch info.Mode().Perm() {
	case 0o400:
		return stat.Uid == uint32(os.Geteuid())
	case 0o440:
		return stat.Uid == 0 && stat.Gid == 10000
	default:
		return false
	}
}
