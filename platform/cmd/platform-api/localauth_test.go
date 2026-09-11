package main

import (
	"slices"
	"testing"
)

func TestLocalAuthRoleMapFallsBackToDefault(t *testing.T) {
	m := localAuthRoleMap(config{})
	if !slices.Contains(m["admin"], "staff.manage") {
		t.Fatalf("留空 XM_AUTH_ROLE_SCOPES 时应回落到 rolepermissions 默认表（含 admin -> staff.manage）, got %v", m["admin"])
	}
}

func TestLocalAuthRoleMapUsesConfiguredOverride(t *testing.T) {
	cfg := config{Auth: authConfig{RoleScopes: map[string][]string{"ops": {"ops.read"}}}}
	m := localAuthRoleMap(cfg)
	if !slices.Equal(m["ops"], []string{"ops.read"}) {
		t.Fatalf("应使用显式配置的表, got %v", m)
	}
	if _, hasAdmin := m["admin"]; hasAdmin {
		t.Fatalf("显式配置时不该混入默认表的其它角色, got %v", m)
	}
}

func TestLocalAuthHandlersOrNilAvoidsTypedNilInterface(t *testing.T) {
	if h := localAuthHandlersOrNil(nil); h != nil {
		t.Fatalf("nil *localauth.Handlers 应变成真正的 nil 接口值, got %#v", h)
	}
}
