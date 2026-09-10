package sms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Check the published declarations against the real registered definitions;
// this does not execute a handler or grant a production machine identity.
func TestConsumerActionContractsMatchPrincipalTypes(t *testing.T) {
	for _, def := range []action.Definition{codeFetchDef(), requestDef(nil), resourceActionDef()} {
		t.Run(def.ID, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "actions", def.ID+".v1.json"))
			if err != nil {
				t.Fatal(err)
			}
			var contract struct {
				PrincipalTypes []principal.Type `json:"principal_types"`
			}
			if err := json.Unmarshal(raw, &contract); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(contract.PrincipalTypes, def.PrincipalTypes) {
				t.Fatalf("principal_types contract=%v runtime=%v", contract.PrincipalTypes, def.PrincipalTypes)
			}
		})
	}
}
