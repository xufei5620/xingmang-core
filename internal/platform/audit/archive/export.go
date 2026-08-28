package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

type LocalAuditSourceReader interface {
	Tip(context.Context) (int64, string, error)
	List(context.Context, int64, int64) ([]audit.Event, error)
	VerifyChain(context.Context, int64, int64) (*audit.ChainProblem, error)
	LatestRoot(context.Context) (audit.ChainRoot, error)
}

func ExportRoot(root audit.ChainRoot, directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("archive root export directory required")
	}
	wire := ChainRootRefV1{
		ID: root.ID.String(), ComputedAt: NewWireTime(root.ComputedAt),
		FromSequence: root.FromSequence, ToSequence: root.ToSequence,
		RootHash: root.RootHash, Signature: root.Signature, KeyID: root.KeyID,
	}
	encoded, err := EncodeChainRootRefV1(wire)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create root export directory: %w", err)
	}
	name := fmt.Sprintf("audit-root-%019d-%s.json", root.ToSequence, root.ID)
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read existing root export: %w", readErr)
		}
		if !bytes.Equal(existing, encoded) {
			return "", fmt.Errorf("root export collision")
		}
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("create root export: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(encoded); err != nil {
		return "", fmt.Errorf("write root export: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync root export: %w", err)
	}
	return path, nil
}
