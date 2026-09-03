package postgresstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// ledgerUUID mirrors autoReconcileUUID's own deterministic-UUID convention
// ('1'=source instance, '2'=user, '3'=account, '5'=funding lot, '6'=freeze,
// '9'=usage event), scoped to this file's own fixtures only.
func ledgerUUID(prefix rune, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", prefix, n)
}

// seedLedgerAccount creates a fresh invoice_user + external_account bound to
// sourceID with the given external user id, then bootstraps
// source_account_eligibility_state/source_cutover_manifests via
// UpsertFundingLot's own fixture-bootstrap side effects (see store.go's
// UpsertFundingLot comment) with one WALLET_CASH lot of amountMinor, fully
// consumed (UpsertFundingLot's own default -- see its VerifiedCashMinor/
// ConsumedCashMinor assignment). Returns the lot id for further
// per-scenario mutation (setOpsConsumedCash, deletion, ...).
func seedLedgerAccount(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, externalUserID string, amountMinor int64) (lotID string) {
	t.Helper()
	userID := ledgerUUID('2', accountSuffix(accountID))
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES($1,'test',$2)`, userID, "ledger-user-"+externalUserID); err != nil {
		t.Fatal(err)
	}
	// external_subject_hmac must be distinct per account sharing one source
	// instance: external_accounts has a UNIQUE NULLS NOT DISTINCT
	// (source_instance_id, external_subject_hmac) constraint, so leaving it
	// NULL on a second account for the same source collides with the first.
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status) VALUES($1,$2,$3,$4,$5,'test','verified')`, accountID, userID, sourceID, externalUserID, testHash("ledger-hmac-"+externalUserID)); err != nil {
		t.Fatal(err)
	}
	lotID = ledgerUUID('5', accountSuffix(accountID))
	lot := domain.FundingLot{ID: lotID, PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
		ExternalOrderID: "order-" + externalUserID, Currency: domain.CurrencyCNY, OriginalMinor: amountMinor, CurrentCapMinor: amountMinor,
		Verification: domain.VerificationVerified, SourceStatus: "COMPLETED", SourceRevision: testHash("ledger-" + externalUserID),
		CompletedAt: time.Now(), ObservedAt: time.Now()}
	if err := store.UpsertFundingLot(ctx, lot); err != nil {
		t.Fatal(err)
	}
	return lotID
}

// accountSuffix recovers the numeric suffix ledgerUUID('3', n) encoded, so
// the paired user/lot ids can be derived deterministically from the account
// id alone without threading an extra parameter through every call site.
func accountSuffix(accountID string) int {
	var n int
	if _, err := fmt.Sscanf(accountID[len(accountID)-12:], "%012d", &n); err != nil {
		panic(err)
	}
	return n
}

func TestEligibilityLedgerPageCoversEveryAccountStateAndFilters(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := ledgerUUID('1', 900)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name) VALUES($1,'sub2api','ledger-test')`, sourceID); err != nil {
		t.Fatal(err)
	}

	// n=1: active, consumed and invoiceable diverge (reserved/issued nonzero)
	// -- mirrors design section 3(E)'s own illustrative numbers.
	activeID := ledgerUUID('3', 901)
	activeLot := seedLedgerAccount(t, store, ctx, sourceID, activeID, "ledger-1", 500_000)
	setOpsConsumedCash(t, store, ctx, activeLot, 500_000, 320_000, 1000, 640, 50_000, 90_000)

	// n=2: not_invoiceable_pending_reconciliation, identical amounts to n=1
	// so the same threshold_reached=true holds even while blocked (the
	// design's own JSON example shows exactly this combination).
	pendingID := ledgerUUID('3', 902)
	pendingLot := seedLedgerAccount(t, store, ctx, sourceID, pendingID, "ledger-2", 500_000)
	setOpsConsumedCash(t, store, ctx, pendingLot, 500_000, 320_000, 1000, 640, 50_000, 90_000)
	pendingSince := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
	pendingDetail := "balance_checkpoint ckpt-test at " + pendingSince.Format(time.RFC3339Nano) + " reported balance -120, expected 380 (difference -500)"
	if _, err := store.pool.Exec(ctx, `
		UPDATE source_account_eligibility_state SET
			eligibility_status='not_invoiceable_pending_reconciliation',
			pending_reconciliation_reason='UNKNOWN_NEGATIVE_BALANCE',
			pending_reconciliation_trigger_type='balance_checkpoint',
			pending_reconciliation_trigger_id='ckpt-test',
			pending_reconciliation_detail=$2,
			pending_reconciliation_since=$3
		WHERE external_account_id=$1`, pendingID, pendingDetail, pendingSince); err != nil {
		t.Fatal(err)
	}

	// n=3: frozen, with two open eligibility_freezes rows -- the query must
	// surface the latest (by opened_at), not merely any open row.
	frozenID := ledgerUUID('3', 903)
	seedLedgerAccount(t, store, ctx, sourceID, frozenID, "ledger-3", 60_000)
	olderFreezeAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	latestFreezeAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,opened_at) VALUES($1,$2,'EVENT_PAYLOAD_DRIFT','funding_lot','stale-older',$3)`, ledgerUUID('6', 931), frozenID, olderFreezeAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id,opened_at) VALUES($1,$2,'SOURCE_GAP','source_stream','usage',$3)`, ledgerUUID('6', 932), frozenID, latestFreezeAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET eligibility_status='frozen' WHERE external_account_id=$1`, frozenID); err != nil {
		t.Fatal(err)
	}

	// n=4: frozen with NO open eligibility_freezes row at all -- defensive
	// edge case the task brief calls out explicitly: must still return a row
	// with null block_*, not error or skip the account. Also below threshold
	// (15_000 < 20_000), covering that side of threshold_reached too.
	frozenNoRowID := ledgerUUID('3', 904)
	seedLedgerAccount(t, store, ctx, sourceID, frozenNoRowID, "ledger-4", 15_000)
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET eligibility_status='frozen' WHERE external_account_id=$1`, frozenNoRowID); err != nil {
		t.Fatal(err)
	}

	// n=5: active with non_invoiceable_overage_* set -- must not leak into
	// block_* (design section 3(E) has no overage field at all) and must not
	// perturb consumed/invoiceable (the overage was never allocated into any
	// cash pool to begin with).
	overageID := ledgerUUID('3', 905)
	seedLedgerAccount(t, store, ctx, sourceID, overageID, "ledger-5", 250_000)
	var manifestHash, configHash, unitCode string
	if err := store.pool.QueryRow(ctx, `SELECT manifest_hash,configuration_hash,unit_code FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&manifestHash, &configHash, &unitCode); err != nil {
		t.Fatal(err)
	}
	usageEventID := ledgerUUID('9', 995)
	usageAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,billing_scope,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at,invoice_eligible)
		VALUES($1,$2,$3,'overage-event','overage-usage',$4,500,$5,$6,$7,'wallet',1,'cursor:overage:1',$4,$8,$4,TRUE)`,
		usageEventID, sourceID, overageID, usageAt, unitCode, manifestHash, configHash, testHash("overage-revision")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE source_account_eligibility_state SET non_invoiceable_overage_units=500,non_invoiceable_overage_usage_event_id=$2 WHERE external_account_id=$1`, overageID, usageEventID); err != nil {
		t.Fatal(err)
	}

	// n=6: bootstrapped account, zero funding lots (delete the bootstrap lot
	// after the account/state row exist) -- must still appear with every
	// amount at zero, not be silently excluded from the page.
	zeroLotsID := ledgerUUID('3', 906)
	zeroLot := seedLedgerAccount(t, store, ctx, sourceID, zeroLotsID, "ledger-6", 10_000)
	if _, err := store.pool.Exec(ctx, `DELETE FROM funding_lot_consumption_state WHERE funding_lot_id=$1`, zeroLot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM funding_lots WHERE id=$1`, zeroLot); err != nil {
		t.Fatal(err)
	}

	byID := func(page EligibilityLedgerPage, accountSuffixWant int) *EligibilityLedgerEntry {
		for i := range page.Items {
			if page.Items[i].externalAccountID == ledgerUUID('3', accountSuffixWant) {
				return &page.Items[i]
			}
		}
		return nil
	}

	t.Run("active with divergent consumed/invoiceable", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "ledger-1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("expected exactly one item for exact external_user_id lookup, got %+v", page.Items)
		}
		item := page.Items[0]
		if item.EligibilityStatus != "active" {
			t.Fatalf("status=%q", item.EligibilityStatus)
		}
		if item.RechargedSincePolicyStartMinor != 500_000 || item.ConsumedMinor != 320_000 || item.InvoiceableMinor != 180_000 {
			t.Fatalf("amounts: recharged=%d consumed=%d invoiceable=%d", item.RechargedSincePolicyStartMinor, item.ConsumedMinor, item.InvoiceableMinor)
		}
		if item.BlockReason != "" || item.BlockDetail != "" || !item.BlockSince.IsZero() {
			t.Fatalf("unblocked account must report empty block_*: %+v", item)
		}
		if item.SourceType != domain.SourceSub2API || item.SourceName != "ledger-test" {
			t.Fatalf("source fields: %+v", item)
		}
	})

	t.Run("not_invoiceable_pending_reconciliation reads the five plaintext columns", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "ledger-2"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		item := page.Items[0]
		if item.EligibilityStatus != "not_invoiceable_pending_reconciliation" {
			t.Fatalf("status=%q", item.EligibilityStatus)
		}
		if item.InvoiceableMinor != 180_000 {
			t.Fatalf("invoiceable_minor=%d, threshold_reached must still be computable from this even while blocked", item.InvoiceableMinor)
		}
		if item.BlockReason != "UNKNOWN_NEGATIVE_BALANCE" {
			t.Fatalf("block_reason=%q", item.BlockReason)
		}
		if item.BlockDetail != pendingDetail {
			t.Fatalf("block_detail=%q want=%q", item.BlockDetail, pendingDetail)
		}
		if !item.BlockSince.Equal(pendingSince) {
			t.Fatalf("block_since=%s want=%s", item.BlockSince, pendingSince)
		}
		// Never sourced from eligibility_freezes/audit_events for this status.
		if strings.Contains(item.BlockReason, "SOURCE_GAP") || strings.Contains(item.BlockReason, "EVENT_PAYLOAD_DRIFT") {
			t.Fatalf("pending-reconciliation block_reason leaked a freeze reason: %q", item.BlockReason)
		}
	})

	t.Run("frozen reads the latest open eligibility_freezes row", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "ledger-3"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		item := page.Items[0]
		if item.EligibilityStatus != "frozen" {
			t.Fatalf("status=%q", item.EligibilityStatus)
		}
		if item.BlockReason != "SOURCE_GAP" {
			t.Fatalf("expected the latest (SOURCE_GAP), got block_reason=%q", item.BlockReason)
		}
		if !item.BlockSince.Equal(latestFreezeAt) {
			t.Fatalf("block_since=%s want=%s (the latest freeze's opened_at, not the older one)", item.BlockSince, latestFreezeAt)
		}
		if !strings.Contains(item.BlockDetail, "source_stream") || !strings.Contains(item.BlockDetail, "usage") {
			t.Fatalf("block_detail should carry the latest freeze's trigger object type/id: %q", item.BlockDetail)
		}
		if item.RechargedSincePolicyStartMinor != 60_000 || item.ConsumedMinor != 60_000 || item.InvoiceableMinor != 60_000 {
			t.Fatalf("amounts unaffected by frozen status: %+v", item)
		}
	})

	t.Run("frozen with no open freeze row still returns null block_*", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "ledger-4"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("frozen account with no open freeze row must still appear: %+v", page.Items)
		}
		item := page.Items[0]
		if item.EligibilityStatus != "frozen" {
			t.Fatalf("status=%q", item.EligibilityStatus)
		}
		if item.BlockReason != "" || item.BlockDetail != "" || !item.BlockSince.IsZero() {
			t.Fatalf("expected null block_* for a frozen account with no open freeze row, got %+v", item)
		}
		if item.InvoiceableMinor != 15_000 {
			t.Fatalf("invoiceable_minor=%d", item.InvoiceableMinor)
		}
	})

	t.Run("overage does not leak into block_* or perturb amounts", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "ledger-5"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items=%+v", page.Items)
		}
		item := page.Items[0]
		if item.EligibilityStatus != "active" {
			t.Fatalf("status=%q", item.EligibilityStatus)
		}
		if item.BlockReason != "" || item.BlockDetail != "" || !item.BlockSince.IsZero() {
			t.Fatalf("recorded overage must not surface as a block: %+v", item)
		}
		if item.RechargedSincePolicyStartMinor != 250_000 || item.ConsumedMinor != 250_000 || item.InvoiceableMinor != 250_000 {
			t.Fatalf("amounts perturbed by recorded overage: %+v", item)
		}
	})

	t.Run("zero lots still appears with every amount at zero", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "ledger-6"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("bootstrapped account with zero lots must still appear: %+v", page.Items)
		}
		item := page.Items[0]
		if item.RechargedSincePolicyStartMinor != 0 || item.ConsumedMinor != 0 || item.InvoiceableMinor != 0 {
			t.Fatalf("expected all-zero amounts, got %+v", item)
		}
		if item.EligibilityStatus != "active" {
			t.Fatalf("status=%q", item.EligibilityStatus)
		}
	})

	t.Run("keyset pagination walks every account exactly once, newest id first", func(t *testing.T) {
		var seen []string
		query := EligibilityLedgerPageQuery{Limit: 3}
		for i := 0; i < 10; i++ {
			page, err := store.ListEligibilityLedgerPage(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 3 {
				t.Fatalf("page exceeded requested limit: %d items", len(page.Items))
			}
			for _, item := range page.Items {
				seen = append(seen, item.externalAccountID)
			}
			if !page.HasMore {
				break
			}
			if page.NextBeforeID == "" {
				t.Fatal("has_more=true but next_before_id is empty")
			}
			query = EligibilityLedgerPageQuery{Limit: 3, BeforeID: page.NextBeforeID}
		}
		// integrationStore's own base fixture account (id suffix 1) is also
		// bootstrapped (it calls UpsertFundingLot too) and, being numerically
		// smallest, sorts last in this descending walk.
		want := []string{ledgerUUID('3', 906), ledgerUUID('3', 905), ledgerUUID('3', 904), ledgerUUID('3', 903), ledgerUUID('3', 902), ledgerUUID('3', 901), ledgerUUID('3', 1)}
		if len(seen) != len(want) {
			t.Fatalf("walked %d accounts, want %d: %v", len(seen), len(want), seen)
		}
		for i := range want {
			if seen[i] != want[i] {
				t.Fatalf("page order[%d]=%s want=%s (must be descending by account id, matching this store's other keyset endpoints)", i, seen[i], want[i])
			}
		}
	})

	t.Run("invalid before_id is rejected", func(t *testing.T) {
		if _, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{BeforeID: "not-a-uuid"}); err == nil {
			t.Fatal("expected an error for a malformed before_id cursor")
		}
	})

	t.Run("invalid external_user_id filter is rejected", func(t *testing.T) {
		if _, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: strings.Repeat("x", 513)}); err == nil {
			t.Fatal("expected an error for an over-long external_user_id filter")
		}
		if _, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{ExternalUserID: "bad\r\nvalue"}); err == nil {
			t.Fatal("expected an error for a CR/LF-bearing external_user_id filter")
		}
	})

	t.Run("limit is bounded to the documented default and max of 100", func(t *testing.T) {
		page, err := store.ListEligibilityLedgerPage(ctx, EligibilityLedgerPageQuery{Limit: 5000})
		if err != nil {
			t.Fatal(err)
		}
		// 6 of this file's own fixture accounts plus integrationStore's own
		// base fixture account (also bootstrapped via UpsertFundingLot).
		if len(page.Items) != 7 {
			t.Fatalf("expected all 7 bootstrapped accounts within the 100 cap, got %d", len(page.Items))
		}
		if byID(page, 901) == nil || byID(page, 906) == nil {
			t.Fatalf("expected every fixture account present in an unfiltered page: %+v", page.Items)
		}
	})
}
