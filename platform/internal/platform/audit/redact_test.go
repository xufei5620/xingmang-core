package audit

import "testing"

func TestRedactReplacesMatchedKeys(t *testing.T) {
	in := map[string]any{"user": "alice", "password": "hunter2", "nested": map[string]any{"api_key": "k"}}
	out := Redact(in, "password", "api_key")

	if out["password"] != "[REDACTED]" {
		t.Fatalf("password 未脱敏: %v", out["password"])
	}
	if out["user"] != "alice" {
		t.Fatalf("非敏感字段被改动: %v", out["user"])
	}
	nested, ok := out["nested"].(map[string]any)
	if !ok || nested["api_key"] != "[REDACTED]" {
		t.Fatalf("嵌套字段未脱敏: %v", out["nested"])
	}
	if in["password"] != "hunter2" {
		t.Fatal("Redact 不应修改入参")
	}
}

func TestRedactDefaultCoversCommonSecretNames(t *testing.T) {
	in := map[string]any{
		"password": "x", "secret": "x", "token": "x", "api_key": "x",
		"credential": "x", "authorization": "x", "cookie": "x", "dsn": "x",
		"Access_Token": "x",
		"safe":         "keep",
	}
	out := RedactDefault(in)
	for k, v := range out {
		if k == "safe" {
			if v != "keep" {
				t.Fatalf("安全字段被误脱敏: %v", v)
			}
			continue
		}
		if v != "[REDACTED]" {
			t.Fatalf("敏感字段 %q 未脱敏: %v", k, v)
		}
	}
}

func TestRedactHandlesNilAndEmpty(t *testing.T) {
	if out := Redact(nil, "x"); out != nil {
		t.Fatalf("nil 入参应返回 nil, got %v", out)
	}
	if out := RedactDefault(map[string]any{}); len(out) != 0 {
		t.Fatalf("空 map 应返回空 map, got %v", out)
	}
}
