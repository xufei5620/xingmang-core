package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"invoice-system/agents/sourceagent"
)

// TestExpectedBridgeRoutineHashesMatchTheReviewedContracts pins
// expectedBridgeRoutineHash to the reviewed SQL under contracts/: check-db
// compares sha256(pg_proc.prosrc) of the installed bridge routine against
// these constants, and prosrc is exactly the text between `AS $bridge$` and
// the closing `$bridge$;` of the CREATE FUNCTION statement psql streams in
// (leading newline included). Editing a bridge body without repinning the
// constant would make every production check-db refuse the freshly
// installed routine; this test fails first (XM-INV-NEGATIVE-DEFICIT).
func TestExpectedBridgeRoutineHashesMatchTheReviewedContracts(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts")
	streams := []string{sourceagent.StreamPayments, sourceagent.StreamUsage, sourceagent.StreamCredits,
		sourceagent.StreamBalances, sourceagent.StreamIdentities}
	for _, source := range []string{sourceagent.SourceSub2API, sourceagent.SourceNewAPI} {
		economic, err := os.ReadFile(filepath.Join(root, source+"-economic-projection-grants.postgresql.sql"))
		if err != nil {
			t.Fatal(err)
		}
		projection, err := os.ReadFile(filepath.Join(root, source+"-source-projection-grants.postgresql.sql"))
		if err != nil {
			t.Fatal(err)
		}
		for _, stream := range streams {
			function, err := sourceBridgeFunctionName(source, stream)
			if err != nil {
				t.Fatal(err)
			}
			body, ok := bridgeRoutineBody(string(economic), function)
			if !ok {
				body, ok = bridgeRoutineBody(string(projection), function)
			}
			if !ok {
				t.Fatalf("%s/%s: routine %s not found in the reviewed contracts", source, stream, function)
			}
			sum := sha256.Sum256([]byte(body))
			got := hex.EncodeToString(sum[:])
			want := expectedBridgeRoutineHash(runConfig{SourceType: source, StreamID: stream})
			if got != want {
				t.Fatalf("%s/%s: reviewed contract body hashes to %s but expectedBridgeRoutineHash pins %s; repin the constant after reviewing the SQL change", source, stream, got, want)
			}
		}
	}
}

func sourceBridgeFunctionName(source, stream string) (string, error) {
	suffix := map[string]string{
		sourceagent.StreamPayments: "payments_v4", sourceagent.StreamUsage: "usage_v4",
		sourceagent.StreamCredits: "credits_v4", sourceagent.StreamBalances: "balances_v4",
		sourceagent.StreamIdentities: "identities_v4",
	}[stream]
	return source + "_" + suffix, nil
}

// bridgeRoutineBody extracts prosrc for one CREATE [OR REPLACE] FUNCTION
// invoice_bridge.<function>(...) statement: everything after `AS $bridge$`
// up to the terminating `$bridge$;`, byte for byte.
func bridgeRoutineBody(contract, function string) (string, bool) {
	pattern := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION invoice_bridge\.` + regexp.QuoteMeta(function) +
		`\([^)]*\)\s*\nRETURNS[^\n]*\n(?:[^\n]*\n)*?AS \$bridge\$(.*?)\$bridge\$;`)
	match := pattern.FindStringSubmatch(contract)
	if match == nil {
		return "", false
	}
	return match[1], true
}
