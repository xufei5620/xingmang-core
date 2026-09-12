package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/rolepermissions"
)

func TestPinnedPythonExportMatchesCurrentRuntimeDefaults(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	source, err := os.ReadFile(filepath.Join(root, sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := export("python", source)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "deploy/unified/audit/default_role_scopes.py"))
	if err != nil {
		t.Fatal(err)
	}
	actual = bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(actual, expected) {
		t.Fatal("default role-scope export differs from runtime/source SHA; regenerate from platform with go run ./cmd/export-role-scope-defaults -format python -output ../deploy/unified/audit/default_role_scopes.py")
	}
}

func TestJSONExportRetainsEveryRuntimeRoleAndScope(t *testing.T) {
	body, err := export("json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string][]string
	if err = json.Unmarshal(body, &actual); err != nil {
		t.Fatal(err)
	}
	expected := rolepermissions.DefaultRoleScopeMap()
	for _, scopes := range expected {
		sort.Strings(scopes)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("export loses or adds runtime permissions")
	}
}
