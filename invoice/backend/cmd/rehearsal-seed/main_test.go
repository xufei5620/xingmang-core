//go:build rehearsal_tools

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/securefields"
)

func fixtureInput() Input {
	return Input{SchemaVersion: 1, Clients: map[string]Client{
		"sub2api": {Identifier: "sub@example.invalid", Password: "synthetic-password-sub-123", Subject: "900001", Email: "sub@example.invalid"},
		"newapi":  {Identifier: "xm-rehearsal-new", Password: "synthetic-password-new-123", Subject: "900002", Email: "new@example.invalid"},
	}, Staff: Staff{ID: "12345678-1234-4234-8234-123456789012", Username: "xm-rehearsal-admin", Password: "synthetic-password-admin-123", TOTP: "JBSWY3DPEHPK3PXP", AdminRole: "admin"}}
}
func fixtureGuard() Guard {
	return Guard{SchemaVersion: 1, Mode: "server-rehearsal", Owner: strings.Repeat("a", 32), Project: "xm-rehearsal-seed-test", Nonce: strings.Repeat("b", 64), Databases: map[string]Database{
		"platform": {Name: "platform", OID: 16384}, "invoice": {Name: "invoice", OID: 16385},
	}, Sources: map[string]Source{"sub2api": {ID: "22345678-1234-4234-8234-123456789012", Issuer: "https://sub.fixture.invalid"}, "newapi": {ID: "32345678-1234-4234-8234-123456789012", Issuer: "https://new.fixture.invalid"}}}
}
func fixtureKeys() securefields.Keyring {
	return securefields.Keyring{CurrentKeyID: "test", EncryptionKeys: map[string][]byte{"test": []byte(strings.Repeat("k", 32))}, IndexKey: []byte(strings.Repeat("i", 32))}
}

func TestRejectUnsafeInputBeforeGeneration(t *testing.T) {
	cases := map[string]func(*Input, *Guard){
		"production":              func(i *Input, g *Guard) { g.Mode = "production" },
		"unowned":                 func(i *Input, g *Guard) { g.Owner = "" },
		"not-random-nonce":        func(i *Input, g *Guard) { g.Nonce = "test" },
		"non-rehearsal-project":   func(i *Input, g *Guard) { g.Project = "production" },
		"zero-db-oid":             func(i *Input, g *Guard) { g.Databases["invoice"] = Database{Name: "invoice"} },
		"missing-platform-db":     func(i *Input, g *Guard) { delete(g.Databases, "platform") },
		"invalid-totp":            func(i *Input, g *Guard) { i.Staff.TOTP = "!invalid" },
		"existing-style-username": func(i *Input, g *Guard) { i.Staff.Username = "admin" },
		"unknown-source":          func(i *Input, g *Guard) { g.Sources["extra"] = g.Sources["sub2api"] },
		"duplicate-source": func(i *Input, g *Guard) {
			s := g.Sources["newapi"]
			s.ID = g.Sources["sub2api"].ID
			g.Sources["newapi"] = s
		},
		"nonnumeric-subject": func(i *Input, g *Guard) {
			c := i.Clients["sub2api"]
			c.Subject = "1; DELETE FROM invoice_users"
			i.Clients["sub2api"] = c
		},
		"http-issuer": func(i *Input, g *Guard) {
			s := g.Sources["newapi"]
			s.Issuer = "http://new.fixture.invalid"
			g.Sources["newapi"] = s
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			i, g := fixtureInput(), fixtureGuard()
			change(&i, &g)
			if err := validate(i, g); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	if err := validate(fixtureInput(), fixtureGuard()); err != nil {
		t.Fatal(err)
	}
}
func TestSQLGuardsPrecedeEveryInsert(t *testing.T) {
	for _, domain := range []string{"platform", "invoice"} {
		sql, _, err := generate(domain, fixtureInput(), fixtureGuard(), fixtureKeys())
		if err != nil {
			t.Fatal(err)
		}
		first := strings.Index(sql, "INSERT INTO")
		for _, proof := range []string{"BEGIN;", "current_database()", "pg_catalog.pg_database", "inet_server_addr() IS NOT DISTINCT FROM NULL::inet", "inet_server_port() IS NOT DISTINCT FROM NULL::integer", "LOCK TABLE xm_rehearsal.owner_guard IN SHARE MODE", "count(*) FROM xm_rehearsal.owner_guard", "RAISE EXCEPTION 'D rehearsal ownership mismatch'"} {
			at := strings.Index(sql, proof)
			if at < 0 || at >= first {
				t.Fatalf("%s: missing or late guard %s", domain, proof)
			}
		}
		for _, forbidden := range []string{"UPDATE ", "DELETE ", "ON CONFLICT", "DISABLE TRIGGER", "session_replication_role", "INSERT INTO source_instances", "INSERT INTO source_cutover_manifests", "INSERT INTO invoice_requests"} {
			if strings.Contains(sql, forbidden) {
				t.Fatalf("forbidden operation %s", forbidden)
			}
		}
		if !strings.HasSuffix(sql, "COMMIT;\n") {
			t.Fatal("missing atomic commit")
		}
	}
}
func TestInvoiceCiphertextsUseOriginalAADAndCompleteConsumption(t *testing.T) {
	i, g, k := fixtureInput(), fixtureGuard(), fixtureKeys()
	sql, summary, err := generate("invoice", i, g, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Clients) != 2 {
		t.Fatal("two clients required")
	}
	for name, c := range summary.Clients {
		if !uuidPattern.MatchString(c.UserID) || c.UserID == i.Staff.ID {
			t.Fatal("random invoice UUID required")
		}
		checks := map[string]string{
			"invoice-user-email\n" + g.Sources[name].Issuer + "\n" + i.Clients[name].Subject: i.Clients[name].Email,
			"invoice-profile\n" + c.UserID + "\n" + c.ProfileID + "\nemail":                  i.Clients[name].Email,
			"invoice-profile\n" + c.UserID + "\n" + c.ProfileID + "\ntitle":                  "xm-rehearsal synthetic customer",
		}
		emailIndex, _ := k.BlindIndex("verified-email/"+c.UserID, i.Clients[name].Email)
		checks["invoice-verified-email\n"+c.UserID+"\n"+emailIndex] = i.Clients[name].Email
		for aad, want := range checks {
			found := false
			for _, m := range cipherPattern.FindAllStringSubmatch(sql, -1) {
				b, _ := hex.DecodeString(m[1])
				plain, e := k.Decrypt(b, aad)
				if e == nil && string(plain) == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("no compatible ciphertext for %s", aad)
			}
		}
		bindIndex, _ := k.BlindIndex("external-platform/"+g.Sources[name].ID, i.Clients[name].Subject)
		if !strings.Contains(sql, bindIndex) {
			t.Fatal("binding index namespace drift")
		}
		if strings.Contains(sql, i.Clients[name].Email) || strings.Contains(sql, i.Clients[name].Password) {
			t.Fatal("plaintext credential leaked")
		}
	}
	for _, table := range []string{"invoice_users", "external_accounts", "verified_emails", "invoice_profiles", "source_account_eligibility_state", "funding_lots", "source_usage_events", "funding_lot_consumption_state", "consumption_allocations"} {
		if strings.Count(sql, "INSERT INTO "+table+"(") != 2 {
			t.Fatalf("two rows for %s required", table)
		}
	}
	for _, proof := range []string{"'WALLET_CASH'", "100000,100000", "'POST_CUTOVER_REPLAY'", "scm.manifest_hash", "scm.configuration_hash", "invoice_eligible", "'xm-rehearsal-"} {
		if !strings.Contains(sql, proof) {
			t.Fatalf("missing financial consistency %s", proof)
		}
	}
	_, again, err := generate("invoice", i, g, k)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Clients["sub2api"].UserID == again.Clients["sub2api"].UserID {
		t.Fatal("UUIDs must not collide between runs")
	}
}
func TestStaffHashAndTOTPReferenceCompatible(t *testing.T) {
	i := fixtureInput()
	sql, _, err := generate("platform", i, fixtureGuard(), fixtureKeys())
	if err != nil {
		t.Fatal(err)
	}
	m := hashPattern.FindStringSubmatch(sql)
	if len(m) != 3 {
		t.Fatal("canonical platform Argon2 PHC missing")
	}
	salt, e := base64.RawStdEncoding.DecodeString(m[1])
	if e != nil || len(salt) != 16 {
		t.Fatal("salt contract")
	}
	want := base64.RawStdEncoding.EncodeToString(argon2.IDKey([]byte(i.Staff.Password), salt, 3, 65536, 2, 32))
	if want != m[2] {
		t.Fatal("platform password verification fails")
	}
	if !strings.Contains(sql, "secret://staff-totp/"+i.Staff.ID) || !strings.Contains(sql, "ARRAY['admin']") {
		t.Fatal("TOTP/role binding missing")
	}
	if strings.Contains(sql, i.Staff.Password) || strings.Contains(sql, i.Staff.TOTP) {
		t.Fatal("plaintext staff credential leaked")
	}
}

func TestCLIRefusesBeforeSQLAndExclusiveSummary(t *testing.T) {
	dir := t.TempDir()
	i, g := fixtureInput(), fixtureGuard()
	write := func(name string, v any) string {
		t.Helper()
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	input := write("input.json", i)
	guard := write("guard.json", g)
	summary := filepath.Join(dir, "summary.json")
	args := []string{"--input", input, "--guard", guard, "--domain", "platform", "--summary", summary}
	var stdout bytes.Buffer
	if err := execute(args, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout.String(), "\\set ON_ERROR_STOP on\nBEGIN;") {
		t.Fatal("no private atomic SQL")
	}
	body, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{i.Staff.Password, i.Staff.TOTP, i.Clients["sub2api"].Email} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("summary contains private material")
		}
	}
	stdout.Reset()
	if err = execute(args, &stdout); err == nil || stdout.Len() != 0 {
		t.Fatal("existing summary must reject before emitting SQL")
	}
	g.Mode = "production"
	write("guard.json", g)
	if err = execute(args, &stdout); err == nil || stdout.Len() != 0 {
		t.Fatal("production must never emit SQL")
	}
	if err = os.WriteFile(input, []byte(`{"schema_version":1,"password":"private-value","unknown":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = execute(args, &stdout); err == nil || stdout.Len() != 0 || strings.Contains(err.Error(), "private-value") {
		t.Fatal("invalid input must not disclose material or emit SQL")
	}
}

func TestTCPGuardPinsAddressAndPort(t *testing.T) {
	g := fixtureGuard()
	addr := "192.0.2.17"
	port := 5432
	d := g.Databases["invoice"]
	d.ServerAddr = &addr
	d.ServerPort = &port
	g.Databases["invoice"] = d
	if err := validate(fixtureInput(), g); err != nil {
		t.Fatal(err)
	}
	sql := guardSQL("invoice", g)
	if !strings.Contains(sql, "inet_server_addr() IS NOT DISTINCT FROM '192.0.2.17'::inet") || !strings.Contains(sql, "inet_server_port() IS NOT DISTINCT FROM 5432::integer") {
		t.Fatal("TCP guard not pinned")
	}
	d.ServerPort = nil
	g.Databases["invoice"] = d
	if err := validate(fixtureInput(), g); err == nil {
		t.Fatal("unpaired socket metadata accepted")
	}
}

func TestSeedPlatformProjectionMatchesVerifiedLogin(t *testing.T) {
	i, g := fixtureInput(), fixtureGuard()
	sql, summary, err := generate("invoice", i, g, fixtureKeys())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []auth.Platform{auth.PlatformSub2API, auth.PlatformNewAPI} {
		t.Run(string(kind), func(t *testing.T) {
			client := i.Clients[string(kind)]
			principal := auth.Principal{Issuer: g.Sources[string(kind)].Issuer, Subject: client.Subject, Platform: kind, PlatformUserID: client.Subject}
			if !principal.Platform.Valid() || principal.PlatformUserID != principal.Subject {
				t.Fatal("invalid verified-login fixture")
			}
			// These are the persisted projection fields read and compared by
			// PostgresIdentityStore.ResolveOrCreate. Pin both columns and values:
			// an existing (issuer,subject) row with NULL projection cannot log in.
			want := fmt.Sprintf("INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,platform,platform_user_id,status,email_ciphertext,email_verified) VALUES('%s','%s','%s','%s','%s','active',",
				summary.Clients[string(kind)].UserID, principal.Issuer, principal.Subject, principal.Platform, principal.PlatformUserID)
			if !strings.Contains(sql, want) {
				t.Fatal("seeded invoice identity lacks exact platform/platform_user_id projection required by verified login")
			}
		})
	}
}
