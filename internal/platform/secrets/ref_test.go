package secrets

import "testing"

func TestParseCredentialRef(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
		scope   string
		name    string
	}{
		{"secret://sub2api-prod/read-only-admin", false, "sub2api-prod", "read-only-admin"},
		{"secret://alerting/telegram-primary", false, "alerting", "telegram-primary"},
		{"secret://a/b", false, "a", "b"},
		{"", true, "", ""},
		{"sub2api-prod/read-only-admin", true, "", ""},
		{"secret://UPPER/name", true, "", ""},
		{"secret://scope/", true, "", ""},
		{"secret:///name", true, "", ""},
		{"secret://scope/na/me", true, "", ""},
		{"secret://scope/../etc", true, "", ""},
		{"secret://-scope/name", true, "", ""},
		{"secret://scope/name extra", true, "", ""},
	}
	for _, tt := range tests {
		ref, err := ParseCredentialRef(tt.in)
		if tt.wantErr != (err != nil) {
			t.Fatalf("Parse(%q) err = %v, wantErr = %v", tt.in, err, tt.wantErr)
		}
		if err != nil {
			continue
		}
		if ref.Scope() != tt.scope || ref.Name() != tt.name {
			t.Fatalf("Parse(%q) = %s/%s, want %s/%s", tt.in, ref.Scope(), ref.Name(), tt.scope, tt.name)
		}
		if ref.String() != tt.in {
			t.Fatalf("String() = %q, want 回环 %q", ref.String(), tt.in)
		}
	}
}

func TestCredentialRefZeroAndText(t *testing.T) {
	var zero CredentialRef
	if !zero.IsZero() {
		t.Fatal("零值应 IsZero")
	}
	ref := MustCredentialRef("secret://ops/token")
	b, err := ref.MarshalText()
	if err != nil || string(b) != "secret://ops/token" {
		t.Fatalf("MarshalText = %q, %v", b, err)
	}
	var back CredentialRef
	if err := back.UnmarshalText(b); err != nil || back != ref {
		t.Fatalf("UnmarshalText 回环失败: %v", err)
	}
	var bad CredentialRef
	if err := bad.UnmarshalText([]byte("junk")); err == nil {
		t.Fatal("非法文本应报错")
	}
}
