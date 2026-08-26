package action

import (
	"errors"
	"testing"
)

func TestParseRiskLevel(t *testing.T) {
	for _, s := range []string{"L0", "L1", "L2", "L3", "L4"} {
		if _, err := ParseRiskLevel(s); err != nil {
			t.Fatalf("ParseRiskLevel(%q) err = %v", s, err)
		}
	}
	for _, s := range []string{"", "l0", "L5", "LOW"} {
		if _, err := ParseRiskLevel(s); !errors.Is(err, ErrInvalidRiskLevel) {
			t.Fatalf("ParseRiskLevel(%q) 应 ErrInvalidRiskLevel, got %v", s, err)
		}
	}
}

func TestRiskLevelFoundationBoundary(t *testing.T) {
	for _, tt := range []struct {
		lvl  RiskLevel
		want bool
	}{{L0, false}, {L1, false}, {L2, true}, {L3, true}, {L4, true}} {
		if got := tt.lvl.RequiresAdvancedControls(); got != tt.want {
			t.Fatalf("%s.RequiresAdvancedControls() = %v, want %v", tt.lvl, got, tt.want)
		}
	}
}
