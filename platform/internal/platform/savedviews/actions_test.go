package savedviews

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeMutator struct {
	setResult    SetResult
	removeResult RemoveResult
	setErr       error
	removeErr    error
	setOwner     Owner
	setInput     SavedView
	removeOwner  Owner
	removeID     uuid.UUID
}

func (f *fakeMutator) Set(_ context.Context, owner Owner, in SavedView) (SetResult, error) {
	f.setOwner, f.setInput = owner, in
	return f.setResult, f.setErr
}

func (f *fakeMutator) Remove(_ context.Context, owner Owner, id uuid.UUID) (RemoveResult, error) {
	f.removeOwner, f.removeID = owner, id
	return f.removeResult, f.removeErr
}

func humanContext() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "https://auth.example/realms/staff", Subject: "sub-alice",
		Environment: "production", Scopes: []string{ScopeManage},
	})
}

func setParams() map[string]any {
	return map[string]any{
		"table_key": "platform.sub2api.channels", "name": "需关注",
		"schema_version": 1, "query": "openai",
		"filters_json": `{"status":"需关注"}`,
		"sort_column":  "grossProfit", "sort_direction": "desc",
		"known_columns":   []string{"name", "status", "grossProfit"},
		"visible_columns": []string{"name", "status"}, "density": "compact",
	}
}

func actionView(name, query string) SavedView {
	return SavedView{
		ID: uuid.New(), TableKey: "platform.sub2api.channels", Name: name,
		State: StateV1{
			SchemaVersion: 1, Query: query, Filters: map[string]string{"status": "需关注"},
			Sort:           &Sort{ColumnID: "grossProfit", Direction: SortDesc},
			KnownColumns:   []string{"name", "status", "grossProfit"},
			VisibleColumns: []string{"name", "status"}, Density: DensityCompact,
		},
	}
}

func TestSavedViewActionDefinitions(t *testing.T) {
	registry := action.NewRegistry()
	if err := RegisterActions(registry, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ActionSet, ActionRemove} {
		def, _, ok := registry.Lookup(id, "1")
		if !ok {
			t.Fatalf("missing %s", id)
		}
		if def.RiskLevel != action.L0 || def.Permission != ScopeManage {
			t.Fatalf("%s definition = %#v", id, def)
		}
		if len(def.PrincipalTypes) != 1 || def.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s principal types = %#v", id, def.PrincipalTypes)
		}
		if strings.Join(def.Environments, ",") != "development,staging,production" {
			t.Fatalf("%s environments = %#v", id, def.Environments)
		}
	}
}

func TestSetSchemaRejectsClientOwnerAndEnvironment(t *testing.T) {
	def := setDefinition()
	for _, field := range []string{"owner_issuer", "owner_subject", "identity_zone", "environment"} {
		params := setParams()
		params[field] = "attacker"
		if err := def.Schema.Validate(params); !errors.Is(err, action.ErrUnknownField) {
			t.Fatalf("field %s error = %v", field, err)
		}
	}
}

func TestSetHandlerUsesPrincipalOwnerAndHashOnlyAudit(t *testing.T) {
	before := strings.Repeat("a", 64)
	after := actionView("需关注", "openai")
	after.StateHash = strings.Repeat("b", 64)
	after.CreatedAt = time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	after.UpdatedAt = after.CreatedAt
	fake := &fakeMutator{setResult: SetResult{BeforeHash: &before, After: after}}

	value, auditInfo, err := action.CaptureAudit(humanContext(), setHandler(fake), setParams())
	if err != nil {
		t.Fatal(err)
	}
	if value == nil || fake.setOwner.Subject != "sub-alice" || fake.setOwner.Environment != "production" {
		t.Fatalf("owner/value = %#v / %#v", fake.setOwner, value)
	}
	if fake.setInput.State.Sort == nil || fake.setInput.State.Sort.ColumnID != "grossProfit" {
		t.Fatalf("state = %#v", fake.setInput.State)
	}
	if auditInfo.ResourceType != resourceSavedView || auditInfo.ResourceID != after.ID.String() {
		t.Fatalf("resource = %#v", auditInfo)
	}
	if len(auditInfo.Before) != 1 || auditInfo.Before["state_hash"] != before ||
		len(auditInfo.After) != 1 || auditInfo.After["state_hash"] != after.StateHash {
		t.Fatalf("audit summaries = %#v / %#v", auditInfo.Before, auditInfo.After)
	}
}

func TestSetHandlerRejectsBoundedAndTrailingFiltersWithoutCallingStore(t *testing.T) {
	for _, raw := range []string{
		strings.Repeat(" ", MaxFiltersJSONBytes+1),
		`{"status":"ok"}{"second":"object"}`,
	} {
		fake := &fakeMutator{}
		params := setParams()
		params["filters_json"] = raw
		if _, err := setHandler(fake)(humanContext(), params); !errors.Is(err, errSavedViewJSONInvalid) {
			t.Fatalf("error = %v", err)
		}
		if fake.setInput.TableKey != "" {
			t.Fatal("store was called")
		}
	}
}

func TestRemoveHandlerUsesOwnedAtomicEvidence(t *testing.T) {
	id := uuid.New()
	hash := strings.Repeat("c", 64)
	fake := &fakeMutator{removeResult: RemoveResult{ID: id, BeforeHash: hash}}
	_, auditInfo, err := action.CaptureAudit(humanContext(), removeHandler(fake), map[string]any{
		"saved_view_id": id.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.removeOwner.Subject != "sub-alice" || fake.removeID != id {
		t.Fatalf("remove call = %#v / %s", fake.removeOwner, fake.removeID)
	}
	if len(auditInfo.Before) != 1 || auditInfo.Before["state_hash"] != hash || auditInfo.After != nil {
		t.Fatalf("remove audit = %#v", auditInfo)
	}
}
