package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"invoice-system/agents/sourceagent"
)

type keygenConfig struct {
	KeyID       string
	PrivatePath string
	PublicPath  string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "source-keygen:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("source-keygen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := keygenConfig{}
	flags.StringVar(&config.KeyID, "key-id", "", "receiver trust key identifier")
	flags.StringVar(&config.PrivatePath, "private-out", "", "absolute PKCS8 private PEM output path")
	flags.StringVar(&config.PublicPath, "public-out", "", "absolute raw-public-key base64 output path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("usage: source-keygen -key-id ID -private-out ABS_PATH -public-out ABS_PATH")
	}
	if stdout == nil {
		return errors.New("stdout writer is required")
	}
	if err := generate(config); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "generated source signing key key_id=%q private_path=%q public_path=%q\n",
		config.KeyID, config.PrivatePath, config.PublicPath)
	return err
}

func generate(config keygenConfig) error {
	if err := sourceagent.ValidateSigningKeyID(config.KeyID); err != nil {
		return err
	}
	privatePath, err := validateOutputPath("private-out", config.PrivatePath)
	if err != nil {
		return err
	}
	publicPath, err := validateOutputPath("public-out", config.PublicPath)
	if err != nil {
		return err
	}
	if samePath(privatePath, publicPath) {
		return errors.New("private and public output paths must be distinct")
	}
	if err := requireAbsent(privatePath); err != nil {
		return err
	}
	if err := requireAbsent(publicPath); err != nil {
		return err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate Ed25519 key failed")
	}
	defer func() {
		for index := range privateKey {
			privateKey[index] = 0
		}
	}()
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return errors.New("encode Ed25519 PKCS8 key failed")
	}
	defer func() {
		for index := range der {
			der[index] = 0
		}
	}()
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	defer func() {
		for index := range privatePEM {
			privatePEM[index] = 0
		}
	}()
	publicBase64 := []byte(base64.StdEncoding.EncodeToString(publicKey) + "\n")

	if err := createExclusiveFile(privatePath, privatePEM, 0o600); err != nil {
		return err
	}
	if err := createExclusiveFile(publicPath, publicBase64, 0o644); err != nil {
		if removeErr := os.Remove(privatePath); removeErr != nil {
			return errors.New("create public key failed and private-key rollback failed; operator cleanup required")
		}
		_ = syncOutputDirectory(filepath.Dir(privatePath))
		return err
	}
	if err := syncOutputDirectory(filepath.Dir(privatePath)); err != nil {
		return err
	}
	if filepath.Dir(publicPath) != filepath.Dir(privatePath) {
		if err := syncOutputDirectory(filepath.Dir(publicPath)); err != nil {
			return err
		}
	}
	return nil
}

func validateOutputPath(name, raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s must be an absolute clean path", name)
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s parent directory is missing or unsafe", name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%s parent directory is group/other writable", name)
	}
	return path, nil
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refuse to overwrite existing output %q", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect key output path failed")
	}
	return nil
}

func createExclusiveFile(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("refuse to overwrite or create key output %q", path)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return errors.New("set key output permissions failed")
	}
	if _, err := file.Write(content); err != nil {
		return errors.New("write key output failed")
	}
	if err := file.Sync(); err != nil {
		return errors.New("fsync key output failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("close key output failed")
	}
	keep = true
	return nil
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
