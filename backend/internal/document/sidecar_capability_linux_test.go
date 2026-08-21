//go:build linux

package document

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadScannerCapabilityNeverFollowsAtomicSymlinkReplacement(t *testing.T) {
	root := t.TempDir()
	capability := filepath.Join(root, "capability")
	trusted := strings.Repeat("a", 64)
	attacker := strings.Repeat("b", 64)
	target := filepath.Join(root, "outside-secret")
	if err := os.WriteFile(capability, []byte(trusted+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(attacker+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			regular := filepath.Join(root, "regular.part")
			_ = os.Remove(regular)
			if err := os.WriteFile(regular, []byte(trusted+"\n"), 0o400); err == nil {
				_ = os.Rename(regular, capability)
			}
			link := filepath.Join(root, "link.part")
			_ = os.Remove(link)
			if err := os.Symlink(target, link); err == nil {
				_ = os.Rename(link, capability)
			}
		}
	}()
	for index := 0; index < 5000; index++ {
		value, err := LoadScannerCapability(capability)
		if err == nil && value != trusted {
			close(stop)
			wait.Wait()
			t.Fatalf("followed replacement and returned untrusted capability %q", value)
		}
	}
	close(stop)
	wait.Wait()
}
