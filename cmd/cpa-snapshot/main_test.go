package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cpasnapshot"
)

func TestParseCommandUsesFixedProductionDefaults(t *testing.T) {
	t.Parallel()

	command, err := parseCommand([]string{"publish"})
	if err != nil {
		t.Fatalf("parseCommand: %v", err)
	}
	if command.action != "publish" ||
		command.source != "/root/cpa-stack/cpam-data/usage.sqlite" ||
		command.target != "/var/lib/xingmang/cpa-snapshot/published/usage.sqlite" ||
		command.groupID != 10001 {
		t.Fatalf("command = %+v, want fixed production defaults", command)
	}
}

func TestParseCommandRejectsUnknownActionAndRelativePaths(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"delete"},
		{"publish", "--source", "usage.sqlite"},
		{"publish", "--target", "snapshot/usage.sqlite"},
		{"publish", "--target", "/srv/xingmang/cpa-snapshot/not-usage.db"},
		{"verify", "unexpected"},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("parseCommand(%q) succeeded", args)
		}
	}
}

func TestRunEmitsOnlyNonSensitiveMetadata(t *testing.T) {
	t.Parallel()

	fixed := cpasnapshot.Metadata{
		Generation: "0123456789abcdef0123456789abcdef",
		ObservedAt: time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC),
	}
	var output bytes.Buffer
	err := run(context.Background(), []string{"publish"}, &output, commandDeps{
		publish: func(context.Context, cpasnapshot.Options) (cpasnapshot.Metadata, error) {
			return fixed, nil
		},
		verify: func(context.Context, string) (cpasnapshot.Metadata, error) {
			return cpasnapshot.Metadata{}, errors.New("must not be called")
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	text := output.String()
	for _, want := range []string{`"status":"published"`, fixed.Generation, "2026-09-01T01:02:03Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q does not contain %q", text, want)
		}
	}
	for _, forbidden := range []string{"/root/cpa-stack", "/srv/xingmang", "api_key", "auths", "config.yaml"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("output %q contains forbidden detail %q", text, forbidden)
		}
	}
}

func TestRunVerifyDoesNotPublish(t *testing.T) {
	t.Parallel()

	called := false
	var output bytes.Buffer
	err := run(context.Background(), []string{"verify"}, &output, commandDeps{
		publish: func(context.Context, cpasnapshot.Options) (cpasnapshot.Metadata, error) {
			called = true
			return cpasnapshot.Metadata{}, nil
		},
		verify: func(context.Context, string) (cpasnapshot.Metadata, error) {
			return cpasnapshot.Metadata{
				Generation: "0123456789abcdef0123456789abcdef",
				ObservedAt: time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC),
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("run verify: %v", err)
	}
	if called {
		t.Fatal("verify command called publisher")
	}
	if !strings.Contains(output.String(), `"status":"verified"`) {
		t.Fatalf("output = %q, want verified status", output.String())
	}
}

func TestRunVersionReportsBuildCommitForInstaller(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := run(context.Background(), []string{"version"}, &output, commandDeps{
		publish: func(context.Context, cpasnapshot.Options) (cpasnapshot.Metadata, error) {
			return cpasnapshot.Metadata{}, nil
		},
		verify: func(context.Context, string) (cpasnapshot.Metadata, error) {
			return cpasnapshot.Metadata{}, nil
		},
		version: func() (string, string) { return "production", "0123456789abcdef0123456789abcdef01234567" },
	})
	if err != nil {
		t.Fatalf("run version: %v", err)
	}
	if output.String() != "production 0123456789abcdef0123456789abcdef01234567\n" {
		t.Fatalf("version output = %q", output.String())
	}
}
