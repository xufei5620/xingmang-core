package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"invoice-system/agents/sourceagent"
)

func TestKeygenCreatesUsableFilesWithoutPrintingKeyMaterial(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "source-private.pem")
	publicPath := filepath.Join(directory, "source-public.b64")
	var stdout bytes.Buffer
	err := run([]string{
		"-key-id", "source-signing-2026-01",
		"-private-out", privatePath,
		"-public-out", publicPath,
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(privatePEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("private output is not one PKCS8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		t.Fatal("private output is not Ed25519")
	}
	publicText, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(publicText)))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		t.Fatal("public output is not standard base64 raw Ed25519 public key")
	}
	derived := privateKey.Public().(ed25519.PublicKey)
	if !bytes.Equal(publicKey, derived) {
		t.Fatal("public output does not match private key")
	}
	loadedPublic, err := sourceagent.LoadRawEd25519PublicKeyFile(publicPath)
	if err != nil || !bytes.Equal(loadedPublic, derived) {
		t.Fatalf("receiver-compatible public-key loader failed: %v", err)
	}
	if bytes.Contains(stdout.Bytes(), privatePEM) || bytes.Contains(stdout.Bytes(), bytes.TrimSpace(publicText)) || !strings.Contains(stdout.String(), "source-signing-2026-01") {
		t.Fatalf("stdout leaked key material or omitted key ID: %q", stdout.String())
	}
	if runtime.GOOS != "windows" {
		privateInfo, _ := os.Stat(privatePath)
		publicInfo, _ := os.Stat(publicPath)
		if privateInfo.Mode().Perm() != 0o600 || publicInfo.Mode().Perm() != 0o644 {
			t.Fatalf("unexpected modes private=%o public=%o", privateInfo.Mode().Perm(), publicInfo.Mode().Perm())
		}
	}
}

func TestKeygenRefusesOverwriteWithoutChangingEitherFile(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private.pem")
	publicPath := filepath.Join(directory, "public.b64")
	if err := os.WriteFile(publicPath, []byte("existing-public"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generate(keygenConfig{KeyID: "key-1", PrivatePath: privatePath, PublicPath: publicPath})
	if err == nil {
		t.Fatal("existing public output was overwritten")
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatal("private key was created despite preflight overwrite rejection")
	}
	public, _ := os.ReadFile(publicPath)
	if string(public) != "existing-public" {
		t.Fatal("existing public output changed")
	}
}

func TestKeygenRejectsInvalidOrAliasedPaths(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private-output")
	publicPath := filepath.Join(directory, "public-output")
	// Distinct, absent outputs make the invalid identifier the only bad input.
	err := generate(keygenConfig{KeyID: "bad id", PrivatePath: privatePath, PublicPath: publicPath})
	if err == nil || !strings.Contains(err.Error(), "signing key id must match") {
		t.Fatalf("expected signing key ID rejection before generation, got %v", err)
	}
	for _, path := range []string{privatePath, publicPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid signing key ID created output")
		}
	}
	if err := generate(keygenConfig{KeyID: "key-1", PrivatePath: privatePath, PublicPath: privatePath}); err == nil {
		t.Fatal("aliased outputs were accepted")
	}
}
