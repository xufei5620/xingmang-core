package oidcauth

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 管理端的角色勾选框是一张**手写的**清单
// （web/apps/admin-web/src/api/staff.ts 的 STAFF_ROLE_CATALOG），
// 与这里的 DefaultRoleScopeMap 是两份独立维护的东西。
//
// 漏掉一个角色的症状很难查：后端的 staff.account.set_roles 会**接受**它
// （Schema 的枚举来自这张表），审计里也看得到，但页面上根本勾不到那个框——
// 于是「已经实现并且能用的功能」对所有人都是 403，而没有任何报错指向真因。
// XM-CARD6 的 fund-operator 就是这么漏的，assurance-probe-admin 早就漏着。
//
// 用读文件的方式钉住：跨语言没有更便宜的办法，而这条断言只在有人新增角色
// 时才会响一次，正是它该响的时候。
func TestStaffRoleCatalogCoversEveryRole(t *testing.T) {
	const catalogPath = "../../../web/apps/admin-web/src/api/staff.ts"

	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("读不到角色目录 %s：这条断言依赖它存在，文件挪了就把路径改了 (%v)",
			catalogPath, err)
	}
	catalog := string(raw)

	for role := range DefaultRoleScopeMap() {
		// 找 `value: "<role>"`——比裸子串严格，避免 "staff" 命中
		// "staff-something" 之类的巧合。
		needle := `value: "` + role + `"`
		if !strings.Contains(catalog, needle) {
			t.Errorf("角色 %q 在 DefaultRoleScopeMap 里，却不在管理端的 STAFF_ROLE_CATALOG 里：\n"+
				"  后端会接受这个角色，但页面上勾不到，等于这个角色永远没人能被授予。\n"+
				"  修复：在 %s 的 STAFF_ROLE_CATALOG 里补一行 { value: %q, label: \"…\" }",
				role, catalogPath, role)
		}
	}
}

// 反方向同样要钉：目录里有、而这张表里没有的角色。
//
// 这个方向的症状更糟——页面上那个框**能勾**，勾了保存却报
// 「未知角色 "auditor"」，而且账号一旦存下这个角色就再也改不动了
// （复选框只渲染目录里的项，看不见的角色仍会被整体提交回去）。
// 2026-09-05 生产上就是这么卡住的：admin 账号带着 auditor，
// 任何一次角色修改都被拒。
func TestStaffRoleCatalogHasNoPhantomRoles(t *testing.T) {
	const catalogPath = "../../../web/apps/admin-web/src/api/staff.ts"

	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("读不到角色目录 %s (%v)", catalogPath, err)
	}

	// 只取 STAFF_ROLE_CATALOG 那一段，免得把文件里别处的 value: "..." 也算进来。
	body := string(raw)
	start := strings.Index(body, "STAFF_ROLE_CATALOG")
	if start < 0 {
		t.Fatalf("%s 里找不到 STAFF_ROLE_CATALOG", catalogPath)
	}
	end := strings.Index(body[start:], "];")
	if end < 0 {
		t.Fatalf("STAFF_ROLE_CATALOG 的结尾找不到，这条断言依赖它是一个数组字面量")
	}
	section := body[start : start+end]

	known := DefaultRoleScopeMap()
	for _, m := range regexp.MustCompile(`value: "([^"]+)"`).FindAllStringSubmatch(section, -1) {
		role := m[1]
		if _, ok := known[role]; !ok {
			t.Errorf("管理端目录里有角色 %q，DefaultRoleScopeMap 里却没有；"+
				"页面会把它显示成可勾选项，勾了保存会被 staff.account.set_roles 拒绝，"+
				"而账号一旦存下它就再也改不动角色了。"+
				"修复：要么从 %s 的 STAFF_ROLE_CATALOG 删掉它，要么在这张表里给它定义 scope。",
				role, catalogPath)
		}
	}
}
