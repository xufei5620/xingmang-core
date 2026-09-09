package postgresstore

import (
	"os"
	"strings"
	"testing"
)

func TestBalanceCarryForwardMigrationIsPublicBoundAndFailClosed(t *testing.T) {
	body, err := os.ReadFile("../../migrations/0014_balance_carry_forward_proof.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"CREATE TABLE public.balance_carry_forward_proofs",
		"CREATE TABLE public.balance_carry_forward_evaluations",
		"SET search_path=pg_catalog,public",
		"NEW.proof_key<>'carry-forward:'||NEW.scan_cycle_id::text||':'||lower(NEW.external_account_id::text)",
		"newer.source_sequence<NEW.source_sequence",
		"newer.source_sequence>prior_sequence",
		"balance carry-forward prior actual is not latest",
		"real balance checkpoint conflicts with carry-forward proof",
	} {
		if !strings.Contains(ddl, required) {
			t.Fatalf("0014 lost contract %q: %s", required, ddl)
		}
	}
	if strings.Count(ddl, "SET search_path=pg_catalog,public") != 2 {
		t.Fatalf("0014 trigger functions are not both search_path hardened: %s", ddl)
	}
}
