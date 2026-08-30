package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func TestNormalizeSecretValue(t *testing.T) {
	v, err := NormalizeSecretValue("  test-value-1 \n")
	if err != nil || v.Reveal() != "test-value-1" {
		t.Fatalf("首尾空白应去除: %q, %v", v.Reveal(), err)
	}
	for name, raw := range map[string]string{
		"空":       "",
		"仅空白":     " \t\n",
		"内嵌换行":    "abc\ndef",
		"内嵌回车":    "abc\rdef",
		"超过 8192": strings.Repeat("a", MaxSecretValueBytes+1),
	} {
		if _, err := NormalizeSecretValue(raw); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("%s 应报 ErrInvalidValue, got %v", name, err)
		}
	}
	if _, err := NormalizeSecretValue(strings.Repeat("a", MaxSecretValueBytes)); err != nil {
		t.Fatalf("恰好 8192 字节应接受: %v", err)
	}
}

func TestNormalizeSecretValueErrorNeverContainsValue(t *testing.T) {
	const marker = "SECRET-MARKER-9f1c"
	_, err := NormalizeSecretValue(marker + "\n" + marker)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("错误文本不得携带值: %v", err)
	}
}

func TestFingerprint(t *testing.T) {
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	got := Fingerprint(secrets.NewSecretValue([]byte("abc")))
	if got != "ba7816bf8f01cfea" {
		t.Fatalf("Fingerprint = %q", got)
	}
	if Fingerprint(secrets.NewSecretValue([]byte("abd"))) == got {
		t.Fatal("不同值不该同指纹")
	}
}

func TestWriteSecretFileIsAtomicAndReadableByFileProvider(t *testing.T) {
	root := t.TempDir()
	store := NewStore(nil, root)
	ref := secrets.MustCredentialRef("secret://sub2api-prod/read-token")

	if err := store.writeSecretFile(ref, secrets.NewSecretValue([]byte("test-value-1"))); err != nil {
		t.Fatal(err)
	}
	// 与连接器读值的 FileProvider 使用同一布局
	v, err := secrets.NewFileProvider(root).Resolve(context.Background(), ref, "test")
	if err != nil || v.Reveal() != "test-value-1" {
		t.Fatalf("FileProvider 应能读回: %q, %v", v.Reveal(), err)
	}
	// 覆盖写：新值替换旧值，目录里不留临时文件
	if err := store.writeSecretFile(ref, secrets.NewSecretValue([]byte("test-value-2"))); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "sub2api-prod"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "read-token" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("scope 目录应只有目标文件, got %v", names)
	}
	b, err := os.ReadFile(store.path(ref))
	if err != nil || string(b) != "test-value-2" {
		t.Fatalf("覆盖后内容 = %q, %v", b, err)
	}
	if !store.available(ref) {
		t.Fatal("写入后 available 应为 true")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.path(ref))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("凭据文件权限应为 0600, got %o", perm)
		}
		dirInfo, err := os.Stat(filepath.Join(root, "sub2api-prod"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Fatalf("scope 目录权限应为 0700, got %o", perm)
		}
	}
}

func TestRemoveSecretFileIsIdempotent(t *testing.T) {
	root := t.TempDir()
	store := NewStore(nil, root)
	ref := secrets.MustCredentialRef("secret://newapi/readonly-token")
	if err := store.removeSecretFile(ref); err != nil {
		t.Fatalf("不存在时删除应成功: %v", err)
	}
	if err := store.writeSecretFile(ref, secrets.NewSecretValue([]byte("test-value-3"))); err != nil {
		t.Fatal(err)
	}
	if err := store.removeSecretFile(ref); err != nil {
		t.Fatal(err)
	}
	if store.available(ref) {
		t.Fatal("删除后 available 应为 false")
	}
	if _, err := secrets.NewFileProvider(root).Resolve(context.Background(), ref, "test"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("删除后 FileProvider 应 ErrNotFound, got %v", err)
	}
}

func TestWriteSecretFileFailsWhenRootIsAFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(nil, root)
	err := store.writeSecretFile(secrets.MustCredentialRef("secret://a/b"), secrets.NewSecretValue([]byte("test-value-4")))
	if err == nil {
		t.Fatal("根目录不可用时应报错")
	}
	if strings.Contains(err.Error(), "test-value-4") {
		t.Fatalf("错误文本不得携带值: %v", err)
	}
}

func TestStoreWithoutPoolFailsClosed(t *testing.T) {
	store := NewStore(nil, t.TempDir())
	ctx := context.Background()
	ref := secrets.MustCredentialRef("secret://a/b")
	value := secrets.NewSecretValue([]byte("test-value-5"))
	if _, err := store.Upsert(ctx, ref, value, "staging", "staff_alice"); !errors.Is(err, ErrStore) {
		t.Fatalf("无 pool 的 Upsert 应 ErrStore, got %v", err)
	}
	if store.available(ref) {
		t.Fatal("无 pool 时不该写文件：DB 行与文件必须一起落")
	}
	if _, err := store.Revoke(ctx, ref, "staging", "staff_alice", "泄漏"); !errors.Is(err, ErrStore) {
		t.Fatalf("无 pool 的 Revoke 应 ErrStore, got %v", err)
	}
	if _, err := store.List(ctx, "staging"); !errors.Is(err, ErrStore) {
		t.Fatalf("无 pool 的 List 应 ErrStore, got %v", err)
	}
	if _, _, err := store.SetConnectorConfig(ctx, ConnectorConfig{
		Platform: "sub2api", Environment: "staging", Mode: ModeFake,
	}, "staff_alice"); !errors.Is(err, ErrStore) {
		t.Fatalf("无 pool 的 SetConnectorConfig 应 ErrStore, got %v", err)
	}
	if _, err := store.ListConnectorConfigs(ctx, "staging"); !errors.Is(err, ErrStore) {
		t.Fatalf("无 pool 的 ListConnectorConfigs 应 ErrStore, got %v", err)
	}
}

func TestStoreRejectsInvalidInputBeforeTouchingAnything(t *testing.T) {
	store := NewStore(nil, t.TempDir())
	ctx := context.Background()
	ref := secrets.MustCredentialRef("secret://a/b")
	value := secrets.NewSecretValue([]byte("test-value-6"))
	if _, err := store.Upsert(ctx, secrets.CredentialRef{}, value, "staging", "alice"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("零值 ref 应 ErrInvalidInput, got %v", err)
	}
	if _, err := store.Upsert(ctx, ref, value, "", "alice"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空环境应 ErrInvalidInput, got %v", err)
	}
	if _, err := store.Upsert(ctx, ref, value, "staging", " "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空 actor 应 ErrInvalidInput, got %v", err)
	}
	if _, err := store.Upsert(ctx, ref, secrets.SecretValue{}, "staging", "alice"); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("零值 SecretValue 应 ErrInvalidValue, got %v", err)
	}
	if _, err := store.Revoke(ctx, ref, "staging", "alice", " "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空 reason 应 ErrInvalidInput, got %v", err)
	}
	if _, err := store.List(ctx, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空环境的 List 应 ErrInvalidInput, got %v", err)
	}
}

func TestParseAllowlist(t *testing.T) {
	got, err := ParseAllowlist(" api.solov.cc, XM.solov.cc ,,api.solov.cc ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "api.solov.cc,xm.solov.cc" {
		t.Fatalf("ParseAllowlist = %v", got)
	}
	empty, err := ParseAllowlist("")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("空串应得到空切片（非 nil）: %#v, %v", empty, err)
	}
	for _, raw := range []string{"https://api.solov.cc", "api solov", "a\\b"} {
		if _, err := ParseAllowlist(raw); !errors.Is(err, ErrInvalidConnectorConfig) {
			t.Fatalf("%q 应报 ErrInvalidConnectorConfig, got %v", raw, err)
		}
	}
}

func realConfig() ConnectorConfig {
	return ConnectorConfig{
		Platform: "sub2api", Environment: "staging", Mode: ModeReal,
		Endpoint: "https://api.solov.cc", TargetAllowlist: []string{"api.solov.cc"},
		CredentialRef: "secret://sub2api-prod/read-token",
	}
}

func TestValidateConnectorConfig(t *testing.T) {
	if err := ValidateConnectorConfig(realConfig()); err != nil {
		t.Fatalf("合法 real 配置: %v", err)
	}
	if err := ValidateConnectorConfig(ConnectorConfig{
		Platform: "newapi", Environment: "staging", Mode: ModeFake, TargetAllowlist: []string{},
	}); err != nil {
		t.Fatalf("三项留空的 fake 配置应合法: %v", err)
	}
	cases := map[string]func(c *ConnectorConfig){
		"未知平台":            func(c *ConnectorConfig) { c.Platform = "openai" },
		"未知模式":            func(c *ConnectorConfig) { c.Mode = "shadow" },
		"空环境":             func(c *ConnectorConfig) { c.Environment = "" },
		"real 缺 endpoint": func(c *ConnectorConfig) { c.Endpoint = "" },
		"http 端点":         func(c *ConnectorConfig) { c.Endpoint = "http://api.solov.cc" },
		"端点带用户信息":         func(c *ConnectorConfig) { c.Endpoint = "https://user:pw@api.solov.cc" },
		"real 空白名单":       func(c *ConnectorConfig) { c.TargetAllowlist = nil },
		"端点主机不在白名单":       func(c *ConnectorConfig) { c.TargetAllowlist = []string{"other.solov.cc"} },
		"real 缺引用":        func(c *ConnectorConfig) { c.CredentialRef = "" },
		"引用格式错":           func(c *ConnectorConfig) { c.CredentialRef = "sub2api/read-token" },
		"fake 但引用格式错":     func(c *ConnectorConfig) { c.Mode = ModeFake; c.CredentialRef = "nope" },
		"fake 但 http 端点":  func(c *ConnectorConfig) { c.Mode = ModeFake; c.Endpoint = "http://x" },
	}
	for name, mutate := range cases {
		c := realConfig()
		mutate(&c)
		if err := ValidateConnectorConfig(c); !errors.Is(err, ErrInvalidConnectorConfig) {
			t.Fatalf("%s 应报 ErrInvalidConnectorConfig, got %v", name, err)
		}
	}
}

func TestExpectedRefsAreValidAndStable(t *testing.T) {
	refs := ExpectedRefs()
	if len(refs) != 6 {
		t.Fatalf("预期清单应有 6 条, got %d", len(refs))
	}
	seen := map[string]bool{}
	for _, e := range refs {
		if _, err := secrets.ParseCredentialRef(e.Ref); err != nil {
			t.Fatalf("%s: %v", e.Ref, err)
		}
		if e.Platform == "" || e.Purpose == "" {
			t.Fatalf("%s 缺 platform / purpose", e.Ref)
		}
		if seen[e.Ref] {
			t.Fatalf("%s 重复", e.Ref)
		}
		seen[e.Ref] = true
	}
	if refs[0].Ref != "secret://sub2api-prod/read-token" || refs[0].Platform != "sub2api" {
		t.Fatalf("首条应是 Sub2API: %+v", refs[0])
	}
}
