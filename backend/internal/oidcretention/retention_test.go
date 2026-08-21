package oidcretention

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeOptionsSafetyBounds(t *testing.T) {
	defaults, err := normalizeOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Retention != DefaultRetention || defaults.BatchSize != 500 || defaults.Execute {
		t.Fatalf("unsafe defaults: %+v", defaults)
	}
	for name, options := range map[string]Options{
		"below minimum retention":                                 {Retention: MinimumRetention - time.Second},
		"zero batch after normalization is valid only as default": {BatchSize: -1},
		"oversized batch":                                         {BatchSize: 5001},
		"short statement timeout":                                 {StatementTimeout: time.Second},
		"execute without confirmation":                            {Execute: true, Reason: "approved"},
		"execute without reason":                                  {Execute: true, MaintenanceConfirmed: true},
		"multiline reason":                                        {Execute: true, MaintenanceConfirmed: true, Reason: "one\ntwo"},
		"oversized reason":                                        {Execute: true, MaintenanceConfirmed: true, Reason: strings.Repeat("x", 257)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, normalizeErr := normalizeOptions(options); !errors.Is(normalizeErr, ErrInvalidOptions) {
				t.Fatalf("error=%v", normalizeErr)
			}
		})
	}
	valid, err := normalizeOptions(Options{
		Retention: MinimumRetention, BatchSize: 1, Execute: true,
		MaintenanceConfirmed: true, Reason: "scheduled replay-record retention",
	})
	if err != nil || !valid.Execute || valid.Retention != MinimumRetention {
		t.Fatalf("valid options=%+v error=%v", valid, err)
	}
}
