package sourceagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CanonicalPayload implements the RFC 8785-compatible subset used by the v1
// event contract. Monetary decimals are strings and numeric payload fields are
// non-negative base-10 integers. Exponents and fractional JSON numbers are
// rejected so cross-language hashes cannot drift.
func CanonicalPayload(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func HashCanonicalPayload(raw []byte) (string, error) {
	canonical, err := CanonicalPayload(raw)
	if err != nil {
		return "", err
	}
	return SHA256Hex(canonical), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(typed)
		out.Write(encoded)
	case json.Number:
		n := typed.String()
		if !isCanonicalInteger(n) {
			return fmt.Errorf("unsupported JSON number %q; decimals must be strings", n)
		}
		out.WriteString(n)
	case []any:
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported payload type %T", value)
	}
	return nil
}

func isCanonicalInteger(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value == "-0" || strings.ContainsAny(value, ".eE+") {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errorsNew("multiple JSON values are not allowed")
	}
	return fmt.Errorf("read JSON trailer: %w", err)
}

// Kept local to avoid importing errors solely for a single constant message.
func errorsNew(message string) error { return fmt.Errorf("%s", message) }
