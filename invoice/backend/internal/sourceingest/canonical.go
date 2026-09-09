package sourceingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

func parseUniqueJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return value, nil
}

func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate JSON key %q", key)
			}
			value, valueErr := readJSONValue(decoder)
			if valueErr != nil {
				return nil, valueErr
			}
			object[key] = value
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return nil, errors.New("unterminated JSON object")
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, valueErr := readJSONValue(decoder)
			if valueErr != nil {
				return nil, valueErr
			}
			array = append(array, value)
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return nil, errors.New("unterminated JSON array")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func canonicalPayload(raw []byte) ([]byte, error) {
	value, err := parseUniqueJSON(raw)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err = writeCanonical(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeCanonical(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(typed)
		output.Write(encoded)
	case json.Number:
		number := typed.String()
		if !canonicalInteger(number) {
			return fmt.Errorf("unsupported JSON number %q", number)
		}
		output.WriteString(number)
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonical(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			output.Write(encoded)
			output.WriteByte(':')
			if err := writeCanonical(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported payload type %T", value)
	}
	return nil
}

func canonicalInteger(value string) bool {
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
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
