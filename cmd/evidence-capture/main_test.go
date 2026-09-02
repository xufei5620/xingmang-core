package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunMainNoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMain(nil, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected usage text on stderr, got:\n%s", stderr.String())
	}
}

func TestRunMainUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"bogus"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), `unknown subcommand "bogus"`) {
		t.Errorf("expected an unknown-subcommand message, got:\n%s", stderr.String())
	}
}

func TestRunMainHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"--help"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "capture") || !strings.Contains(stdout.String(), "reqlog") {
		t.Errorf("help text should mention both subcommands, got:\n%s", stdout.String())
	}
}

func TestRunMainDispatchesToCapture(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"capture", "--platform", "sub2api", "--dry-run"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), sub2apiRouteVersion) {
		t.Errorf("expected capture's dry-run plan on stdout, got:\n%s", stdout.String())
	}
}

func TestRunMainDispatchesToReqlog(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"reqlog", "--dry-run"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "index.jsonl") {
		t.Errorf("expected reqlog's dry-run plan on stdout, got:\n%s", stdout.String())
	}
}
