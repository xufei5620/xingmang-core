package principal

import (
	"context"
	"errors"
	"testing"
)

func validHuman() Principal {
	return Principal{
		ID:                  "staff_alice",
		Type:                TypeHuman,
		IdentityZone:        "staff",
		Issuer:              "https://auth.solov.cc/realms/solov-staff",
		Subject:             "1a2b3c",
		AuthenticationLevel: "mfa",
		Environment:         "production",
		Scopes:              []string{"registry.read", "registry.service.manage"},
	}
}

func TestParseType(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    Type
		wantErr bool
	}{
		{"HUMAN", TypeHuman, false},
		{"SERVICE", TypeService, false},
		{"AI", TypeAI, false},
		{"SERVER_AGENT", TypeServerAgent, false},
		{"human", "", true},
		{"", "", true},
		{"ROBOT", "", true},
	} {
		got, err := ParseType(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrInvalidType) {
				t.Fatalf("ParseType(%q) err = %v, want ErrInvalidType", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("ParseType(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestPrincipalValidate(t *testing.T) {
	if err := validHuman().Validate(); err != nil {
		t.Fatalf("合法 Principal 被拒绝: %v", err)
	}
	for name, mutate := range map[string]func(*Principal){
		"空 ID":          func(p *Principal) { p.ID = "" },
		"非法 Type":       func(p *Principal) { p.Type = Type("ROBOT") },
		"空 Environment": func(p *Principal) { p.Environment = "" },
		"空 Issuer":      func(p *Principal) { p.Issuer = "" },
	} {
		p := validHuman()
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Fatalf("%s：应被拒绝但通过了", name)
		}
	}
}

func TestPrincipalScopeAndMachine(t *testing.T) {
	p := validHuman()
	if !p.HasScope("registry.read") {
		t.Fatal("HasScope 应命中")
	}
	if p.HasScope("registry.connection.manage") {
		t.Fatal("HasScope 不应命中未授予的权限")
	}
	if p.IsMachine() {
		t.Fatal("HUMAN 不是机器身份")
	}
	for _, ty := range []Type{TypeService, TypeAI, TypeServerAgent} {
		m := validHuman()
		m.Type = ty
		if !m.IsMachine() {
			t.Fatalf("%s 应是机器身份", ty)
		}
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := FromContext(ctx); ok {
		t.Fatal("空 ctx 不应有 Principal")
	}
	want := validHuman()
	got, ok := FromContext(WithPrincipal(ctx, want))
	if !ok || got.ID != want.ID {
		t.Fatalf("FromContext = %+v, %v", got, ok)
	}
}
