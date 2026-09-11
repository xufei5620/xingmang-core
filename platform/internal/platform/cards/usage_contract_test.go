package cards

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestUsageContractSubscriptionFieldsMatchRuntime(t *testing.T) {
	type parameter struct {
		Type     string   `json:"type"`
		Required bool     `json:"required"`
		Enum     []string `json:"enum"`
	}
	raw, err := os.ReadFile("../../../contracts/actions/cards.card.usage.set.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Params map[string]parameter `json:"params"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	def := usageSetDef(nil)
	for _, name := range []string{"subscription_amount", "subscription_cycle"} {
		t.Run(name, func(t *testing.T) {
			for _, field := range def.Schema.Fields {
				if field.Name != name {
					continue
				}
				want := parameter{Type: string(field.Type), Required: field.Required, Enum: field.Enum}
				got, exists := contract.Params[name]
				if !exists || !reflect.DeepEqual(got, want) {
					t.Fatalf("contract parameter %s=%+v (present=%v), runtime=%+v", name, got, exists, want)
				}
				return
			}
			t.Fatalf("runtime subscription parameter %s is missing", name)
		})
	}
}
