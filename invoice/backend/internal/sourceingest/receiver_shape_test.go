package sourceingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeBatchPublishedUnsignedExamples(t *testing.T) {
	for _, name := range []string{"source-agent-batch.v2.newapi.json", "source-agent-batch.v3.sub2api-usage.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "examples", name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeBatch(raw); err != nil {
				t.Fatalf("published unsigned example rejected: %v", err)
			}
		})
	}
}

// This fixture is intentionally empty but otherwise valid. A wrong required
// JSON type must be the sole rejection reason; no signature or database is used.
func emptyBatchShapeForTest(version string) map[string]any {
	body := map[string]any{
		"schema_version": version, "source_instance_id": "52000000-0000-4000-8000-000000000001",
		"source_type": "sub2api", "stream_id": "payments", "source_runtime_version": "test",
		"agent_version": "test", "batch_id": "52000000-0000-4000-8000-000000000002",
		"sequence": 1, "previous_batch_hash": nil, "captured_at": "2026-09-10T10:00:00Z",
		"mode": "db_projection", "projection_status": "healthy", "records": []any{},
	}
	if version == "3.0" {
		body["stream_id"] = "usage"
		body["stream_watermark_at"] = "2026-09-10T09:00:00Z"
		body["source_cursor"] = "0"
		body["scan_ceiling_at"] = "2026-09-10T10:00:00Z"
		body["scan_ceiling_cursor"] = "0"
		body["scan_cycle_id"] = "52000000-0000-4000-8000-000000000003"
		body["scan_complete"] = false
	}
	return body
}

func decodeBatchShapeForTest(t *testing.T, body map[string]any) (batchV2, error) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return decodeBatch(raw)
}

func TestDecodeBatchPreservesLegalEmptyShapes(t *testing.T) {
	for _, version := range []string{"2.0", "3.0"} {
		for _, complete := range []bool{false, true} {
			if version == "2.0" && complete {
				continue
			}
			t.Run(version+map[bool]string{false: "/incomplete", true: "/complete"}[complete], func(t *testing.T) {
				body := emptyBatchShapeForTest(version)
				if version == "3.0" {
					body["scan_complete"] = complete
				}
				got, err := decodeBatchShapeForTest(t, body)
				if err != nil {
					t.Fatalf("legal empty batch rejected: %v", err)
				}
				if len(got.Records) != 0 || got.ScanComplete != complete || got.PreviousBatchHash != nil {
					t.Fatal("legal empty batch changed records, completion or first-batch hash")
				}
			})
		}
	}
}

func TestDecodeBatchRejectsInvalidRequiredJSONTypes(t *testing.T) {
	for _, version := range []string{"2.0", "3.0"} {
		for _, field := range []string{"records", "scan_complete"} {
			if version == "2.0" && field == "scan_complete" {
				continue
			}
			for name, invalid := range map[string]any{"null": nil, "string": "wrong", "number": 1, "object": map[string]any{}} {
				t.Run(version+"/"+field+"/"+name, func(t *testing.T) {
					body := emptyBatchShapeForTest(version)
					body[field] = invalid
					if _, err := decodeBatchShapeForTest(t, body); err == nil {
						t.Fatalf("required %s accepted %s", field, name)
					}
				})
			}
			t.Run(version+"/"+field+"/missing", func(t *testing.T) {
				body := emptyBatchShapeForTest(version)
				delete(body, field)
				if _, err := decodeBatchShapeForTest(t, body); err == nil {
					t.Fatalf("missing required %s accepted", field)
				}
			})
		}
	}
}
