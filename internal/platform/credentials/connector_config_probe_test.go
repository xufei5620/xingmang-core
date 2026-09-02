package credentials_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
)

func TestSetProbeSwitchRequiresExistingConnectorConfig(t *testing.T) {
	store := credentials.NewStore(credentialPool(t), t.TempDir())
	ctx := context.Background()
	_, _, err := store.SetProbeSwitch(ctx, "sub2api", "staging", true, strPtr("secret://sub2api-probe/token"), "alice")
	if !errors.Is(err, credentials.ErrConnectorConfigNotFound) {
		t.Fatalf("err = %v, want ErrConnectorConfigNotFound", err)
	}
}

func TestSetProbeSwitchRequiresRealModeAndCredentialRef(t *testing.T) {
	store := credentials.NewStore(credentialPool(t), t.TempDir())
	ctx := context.Background()
	_, _, err := store.SetConnectorConfig(ctx, credentials.ConnectorConfig{
		Platform: "sub2api", Environment: "staging", Mode: "fake",
	}, "alice")
	if err != nil {
		t.Fatalf("SetConnectorConfig() = %v, want nil", err)
	}

	if _, _, err := store.SetProbeSwitch(ctx, "sub2api", "staging", true, strPtr("secret://sub2api-probe/token"), "alice"); err == nil {
		t.Fatal("SetProbeSwitch(enabled=true) on a mode=fake connector = nil error, want error (must switch to real first)")
	}

	if _, _, err := store.SetProbeSwitch(ctx, "sub2api", "staging", true, nil, "alice"); err == nil {
		t.Fatal("SetProbeSwitch(enabled=true) without a credential ref = nil error, want error")
	}
}

func TestSetProbeSwitchSuccessDoesNotTouchOtherColumns(t *testing.T) {
	store := credentials.NewStore(credentialPool(t), t.TempDir())
	ctx := context.Background()
	before, _, err := store.SetConnectorConfig(ctx, credentials.ConnectorConfig{
		Platform: "sub2api", Environment: "staging", Mode: "real",
		Endpoint: "https://api.example.test", TargetAllowlist: []string{"api.example.test"},
		CredentialRef: "secret://sub2api-prod/read-token",
	}, "alice")
	_ = before

	_, after, err := store.SetProbeSwitch(ctx, "sub2api", "staging", true, strPtr("secret://sub2api-probe/token"), "bob")
	if err != nil {
		t.Fatalf("SetProbeSwitch() = %v, want nil", err)
	}
	if !after.ProbeEnabled || after.ProbeCredentialRef != "secret://sub2api-probe/token" {
		t.Fatalf("after = %+v, probe fields not applied", after)
	}
	// 只读凭据/端点/白名单/mode 一律不变——kill_switch.set 只碰两列。
	if after.Mode != "real" || after.Endpoint != "https://api.example.test" ||
		after.CredentialRef != "secret://sub2api-prod/read-token" {
		t.Fatalf("after = %+v, SetProbeSwitch must not touch mode/endpoint/credential_ref", after)
	}

	// 再次调用且省略 ref：应保留刚写入的引用不变。
	_, after2, err := store.SetProbeSwitch(ctx, "sub2api", "staging", false, nil, "carol")
	if err != nil {
		t.Fatalf("SetProbeSwitch(disable, omitted ref) = %v, want nil", err)
	}
	if after2.ProbeEnabled {
		t.Fatal("probe_enabled should now be false")
	}
	if after2.ProbeCredentialRef != "secret://sub2api-probe/token" {
		t.Fatalf("ProbeCredentialRef = %q, want unchanged (ref param omitted)", after2.ProbeCredentialRef)
	}

	// 显式传空串应清空。
	_, after3, err := store.SetProbeSwitch(ctx, "sub2api", "staging", false, strPtr(""), "dave")
	if err != nil {
		t.Fatalf("SetProbeSwitch(disable, empty ref) = %v, want nil", err)
	}
	if after3.ProbeCredentialRef != "" {
		t.Fatalf("ProbeCredentialRef = %q, want empty (explicit clear)", after3.ProbeCredentialRef)
	}
}

func TestSetProbeSwitchInvalidCredentialRefRejected(t *testing.T) {
	store := credentials.NewStore(credentialPool(t), t.TempDir())
	ctx := context.Background()
	if _, _, err := store.SetConnectorConfig(ctx, credentials.ConnectorConfig{
		Platform: "newapi", Environment: "staging", Mode: "real",
		Endpoint: "https://api.example.test", TargetAllowlist: []string{"api.example.test"},
		CredentialRef: "secret://newapi-prod/read-token",
	}, "alice"); err != nil {
		t.Fatalf("SetConnectorConfig() = %v, want nil", err)
	}
	if _, _, err := store.SetProbeSwitch(ctx, "newapi", "staging", true, strPtr("not-a-valid-ref"), "alice"); err == nil {
		t.Fatal("SetProbeSwitch with a malformed credential ref = nil error, want error")
	}
}

func TestGetConnectorConfigReturnsNilWhenAbsent(t *testing.T) {
	store := credentials.NewStore(credentialPool(t), t.TempDir())
	got, err := store.GetConnectorConfig(context.Background(), "sub2api", "staging")
	if err != nil {
		t.Fatalf("GetConnectorConfig() = %v, want nil error", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, want nil (no row is a legitimate fake-mode default)", got)
	}
}

func strPtr(s string) *string { return &s }
