package securefields

import (
	"bytes"
	"testing"
)

func testKeyring() Keyring {
	return Keyring{CurrentKeyID: "key-2026-01", EncryptionKeys: map[string][]byte{"key-2026-01": bytes.Repeat([]byte{1}, 32), "key-old": bytes.Repeat([]byte{2}, 32)}, IndexKey: bytes.Repeat([]byte{3}, 32)}
}

func TestEncryptDecryptUsesAADAndDetectsTampering(t *testing.T) {
	keys := testKeyring()
	ciphertext, err := keys.Encrypt([]byte("91310000TEST000001"), "tenant/user/profile/tax_id")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := keys.Decrypt(ciphertext, "tenant/user/profile/tax_id")
	if err != nil || string(plaintext) != "91310000TEST000001" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}
	if _, err = keys.Decrypt(ciphertext, "tenant/other/profile/tax_id"); err == nil {
		t.Fatal("AAD substitution accepted")
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err = keys.Decrypt(ciphertext, "tenant/user/profile/tax_id"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestBlindIndexNormalizesButUsesSeparateNamespace(t *testing.T) {
	keys := testKeyring()
	a, err := keys.BlindIndex("tax-id", " 9131 0000 test ")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := keys.BlindIndex("tax-id", "91310000TEST")
	c, _ := keys.BlindIndex("bank-account", "91310000TEST")
	if a != b {
		t.Fatalf("normalization mismatch %s %s", a, b)
	}
	if a == c {
		t.Fatal("cross-field blind index collision")
	}
}
