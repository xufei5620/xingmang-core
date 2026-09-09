//go:build !windows

package sourceagent

import "os"

func secureStatePermissions(info os.FileInfo) bool {
	return info != nil && info.Mode().Perm()&0o077 == 0
}
