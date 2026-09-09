package sourceagent

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Source bridge functions are SECURITY DEFINER entry points installed in an
// integration-owned schema. Their PL/pgSQL bodies use only fixed dynamic SQL,
// so PostgreSQL records no dependency from the bridge to an upstream relation
// or column. Callers receive EXECUTE only; the NOLOGIN bridge owner holds the
// reviewed column grants.
func sourceBridgeFunction(source, stream string) (string, error) {
	if source != SourceSub2API && source != SourceNewAPI {
		return "", errors.New("unsupported source bridge")
	}
	var suffix string
	switch stream {
	case StreamPayments:
		suffix = "payments_v4"
	case StreamUsage:
		suffix = "usage_v4"
	case StreamCredits:
		suffix = "credits_v4"
	case StreamBalances:
		suffix = "balances_v4"
	case StreamIdentities:
		suffix = "identities_v4"
	default:
		return "", errors.New("unsupported source bridge stream")
	}
	return "invoice_bridge." + source + "_" + suffix, nil
}

func bridgeJSONRecordRelation(source, stream, operation, recordDefinition string) (string, error) {
	function, err := sourceBridgeFunction(source, stream)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`%s('%s',$1::jsonb) AS bridge(payload)
CROSS JOIN LATERAL jsonb_to_record(bridge.payload) AS projected(%s)`, function, operation, recordDefinition), nil
}

func marshalBridgeRequest(values map[string]any) (string, error) {
	if values == nil {
		values = map[string]any{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", errors.New("encode source bridge request failed")
	}
	if len(raw) > 16<<10 {
		return "", errors.New("source bridge request is too large")
	}
	return string(raw), nil
}
