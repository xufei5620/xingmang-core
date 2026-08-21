//go:build windows

package sourceagent

import "os"

// Windows DACL enforcement remains an external deployment gate.
func secureStateDirectoryPermissions(info os.FileInfo) bool {
	return info != nil && info.IsDir()
}
