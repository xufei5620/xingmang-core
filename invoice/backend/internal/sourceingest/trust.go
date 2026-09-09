package sourceingest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxTrustFileBytes int64 = 1 << 20

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	streamPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	keyIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	hexPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	serialPattern = regexp.MustCompile(`^[0-9A-F]{1,128}$`)
)

type trustFile struct {
	Sources []trustSourceFile `json:"sources"`
}

type trustSourceFile struct {
	SourceID   string                     `json:"source_id"`
	SourceType string                     `json:"source_type"`
	Streams    map[string]trustStreamFile `json:"streams"`
}

type trustStreamFile struct {
	CertificateSerials []string          `json:"certificate_serials"`
	SigningKeys        map[string]string `json:"signing_keys"`
}

type TrustStore struct {
	sources map[string]trustedSource
}

type trustedSource struct {
	sourceType string
	streams    map[string]trustedStream
}

type trustedStream struct {
	serials map[string]struct{}
	keys    map[string]ed25519.PublicKey
}

func LoadTrustFile(path string) (*TrustStore, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("source trust file path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxTrustFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || int64(len(body)) > maxTrustFileBytes {
		return nil, errors.New("source trust file has invalid size")
	}
	if _, err = parseUniqueJSON(body); err != nil {
		return nil, fmt.Errorf("source trust file JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var raw trustFile
	if err = decoder.Decode(&raw); err != nil {
		return nil, err
	}
	store := &TrustStore{sources: make(map[string]trustedSource, len(raw.Sources))}
	for _, source := range raw.Sources {
		if !uuidPattern.MatchString(source.SourceID) || (source.SourceType != "sub2api" && source.SourceType != "newapi") || len(source.Streams) != 5 {
			return nil, errors.New("source trust entry is invalid")
		}
		if _, duplicate := store.sources[source.SourceID]; duplicate {
			return nil, errors.New("duplicate trusted source")
		}
		trusted := trustedSource{sourceType: source.SourceType, streams: make(map[string]trustedStream, len(source.Streams))}
		for streamID, stream := range source.Streams {
			if !streamPattern.MatchString(streamID) ||
				(streamID != "payments" && streamID != "identities" && streamID != "usage" && streamID != "credits" && streamID != "balances") ||
				len(stream.CertificateSerials) == 0 || len(stream.SigningKeys) == 0 {
				return nil, errors.New("source trust stream is invalid")
			}
			entry := trustedStream{serials: make(map[string]struct{}, len(stream.CertificateSerials)), keys: make(map[string]ed25519.PublicKey, len(stream.SigningKeys))}
			for _, serial := range stream.CertificateSerials {
				serial = strings.ToUpper(strings.TrimSpace(serial))
				if !serialPattern.MatchString(serial) {
					return nil, errors.New("source certificate serial is invalid")
				}
				entry.serials[serial] = struct{}{}
			}
			for keyID, encoded := range stream.SigningKeys {
				if !keyIDPattern.MatchString(keyID) {
					return nil, errors.New("source signing key ID is invalid")
				}
				decoded, decodeErr := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(encoded))
				if decodeErr != nil || len(decoded) != ed25519.PublicKeySize {
					return nil, errors.New("source signing public key is invalid")
				}
				entry.keys[keyID] = ed25519.PublicKey(append([]byte(nil), decoded...))
			}
			trusted.streams[streamID] = entry
		}
		for _, required := range []string{"payments", "identities", "usage", "credits", "balances"} {
			if _, ok := trusted.streams[required]; !ok {
				return nil, errors.New("source trust entry is missing a required stream")
			}
		}
		store.sources[source.SourceID] = trusted
	}
	if len(store.sources) == 0 {
		return nil, errors.New("source trust file is empty")
	}
	return store, nil
}

func (s *TrustStore) resolve(sourceID, streamID, sourceType, serial, keyID string) (ed25519.PublicKey, bool) {
	if s == nil {
		return nil, false
	}
	source, ok := s.sources[sourceID]
	if !ok || source.sourceType != sourceType {
		return nil, false
	}
	stream, ok := source.streams[streamID]
	if !ok {
		return nil, false
	}
	if _, ok = stream.serials[strings.ToUpper(strings.TrimSpace(serial))]; !ok {
		return nil, false
	}
	key, ok := stream.keys[keyID]
	return append(ed25519.PublicKey(nil), key...), ok
}
