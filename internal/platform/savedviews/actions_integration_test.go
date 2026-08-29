package savedviews

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type captureRunStore struct{ runs []action.Run }

func (s *captureRunStore) InsertRun(_ context.Context, run action.Run) error {
	s.runs = append(s.runs, run)
	return nil
}

type captureAuditSink struct{ events []action.AuditEvent }

func (s *captureAuditSink) Append(_ context.Context, event action.AuditEvent) error {
	s.events = append(s.events, event)
	return nil
}

func savedViewKernel(t *testing.T, mutator savedViewMutator) (*action.Kernel, *captureRunStore, *captureAuditSink) {
	t.Helper()
	registry := action.NewRegistry()
	if err := registry.Register(setDefinition(), setHandler(mutator)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(removeDefinition(), removeHandler(mutator)); err != nil {
		t.Fatal(err)
	}
	runs := &captureRunStore{}
	auditSink := &captureAuditSink{}
	return action.NewKernel(registry, runs, action.WithAuditSink(auditSink)), runs, auditSink
}

func kernelContext() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "https://auth.example/realms/staff", Subject: "sub-alice",
		Environment: "production", Scopes: []string{ScopeManage},
	})
}

func TestSavedViewSchemaErrorsRemainInvalidParamsWithoutMarkerLeak(t *testing.T) {
	marker := "SCHEMA_MARKER_DO_NOT_LOG"
	kernel, runs, auditSink := savedViewKernel(t, &fakeMutator{})
	params := setParams()
	params["owner_subject"] = marker
	_, err := kernel.Execute(kernelContext(), action.Request{
		ActionID: ActionSet, ActionVersion: "1", RequestID: "req-schema", Params: params,
	})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("error = %v", err)
	}
	assertMarkerAbsent(t, marker, err, runs, auditSink)
}

func TestSavedViewHandlerValidationBecomesExecutionFailedWithoutMarkerLeak(t *testing.T) {
	marker := "HANDLER_MARKER_DO_NOT_LOG"
	kernel, runs, auditSink := savedViewKernel(t, &fakeMutator{})
	params := setParams()
	params["name"] = marker
	params["filters_json"] = `{"status":"ok"}{"` + marker + `":"x"}`
	_, err := kernel.Execute(kernelContext(), action.Request{
		ActionID: ActionSet, ActionVersion: "1", RequestID: "req-handler", Params: params,
	})
	if action.ErrorCode(err) != action.CodeExecutionFailed {
		t.Fatalf("error = %v", err)
	}
	assertMarkerAbsent(t, marker, err, runs, auditSink)
}

func TestSavedViewStoreFailureBecomesExecutionFailedWithoutMarkerLeak(t *testing.T) {
	marker := "STORE_MARKER_DO_NOT_LOG"
	fake := &fakeMutator{setErr: errors.New(marker)}
	kernel, runs, auditSink := savedViewKernel(t, fake)
	params := setParams()
	params["name"] = marker
	_, err := kernel.Execute(kernelContext(), action.Request{
		ActionID: ActionSet, ActionVersion: "1", RequestID: "req-store", Params: params,
	})
	if action.ErrorCode(err) != action.CodeExecutionFailed {
		t.Fatalf("error = %v", err)
	}
	assertMarkerAbsent(t, marker, err, runs, auditSink)
}

func TestSuccessfulSavedViewAuditContainsOnlyHashes(t *testing.T) {
	marker := "SENSITIVE_QUERY_MARKER"
	created := actionView("含敏感文本的视图", marker)
	created.StateHash = strings.Repeat("d", 64)
	fake := &fakeMutator{setResult: SetResult{After: created}}
	kernel, _, auditSink := savedViewKernel(t, fake)
	params := setParams()
	params["query"] = marker
	if _, err := kernel.Execute(kernelContext(), action.Request{
		ActionID: ActionSet, ActionVersion: "1", RequestID: "req-success", Params: params,
	}); err != nil {
		t.Fatal(err)
	}
	if len(auditSink.events) != 1 {
		t.Fatalf("audit events = %#v", auditSink.events)
	}
	raw, _ := json.Marshal(auditSink.events[0])
	if strings.Contains(string(raw), marker) || strings.Contains(string(raw), "含敏感文本") {
		t.Fatalf("audit leaked saved view state: %s", raw)
	}
	if auditSink.events[0].ResourceID != created.ID.String() ||
		auditSink.events[0].AfterSummary["state_hash"] != created.StateHash {
		t.Fatalf("audit = %#v", auditSink.events[0])
	}
}

func assertMarkerAbsent(t *testing.T, marker string, err error, runs *captureRunStore, auditSink *captureAuditSink) {
	t.Helper()
	raw, marshalErr := json.Marshal(struct {
		Error string              `json:"error"`
		Runs  []action.Run        `json:"runs"`
		Audit []action.AuditEvent `json:"audit"`
	}{Error: err.Error(), Runs: runs.runs, Audit: auditSink.events})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatalf("marker leaked: %s", raw)
	}
}
