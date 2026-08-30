package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAUD2OperationIntentIsAppendOnlyAndByteBound(t *testing.T) {
	pool := aud2ContractPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	operationID := uuid.New()
	canonical := []byte(`{"operation_id":"contract"}`)
	digest := sha256.Sum256(canonical)
	_, err = tx.Exec(context.Background(), `
		INSERT INTO audit.archive_operation_intent
		  (operation_id, approval_envelope_sha256, deterministic_bytes_digest, canonical_intent_bytes, created_at)
		VALUES ($1, $2, $3, $4, $5)`, operationID, strings.Repeat("a", 64), hex.EncodeToString(digest[:]), canonical, time.Now().UTC())
	if err != nil {
		t.Fatalf("intent insert rejected: %v", err)
	}
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO audit.archive_operation_intent
		  (operation_id, approval_envelope_sha256, deterministic_bytes_digest, canonical_intent_bytes, created_at)
		VALUES ($1, $2, $3, $4, $5)`, operationID, strings.Repeat("a", 64), hex.EncodeToString(digest[:]), canonical, time.Now().UTC()); err == nil {
		t.Fatal("duplicate intent unexpectedly accepted")
	}
	if _, err := tx.Exec(context.Background(), `UPDATE audit.archive_operation_intent SET canonical_intent_bytes = $2 WHERE operation_id = $1`, operationID, []byte("changed")); err == nil {
		t.Fatal("intent UPDATE unexpectedly accepted")
	}
}

func TestAUD2PutReceiptUniqueByOrdinalAndTerminalAppendOnly(t *testing.T) {
	pool := aud2ContractPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	operationID := uuid.New()
	intentBytes := []byte(`{"intent":"contract"}`)
	_, err = tx.Exec(context.Background(), `
		INSERT INTO audit.archive_operation_intent
		  (operation_id, approval_envelope_sha256, deterministic_bytes_digest, canonical_intent_bytes, created_at)
		VALUES ($1, $2, $3, $4, $5)`, operationID, strings.Repeat("b", 64), strings.Repeat("c", 64), intentBytes, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	objectBytes := []byte(`{"version_id":"fixture"}`)
	_, err = tx.Exec(context.Background(), `
		INSERT INTO audit.archive_put_receipt
		  (operation_id, ordinal, object_version_bytes, object_version_sha256, recorded_at)
		VALUES ($1, 0, $2, $3, $4)`, operationID, objectBytes, strings.Repeat("d", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO audit.archive_put_receipt
		  (operation_id, ordinal, object_version_bytes, object_version_sha256, recorded_at)
		VALUES ($1, 0, $2, $3, $4)`, operationID, []byte("different"), strings.Repeat("e", 64), time.Now().UTC()); err == nil {
		t.Fatal("same operation/ordinal with different bytes unexpectedly accepted")
	}
	_, err = tx.Exec(context.Background(), `
		INSERT INTO audit.archive_terminal_receipt
		  (operation_id, signed_result_bytes, terminal_result_digest, recorded_at)
		VALUES ($1, $2, $3, $4)`, operationID, []byte(`{"result":"contract"}`), strings.Repeat("f", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `DELETE FROM audit.archive_terminal_receipt WHERE operation_id = $1`, operationID); err == nil {
		t.Fatal("terminal receipt DELETE unexpectedly accepted")
	}
}

func TestAUD2ReceiptTablesRejectTruncate(t *testing.T) {
	pool := aud2ContractPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for _, table := range []string{"audit.archive_operation_intent", "audit.archive_put_receipt", "audit.archive_terminal_receipt"} {
		if _, err := tx.Exec(context.Background(), "TRUNCATE "+table); err == nil {
			t.Fatalf("%s TRUNCATE unexpectedly accepted", table)
		}
	}
}
