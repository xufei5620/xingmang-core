package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
)

// No database, encryption or generation is involved in this result-boundary fake.
type batchResultFixture struct{ failed, succeeded bool }

func (s batchResultFixture) RepairQueueNarrowEligibility(_ context.Context, in postgresstore.QueueNarrowRepairInput, _ postgresstore.AuditActor) (postgresstore.QueueNarrowRepairResult, error) {
	r := postgresstore.QueueNarrowRepairResult{Applied: in.Apply}
	if s.failed {
		r.Errors = []postgresstore.QueueNarrowRepairAccountError{{ExternalAccountID: "fixture-failed", Message: "fixture transaction rolled back"}}
	}
	if s.succeeded {
		r.Accounts = []postgresstore.QueueNarrowRepairAccount{{ExternalAccountID: "fixture-success"}}
	}
	return r, nil
}
func (s batchResultFixture) RepairProjectionRequeueDead(_ context.Context, in postgresstore.ProjectionRequeueDeadRepairInput, _ postgresstore.AuditActor) (postgresstore.ProjectionRequeueDeadRepairResult, error) {
	r := postgresstore.ProjectionRequeueDeadRepairResult{Applied: in.Apply}
	if s.failed {
		r.Errors = []postgresstore.ProjectionRequeueDeadRepairAccountError{{ExternalAccountID: "fixture-failed", Message: "fixture transaction rolled back"}}
	}
	if s.succeeded {
		r.Accounts = []postgresstore.ProjectionRequeueDeadRepairAccount{{ExternalAccountID: "fixture-success"}}
	}
	return r, nil
}
func (s batchResultFixture) RepairIngestRequeueDead(_ context.Context, in postgresstore.IngestRequeueDeadRepairInput, _ postgresstore.AuditActor) (postgresstore.IngestRequeueDeadRepairResult, error) {
	r := postgresstore.IngestRequeueDeadRepairResult{Applied: in.Apply}
	if s.failed {
		r.Errors = []postgresstore.IngestRequeueDeadRepairEventError{{EventKey: "fixture-failed", Message: "fixture transaction rolled back"}}
	}
	if s.succeeded {
		r.Events = []postgresstore.IngestRequeueDeadRepairEvent{{EventID: "fixture-success"}}
	}
	return r, nil
}
func (s batchResultFixture) RepairPolicyStartReanchorEligibility(_ context.Context, in postgresstore.PolicyStartReanchorRepairInput, _ postgresstore.AuditActor) (postgresstore.PolicyStartReanchorRepairResult, error) {
	r := postgresstore.PolicyStartReanchorRepairResult{Applied: in.Apply}
	if s.failed {
		r.Errors = []postgresstore.PolicyStartReanchorRepairAccountError{{ExternalAccountID: "fixture-failed", Message: "fixture transaction rolled back"}}
	}
	if s.succeeded {
		r.Accounts = []postgresstore.PolicyStartReanchorRepairAccount{{ExternalAccountID: "fixture-success", NoOp: true, Blocked: true}}
	}
	return r, nil
}

func TestBatchRepairResultsPreserveErrorsAndSuccessfulItems(t *testing.T) {
	for _, repair := range []struct {
		name string
		call func(batchResultFixture, *bytes.Buffer) error
	}{
		// Queue-narrow dry-run reaches the same result boundary without field keys.
		{"queue", func(s batchResultFixture, out *bytes.Buffer) error {
			return runQueueNarrow(context.Background(), s, false, "fixture-operator", securefields.Keyring{}, out)
		}},
		{"projection", func(s batchResultFixture, out *bytes.Buffer) error {
			return runProjectionRequeueDead(context.Background(), s, true, "fixture-operator", "", out)
		}},
		{"ingest", func(s batchResultFixture, out *bytes.Buffer) error {
			return runIngestRequeueDead(context.Background(), s, true, "fixture-operator", repairFilters{}, out)
		}},
		{"policy", func(s batchResultFixture, out *bytes.Buffer) error {
			return runPolicyStartReanchor(context.Background(), s, true, "fixture-operator", out)
		}},
	} {
		for _, result := range []struct {
			name    string
			fixture batchResultFixture
		}{
			{"clean", batchResultFixture{succeeded: true}},
			{"all-failed", batchResultFixture{failed: true}},
			{"partial-failed", batchResultFixture{failed: true, succeeded: true}},
		} {
			t.Run(repair.name+"/"+result.name, func(t *testing.T) {
				var out bytes.Buffer
				err := repair.call(result.fixture, &out)
				if result.fixture.failed && err == nil {
					t.Error("per-item failures returned success")
				}
				if !result.fixture.failed && err != nil {
					t.Errorf("error-free result was rejected: %v", err)
				}
				if result.fixture.failed && !strings.Contains(out.String(), "fixture-failed") {
					t.Error("failed item missing from report")
				}
				if result.fixture.succeeded && !strings.Contains(out.String(), "fixture-success") {
					t.Error("successful item missing from report")
				}
			})
		}
	}
}
