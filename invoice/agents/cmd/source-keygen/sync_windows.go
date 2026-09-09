//go:build windows

package main

// Windows FlushFileBuffers does not support directory handles opened through
// os.Open. Each key file itself is flushed before this no-op.
func syncOutputDirectory(string) error { return nil }
