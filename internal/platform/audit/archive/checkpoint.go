package archive

// ValidateCheckpointBinding validates the frozen checkpoint envelope before a
// caller persists or publishes its object reference. It intentionally does not
// perform keyring trust lookup; VerifyCheckpointSignature does that separately.
func ValidateCheckpointBinding(value SignedCheckpointV1) error {
	if err := validateSignedCheckpoint(value); err != nil {
		return err
	}
	if _, err := EncodeSignedCheckpointV1(value); err != nil {
		return err
	}
	return nil
}

// SignedCheckpointDigest is the content digest of the exact signed wire bytes,
// including its required trailing LF. It is distinct from UnsignedSHA256.
func SignedCheckpointDigest(value SignedCheckpointV1) (string, error) {
	encoded, err := EncodeSignedCheckpointV1(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}
