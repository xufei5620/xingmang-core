package registry

import (
	"errors"
	"testing"
)

func TestParseEnvironment(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    Environment
		wantErr bool
	}{
		{"development", EnvDevelopment, false},
		{"staging", EnvStaging, false},
		{"production", EnvProduction, false},
		{"", "", true},
		{"prod", "", true},
		{"PRODUCTION", "", true},
		{" production", "", true},
	} {
		got, err := ParseEnvironment(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("ParseEnvironment(%q) err = %v, want ErrInvalidEnvironment", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("ParseEnvironment(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestEnvironmentHelpers(t *testing.T) {
	if !EnvProduction.IsProduction() {
		t.Fatal("production 应 IsProduction")
	}
	if EnvStaging.IsProduction() {
		t.Fatal("staging 不应 IsProduction")
	}
	if EnvDevelopment.String() != "development" {
		t.Fatalf("String() = %q", EnvDevelopment.String())
	}
	all := AllEnvironments()
	if len(all) != 3 {
		t.Fatalf("AllEnvironments 应为 3 个，got %d", len(all))
	}
}
