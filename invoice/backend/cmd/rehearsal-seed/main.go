//go:build rehearsal_tools

// rehearsal-seed emits private SQL for a caller-attested, frozen D copy only.
// Container IDs, image IDs, owner labels, internal networks and all eight fresh
// volumes MUST be verified by the calling driver before each psql invocation.
// Database names alone never establish isolation. No database connection is
// made by this binary, and it never edits upstream records or existing rows.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/argon2"
	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/securefields"
)

type Client struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
	Subject    string `json:"subject"`
	Email      string `json:"email"`
}
type Staff struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	TOTP      string `json:"totp"`
	AdminRole string `json:"admin_role"`
}
type Input struct {
	SchemaVersion int               `json:"schema_version"`
	Clients       map[string]Client `json:"clients"`
	Staff         Staff             `json:"staff"`
}
type Database struct {
	Name       string  `json:"name"`
	OID        uint32  `json:"oid"`
	ServerAddr *string `json:"server_addr"`
	ServerPort *int    `json:"server_port"`
}
type Source struct {
	ID     string `json:"id"`
	Issuer string `json:"issuer"`
}
type Guard struct {
	SchemaVersion   int                 `json:"schema_version"`
	Mode            string              `json:"mode"`
	Owner           string              `json:"owner"`
	Project         string              `json:"project"`
	Nonce           string              `json:"nonce"`
	Databases       map[string]Database `json:"databases"`
	Sources         map[string]Source   `json:"sources"`
	BindIP          string              `json:"bind_ip,omitempty"`
	NetworkCIDR     string              `json:"network_cidr,omitempty"`
	ClientSourceIDs map[string]string   `json:"client_source_ids,omitempty"`
}
type ClientSummary struct {
	UserID          string `json:"user_id"`
	AccountID       string `json:"account_id"`
	ProfileID       string `json:"existing_profile_id"`
	LotID           string `json:"lot_id"`
	UsageID         string `json:"usage_id"`
	AllocationID    string `json:"allocation_id"`
	VerifiedEmailID string `json:"verified_email_id"`
}
type Summary struct {
	SchemaVersion int                      `json:"schema_version"`
	Mode          string                   `json:"mode"`
	Owner         string                   `json:"owner"`
	Project       string                   `json:"project"`
	Domain        string                   `json:"domain"`
	StaffID       string                   `json:"staff_id,omitempty"`
	Clients       map[string]ClientSummary `json:"clients,omitempty"`
	SQLSHA256     string                   `json:"sql_sha256"`
	Isolation     string                   `json:"isolation"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var sourceUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var tokenPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var subjectPattern = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
var ownerPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var noncePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var cipherPattern = regexp.MustCompile(`decode\('([0-9a-f]+)','hex'\)`)
var hashPattern = regexp.MustCompile(`\$argon2id\$v=19\$m=65536,t=3,p=2\$([A-Za-z0-9+/]+)\$([A-Za-z0-9+/]+)`)

func validate(i Input, g Guard) error {
	if i.SchemaVersion != 1 || g.SchemaVersion != 1 {
		return errors.New("unsupported schema")
	}
	if g.Mode != "server-rehearsal" && g.Mode != "local-synthetic" {
		return errors.New("D rehearsal mode required")
	}
	if !ownerPattern.MatchString(g.Owner) || !noncePattern.MatchString(g.Nonce) || !strings.HasPrefix(g.Project, "xm-rehearsal-") || !tokenPattern.MatchString(g.Project) {
		return errors.New("random D owner, nonce and project required")
	}
	if len(g.Databases) != 2 {
		return errors.New("exact platform and invoice databases required")
	}
	for _, name := range []string{"platform", "invoice"} {
		d, ok := g.Databases[name]
		if !ok || d.OID == 0 || !tokenPattern.MatchString(d.Name) {
			return errors.New("database identity required")
		}
		if (d.ServerAddr == nil) != (d.ServerPort == nil) {
			return errors.New("database socket metadata must be paired")
		}
		if d.ServerAddr != nil && (net.ParseIP(*d.ServerAddr) == nil || *d.ServerPort < 1 || *d.ServerPort > 65535) {
			return errors.New("database endpoint invalid")
		}
	}
	if len(g.Sources) != 2 || len(i.Clients) != 2 {
		return errors.New("exactly two existing sources required")
	}
	for _, name := range []string{"sub2api", "newapi"} {
		s, ok := g.Sources[name]
		c, present := i.Clients[name]
		u, err := url.Parse(s.Issuer)
		if !ok || !present || !sourceUUIDPattern.MatchString(s.ID) || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(s.Issuer, "/") {
			return errors.New("source identity or canonical HTTPS issuer invalid")
		}
		if !subjectPattern.MatchString(c.Subject) || len(c.Password) < 24 || len(c.Password) > 256 || (name == "sub2api" && c.Identifier != c.Email) || (name == "newapi" && !tokenPattern.MatchString(c.Identifier)) {
			return errors.New("synthetic client identity invalid")
		}
		email, err := mail.ParseAddress(c.Email)
		if err != nil || email.Address != c.Email || strings.ToLower(c.Email) != c.Email || len(c.Email) > 254 || !strings.HasSuffix(c.Email, ".invalid") {
			return errors.New("canonical client email invalid")
		}
	}
	if i.Clients["sub2api"].Email == i.Clients["newapi"].Email || i.Clients["sub2api"].Password == i.Clients["newapi"].Password {
		return errors.New("synthetic clients must be distinct")
	}
	if g.ClientSourceIDs != nil {
		if len(g.ClientSourceIDs) != 2 || g.ClientSourceIDs["sub2api"] != g.Sources["sub2api"].ID || g.ClientSourceIDs["newapi"] != g.Sources["newapi"].ID {
			return errors.New("provider source guard differs from database sources")
		}
	}
	if g.Sources["sub2api"].ID == g.Sources["newapi"].ID || g.Sources["sub2api"].Issuer == g.Sources["newapi"].Issuer {
		return errors.New("sources must be distinct")
	}
	if !uuidPattern.MatchString(i.Staff.ID) || !strings.HasPrefix(i.Staff.Username, "xm-rehearsal-") || !tokenPattern.MatchString(i.Staff.Username) || !tokenPattern.MatchString(i.Staff.AdminRole) || len(i.Staff.Password) < 16 || len(i.Staff.Password) > 256 {
		return errors.New("synthetic staff identity invalid")
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(i.Staff.TOTP)
	if err != nil || len(secret) < 10 || len(secret) > 64 {
		return errors.New("TOTP enrollment material invalid")
	}
	return nil
}
func quote(s string) string    { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func bytesSQL(b []byte) string { return "decode('" + hex.EncodeToString(b) + "','hex')" }
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func guardSQL(domain string, g Guard) string {
	d := g.Databases[domain]
	addr, port := "NULL::inet", "NULL::integer"
	if d.ServerAddr != nil {
		addr = quote(*d.ServerAddr) + "::inet"
		port = fmt.Sprint(*d.ServerPort) + "::integer"
	}
	return fmt.Sprintf(`\set ON_ERROR_STOP on
BEGIN;
SET LOCAL standard_conforming_strings=on;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='30s';
LOCK TABLE xm_rehearsal.owner_guard IN SHARE MODE;
DO $guard$
BEGIN
 IF NOT (current_database()=%s
     AND (SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())=%d::oid
     AND inet_server_addr() IS NOT DISTINCT FROM %s
     AND inet_server_port() IS NOT DISTINCT FROM %s)
 THEN RAISE EXCEPTION 'D rehearsal database identity mismatch'; END IF;
 IF (SELECT count(*) FROM xm_rehearsal.owner_guard)<>1
    OR NOT EXISTS (SELECT 1 FROM xm_rehearsal.owner_guard WHERE owner=%s AND project=%s AND nonce=%s)
 THEN RAISE EXCEPTION 'D rehearsal ownership mismatch'; END IF;
END;
$guard$;
`, quote(d.Name), d.OID, addr, port, quote(g.Owner), quote(g.Project), quote(g.Nonce))
}

func generate(domain string, i Input, g Guard, k securefields.Keyring) (string, Summary, error) {
	if err := validate(i, g); err != nil {
		return "", Summary{}, err
	}
	if domain != "platform" && domain != "invoice" {
		return "", Summary{}, errors.New("domain must be platform or invoice")
	}
	summary := Summary{SchemaVersion: 1, Mode: g.Mode, Owner: g.Owner, Project: g.Project, Domain: domain, Isolation: "caller must independently verify exact D container, image, owner, internal networks and eight fresh frozen volumes"}
	var b strings.Builder
	b.WriteString(guardSQL(domain, g))
	if domain == "platform" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return "", summary, err
		}
		hash := argon2.IDKey([]byte(i.Staff.Password), salt, 3, 65536, 2, 32)
		phc := fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
		fmt.Fprintf(&b, "INSERT INTO core.staff_account(id,username,display_name,password_hash,roles,disabled,must_change_password,must_enroll_totp,totp_secret_ref,totp_enrolled_at,created_at,updated_at,updated_by) VALUES(%s,%s,'xm-rehearsal synthetic staff',%s,ARRAY[%s],false,false,false,%s,now(),now(),now(),%s);\n", quote(i.Staff.ID), quote(i.Staff.Username), quote(phc), quote(i.Staff.AdminRole), quote("secret://staff-totp/"+i.Staff.ID), quote("xm-rehearsal-"+g.Owner))
		summary.StaffID = i.Staff.ID
	} else {
		if err := k.Validate(); err != nil {
			return "", summary, errors.New("field keyring invalid")
		}
		summary.Clients = map[string]ClientSummary{}
		fmt.Fprintf(&b, `DO $sources$ BEGIN
 IF (SELECT count(*) FROM source_instances WHERE enabled)<>2
 OR NOT EXISTS(SELECT 1 FROM source_instances WHERE id=%s AND source_type='sub2api' AND enabled)
 OR NOT EXISTS(SELECT 1 FROM source_instances WHERE id=%s AND source_type='newapi' AND enabled)
 OR (SELECT count(*) FROM source_cutover_manifests WHERE source_instance_id IN (%s,%s))<>2
 THEN RAISE EXCEPTION 'D rehearsal existing source contract mismatch'; END IF;
END; $sources$;
`, quote(g.Sources["sub2api"].ID), quote(g.Sources["newapi"].ID), quote(g.Sources["sub2api"].ID), quote(g.Sources["newapi"].ID))
		for _, name := range []string{"sub2api", "newapi"} {
			c, s := i.Clients[name], g.Sources[name]
			ids := make([]string, 7)
			for n := range ids {
				v, e := randomID()
				if e != nil {
					return "", summary, e
				}
				ids[n] = v
			}
			r := ClientSummary{UserID: ids[0], AccountID: ids[1], ProfileID: ids[2], LotID: ids[3], UsageID: ids[4], AllocationID: ids[5], VerifiedEmailID: ids[6]}
			summary.Clients[name] = r
			encrypt := func(value, aad string) (string, error) { v, e := k.Encrypt([]byte(value), aad); return bytesSQL(v), e }
			userEmail, e := encrypt(c.Email, application.UserEmailAAD(s.Issuer, c.Subject))
			if e != nil {
				return "", summary, e
			}
			emailIndex, e := k.BlindIndex("verified-email/"+r.UserID, c.Email)
			if e != nil {
				return "", summary, e
			}
			verifiedEmail, e := encrypt(c.Email, "invoice-verified-email\n"+r.UserID+"\n"+emailIndex)
			if e != nil {
				return "", summary, e
			}
			profileEmail, e := encrypt(c.Email, "invoice-profile\n"+r.UserID+"\n"+r.ProfileID+"\nemail")
			if e != nil {
				return "", summary, e
			}
			title, e := encrypt("xm-rehearsal synthetic customer", "invoice-profile\n"+r.UserID+"\n"+r.ProfileID+"\ntitle")
			if e != nil {
				return "", summary, e
			}
			binding, e := application.PlatformBindingSubjectIndex(k, s.ID, c.Subject)
			if e != nil {
				return "", summary, e
			}
			fmt.Fprintf(&b, "INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,platform,platform_user_id,status,email_ciphertext,email_verified) VALUES(%s,%s,%s,%s,%s,'active',%s,true);\n", quote(r.UserID), quote(s.Issuer), quote(c.Subject), quote(name), quote(c.Subject), userEmail)
			fmt.Fprintf(&b, "INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status,verified_at,source_last_observed_at) VALUES(%s,%s,%s,%s,%s,'platform_password_login','verified',now(),now());\n", quote(r.AccountID), quote(r.UserID), quote(s.ID), quote(c.Subject), quote(binding))
			fmt.Fprintf(&b, "INSERT INTO verified_emails(id,invoice_user_id,email_ciphertext,normalized_email_hmac,verification_source,verified_at) VALUES(%s,%s,%s,%s,'email_challenge',now());\n", quote(r.VerifiedEmailID), quote(r.UserID), verifiedEmail, quote(emailIndex))
			fmt.Fprintf(&b, "INSERT INTO invoice_profiles(id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified,is_default) VALUES(%s,%s,'personal',%s,%s,true,true);\n", quote(r.ProfileID), quote(r.UserID), title, profileEmail)
			writeFinance(&b, r, s, g)
		}
	}
	b.WriteString("COMMIT;\n")
	sql := b.String()
	sum := sha256.Sum256([]byte(sql))
	summary.SQLSHA256 = hex.EncodeToString(sum[:])
	return sql, summary, nil
}

func writeFinance(b *strings.Builder, r ClientSummary, s Source, g Guard) {
	q := map[string]string{"user": quote(r.UserID), "account": quote(r.AccountID), "source": quote(s.ID), "lot": quote(r.LotID), "usage": quote(r.UsageID), "allocation": quote(r.AllocationID), "event": quote("xm-rehearsal-" + r.UsageID), "order": quote("xm-rehearsal-" + r.LotID)}
	revision := sha256.Sum256([]byte(g.Nonce + "/" + r.LotID))
	q["revision"] = quote(hex.EncodeToString(revision[:]))
	sql := `INSERT INTO source_account_eligibility_state(external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,eligibility_status)
 SELECT {account},{source},scm.cutover_at,scm.unit_code,0,scm.manifest_hash,'POST_CUTOVER_REPLAY',now(),60,'active' FROM source_cutover_manifests scm WHERE scm.source_instance_id={source};
INSERT INTO funding_lots(id,invoice_user_id,external_account_id,source_instance_id,external_order_id,trade_no,currency,original_minor,current_cap_minor,verified_cash_minor,consumed_cash_minor,verification_state,source_status,source_revision_hash,completed_at,observed_at,source_last_observed_at,eligibility_kind,eligibility_cutover_at)
 VALUES({lot},{user},{account},{source},{order},{order},'CNY',100000,100000,100000,100000,'verified','xm-rehearsal-completed',{revision},now()-interval '2 minutes',now(),now(),'WALLET_CASH',now()-interval '2 minutes');
INSERT INTO source_usage_events(id,source_instance_id,external_account_id,external_event_id,external_usage_id,event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,billing_scope,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at,invoice_eligible)
 SELECT {usage},{source},{account},{event},{event},now()-interval '1 minute',1,scm.unit_code,scm.manifest_hash,scm.configuration_hash,'wallet',1,{event},now(),{revision},now(),true FROM source_cutover_manifests scm WHERE scm.source_instance_id={source};
INSERT INTO funding_lot_consumption_state(funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,rounded_consumed_cash_minor,rounding_remainder_numerator)
 VALUES({lot},1,1,100000,100000,0);
INSERT INTO consumption_allocations(id,usage_event_id,funding_lot_id,allocation_order,service_units,cash_minor_delta,projection_version)
 VALUES({allocation},{usage},{lot},1,1,100000,1);
`
	for key, v := range q {
		sql = strings.ReplaceAll(sql, "{"+key+"}", v)
	}
	b.WriteString(sql)
}

func loadJSON(path string, value any) error {
	f, e := os.Open(path)
	if e != nil {
		return errors.New("input file unavailable")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if e = d.Decode(value); e != nil {
		return errors.New("input JSON invalid")
	}
	var trailing any
	if e = d.Decode(&trailing); e != io.EOF {
		return errors.New("trailing input JSON rejected")
	}
	return nil
}
func execute(args []string, stdout io.Writer) error {
	f := flag.NewFlagSet("rehearsal-seed", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	input := f.String("input", "", "private shared identity JSON")
	guard := f.String("guard", "", "public exact D database guard JSON")
	domain := f.String("domain", "", "platform or invoice")
	summaryPath := f.String("summary", "", "exclusive metadata-only summary file")
	if e := f.Parse(args); e != nil || f.NArg() != 0 {
		return errors.New("invalid flags")
	}
	if *input == "" || *guard == "" || *summaryPath == "" {
		return errors.New("input, guard and summary paths required")
	}
	var i Input
	var g Guard
	if e := loadJSON(*input, &i); e != nil {
		return e
	}
	if e := loadJSON(*guard, &g); e != nil {
		return e
	}
	if e := validate(i, g); e != nil {
		return e
	}
	var k securefields.Keyring
	var e error
	if *domain == "invoice" {
		k, e = securefields.LoadKeyringFile(os.Getenv("FIELD_KEYRING_FILE"))
		if e != nil {
			return errors.New("field keyring unavailable or invalid")
		}
	}
	sql, s, e := generate(*domain, i, g, k)
	if e != nil {
		return e
	}
	body, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	file, e := os.OpenFile(*summaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return errors.New("summary path must be new and writable")
	}
	if _, e = file.Write(append(body, '\n')); e != nil {
		file.Close()
		return errors.New("summary write failed")
	}
	if e = file.Close(); e != nil {
		return errors.New("summary close failed")
	}
	_, e = io.WriteString(stdout, sql)
	return e
}
func main() {
	if e := execute(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "rehearsal-seed refused: "+e.Error())
		os.Exit(1)
	}
}
