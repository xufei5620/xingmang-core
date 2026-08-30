package main

import (
	"io"
	"log/slog"
	"testing"
)

func discardLoggerCPA() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseCPAMode(t *testing.T) {
	cases := map[string]struct {
		want    cpaMode
		wantErr bool
	}{
		"":        {want: cpaModeOff},
		"off":     {want: cpaModeOff},
		"OFF":     {want: cpaModeOff},
		"file":    {want: cpaModeFile},
		" file  ": {want: cpaModeFile},
		"real":    {wantErr: true},
		"fake":    {wantErr: true},
		"bogus":   {wantErr: true},
	}
	for input, tc := range cases {
		got, err := parseCPAMode(input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseCPAMode(%q) = %q, nil; want error", input, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseCPAMode(%q) = %q, %v; want %q, nil", input, got, err, tc.want)
		}
	}
}

func TestCPAConfigFromEnv(t *testing.T) {
	values := map[string]string{
		"XM_CPA_MODE":     "file",
		"XM_CPA_DATA_DIR": "/var/lib/xm/cpa",
	}
	cfg, err := cpaConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != cpaModeFile || cfg.DataDir != "/var/lib/xm/cpa" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestCPAConfigFromEnvDefaultsToOff(t *testing.T) {
	cfg, err := cpaConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != cpaModeOff {
		t.Fatalf("Mode = %q, want off", cfg.Mode)
	}
}

func TestNewCPAKeysQuerier_OffReturnsNilNil(t *testing.T) {
	q, err := newCPAKeysQuerier(cpaConfig{Mode: cpaModeOff}, discardLoggerCPA())
	if err != nil {
		t.Fatalf("off mode should not error: %v", err)
	}
	if q != nil {
		t.Fatal("off mode should return a nil querier so the route does not mount")
	}
}

func TestNewCPAKeysQuerier_FileRequiresDataDir(t *testing.T) {
	if _, err := newCPAKeysQuerier(cpaConfig{Mode: cpaModeFile}, discardLoggerCPA()); err == nil {
		t.Fatal("file mode with empty DataDir should fail closed at startup")
	}
}

func TestNewCPAKeysQuerier_FileConstructsWithoutTouchingDisk(t *testing.T) {
	// NewFileClient only validates strings at construction (see
	// connectors/cpa.NewFileClient's doc comment) — this must succeed even
	// though the directory does not exist; Health()/KeyUsage() are what
	// would fail, not construction.
	q, err := newCPAKeysQuerier(cpaConfig{Mode: cpaModeFile, DataDir: "/does/not/exist"}, discardLoggerCPA())
	if err != nil {
		t.Fatalf("construction should not touch the filesystem: %v", err)
	}
	if q == nil {
		t.Fatal("file mode with a data dir should return a non-nil querier")
	}
}

func TestNewCPAKeysQuerier_UnknownModeRejected(t *testing.T) {
	if _, err := newCPAKeysQuerier(cpaConfig{Mode: "bogus"}, discardLoggerCPA()); err == nil {
		t.Fatal("unknown mode should be rejected")
	}
}
