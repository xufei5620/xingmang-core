package archive

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestDecodeRejectsMissingZeroNullUnknownAndDuplicateFields(t *testing.T) {
	cases := map[string][]byte{}
	for _, name := range []string{
		"missing-canonical", "zero-canonical", "null-field", "unknown-field",
		"duplicate-field", "misordered-field",
	} {
		raw, err := os.ReadFile("testdata/" + name + ".ndjson")
		if err != nil {
			t.Fatal(err)
		}
		cases[name] = raw
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePayload(bytes.NewReader(raw)); err == nil {
				t.Fatal("invalid frozen wire was accepted")
			} else {
				var compatibility *CompatibilityError
				if errors.As(err, &compatibility) {
					t.Fatalf("format defect mislabeled as verifier-outdated: %v", err)
				}
			}
		})
	}
}

func TestDecodeRejectsUnknownCanonicalAsVerifierOutdated(t *testing.T) {
	raw, err := os.ReadFile("testdata/unknown-canonical.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodePayload(bytes.NewReader(raw))
	var compatibility *CompatibilityError
	if !errors.As(err, &compatibility) || compatibility.Code != CompatibilityVerifierOutdated ||
		compatibility.CanonicalVersion != 3 {
		t.Fatalf("unknown canonical error = %#v, want typed verifier-outdated", err)
	}
}

func TestDecodeRejectsTruncatedFinalLineAndMissingLF(t *testing.T) {
	truncated, err := os.ReadFile("testdata/truncated-final-line.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"truncated-json-token": truncated,
		"missing-final-lf":     bytes.TrimSuffix(valid, []byte("\n")),
		"extra-final-lf":       append(append([]byte{}, valid...), '\n'),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePayload(bytes.NewReader(raw)); err == nil {
				t.Fatal("partial or non-canonical final boundary was accepted")
			}
		})
	}
}
