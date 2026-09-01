//go:build windows

package cpasnapshot

import (
	"fmt"
	"os"
)

// Windows is a development/test platform for this lifecycle tool. Exclusive
// creation provides deterministic in-process test coverage; production uses
// the Linux flock implementation below.
func acquirePublishLock(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cpa snapshot: another publisher holds the lock: %w", err)
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(path)
	}, nil
}
