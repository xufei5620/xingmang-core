package buildinfo

import (
	"strings"
	"testing"
)

func TestStringContainsNameAndVersion(t *testing.T) {
	s := String()
	if !strings.Contains(s, "xingmang-platform") {
		t.Fatalf("String() = %q, 缺少项目名", s)
	}
	if !strings.Contains(s, Version) {
		t.Fatalf("String() = %q, 缺少版本 %q", s, Version)
	}
}
