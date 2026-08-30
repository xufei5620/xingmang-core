// Command db-role-verify performs a read-only PostgreSQL catalog comparison
// against the committed DBR1 role policy. It never executes DDL/DML or role
// statements and never prints a DSN/password.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/dbroles"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
)

const (
	exitMatch     = 0
	exitViolation = 1
	exitConfig    = 2
)

var lookupEnv = os.LookupEnv

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, lookupEnv)) }

func run(args []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) int {
	flags := flag.NewFlagSet("db-role-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", filepath.FromSlash("contracts/database/role-policy.v1.json"), "path to the role policy artifact")
	dsnEnv := flags.String("database-url-env", "DATABASE_URL", "environment variable containing DATABASE_URL")
	format := flags.String("format", "text", "output format: text or json")
	nowText := flags.String("now", "", "optional UTC RFC3339Nano timestamp (for deterministic review)")
	if err := flags.Parse(args); err != nil {
		return exitConfig
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return exitConfig
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(stderr, "--format must be text or json")
		return exitConfig
	}
	now := time.Now().UTC()
	if *nowText != "" {
		parsed, ok := parseUTC(*nowText)
		if !ok {
			fmt.Fprintln(stderr, "--now must be UTC RFC3339Nano")
			return exitConfig
		}
		now = parsed
	}
	resolvedPolicyPath := resolvePolicyPath(*policyPath)
	policyBytes, err := os.ReadFile(resolvedPolicyPath)
	if err != nil {
		fmt.Fprintln(stderr, "policy artifact could not be read")
		return exitConfig
	}
	policy, _, err := dbroles.LoadPolicy(policyBytes)
	if err != nil {
		fmt.Fprintln(stderr, "policy artifact rejected")
		return exitConfig
	}
	dsn, ok := lookup(*dsnEnv)
	if !ok || strings.TrimSpace(dsn) == "" {
		fmt.Fprintln(stderr, "DATABASE_URL environment variable is empty")
		return exitConfig
	}
	if err := pgdsn.Validate(dsn, true); err != nil {
		fmt.Fprintln(stderr, "DATABASE_URL rejected")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := dbroles.OpenVerifierPool(ctx, dsn)
	if err != nil {
		fmt.Fprintln(stderr, "database connection failed")
		return exitConfig
	}
	defer pool.Close()
	violations, err := dbroles.VerifyCatalog(ctx, pool, policy, now)
	if err != nil {
		fmt.Fprintln(stderr, "catalog verification failed")
		return exitConfig
	}
	if *format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Match      bool                `json:"match"`
			Violations []dbroles.Violation `json:"violations"`
		}{Match: len(violations) == 0, Violations: violations}); err != nil {
			return exitConfig
		}
	} else if len(violations) == 0 {
		fmt.Fprintln(stdout, "DB ROLE POLICY MATCH")
	} else {
		for _, violation := range violations {
			fmt.Fprintf(stdout, "%s", formatViolation(violation))
			fmt.Fprintln(stdout)
		}
	}
	if len(violations) > 0 {
		return exitViolation
	}
	return exitMatch
}

func resolvePolicyPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	// `go test ./cmd/db-role-verify` runs with that package as cwd; walk toward
	// the repository root without accepting arbitrary external fallback paths.
	dir, err := os.Getwd()
	if err != nil {
		return path
	}
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, path)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return path
}

func formatViolation(v dbroles.Violation) string {
	parts := []string{v.Code}
	if v.Capability != "" {
		parts = append(parts, "capability="+v.Capability)
	}
	if v.Identity != "" {
		parts = append(parts, "identity="+v.Identity)
	}
	if v.Object != "" {
		parts = append(parts, "object="+v.Object)
	}
	if v.Message != "" {
		parts = append(parts, v.Message)
	}
	return strings.Join(parts, " ")
}

func parseUTC(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || !strings.HasSuffix(value, "Z") || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, false
	}
	return parsed, true
}
