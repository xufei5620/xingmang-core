package oidcauth

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

func TestTranslateRoles(t *testing.T) {
	m := map[string][]string{
		"staff":   {"registry.read", "ops.read"},
		"auditor": {"audit.read"},
	}

	t.Run("先剔漂移再查表", func(t *testing.T) {
		// 即便映射表里也写了 registry.read 当键（正常配置不会，这里是防御性验证），
		// 令牌里的 registry.read 角色也不能被当成一次合法授权
		bad := map[string][]string{"registry.read": {"registry.read"}, "staff": {"registry.read"}}
		got := translateRoles([]string{"registry.read"}, bad)
		if len(got.Scopes) != 0 {
			t.Fatalf("漂移角色不该产生任何 scope, got %v", got.Scopes)
		}
		if !slices.Equal(got.DriftRoles, []string{"registry.read"}) {
			t.Fatalf("DriftRoles = %v", got.DriftRoles)
		}
	})

	t.Run("并集去重且顺序稳定", func(t *testing.T) {
		a := translateRoles([]string{"staff", "auditor"}, m)
		b := translateRoles([]string{"auditor", "staff", "staff"}, m)
		want := []string{"audit.read", "ops.read", "registry.read"}
		if !slices.Equal(a.Scopes, want) || !slices.Equal(b.Scopes, want) {
			t.Fatalf("a = %v, b = %v, want %v", a.Scopes, b.Scopes, want)
		}
	})

	t.Run("未知角色静默忽略", func(t *testing.T) {
		got := translateRoles([]string{"offline_access", "uma_authorization", "default-roles-solov-staff"}, m)
		if len(got.Scopes) != 0 || len(got.DriftRoles) != 0 {
			t.Fatalf("Keycloak 的默认角色不该产生 scope 或 warn: %+v", got)
		}
	})

	t.Run("空与空白角色名跳过", func(t *testing.T) {
		got := translateRoles([]string{"", "   ", "staff"}, m)
		if !slices.Equal(got.Scopes, []string{"ops.read", "registry.read"}) {
			t.Fatalf("Scopes = %v", got.Scopes)
		}
	})
}
