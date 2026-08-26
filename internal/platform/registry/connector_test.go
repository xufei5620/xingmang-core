package registry

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func validConnector() Connector {
	return Connector{
		ID:                        uuid.New(),
		Key:                       "sub2api",
		Version:                   "1.0.0",
		ContractVersion:           "1",
		ConnectionSchemaPath:      "contracts/connectors/sub2api.connection.v1.json",
		TargetAllowlist:           []string{"api.solov.cc"},
		ReadCapabilities:          []Capability{"sub2api.users.read", "sub2api.orders.read"},
		WriteCapabilities:         []Capability{"sub2api.accounts.import"},
		SupportedUpstreamVersions: []string{">=0.1.150 <0.2.0"},
		CompatibilityTestPath:     "tests/compatibility/sub2api",
	}
}

func validConnection() Connection {
	return Connection{
		ID:                  uuid.New(),
		ConnectorID:         uuid.New(),
		ServiceID:           uuid.New(),
		Environment:         EnvProduction,
		CredentialRef:       "secret://sub2api-prod/read-only-admin",
		TargetAllowlist:     []string{"api.solov.cc"},
		GrantedCapabilities: []Capability{"sub2api.users.read"},
		Status:              ConnectionEnabled,
	}
}

func TestCapabilityParseAndWriteDetection(t *testing.T) {
	for _, tt := range []struct {
		in      string
		wantErr bool
		isWrite bool
	}{
		{"sub2api.users.read", false, false},
		{"sub2api.accounts.list", false, false},
		{"newapi.channels.manage", false, true},
		{"sub2api.accounts.import", false, true},
		{"sub2api.accounts.disable", false, true},
		{"invoice.application.approve", false, true},
		{"", true, false},
		{"sub2api", true, false},
		{"Sub2API.users.read", true, false},
		{"sub2api..read", true, false},
	} {
		got, err := ParseCapability(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("ParseCapability(%q) 应报错", tt.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseCapability(%q) err = %v", tt.in, err)
		}
		if got.IsWrite() != tt.isWrite {
			t.Fatalf("Capability(%q).IsWrite() = %v, want %v", tt.in, got.IsWrite(), tt.isWrite)
		}
	}
}

func TestConnectorValidateAcceptsValid(t *testing.T) {
	if err := validConnector().Validate(); err != nil {
		t.Fatalf("合法 Connector 被拒绝: %v", err)
	}
}

func TestConnectorRequiresAllowlist(t *testing.T) {
	c := validConnector()
	c.TargetAllowlist = nil
	if err := c.Validate(); !errors.Is(err, ErrAllowlistRequired) {
		t.Fatalf("ADR-004：无 allowlist 必须拒绝，got %v", err)
	}
	c = validConnector()
	c.TargetAllowlist = []string{""}
	if err := c.Validate(); err == nil {
		t.Fatal("空串 allowlist 项应被拒绝")
	}
}

func TestConnectorValidateRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Connector){
		"空 Key":             func(c *Connector) { c.Key = "" },
		"非法 Key":            func(c *Connector) { c.Key = "Sub2API" },
		"空 Version":         func(c *Connector) { c.Version = "" },
		"非语义 Version":       func(c *Connector) { c.Version = "v1" },
		"空 ContractVersion": func(c *Connector) { c.ContractVersion = "" },
		"空 SchemaPath":      func(c *Connector) { c.ConnectionSchemaPath = "" },
		"非法 Capability":     func(c *Connector) { c.ReadCapabilities = []Capability{"BAD"} },
		"读写能力重叠":            func(c *Connector) { c.WriteCapabilities = append(c.WriteCapabilities, "sub2api.users.read") },
		"写集合含读能力":           func(c *Connector) { c.WriteCapabilities = []Capability{"sub2api.users.read"} },
		"读集合含写能力":           func(c *Connector) { c.ReadCapabilities = []Capability{"sub2api.accounts.import"} },
	} {
		c := validConnector()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("%s：应被拒绝但通过了", name)
		}
	}
}

func TestConnectionValidateAcceptsValid(t *testing.T) {
	if err := validConnection().Validate(); err != nil {
		t.Fatalf("合法 Connection 被拒绝: %v", err)
	}
}

func TestConnectionRejectsPlaintextCredential(t *testing.T) {
	c := validConnection()
	c.CredentialRef = "postgres://user:pass@host/db"
	if err := c.Validate(); !errors.Is(err, ErrInvalidCredentialRef) {
		t.Fatalf("明文凭据必须拒绝（宪法 7 条），got %v", err)
	}
	c.CredentialRef = ""
	if err := c.Validate(); err == nil {
		t.Fatal("空 CredentialRef 应被拒绝")
	}
}

func TestConnectionWriteRequiresKillSwitch(t *testing.T) {
	c := validConnection()
	c.GrantedCapabilities = []Capability{"sub2api.accounts.import"}
	c.KillSwitch = ""
	if err := c.Validate(); !errors.Is(err, ErrKillSwitchRequired) {
		t.Fatalf("ADR-004：有写能力必须有 Kill Switch，got %v", err)
	}
	if !c.HasWriteCapability() {
		t.Fatal("HasWriteCapability 应为 true")
	}
	c.KillSwitch = "sub2api-write"
	if err := c.Validate(); err != nil {
		t.Fatalf("补上 Kill Switch 后应通过: %v", err)
	}
}

func TestConnectionRequiresAllowlist(t *testing.T) {
	c := validConnection()
	c.TargetAllowlist = nil
	if err := c.Validate(); !errors.Is(err, ErrAllowlistRequired) {
		t.Fatalf("Connection 也必须有 allowlist，got %v", err)
	}
}
