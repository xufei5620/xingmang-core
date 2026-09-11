package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSecretLineCaptureContract exercises only inert files. Error messages
// describe the boundary and never include captured data or its rejected value.
func TestReadSecretLineCaptureContract(t *testing.T) {
	const marker = "inert-format-fixture"
	const limit = int64(64)
	for _, tc := range []struct {
		name   string
		body   string
		wantOK bool
	}{
		{"plain", marker, true},
		{"lf", marker + "\n", true},
		{"crlf", marker + "\r\n", true},
		{"crlf-multiline", marker + "\r\nsecond-line\r\n", false},
		{"lf-multiline", marker + "\nsecond-line\n", false},
		{"nul", marker + "\x00", false},
		{"empty", "", false},
		{"empty-line", "\r\n", false},
		{"over-limit", strings.Repeat("x", int(limit)+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inert-input.txt")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal("could not create inert input")
			}
			value, err := readSecretLine(path, limit)
			if tc.wantOK {
				if err != nil || value != marker {
					t.Fatal("valid single-line capture rejected or changed")
				}
			} else if err == nil || value != "" {
				t.Fatal("invalid capture must fail without returning data")
			}
		})
	}
	for _, kind := range []string{"missing", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			if kind == "missing" {
				path = filepath.Join(path, "not-created.txt")
			}
			if value, err := readSecretLine(path, limit); err == nil || value != "" {
				t.Fatal("read failure must reject without returning data")
			}
		})
	}
}
