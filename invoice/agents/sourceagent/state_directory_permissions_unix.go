//go:build !windows

package sourceagent

import "os"

func secureStateDirectoryPermissions(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0o077 == 0
}
