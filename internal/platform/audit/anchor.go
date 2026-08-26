package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit/gen"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Signer 对链根摘要签名。实现方持有私钥，本包其余部分只经本接口用它。
type Signer interface {
	// KeyID 标识所用密钥，落库后支持轮换追溯。
	KeyID() string
	Sign(msg []byte) []byte
	Public() ed25519.PublicKey
}

type ed25519Signer struct {
	keyID string
	priv  ed25519.PrivateKey
}

func (s *ed25519Signer) KeyID() string             { return s.keyID }
func (s *ed25519Signer) Sign(msg []byte) []byte    { return ed25519.Sign(s.priv, msg) }
func (s *ed25519Signer) Public() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

// NewEd25519Signer 用 32 字节种子创建签名器。
func NewEd25519Signer(keyID string, seed []byte) (Signer, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, fmt.Errorf("keyID 不能为空：链根落库后需要它来追溯轮换")
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("审计签名种子必须是 %d 字节，实际 %d", ed25519.SeedSize, len(seed))
	}
	return &ed25519Signer{keyID: keyID, priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// SignerFromSecret 经 CredentialRef 解析签名种子并创建签名器。
//
// 这是本包**唯一**接触密钥材料的入口（ADR-014）：种子只经 SecretProvider 取，
// 绝不从环境变量直读、不落库、不进日志。接受 hex 或 base64 编码的 32 字节种子。
func SignerFromSecret(
	ctx context.Context, sp secrets.SecretProvider, ref secrets.CredentialRef, keyID string,
) (Signer, error) {
	v, err := sp.Resolve(ctx, ref, "audit chain root signing")
	if err != nil {
		return nil, fmt.Errorf("解析审计签名密钥 %s: %w", ref, err)
	}
	seed, err := decodeSeed(v.Reveal())
	if err != nil {
		// 不把密钥内容放进错误信息
		return nil, fmt.Errorf("审计签名密钥 %s 格式非法: %w", ref, err)
	}
	return NewEd25519Signer(keyID, seed)
}

func decodeSeed(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == ed25519.SeedSize {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == ed25519.SeedSize {
		return b, nil
	}
	return nil, fmt.Errorf("期望 %d 字节种子的 hex 或 base64 编码", ed25519.SeedSize)
}

// ChainRoot 是链尖哈希的签名快照（规格 §4.4：根摘要签名并存放于库外）。
type ChainRoot struct {
	ID           uuid.UUID
	ComputedAt   time.Time
	FromSequence int64
	ToSequence   int64
	RootHash     string
	Signature    string // base64(Ed25519)
	KeyID        string
	ExportedAt   *time.Time
	ExportTarget string
}

// SigningPayload 是被签名的字节序列。
//
// 签的不只是 root_hash，还包括区间与时间：只签哈希的话，攻击者可以把一个
// 旧的合法签名挪到别的区间上冒充。
func (r ChainRoot) SigningPayload() []byte {
	return []byte(fmt.Sprintf("xm-audit-root\nfrom=%d\nto=%d\nroot=%s\ncomputed_at=%s\n",
		r.FromSequence, r.ToSequence, r.RootHash, r.ComputedAt.UTC().Format(occurredAtLayout)))
}

// VerifyRoot 用公钥验证链根签名。
func VerifyRoot(root ChainRoot, pub ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(root.Signature)
	if err != nil {
		return fmt.Errorf("签名不是合法 base64: %w", err)
	}
	if !ed25519.Verify(pub, root.SigningPayload(), sig) {
		return fmt.Errorf("链根签名校验失败：root=%s 区间 [%d,%d]",
			root.RootHash, root.FromSequence, root.ToSequence)
	}
	return nil
}

func rootFromRow(r gen.AuditChainRoot) ChainRoot {
	out := ChainRoot{
		ID:           r.ID,
		ComputedAt:   fromTS(r.ComputedAt),
		FromSequence: r.FromSequence,
		ToSequence:   r.ToSequence,
		RootHash:     r.RootHash,
		Signature:    r.Signature,
		KeyID:        r.KeyID,
		ExportTarget: r.ExportTarget,
	}
	if r.ExportedAt.Valid {
		t := r.ExportedAt.Time.UTC()
		out.ExportedAt = &t
	}
	return out
}

// ComputeAndSignRoot 校验链完整性、取链尖为根、签名并落库。
//
// 先校验再签名：给一条已经断掉的链盖章，等于用签名给篡改背书。
func (s *Store) ComputeAndSignRoot(ctx context.Context, signer Signer) (ChainRoot, error) {
	if signer == nil {
		return ChainRoot{}, fmt.Errorf("signer 为空")
	}
	tipSeq, tipHash, err := s.Tip(ctx)
	if err != nil {
		return ChainRoot{}, err
	}
	if tipSeq == 0 {
		return ChainRoot{}, fmt.Errorf("审计链为空，无根可签")
	}

	from := int64(1)
	if latest, err := gen.New(s.pool).GetLatestChainRoot(ctx); err == nil {
		// 上一个根之后的区间；根本身仍从链首校验，避免只验增量而漏掉历史篡改
		from = latest.ToSequence + 1
		if from > tipSeq {
			return ChainRoot{}, fmt.Errorf("自上个链根以来没有新事件（链尖 %d）", tipSeq)
		}
	}

	// 全链校验：只验增量的话，历史段被改动不会被发现
	problem, err := s.VerifyChain(ctx, 1, tipSeq)
	if err != nil {
		return ChainRoot{}, err
	}
	if problem != nil {
		return ChainRoot{}, fmt.Errorf("拒绝为已损坏的链签名：%s", problem)
	}

	root := ChainRoot{
		ID:           uuid.New(),
		ComputedAt:   time.Now().UTC().Truncate(time.Microsecond),
		FromSequence: from,
		ToSequence:   tipSeq,
		RootHash:     tipHash,
		KeyID:        signer.KeyID(),
	}
	root.Signature = base64.StdEncoding.EncodeToString(signer.Sign(root.SigningPayload()))

	row, err := gen.New(s.pool).InsertChainRoot(ctx, gen.InsertChainRootParams{
		ID:           root.ID,
		ComputedAt:   ts(root.ComputedAt),
		FromSequence: root.FromSequence,
		ToSequence:   root.ToSequence,
		RootHash:     root.RootHash,
		Signature:    root.Signature,
		KeyID:        root.KeyID,
	})
	if err != nil {
		return ChainRoot{}, fmt.Errorf("insert chain root: %w", err)
	}
	return rootFromRow(row), nil
}

// LatestRoot 返回最近一次链根。
func (s *Store) LatestRoot(ctx context.Context) (ChainRoot, error) {
	row, err := gen.New(s.pool).GetLatestChainRoot(ctx)
	if err != nil {
		return ChainRoot{}, fmt.Errorf("get latest chain root: %w", err)
	}
	return rootFromRow(row), nil
}

// exportedRoot 是导出文件的结构。**只含公开可验证的信息，不含任何密钥材料。**
type exportedRoot struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	ComputedAt   string `json:"computed_at"`
	FromSequence int64  `json:"from_sequence"`
	ToSequence   int64  `json:"to_sequence"`
	RootHash     string `json:"root_hash"`
	Signature    string `json:"signature"`
	KeyID        string `json:"key_id"`
	PublicKey    string `json:"public_key"`
	Payload      string `json:"signed_payload"`
}

// ExportRoot 把链根写成 JSON 文件并回写导出位置。
//
// 规格 §4.4 要求根摘要存放于平台数据库之外的异故障域：本方法负责生成文件，
// 把它同步到另一台主机或对象存储是部署侧的事（见 RUNBOOK）。
func (s *Store) ExportRoot(ctx context.Context, root ChainRoot, dir string, pub ed25519.PublicKey) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("导出目录为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建导出目录: %w", err)
	}
	name := fmt.Sprintf("audit-root-%019d-%s.json", root.ToSequence, root.ID)
	path := filepath.Join(dir, name)

	payload := exportedRoot{
		Kind:         "xingmang-audit-chain-root",
		ID:           root.ID.String(),
		ComputedAt:   root.ComputedAt.UTC().Format(time.RFC3339Nano),
		FromSequence: root.FromSequence,
		ToSequence:   root.ToSequence,
		RootHash:     root.RootHash,
		Signature:    root.Signature,
		KeyID:        root.KeyID,
		PublicKey:    base64.StdEncoding.EncodeToString(pub),
		Payload:      string(root.SigningPayload()),
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化链根: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", fmt.Errorf("写出链根文件: %w", err)
	}

	if _, err := gen.New(s.pool).MarkChainRootExported(ctx, gen.MarkChainRootExportedParams{
		ID:           root.ID,
		ExportedAt:   ts(time.Now().UTC()),
		ExportTarget: path,
	}); err != nil {
		return path, fmt.Errorf("回写导出位置（文件已生成于 %s）: %w", path, err)
	}
	return path, nil
}
