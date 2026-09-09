package assurance

import (
	"errors"
	"testing"
)

func TestParseTargetsValid(t *testing.T) {
	targets, err := ParseTargets(`[{"channel_id":"chn-1","external_channel_id":"12","model":"claude-sonnet-4"}]`)
	if err != nil {
		t.Fatalf("ParseTargets() = %v, want nil", err)
	}
	if len(targets) != 1 || targets[0].ChannelID != "chn-1" || targets[0].Model != "claude-sonnet-4" {
		t.Fatalf("targets = %+v, unexpected shape", targets)
	}
}

func TestParseTargetsRejectsEmptyArray(t *testing.T) {
	if _, err := ParseTargets(`[]`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseTargetsRejectsMissingChannelID(t *testing.T) {
	if _, err := ParseTargets(`[{"model":"gpt-4o"}]`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseTargetsRejectsMissingModel(t *testing.T) {
	if _, err := ParseTargets(`[{"channel_id":"chn-1"}]`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseTargetsRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseTargets(`not json`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseExpectedShapeValid(t *testing.T) {
	shape, err := ParseExpectedShape(`{"min_length":1,"max_length":2000,"must_contain":["ok"],"timeout_ms":30000}`)
	if err != nil {
		t.Fatalf("ParseExpectedShape() = %v, want nil", err)
	}
	if shape.MinLength != 1 || shape.MaxLength != 2000 || shape.TimeoutMS != 30000 || len(shape.MustContain) != 1 {
		t.Fatalf("shape = %+v, unexpected", shape)
	}
}

func TestParseExpectedShapeRejectsInvertedBounds(t *testing.T) {
	if _, err := ParseExpectedShape(`{"min_length":100,"max_length":10,"timeout_ms":1000}`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseExpectedShapeRejectsTimeoutAboveHardCap(t *testing.T) {
	if _, err := ParseExpectedShape(`{"min_length":1,"max_length":10,"timeout_ms":60000}`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput (declared timeout above the 30s Job hard cap is self-contradictory)", err)
	}
}

func TestParseExpectedShapeRejectsZeroTimeout(t *testing.T) {
	if _, err := ParseExpectedShape(`{"min_length":1,"max_length":10,"timeout_ms":0}`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestParseExpectedShapeRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseExpectedShape(`{"min_length":`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}
