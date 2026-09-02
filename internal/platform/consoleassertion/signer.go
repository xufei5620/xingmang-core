package consoleassertion

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// signingKeyRefPrefix is the naming convention the technical spec fixes for
// private key custody (§3.3: "secret://console-assertion/signing-key-
// <key_id>"). The key_id embedded in the ref name is also the JWS kid --
// one env var (XM_INVOICE_CONSOLE_ASSERTION_KEY_REF) is therefore enough to
// pin both which secret to load and what to call it in the header, with no
// separate "key id" configuration to drift out of sync with the ref.
const signingKeyRefPrefix = "signing-key-"

// signingKeyPurpose is the secrets.AccessRecord.Purpose recorded on every
// resolve of the private key (never the key material itself -- see
// SecretValue's redaction guarantees).
const signingKeyPurpose = "console assertion signing"

// SigningKeyProvider is the narrow secret-resolution dependency Signer
// needs (any secrets.SecretProvider satisfies it; production wiring passes
// the same file-backed, audited provider used for TOTP secrets -- see
// cmd/platform-api's platformUsersSecretProvider).
type SigningKeyProvider interface {
	Resolve(ctx context.Context, ref secrets.CredentialRef, purpose string) (secrets.SecretValue, error)
}

// Signer holds exactly one active Ed25519 signing keypair in memory (spec
// §3.3: "私钥...启动时加载进内存,不落日志、不落 HTTP 响应,不进 AI 上下文").
//
// Rotation is an operator action, not a runtime "hold N keys, pick the
// newest valid one" decision: generate a new keypair with
// cmd/console-assertion-keygen, publish its public entry to
// contracts/auth/console-assertion-keyring.v1.json in *both* repositories
// (reviewed, per the spec's "静态清单文件,不采用实时 JWKS" design), then point
// XM_INVOICE_CONSOLE_ASSERTION_KEY_REF at the new secret ref and redeploy.
// This is enough given the assertion's own <=5 minute lifetime: unlike
// internal/platform/jobs/fleet_keyring.go's artifacts (which can be verified
// long after signing, so a verifier must keep every historically-valid
// public key around), nothing here needs to *verify* an old assertion once
// the active signing key has moved on -- the manifest's valid_from/
// valid_until/revoked_at trio exists for the *invoice-side verifier's*
// benefit (defending against a leaked old key being used past its
// intended lifetime), not for this signer's own key selection.
type Signer struct {
	private     ed25519.PrivateKey
	public      ed25519.PublicKey
	keyID       string
	fingerprint string
}

// NewSigner resolves ref (expected shape secret://console-assertion/
// signing-key-<key_id>) via provider, derives key_id from the ref name, and
// constructs the Ed25519 keypair. The stored secret value must be the
// base64-standard-encoded 32-byte Ed25519 seed (the same encoding
// convention internal/platform/jobs/fleet_keyring.go uses for its own public
// key material) -- cmd/console-assertion-keygen writes exactly this via its
// one-time "paste this into credential.secret.upsert" output.
func NewSigner(ctx context.Context, provider SigningKeyProvider, ref secrets.CredentialRef) (*Signer, error) {
	if provider == nil {
		return nil, errors.New("consoleassertion: signing key provider 未装配")
	}
	if ref.IsZero() {
		return nil, errors.New("consoleassertion: signing key ref 为空")
	}
	keyID := strings.TrimPrefix(ref.Name(), signingKeyRefPrefix)
	if keyID == "" || keyID == ref.Name() {
		return nil, fmt.Errorf(
			"consoleassertion: signing key ref %q 不符合约定形状 secret://console-assertion/%s<key_id>",
			ref, signingKeyRefPrefix)
	}
	value, err := provider.Resolve(ctx, ref, signingKeyPurpose)
	if err != nil {
		return nil, fmt.Errorf("consoleassertion: 解析签名私钥失败: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value.Reveal()))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("consoleassertion: 签名私钥不是合法的 base64 Ed25519 种子（应为 %d 字节）", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("consoleassertion: 派生公钥失败")
	}
	sum := sha256.Sum256(pub)
	return &Signer{private: priv, public: pub, keyID: keyID, fingerprint: hex.EncodeToString(sum[:])}, nil
}

// KeyID / PublicKeyBase64 / Fingerprint are all public information, safe to
// log at startup so an operator can visually cross-check them against the
// matching key_id record in contracts/auth/console-assertion-keyring.v1.json
// (in both repositories) without ever touching the private key itself.
func (s *Signer) KeyID() string           { return s.keyID }
func (s *Signer) PublicKeyBase64() string { return base64.StdEncoding.EncodeToString(s.public) }
func (s *Signer) Fingerprint() string     { return s.fingerprint }

// ClaimsInput is the part of the assertion the caller controls. acr/amr/
// nonce/iat/nbf/exp are computed entirely inside Sign: a caller cannot
// stretch the assertion's lifetime past assertionTTL, and cannot forge amr
// -- by the time Handlers.Issue calls Sign, it has already independently
// established (via localauth.RequireFreshOTP) that this session's own
// mfa_at is fresh, which is the fact amr=["pwd","otp"] asserts.
type ClaimsInput struct {
	Issuer   string
	Audience string
	Subject  string
	Username string
	Roles    []string
	Scope    string
}

// Sign issues one assertion, returning its compact JWS, its exp time, and
// its nonce (the last purely so callers can record a truncated form in an
// audit trail without re-parsing the token they just produced -- spec §7:
// "ResourceID：断言的 nonce（截断展示，完整值不落审计）").
func (s *Signer) Sign(now time.Time, in ClaimsInput) (assertion string, expiresAt time.Time, nonce string, err error) {
	nonce, err = randomNonce()
	if err != nil {
		return "", time.Time{}, "", err
	}
	iat := now.UTC()
	exp := iat.Add(assertionTTL)
	claims := wireClaims{
		Issuer: in.Issuer, Audience: in.Audience, Subject: in.Subject, Username: in.Username,
		Roles: append([]string(nil), in.Roles...), ACR: ACR, AMR: []string{"pwd", "otp"},
		Scope: in.Scope, Nonce: nonce,
		IssuedAt: iat.Unix(), NotBefore: iat.Unix(), ExpiresAt: exp.Unix(),
	}
	compact, err := signCompact(s.private, s.keyID, claims)
	if err != nil {
		return "", time.Time{}, "", err
	}
	return compact, exp, nonce, nil
}
