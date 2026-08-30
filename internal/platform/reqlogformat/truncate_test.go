package reqlogformat

import "testing"

func TestTruncate(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Fatalf("短于上限不该截断: got %q", got)
	}
	got := Truncate("hello world", 5)
	want := "hello ...(共11字节)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
