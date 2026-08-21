//go:build windows

package sourceagent

import "os"

// Windows os.FileMode does not expose the DACL and reports writable files as
// 0666 even after Chmod(0600). The production Windows gate must therefore
// validate the dedicated volume ACL externally; Linux production is enforced
// directly as 0600 by secureStatePermissions.
func secureStatePermissions(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}
