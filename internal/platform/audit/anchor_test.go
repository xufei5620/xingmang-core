package audit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func testSeed(t *testing.T) []byte {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	return seed
}

func TestNewEd25519SignerValidatesInput(t *testing.T) {
	seed := testSeed(t)
	if _, err := audit.NewEd25519Signer("", seed); err == nil {
		t.Fatal("空 keyID 应被拒绝——链根落库后需要它追溯轮换")
	}
	for name, bad := range map[string][]byte{
		"过短": seed[:16],
		"过长": append(seed, seed...),
		"空":  {},
	} {
		if _, err := audit.NewEd25519Signer("k1", bad); err == nil {
			t.Fatalf("%s的种子应被拒绝", name)
		}
	}
	if _, err := audit.NewEd25519Signer("k1", seed); err != nil {
		t.Fatalf("合法种子被拒绝: %v", err)
	}
}

func TestSignerFromSecretUsesCredentialRef(t *testing.T) {
	seed := testSeed(t)
	ctx := context.Background()
	ref := secrets.MustCredentialRef("secret://audit/chain-signing-key")

	for name, encoded := range map[string]string{
		"hex":    hex.EncodeToString(seed),
		"base64": base64.StdEncoding.EncodeToString(seed),
	} {
		sp, err := secrets.NewEnvProvider(
			map[string]string{ref.String(): "XM_AUDIT_SEED"},
			secrets.WithLookup(func(k string) (string, bool) {
				if k == "XM_AUDIT_SEED" {
					return encoded, true
				}
				return "", false
			}),
		)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := audit.SignerFromSecret(ctx, sp, ref, "k1")
		if err != nil {
			t.Fatalf("%s 编码的种子应被接受: %v", name, err)
		}
		want := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		if !signer.Public().Equal(want) {
			t.Fatalf("%s：解析出的密钥与预期不符", name)
		}
	}
}

func TestSignerFromSecretRejectsBadSeedWithoutLeakingIt(t *testing.T) {
	ctx := context.Background()
	ref := secrets.MustCredentialRef("secret://audit/chain-signing-key")
	bogus := "this-is-not-a-valid-seed-but-looks-secret"
	sp, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): "XM_AUDIT_SEED"},
		secrets.WithLookup(func(string) (string, bool) { return bogus, true }),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = audit.SignerFromSecret(ctx, sp, ref, "k1")
	if err == nil {
		t.Fatal("非法种子应被拒绝")
	}
	// 错误信息绝不能带上密钥内容（宪法 7 条）
	if strings.Contains(err.Error(), bogus) {
		t.Fatalf("错误信息泄漏了密钥内容: %v", err)
	}
}

func TestComputeAndSignRootRoundTrip(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, evt("registry.service.create")); err != nil {
			t.Fatal(err)
		}
	}
	signer, err := audit.NewEd25519Signer("k1", testSeed(t))
	if err != nil {
		t.Fatal(err)
	}

	root, err := s.ComputeAndSignRoot(ctx, signer)
	if err != nil {
		t.Fatalf("ComputeAndSignRoot: %v", err)
	}
	if root.ToSequence != 3 || root.FromSequence != 1 {
		t.Fatalf("区间应为 [1,3]: %+v", root)
	}
	// 根哈希必须等于链尖 event_hash
	_, tipHash, err := s.Tip(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if root.RootHash != tipHash {
		t.Fatalf("root_hash 应等于链尖 event_hash: %s vs %s", root.RootHash, tipHash)
	}
	if err := audit.VerifyRoot(root, signer.Public()); err != nil {
		t.Fatalf("自签名应能验通: %v", err)
	}
}

func TestVerifyRootRejectsWrongKeyAndTampering(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()
	if _, err := s.Append(ctx, evt("registry.service.create")); err != nil {
		t.Fatal(err)
	}
	signer, _ := audit.NewEd25519Signer("k1", testSeed(t))
	root, err := s.ComputeAndSignRoot(ctx, signer)
	if err != nil {
		t.Fatal(err)
	}

	other, _ := audit.NewEd25519Signer("k2", testSeed(t))
	if err := audit.VerifyRoot(root, other.Public()); err == nil {
		t.Fatal("用别的公钥验签必须失败")
	}

	// 签名覆盖区间与时间，不只是 root_hash——挪用旧签名到别的区间必须失败
	for name, mutate := range map[string]func(*audit.ChainRoot){
		"改 root_hash":     func(r *audit.ChainRoot) { r.RootHash = strings.Repeat("a", 64) },
		"改 to_sequence":   func(r *audit.ChainRoot) { r.ToSequence += 1 },
		"改 from_sequence": func(r *audit.ChainRoot) { r.FromSequence += 1 },
		// 用秒级偏移：签名载荷是微秒精度（与库中存储精度一致），
		// 亚微秒的差异本来就无法表示，不该也不能影响验签
		"改 computed_at": func(r *audit.ChainRoot) { r.ComputedAt = r.ComputedAt.Add(time.Second) },
	} {
		bad := root
		mutate(&bad)
		if err := audit.VerifyRoot(bad, signer.Public()); err == nil {
			t.Fatalf("%s 后验签必须失败", name)
		}
	}
}

func TestComputeAndSignRootRefusesBrokenChain(t *testing.T) {
	// 给断掉的链盖章 = 用签名给篡改背书
	pool := testPool(t)
	s := audit.NewStore(pool)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, evt("registry.service.create")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE audit.audit_event DISABLE RULE audit_event_no_update"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), "ALTER TABLE audit.audit_event ENABLE RULE audit_event_no_update")
	}()
	if _, err := pool.Exec(ctx,
		"UPDATE audit.audit_event SET reason = 'tampered' WHERE sequence = 2"); err != nil {
		t.Fatal(err)
	}

	signer, _ := audit.NewEd25519Signer("k1", testSeed(t))
	if _, err := s.ComputeAndSignRoot(ctx, signer); err == nil {
		t.Fatal("链已损坏时必须拒绝签名")
	}
}

func TestComputeAndSignRootRefusesEmptyChain(t *testing.T) {
	s := audit.NewStore(testPool(t))
	signer, _ := audit.NewEd25519Signer("k1", testSeed(t))
	if _, err := s.ComputeAndSignRoot(context.Background(), signer); err == nil {
		t.Fatal("空链应拒绝签名")
	}
}

func TestExportRootWritesVerifiableFileWithoutPrivateKey(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()
	if _, err := s.Append(ctx, evt("registry.service.create")); err != nil {
		t.Fatal(err)
	}
	seed := testSeed(t)
	signer, _ := audit.NewEd25519Signer("k1", seed)
	root, err := s.ComputeAndSignRoot(ctx, signer)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path, err := s.ExportRoot(ctx, root, dir, signer.Public())
	if err != nil {
		t.Fatalf("ExportRoot: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("导出路径不在指定目录: %s", path)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	// 导出文件绝不能含私钥材料
	if strings.Contains(content, hex.EncodeToString(seed)) ||
		strings.Contains(content, base64.StdEncoding.EncodeToString(seed)) {
		t.Fatal("导出文件泄漏了签名私钥种子")
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("导出文件不是合法 JSON: %v", err)
	}
	for _, k := range []string{"root_hash", "signature", "key_id", "public_key", "signed_payload"} {
		if got[k] == nil || got[k] == "" {
			t.Fatalf("导出文件缺字段 %q: %v", k, got)
		}
	}
	// 用文件里的公钥能独立验签——这才叫「可离线核验的锚点」
	pubB64, _ := got["public_key"].(string)
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.VerifyRoot(root, ed25519.PublicKey(pub)); err != nil {
		t.Fatalf("用导出文件中的公钥应能验通: %v", err)
	}

	// 导出位置应回写库中
	latest, err := s.LatestRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ExportedAt == nil || latest.ExportTarget != path {
		t.Fatalf("导出位置未回写: %+v", latest)
	}
}
