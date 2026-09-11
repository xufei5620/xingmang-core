package rolepermissions

import (
	"slices"
	"testing"
)

func TestParseRoleScopeMap(t *testing.T) {
	t.Run("留空返回 nil（用默认表）", func(t *testing.T) {
		m, err := ParseRoleScopeMap("   ")
		if err != nil || m != nil {
			t.Fatalf("m = %v, err = %v", m, err)
		}
	})

	t.Run("正常解析并排序去重", func(t *testing.T) {
		m, err := ParseRoleScopeMap(`{"staff":["ops.read","registry.read","ops.read"]}`)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"ops.read", "registry.read"}; !slices.Equal(m["staff"], want) {
			t.Fatalf("m[staff] = %v, want %v", m["staff"], want)
		}
	})

	for name, in := range map[string]string{
		"不是 JSON":      `staff=registry.read`,
		"值不是数组":        `{"staff":"registry.read"}`,
		"空表":           `{}`,
		"空角色名":         `{"  ":["registry.read"]}`,
		"角色映射到空数组":     `{"staff":[]}`,
		"角色只有空白 scope": `{"staff":["  "]}`,
		// 方向写反：左边应是 Keycloak 的粗粒度角色，右边才是平台 scope。
		// 把 registry.service.manage 当角色名，意味着有人打算在 Realm 里建它
		"角色名长成平台权限的样子":  `{"registry.service.manage":["registry.service.manage"]}`,
		"角色名是 ops.read": `{"ops.read":["ops.read"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRoleScopeMap(in); err == nil {
				t.Fatalf("应拒绝: %s", in)
			}
		})
	}
}

func TestLooksLikePlatformScope(t *testing.T) {
	for _, s := range []string{
		"registry.read", "registry.service.manage", "ops.read", "audit.read",
		"platform.cross_env.read", "action.execute", "connector.x", "ui.saved_view.manage",
		"REGISTRY.READ", " ops.read ",
	} {
		if !looksLikePlatformScope(s) {
			t.Errorf("%q 应被识别为平台细粒度权限", s)
		}
	}
	for _, s := range []string{
		"staff", "admin", "offline_access", "uma_authorization",
		"default-roles-solov-staff", "openid", "profile", "email", "",
		// 前缀相近但不是命名空间：不能误伤
		"registryadmin", "operations",
	} {
		if looksLikePlatformScope(s) {
			t.Errorf("%q 不该被当成平台细粒度权限", s)
		}
	}
}

func TestDefaultRoleScopeMapGrantsSelfOnlySavedViews(t *testing.T) {
	m := DefaultRoleScopeMap()
	for _, role := range []string{"staff", "admin"} {
		if !slices.Contains(m[role], "ui.saved_view.manage") {
			t.Fatalf("%s requires self-only personal SavedView scope, got %v", role, m[role])
		}
	}
}
