package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "capture":
		return runCapture(args[1:], stdout, stderr, captureDeps{})
	case "reqlog":
		return runReqlog(args[1:], stdout, stderr, reqlogDeps{})
	case "-h", "--help", "help":
		printUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "evidence-capture: unknown subcommand %q\n\n", args[0])
		printUsage(stderr)
		return exitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `evidence-capture - read-only evidence for SUB2_REAL_APPROVAL / NEWAPI_REAL_APPROVAL / REQLOG_USERREF_APPROVAL

Usage:
  evidence-capture capture --platform sub2api|newapi [--dry-run] [flags...]
  evidence-capture reqlog  --data-dir <path> --tokenmap <path> [--dry-run] [flags...]

Run "evidence-capture capture -h" or "evidence-capture reqlog -h" for flags.
See docs/runbooks/USERS-REAL-APPROVAL.md for the full operator procedure.`)
}

// defaultLogger is the structured logger passed to secrets.NewSlogRecorder
// for credential access audit records (secrets.AccessRecord never carries
// plaintext - see internal/platform/secrets/audit.go). Kept as its own
// function (rather than a package var) so a future caller could swap it for
// a file-backed logger without touching capture.go/reqlog.go.
func defaultLogger() *slog.Logger { return slog.Default() }
