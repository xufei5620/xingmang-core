package adminsettings

import (
	"context"

	"invoice-system/backend/internal/securefields"
)

type SecureFieldsBox struct {
	Keyring securefields.Keyring
	AAD     string
}

func (b SecureFieldsBox) Seal(_ context.Context, plaintext []byte) (SecretEnvelope, error) {
	ciphertext, err := b.Keyring.Encrypt(plaintext, b.AAD)
	if err != nil {
		return SecretEnvelope{}, err
	}
	return SecretEnvelope{Ciphertext: ciphertext, KeyVersion: b.Keyring.CurrentKeyID}, nil
}
func (b SecureFieldsBox) Open(_ context.Context, envelope SecretEnvelope) ([]byte, error) {
	return b.Keyring.Decrypt(envelope.Ciphertext, b.AAD)
}
