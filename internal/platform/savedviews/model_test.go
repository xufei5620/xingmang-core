package savedviews

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func validState() StateV1 {
	return StateV1{
		SchemaVersion:  1,
		Query:          "openai",
		Filters:        map[string]string{"status": "需关注"},
		Sort:           &Sort{ColumnID: "grossProfit", Direction: SortDesc},
		KnownColumns:   []string{"name", "status", "grossProfit"},
		VisibleColumns: []string{"name", "status"},
		Density:        DensityCompact,
	}
}

func TestOwnerFromPrincipalUsesImmutableOIDCSubject(t *testing.T) {
	p := principal.Principal{
		ID: "renamed-alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "https://auth.example/realms/staff", Subject: "immutable-sub-1",
		Environment: "production",
	}
	owner, err := OwnerFromPrincipal(p)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Subject != "immutable-sub-1" || owner.Subject == p.ID {
		t.Fatalf("owner subject = %q, principal id = %q", owner.Subject, p.ID)
	}
}

func TestOwnerFromPrincipalAllowsDevIDFallbackOutsideProduction(t *testing.T) {
	p := principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "dev://header-resolver", Environment: "development",
	}
	owner, err := OwnerFromPrincipal(p)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Subject != p.ID {
		t.Fatalf("subject = %q, want %q", owner.Subject, p.ID)
	}
}

func TestOwnerFromPrincipalRejectsMissingOIDCSubject(t *testing.T) {
	for _, p := range []principal.Principal{
		{ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff", Issuer: "https://auth.example", Environment: "staging"},
		{ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff", Issuer: "dev://header-resolver", Environment: "production"},
	} {
		if _, err := OwnerFromPrincipal(p); err == nil {
			t.Fatalf("accepted principal: %#v", p)
		}
	}
}

func TestOwnerFromPrincipalRejectsMachineTypes(t *testing.T) {
	for _, typ := range []principal.Type{principal.TypeService, principal.TypeAI, principal.TypeServerAgent} {
		p := principal.Principal{ID: "machine", Type: typ, IdentityZone: "machine", Issuer: "issuer", Subject: "sub", Environment: "development"}
		if _, err := OwnerFromPrincipal(p); err == nil {
			t.Fatalf("accepted %s", typ)
		}
	}
}

func TestStateV1ValidationLimits(t *testing.T) {
	tests := []struct {
		name string
		edit func(*StateV1)
	}{
		{"version", func(s *StateV1) { s.SchemaVersion = 2 }},
		{"query runes", func(s *StateV1) { s.Query = strings.Repeat("界", 257) }},
		{"filter count", func(s *StateV1) {
			s.Filters = map[string]string{}
			for i := 0; i < 17; i++ {
				s.Filters[string(rune('a'+i))] = "x"
			}
		}},
		{"filter value", func(s *StateV1) { s.Filters["status"] = strings.Repeat("界", 257) }},
		{"known empty", func(s *StateV1) { s.KnownColumns = nil; s.VisibleColumns = nil }},
		{"known duplicate", func(s *StateV1) { s.KnownColumns = []string{"name", "name"}; s.VisibleColumns = []string{"name"} }},
		{"invalid density", func(s *StateV1) { s.Density = "dense" }},
		{"sort pair", func(s *StateV1) { s.Sort = &Sort{ColumnID: "name", Direction: "sideways"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validState()
			tt.edit(&s)
			if err := ValidateState(s); err == nil {
				t.Fatalf("ValidateState accepted %#v", s)
			}
		})
	}
}

func TestVisibleColumnsMustBeSubsetOfKnown(t *testing.T) {
	s := validState()
	s.VisibleColumns = append(s.VisibleColumns, "secretColumn")
	if err := ValidateState(s); err == nil {
		t.Fatal("accepted visible column absent from known")
	}
}

func TestNormalizeNameRejectsReservedNames(t *testing.T) {
	if got, err := NormalizeName("  高风险   复核  "); err != nil || got != "高风险 复核" {
		t.Fatalf("normalize = %q, %v", got, err)
	}
	for _, name := range []string{"", "   ", "全部", "自定义", strings.Repeat("界", 25)} {
		if _, err := NormalizeName(name); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
}

func TestCanonicalHashIgnoresFilterMapInsertionOrder(t *testing.T) {
	a := validState()
	a.Filters = map[string]string{"status": "需关注", "method": "Key"}
	b := validState()
	b.Filters = map[string]string{}
	b.Filters["method"] = "Key"
	b.Filters["status"] = "需关注"
	ha, err := CanonicalStateHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalStateHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb || len(ha) != 64 {
		t.Fatalf("hashes = %q / %q", ha, hb)
	}
}

func TestBoundedJSONDecodersRejectInvalidAndTrailingInput(t *testing.T) {
	if _, err := DecodeFiltersJSON(bytes.Repeat([]byte{' '}, MaxFiltersJSONBytes+1)); err == nil {
		t.Fatal("accepted over-cap filters JSON")
	}
	if _, err := DecodeFiltersJSON([]byte{0xff, 0xfe}); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	if _, err := DecodeFiltersJSON([]byte(`{"status":"ok"}{"extra":"no"}`)); err == nil {
		t.Fatal("accepted trailing JSON object")
	}
	escaped := []byte(`{"status":"` + strings.Repeat(`\u754c`, 257) + `"}`)
	if utf8.Valid(escaped) == false {
		t.Fatal("test fixture must be valid UTF-8")
	}
	if _, err := DecodeFiltersJSON(escaped); err == nil {
		t.Fatal("accepted escaped value expansion past rune limit")
	}
	if _, err := DecodeStateV1JSON(bytes.Repeat([]byte{' '}, MaxStateJSONBytes+1)); err == nil {
		t.Fatal("accepted over-cap state JSON")
	}
}
