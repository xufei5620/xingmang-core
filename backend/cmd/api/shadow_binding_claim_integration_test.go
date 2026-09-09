package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
	"invoice-system/backend/internal/testdb"
)

// This file substantiates design XM-INV-SHADOW-BINDING section E premise 5, the one
// premise the whole "option (a): leave the binding, the customer claims it
// later" withdrawal stance rests on and which the design itself could only
// mark as INFERRED from reading runtime.go and identity.go:
//
//	"a shadow user's later real login lands on the shadow account -- it is
//	 not rejected with 403, and it does not mint a second invoice_user"
//
// It exercises the production provisioning function itself
// (provisionPlatformOrOIDCUser) against a real database and a real
// application.Service, because the inference spans two packages: the claim
// branch in runtime.go never calls auth.ResolveOrCreate, which is precisely
// why identity.go's "stored platform identity does not match the verified
// login" rejection is never reached.

const (
	claimSourceID   = "10000000-0000-4000-8000-0000000000e0"
	claimIssuer     = "https://api.solov.cc"
	claimExternalID = "31415"
	claimOperatorID = "70000000-0000-4000-8000-0000000000e0"
)

type fixedClaimSettings struct{ value adminsettings.Settings }

func (s fixedClaimSettings) Get(context.Context) (adminsettings.Settings, error) { return s.value, nil }

// countingIdentityStore delegates to the real PostgreSQL identity store but
// records how many times ResolveOrCreate was called. On the claim path that
// count must stay zero: reaching ResolveOrCreate at all means the login went
// down the create-or-find branch and is about to mint a second identity.
type countingIdentityStore struct {
	inner *auth.PostgresIdentityStore
	calls int
}

func (s *countingIdentityStore) ResolveOrCreate(ctx context.Context, principal auth.Principal, requestID string) (auth.InvoiceIdentity, error) {
	s.calls++
	return s.inner.ResolveOrCreate(ctx, principal, requestID)
}

func claimTestKeyring() securefields.Keyring {
	return securefields.Keyring{
		CurrentKeyID:   "shadow-v1",
		EncryptionKeys: map[string][]byte{"shadow-v1": bytes.Repeat([]byte{0x31}, 32)},
		IndexKey:       bytes.Repeat([]byte{0x52}, 32),
	}
}

func claimBlindIndex(t *testing.T, namespace, value string) string {
	t.Helper()
	mac := hmac.New(sha256.New, claimTestKeyring().IndexKey)
	_, _ = mac.Write([]byte(namespace + "\n" + strings.ToUpper(strings.Join(strings.Fields(value), ""))))
	return fmt.Sprintf("h1:%x", mac.Sum(nil))
}

func setupClaimFixture(t *testing.T) (*postgresstore.Store, *application.Service, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	policyStart := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`ALTER TABLE invoice_eligibility_policy DISABLE TRIGGER invoice_eligibility_policy_guard`, nil},
		{`UPDATE invoice_eligibility_policy SET eligibility_start_at=$1,policy_version=policy_version+1,
			updated_by='shadow-claim-test' WHERE singleton_id=1`, []any{policyStart}},
		{`ALTER TABLE invoice_eligibility_policy ENABLE TRIGGER invoice_eligibility_policy_guard`, nil},
		{`INSERT INTO admin_settings(singleton_id,issuer_name,service_item,minimum_request_minor,smtp_host,
			smtp_port,smtp_from,smtp_from_name,smtp_starttls,admin_cidrs,revision,updated_by)
			VALUES(1,'测试开票主体','技术服务',20000,'smtp.qq.com',587,'invoice@qq.com','发票中心',TRUE,
			ARRAY['127.0.0.1/32']::inet[],1,'shadow-claim-test')`, nil},
		{`INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
			VALUES($1,'sub2api','shadow claim fixture','fixture-runtime',TRUE)`, []any{claimSourceID}},
	} {
		if _, err = pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.sql, err)
		}
	}
	store := postgresstore.New(pool)
	service, err := application.NewService(store, claimTestKeyring(), fixedClaimSettings{value: adminsettings.Settings{
		IssuerName: "测试开票主体", ServiceItem: domain.FixedServiceItem,
		MinimumRequestMinor: domain.MinimumRequestMinor, EligibilityStartAt: policyStart,
		EligibilityPolicyVersion: 2, Revision: 1,
	}}, application.Options{
		MinimumRequestMinor: domain.MinimumRequestMinor,
		DownloadBaseURL:     "https://invoice.example/",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, service, pool, ctx
}

func shadowBind(t *testing.T, store *postgresstore.Store, ctx context.Context, externalUserID string) postgresstore.OperatorBindResult {
	t.Helper()
	result, err := store.OperatorBindExternalAccount(ctx, postgresstore.OperatorBindInput{
		Platform: "sub2api", Issuer: claimIssuer, ExternalUserID: externalUserID,
		ExternalSubjectHMAC: claimBlindIndex(t, "external-platform/"+claimSourceID, externalUserID),
		DependencyKeyHMAC: claimBlindIndex(t, "source-dependency/source_external_account",
			claimSourceID+"\n"+externalUserID),
		Apply: true, OperatorID: claimOperatorID,
	}, postgresstore.AuditActor{Type: "operator", ID: claimOperatorID, RequestID: "claim-test", Reason: "shadow bind"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func countInvoiceUsers(t *testing.T, pool *pgxpool.Pool, ctx context.Context) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoice_users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestShadowBoundAccountIsClaimedByItsOwnersFirstRealLogin is design section E
// premise 5, substantiated rather than inferred.
//
// The negative control is not decoration: without it, the assertion "the
// login landed on user X" would also pass in a world where the shadow bind
// did nothing and the login simply created X itself. The control runs the
// identical login for an account that was never shadow-bound and shows the
// other branch being taken -- ResolveOrCreate called, a brand new identity
// minted, Claimed false.
func TestShadowBoundAccountIsClaimedByItsOwnersFirstRealLogin(t *testing.T) {
	store, service, pool, ctx := setupClaimFixture(t)
	bound := shadowBind(t, store, ctx, claimExternalID)
	if countInvoiceUsers(t, pool, ctx) != 1 {
		t.Fatalf("expected exactly the shadow user before any login, found %d", countInvoiceUsers(t, pool, ctx))
	}

	identity := &countingIdentityStore{inner: auth.NewPostgresIdentityStore(pool)}
	principal := auth.Principal{
		Issuer: claimIssuer, Subject: claimExternalID,
		Platform: auth.PlatformSub2API, PlatformUserID: claimExternalID,
		AuthTime: time.Now().UTC(),
	}
	sessionUser, err := provisionPlatformOrOIDCUser(ctx, service, identity, principal, "claim-login-1",
		map[auth.Platform]string{auth.PlatformSub2API: claimSourceID})
	if err != nil {
		t.Fatalf("the customer's first real login was rejected: %v", err)
	}
	if sessionUser.ID != bound.InvoiceUserID {
		t.Fatalf("login landed on invoice_user %s, want the shadow row %s", sessionUser.ID, bound.InvoiceUserID)
	}
	if !sessionUser.Claimed {
		t.Fatal("login did not report taking the claim path")
	}
	if identity.calls != 0 {
		t.Fatalf("ResolveOrCreate was called %d time(s); the login went down the create-or-find branch", identity.calls)
	}
	if count := countInvoiceUsers(t, pool, ctx); count != 1 {
		t.Fatalf("%d invoice_users rows after the claim login, want 1 -- a second, orphaned identity was minted", count)
	}

	// A repeat login must be just as stable: still one row, still the shadow
	// one. The platform columns were already filled by the bind, so this also
	// covers ClaimPlatformIdentity's "never overwrite, report what is stored"
	// branch reached with an already-claimed row.
	repeat, err := provisionPlatformOrOIDCUser(ctx, service, identity, principal, "claim-login-2",
		map[auth.Platform]string{auth.PlatformSub2API: claimSourceID})
	if err != nil {
		t.Fatalf("the customer's second login was rejected: %v", err)
	}
	if repeat.ID != bound.InvoiceUserID || countInvoiceUsers(t, pool, ctx) != 1 {
		t.Fatalf("second login moved the identity to %s (%d rows)", repeat.ID, countInvoiceUsers(t, pool, ctx))
	}

	t.Run("negative control: an account that was never shadow-bound takes the other branch", func(t *testing.T) {
		control := &countingIdentityStore{inner: auth.NewPostgresIdentityStore(pool)}
		controlPrincipal := auth.Principal{
			Issuer: claimIssuer, Subject: "99999",
			Platform: auth.PlatformSub2API, PlatformUserID: "99999",
			AuthTime: time.Now().UTC(),
		}
		fresh, controlErr := provisionPlatformOrOIDCUser(ctx, service, control, controlPrincipal, "control-login",
			map[auth.Platform]string{auth.PlatformSub2API: claimSourceID})
		if controlErr != nil {
			t.Fatal(controlErr)
		}
		if fresh.ID == bound.InvoiceUserID || fresh.Claimed {
			t.Fatalf("an unbound account claimed the shadow identity: %+v", fresh)
		}
		if control.calls != 1 {
			t.Fatalf("ResolveOrCreate was called %d time(s) on the create path, want 1", control.calls)
		}
		if count := countInvoiceUsers(t, pool, ctx); count != 2 {
			t.Fatalf("%d invoice_users rows after the control login, want 2", count)
		}
	})
}

// TestShadowBindWithAWrongIssuerStillClaimsButLeavesTheIdentityWrong records
// what a mistyped issuer actually costs, because "it must match byte for
// byte" is easy to state and easy to disbelieve.
//
// The claim branch keys on (source_instance_id, external_user_id), not on the
// issuer, so the login still lands on the shadow row and no second identity
// appears -- the operator is NOT locked out by this mistake. What does not
// heal is invoice_users.oidc_issuer: it keeps the wrong value forever, and
// every session, audit identity hash and email AAD for that customer is
// derived from it. That is why cmd/account-bind reads the origin from the
// same environment variable cmd/api does instead of taking it as a flag.
func TestShadowBindWithAWrongIssuerStillClaimsButLeavesTheIdentityWrong(t *testing.T) {
	store, service, pool, ctx := setupClaimFixture(t)
	const wrongIssuer = "https://wrong.example"
	bound, err := store.OperatorBindExternalAccount(ctx, postgresstore.OperatorBindInput{
		Platform: "sub2api", Issuer: wrongIssuer, ExternalUserID: claimExternalID,
		ExternalSubjectHMAC: claimBlindIndex(t, "external-platform/"+claimSourceID, claimExternalID),
		DependencyKeyHMAC: claimBlindIndex(t, "source-dependency/source_external_account",
			claimSourceID+"\n"+claimExternalID),
		Apply: true, OperatorID: claimOperatorID,
	}, postgresstore.AuditActor{Type: "operator", ID: claimOperatorID, RequestID: "wrong-issuer-test", Reason: "shadow bind"})
	if err != nil {
		t.Fatal(err)
	}

	identity := &countingIdentityStore{inner: auth.NewPostgresIdentityStore(pool)}
	sessionUser, err := provisionPlatformOrOIDCUser(ctx, service, identity, auth.Principal{
		Issuer: claimIssuer, Subject: claimExternalID,
		Platform: auth.PlatformSub2API, PlatformUserID: claimExternalID, AuthTime: time.Now().UTC(),
	}, "wrong-issuer-login", map[auth.Platform]string{auth.PlatformSub2API: claimSourceID})
	if err != nil {
		t.Fatalf("a wrong issuer locked the customer out: %v", err)
	}
	if sessionUser.ID != bound.InvoiceUserID || !sessionUser.Claimed {
		t.Fatalf("login did not claim the shadow row: %+v", sessionUser)
	}
	if count := countInvoiceUsers(t, pool, ctx); count != 1 {
		t.Fatalf("%d invoice_users rows, want 1", count)
	}
	var storedIssuer string
	if err = pool.QueryRow(ctx, `SELECT oidc_issuer FROM invoice_users WHERE id=$1`, bound.InvoiceUserID).Scan(&storedIssuer); err != nil {
		t.Fatal(err)
	}
	if storedIssuer != wrongIssuer {
		t.Fatalf("stored issuer is %q; this test's whole point is that the login does NOT repair it", storedIssuer)
	}
}

// TestShadowBoundAccountLoginIsRejectedWhenTheShadowUserIsDisabled pins
// withdrawal option (b) from design section C.7 -- the option the design
// recommends AGAINST. It is here so the runbook's claim that (b) locks the
// customer out is a tested fact rather than a reading of identity.go, and so
// that anyone who later reaches for (b) as a quick undo sees the consequence
// spelled out in a test name.
func TestShadowBoundAccountLoginIsRejectedWhenTheShadowUserIsDisabled(t *testing.T) {
	store, service, pool, ctx := setupClaimFixture(t)
	bound := shadowBind(t, store, ctx, claimExternalID)
	if _, err := pool.Exec(ctx, `UPDATE invoice_users SET status='disabled' WHERE id=$1`, bound.InvoiceUserID); err != nil {
		t.Fatal(err)
	}
	identity := &countingIdentityStore{inner: auth.NewPostgresIdentityStore(pool)}
	_, err := provisionPlatformOrOIDCUser(ctx, service, identity,
		auth.Principal{
			Issuer: claimIssuer, Subject: claimExternalID,
			Platform: auth.PlatformSub2API, PlatformUserID: claimExternalID, AuthTime: time.Now().UTC(),
		}, "disabled-login", map[auth.Platform]string{auth.PlatformSub2API: claimSourceID})
	if err == nil {
		t.Fatal("a disabled shadow user still let its owner log in")
	}
}
