//go:build !windows

package main

import (
	"errors"
	"os"
)

func syncOutputDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open key output directory for fsync failed")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("fsync key output directory failed")
	}
	return nil
}
