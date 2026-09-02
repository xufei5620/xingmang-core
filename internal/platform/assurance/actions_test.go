package assurance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func TestRegisterActionsRejectsNilDependencies(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, nil, &fakeSwitcher{}); err == nil {
		t.Fatal("RegisterActions(nil store, ...) = nil error, want error")
	}
	if err := RegisterActions(reg, &Store{}, nil); err == nil {
		t.Fatal("RegisterActions(..., nil switcher) = nil error, want error")
	}
}

func TestActionDefinitionsShapeMatchesContracts(t *testing.T) {
	cases := []struct {
		def       action.Definition
		wantID    string
		wantPerm  string
		wantTypes []principal.Type
	}{
		{declareDefinition(), ActionDeclare, ScopeManage, humanOnly},
		{cancelDefinition(), ActionCancel, ScopeManage, humanOnly},
		{runDefinition(), ActionRun, ScopeRun, humanAndService},
		{killSwitchSetDefinition(), ActionKillSwitchSet, ScopeKillSwitch, humanOnly},
	}
	for _, c := range cases {
		if c.def.ID != c.wantID {
			t.Errorf("ID = %q, want %q", c.def.ID, c.wantID)
		}
		if c.def.RiskLevel != action.L1 {
			t.Errorf("%s: RiskLevel = %q, want L1 (ADR-019 决策·三)", c.wantID, c.def.RiskLevel)
		}
		if c.def.Permission != c.wantPerm {
			t.Errorf("%s: Permission = %q, want %q", c.wantID, c.def.Permission, c.wantPerm)
		}
		if err := c.def.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", c.wantID, err)
		}
		if len(c.def.PrincipalTypes) != len(c.wantTypes) {
			t.Errorf("%s: PrincipalTypes = %v, want %v", c.wantID, c.def.PrincipalTypes, c.wantTypes)
			continue
		}
		for i, pt := range c.wantTypes {
			if c.def.PrincipalTypes[i] != pt {
				t.Errorf("%s: PrincipalTypes = %v, want %v", c.wantID, c.def.PrincipalTypes, c.wantTypes)
				break
			}
		}
	}
}

func TestKillSwitchSetActionIDMatchesFrozenDesign(t *testing.T) {
	// 团队交接消息用的简写是 assurance.probe.switch@1；冻结设计稿
	// （docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md
	// §2.3）用的是四段式 assurance.probe.kill_switch.set@1。本测试钉住这个
	// 命名选择，防止后来者按交接消息的简写"修正"回去——见
	// docs/handoffs/slices/XM-ASSURE1-core.md 的"与派工消息的偏离"一节。
	if ActionKillSwitchSet != "assurance.probe.kill_switch.set" {
		t.Fatalf("ActionKillSwitchSet = %q, want the frozen design's ID", ActionKillSwitchSet)
	}
}

type fakeSwitcher struct {
	before, after credentials.ConnectorConfig
	err           error
	gotPlatform   string
	gotEnabled    bool
	gotRef        *string
}

func (f *fakeSwitcher) SetProbeSwitch(
	_ context.Context, platform, _ string, probeEnabled bool, credentialRef *string, _ string,
) (credentials.ConnectorConfig, credentials.ConnectorConfig, error) {
	f.gotPlatform, f.gotEnabled, f.gotRef = platform, probeEnabled, credentialRef
	if f.err != nil {
		return credentials.ConnectorConfig{}, credentials.ConnectorConfig{}, f.err
	}
	return f.before, f.after, nil
}

func testPrincipalContext() context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff:alice", Type: principal.TypeHuman, Environment: "staging",
		Scopes: []string{ScopeKillSwitch},
	})
}

func TestKillSwitchSetHandlerSuccessPassesParamsThrough(t *testing.T) {
	f := &fakeSwitcher{after: credentials.ConnectorConfig{
		Platform: "sub2api", Mode: "real", ProbeEnabled: true,
		ProbeCredentialRef: "secret://sub2api-probe/token", UpdatedAt: time.Now(), UpdatedBy: "staff:alice",
	}}
	handler := killSwitchSetHandler(f)
	out, err := handler(testPrincipalContext(), map[string]any{
		"platform": "sub2api", "probe_enabled": true, "probe_credential_ref": "secret://sub2api-probe/token",
	})
	if err != nil {
		t.Fatalf("handler() = %v, want nil", err)
	}
	if f.gotPlatform != "sub2api" || !f.gotEnabled || f.gotRef == nil || *f.gotRef != "secret://sub2api-probe/token" {
		t.Fatalf("switcher got platform=%q enabled=%v ref=%v, unexpected", f.gotPlatform, f.gotEnabled, f.gotRef)
	}
	body, ok := out.(map[string]any)
	if !ok || body["platform"] != "sub2api" {
		t.Fatalf("out = %#v, unexpected shape", out)
	}
}

func TestKillSwitchSetHandlerOmittedRefLeavesNilPointer(t *testing.T) {
	f := &fakeSwitcher{}
	handler := killSwitchSetHandler(f)
	if _, err := handler(testPrincipalContext(), map[string]any{"platform": "sub2api", "probe_enabled": false}); err != nil {
		t.Fatalf("handler() = %v, want nil", err)
	}
	if f.gotRef != nil {
		t.Fatalf("gotRef = %v, want nil (probe_credential_ref omitted means leave existing value untouched)", f.gotRef)
	}
}

func TestKillSwitchSetHandlerExplicitEmptyRefClears(t *testing.T) {
	f := &fakeSwitcher{}
	handler := killSwitchSetHandler(f)
	if _, err := handler(testPrincipalContext(), map[string]any{
		"platform": "sub2api", "probe_enabled": false, "probe_credential_ref": "",
	}); err != nil {
		t.Fatalf("handler() = %v, want nil", err)
	}
	if f.gotRef == nil || *f.gotRef != "" {
		t.Fatalf("gotRef = %v, want a non-nil pointer to an empty string (explicit clear)", f.gotRef)
	}
}

func TestKillSwitchSetHandlerMapsConnectorConfigNotFound(t *testing.T) {
	f := &fakeSwitcher{err: credentials.ErrConnectorConfigNotFound}
	handler := killSwitchSetHandler(f)
	_, err := handler(testPrincipalContext(), map[string]any{"platform": "sub2api", "probe_enabled": true, "probe_credential_ref": "secret://a/b"})
	if action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("ErrorCode(err) = %q, want PRECONDITION_FAILED", action.ErrorCode(err))
	}
}

func TestKillSwitchSetHandlerMapsInvalidConnectorConfig(t *testing.T) {
	f := &fakeSwitcher{err: credentials.ErrInvalidConnectorConfig}
	handler := killSwitchSetHandler(f)
	_, err := handler(testPrincipalContext(), map[string]any{"platform": "sub2api", "probe_enabled": true, "probe_credential_ref": "not-a-ref"})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("ErrorCode(err) = %q, want INVALID_PARAMS", action.ErrorCode(err))
	}
}

func TestKillSwitchSetHandlerRequiresPrincipal(t *testing.T) {
	handler := killSwitchSetHandler(&fakeSwitcher{})
	_, err := handler(context.Background(), map[string]any{"platform": "sub2api", "probe_enabled": false})
	if action.ErrorCode(err) != action.CodePermissionDenied {
		t.Fatalf("ErrorCode(err) = %q, want PERMISSION_DENIED", action.ErrorCode(err))
	}
}

func TestDomainErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want action.Code
	}{
		{ErrInvalidInput, action.CodeInvalidParams},
		{ErrNotFound, action.CodePreconditionFailed},
		{ErrDeclarationNotActive, action.CodePreconditionFailed},
		{ErrVersionConflict, action.CodePreconditionFailed},
		{errors.New("something else"), action.CodeExecutionFailed},
	}
	for _, c := range cases {
		if got := action.ErrorCode(domainError(c.err)); got != c.want {
			t.Errorf("domainError(%v) code = %q, want %q", c.err, got, c.want)
		}
	}
}
