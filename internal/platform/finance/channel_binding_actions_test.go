package finance

import (
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func TestChannelBindingActionsAreL1HumanOnlyAndNotDefaultRole(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterChannelBindingActions(reg, nil, ChannelInventoryGate{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ActionPlatformChannelBindingSet, ActionPlatformChannelBindingRemove} {
		def, _, ok := reg.Lookup(id, "1")
		if !ok || def.RiskLevel != action.L1 || def.Permission != ScopePlatformChannelBindingManage {
			t.Fatalf("definition %s = %#v", id, def)
		}
		if len(def.PrincipalTypes) != 1 || def.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("principal types %s = %#v", id, def.PrincipalTypes)
		}
	}
}

func TestChannelBindingActionSchemaHasNoClientEnvironment(t *testing.T) {
	def := channelBindingSetDefinition()
	params := map[string]any{
		"service_id": "x", "external_channel_id": "1", "upstream_account_id": "y", "reason": "manual",
		"environment": "production",
	}
	if err := def.Schema.Validate(params); err == nil {
		t.Fatal("schema accepted client-controlled environment")
	}
}
