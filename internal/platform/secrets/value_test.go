package secrets

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretValueNeverLeaks(t *testing.T) {
	v := NewSecretValue([]byte("test-value-super"))
	if v.Reveal() != "test-value-super" {
		t.Fatalf("Reveal() = %q", v.Reveal())
	}
	for name, got := range map[string]string{
		"String":   v.String(),
		"GoString": v.GoString(),
		"fmt %v":   fmt.Sprintf("%v", v),
		"fmt %s":   fmt.Sprintf("%s", v),
		"fmt %+v":  fmt.Sprintf("%+v", v),
		"fmt %#v":  fmt.Sprintf("%#v", v),
	} {
		if strings.Contains(got, "test-value-super") {
			t.Fatalf("%s 泄漏明文: %q", name, got)
		}
		if !strings.Contains(got, "REDACTED") {
			t.Fatalf("%s 未标记脱敏: %q", name, got)
		}
	}
}

func TestSecretValueJSONRefused(t *testing.T) {
	v := NewSecretValue([]byte("test-value-super"))
	if _, err := json.Marshal(v); err == nil {
		t.Fatal("SecretValue 参与 JSON 序列化必须报错")
	}
	if _, err := json.Marshal(struct{ V SecretValue }{v}); err == nil {
		t.Fatal("嵌入结构体序列化也必须报错")
	}
}

func TestSecretValueSlogRedacted(t *testing.T) {
	var sb strings.Builder
	l := slog.New(slog.NewTextHandler(&sb, nil))
	l.Info("m", "secret", NewSecretValue([]byte("test-value-super")))
	if strings.Contains(sb.String(), "test-value-super") {
		t.Fatalf("slog 泄漏明文: %s", sb.String())
	}
}

func TestSecretValueZero(t *testing.T) {
	var zero SecretValue
	if !zero.IsZero() || zero.Reveal() != "" {
		t.Fatal("零值语义错误")
	}
}
