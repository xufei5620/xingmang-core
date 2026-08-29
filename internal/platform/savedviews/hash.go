package savedviews

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func CanonicalStateJSON(state StateV1) ([]byte, error) {
	if err := ValidateState(state); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(stateToWire(state))
	if err != nil {
		return nil, ErrInvalidState
	}
	if len(raw) > MaxStateJSONBytes {
		return nil, ErrInvalidState
	}
	return raw, nil
}

func CanonicalStateHash(state StateV1) (string, error) {
	raw, err := CanonicalStateJSON(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
