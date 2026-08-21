//go:build !windows

package sourceagent

import (
	"errors"
	"os"
)

func atomicReplaceFile(oldPath, newPath string) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return errors.New("atomically replace agent state failed")
	}
	return nil
}

func syncStateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open agent state directory for fsync failed")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("fsync agent state directory failed")
	}
	return nil
}
