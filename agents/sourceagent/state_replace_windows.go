//go:build windows

package sourceagent

import (
	"errors"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func atomicReplaceFile(oldPath, newPath string) error {
	oldPointer, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return errors.New("encode temporary agent state path failed")
	}
	newPointer, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return errors.New("encode agent state path failed")
	}
	result, _, _ := moveFileExW.Call(
		uintptr(unsafe.Pointer(oldPointer)),
		uintptr(unsafe.Pointer(newPointer)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if result == 0 {
		return errors.New("atomically replace agent state failed")
	}
	return nil
}

// MoveFileExW with MOVEFILE_WRITE_THROUGH flushes the replacement on Windows;
// opening a directory and calling FlushFileBuffers is not supported.
func syncStateDirectory(string) error { return nil }
