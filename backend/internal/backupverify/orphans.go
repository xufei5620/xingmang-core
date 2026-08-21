package backupverify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type OrphanDocument struct {
	ObjectKey  string
	SizeBytes  int64
	ModifiedAt time.Time
}

func FindOrphanDocuments(ctx context.Context, pool *pgxpool.Pool, root string, minimumAge time.Duration, now time.Time) ([]OrphanDocument, error) {
	if pool == nil || !filepath.IsAbs(root) || minimumAge < time.Hour {
		return nil, errors.New("database, absolute document root and minimum age of one hour are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := pool.Query(ctx, `SELECT object_key FROM invoice_documents`)
	if err != nil {
		return nil, err
	}
	referenced := map[string]struct{}{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		referenced[key] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	issued := filepath.Join(root, "issued")
	entries, err := os.ReadDir(issued)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	orphans := make([]OrphanDocument, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pdf.enc") || strings.ContainsAny(entry.Name(), `/\\`) {
			return nil, fmt.Errorf("unexpected issued document entry: %s", entry.Name())
		}
		path := filepath.Join(issued, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe issued document entry: %s", entry.Name())
		}
		key := filepath.ToSlash(filepath.Join("issued", entry.Name()))
		if _, ok := referenced[key]; ok || now.Sub(info.ModTime()) < minimumAge {
			continue
		}
		orphans = append(orphans, OrphanDocument{ObjectKey: key, SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC()})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].ObjectKey < orphans[j].ObjectKey })
	return orphans, nil
}

func IsDocumentReferenced(ctx context.Context, pool *pgxpool.Pool, objectKey string) (bool, error) {
	var found bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice_documents WHERE object_key=$1)`, objectKey).Scan(&found)
	return found, err
}
