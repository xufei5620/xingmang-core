package savedviews

import (
	"time"

	"github.com/google/uuid"
)

const StateSchemaVersion = 1

type SortDirection string

const (
	SortAsc  SortDirection = "asc"
	SortDesc SortDirection = "desc"
)

type Density string

const (
	DensityCompact     Density = "compact"
	DensityStandard    Density = "standard"
	DensityComfortable Density = "comfortable"
)

type Owner struct {
	Issuer       string
	Subject      string
	IdentityZone string
	Environment  string
}

type Sort struct {
	ColumnID  string        `json:"column_id"`
	Direction SortDirection `json:"direction"`
}

type StateV1 struct {
	SchemaVersion  int               `json:"schema_version"`
	Query          string            `json:"query"`
	Filters        map[string]string `json:"filters"`
	Sort           *Sort             `json:"sort"`
	KnownColumns   []string          `json:"-"`
	VisibleColumns []string          `json:"-"`
	Density        Density           `json:"density"`
}

// stateV1Wire keeps the public JSON contract nested under columns while the domain keeps the two
// slices flat for database conversion. Keeping this shape explicit prevents accidental storage
// fields from leaking into the wire/hash contract.
type stateV1Wire struct {
	SchemaVersion int               `json:"schema_version"`
	Query         string            `json:"query"`
	Filters       map[string]string `json:"filters"`
	Sort          *Sort             `json:"sort"`
	Columns       stateColumnsWire  `json:"columns"`
	Density       Density           `json:"density"`
}

type stateColumnsWire struct {
	Known   []string `json:"known"`
	Visible []string `json:"visible"`
}

func stateToWire(state StateV1) stateV1Wire {
	return stateV1Wire{
		SchemaVersion: state.SchemaVersion,
		Query:         state.Query,
		Filters:       state.Filters,
		Sort:          state.Sort,
		Columns: stateColumnsWire{
			Known:   state.KnownColumns,
			Visible: state.VisibleColumns,
		},
		Density: state.Density,
	}
}

func stateFromWire(wire stateV1Wire) StateV1 {
	return StateV1{
		SchemaVersion:  wire.SchemaVersion,
		Query:          wire.Query,
		Filters:        wire.Filters,
		Sort:           wire.Sort,
		KnownColumns:   wire.Columns.Known,
		VisibleColumns: wire.Columns.Visible,
		Density:        wire.Density,
	}
}

type SavedView struct {
	ID        uuid.UUID
	TableKey  string
	Name      string
	State     StateV1
	StateHash string
	CreatedAt time.Time
	UpdatedAt time.Time
}
