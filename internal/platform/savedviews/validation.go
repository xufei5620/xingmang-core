package savedviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MaxViewsPerTable       = 20
	MaxViewsPerEnvironment = 200
	MaxNameRunes           = 24
	MaxTableKeyBytes       = 128
	MaxQueryRunes          = 256
	MaxFilters             = 16
	MaxFilterKeyBytes      = 64
	MaxFilterValueRunes    = 256
	MaxFiltersJSONBytes    = 4096
	MaxColumns             = 64
	MaxColumnIDBytes       = 128
	MaxStateJSONBytes      = 16384
)

var (
	ErrInvalidState    = errors.New("saved_view_state_invalid")
	ErrInvalidName     = errors.New("saved_view_name_invalid")
	ErrInvalidTableKey = errors.New("saved_view_table_key_invalid")
	ErrInvalidJSON     = errors.New("saved_view_json_invalid")

	tableKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	columnIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	filterIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func NormalizeName(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidName
	}
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" || utf8.RuneCountInString(name) > MaxNameRunes {
		return "", ErrInvalidName
	}
	if name == "全部" || name == "自定义" {
		return "", ErrInvalidName
	}
	return name, nil
}

func ValidateTableKey(tableKey string) error {
	if len(tableKey) == 0 || len(tableKey) > MaxTableKeyBytes || !tableKeyPattern.MatchString(tableKey) {
		return ErrInvalidTableKey
	}
	return nil
}

func validateFilters(filters map[string]string) error {
	if filters == nil || len(filters) > MaxFilters {
		return ErrInvalidState
	}
	for key, value := range filters {
		if len(key) == 0 || len(key) > MaxFilterKeyBytes || !filterIDPattern.MatchString(key) {
			return ErrInvalidState
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxFilterValueRunes {
			return ErrInvalidState
		}
	}
	return nil
}

func validateColumns(known, visible []string) error {
	if len(known) == 0 || len(known) > MaxColumns || len(visible) == 0 || len(visible) > MaxColumns {
		return ErrInvalidState
	}
	knownSet := make(map[string]struct{}, len(known))
	for _, id := range known {
		if len(id) == 0 || len(id) > MaxColumnIDBytes || !columnIDPattern.MatchString(id) {
			return ErrInvalidState
		}
		if _, exists := knownSet[id]; exists {
			return ErrInvalidState
		}
		knownSet[id] = struct{}{}
	}
	visibleSet := make(map[string]struct{}, len(visible))
	for _, id := range visible {
		if _, exists := knownSet[id]; !exists {
			return ErrInvalidState
		}
		if _, exists := visibleSet[id]; exists {
			return ErrInvalidState
		}
		visibleSet[id] = struct{}{}
	}
	return nil
}

func ValidateState(state StateV1) error {
	if state.SchemaVersion != StateSchemaVersion || !utf8.ValidString(state.Query) || utf8.RuneCountInString(state.Query) > MaxQueryRunes {
		return ErrInvalidState
	}
	if err := validateFilters(state.Filters); err != nil {
		return err
	}
	if err := validateColumns(state.KnownColumns, state.VisibleColumns); err != nil {
		return err
	}
	if state.Sort != nil {
		if !columnIDPattern.MatchString(state.Sort.ColumnID) ||
			(state.Sort.Direction != SortAsc && state.Sort.Direction != SortDesc) {
			return ErrInvalidState
		}
	}
	switch state.Density {
	case DensityCompact, DensityStandard, DensityComfortable:
	default:
		return ErrInvalidState
	}
	return nil
}

func DecodeFiltersJSON(raw []byte) (map[string]string, error) {
	if !utf8.Valid(raw) || len(raw) > MaxFiltersJSONBytes {
		return nil, ErrInvalidJSON
	}
	var filters map[string]string
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&filters); err != nil {
		return nil, ErrInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidJSON
	}
	if err := validateFilters(filters); err != nil {
		return nil, ErrInvalidJSON
	}
	return filters, nil
}

func DecodeStateV1JSON(raw []byte) (StateV1, error) {
	if !utf8.Valid(raw) || len(raw) > MaxStateJSONBytes {
		return StateV1{}, ErrInvalidJSON
	}
	var wire stateV1Wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return StateV1{}, ErrInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return StateV1{}, ErrInvalidJSON
	}
	state := stateFromWire(wire)
	if err := ValidateState(state); err != nil {
		return StateV1{}, ErrInvalidJSON
	}
	return state, nil
}
