package auth

import (
	"bytes"
	"testing"
)

func TestBindingKeyLineEndingNormalizationPreservesContents(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"  exact key  \r\n", "  exact key  "}, {"key\n\n", "key\n"}, {"key\r", "key\r"}, {"key", "key"}} {
		if got := stripOneLineEnding([]byte(tc.in)); !bytes.Equal(got, []byte(tc.want)) {
			t.Fatalf("line ending normalization changed content: %q", got)
		}
	}
}
