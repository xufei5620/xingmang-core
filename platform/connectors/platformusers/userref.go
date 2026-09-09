package platformusers

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxUserIDBytes = 512

type UserRef struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
}

func (r UserRef) Validate() error {
	platform, err := ParseSource(r.Platform)
	if err != nil || platform != strings.TrimSpace(r.Platform) || r.ID == "" || !utf8.ValidString(r.ID) || len([]byte(r.ID)) > maxUserIDBytes {
		return errors.New("invalid platform user ref")
	}
	return nil
}

func EncodeUserIDSegment(userID string) (string, error) {
	if userID == "" || !utf8.ValidString(userID) || len([]byte(userID)) > maxUserIDBytes {
		return "", errors.New("invalid user id")
	}
	return "u-" + hex.EncodeToString([]byte(userID)), nil
}

func DecodeUserIDSegment(segment string) (string, error) {
	if !strings.HasPrefix(segment, "u-") {
		return "", errors.New("invalid user id segment")
	}
	hexPart := strings.TrimPrefix(segment, "u-")
	if hexPart == "" || len(hexPart)%2 != 0 || strings.ToLower(hexPart) != hexPart {
		return "", errors.New("invalid user id segment")
	}
	decoded, err := hex.DecodeString(hexPart)
	if err != nil || len(decoded) == 0 || len(decoded) > maxUserIDBytes || !utf8.Valid(decoded) {
		return "", errors.New("invalid user id segment")
	}
	id := string(decoded)
	canonical, err := EncodeUserIDSegment(id)
	if err != nil || canonical != segment {
		return "", fmt.Errorf("invalid user id segment")
	}
	return id, nil
}
