package sourceagent

import (
	"context"
	"errors"
	"sync"
)

// CursorStore is intentionally separate from SequenceStore. A production
// implementation stores both in the invoice control database but advances the
// source cursor only after Publisher has received and validated the batch ACK.
type CursorStore interface {
	Load(context.Context, string) (ScanCursor, error)
	CompareAndSwap(context.Context, string, ScanCursor, ScanCursor) (bool, error)
}

type MemoryCursorStore struct {
	mu      sync.Mutex
	cursors map[string]ScanCursor
}

func NewMemoryCursorStore() *MemoryCursorStore {
	return &MemoryCursorStore{cursors: make(map[string]ScanCursor)}
}

func (s *MemoryCursorStore) Load(_ context.Context, sourceID string) (ScanCursor, error) {
	if s == nil || sourceID == "" {
		return ScanCursor{}, errors.New("cursor store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[sourceID], nil
}

func (s *MemoryCursorStore) CompareAndSwap(_ context.Context, sourceID string, oldCursor, newCursor ScanCursor) (bool, error) {
	if s == nil || sourceID == "" {
		return false, errors.New("cursor store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursors[sourceID] != oldCursor {
		return false, nil
	}
	newCursor.Revision = oldCursor.Revision + 1
	s.cursors[sourceID] = newCursor
	return true, nil
}

type SyncCoordinator struct {
	SourceID  string
	Connector Connector
	Cursors   CursorStore
	Publisher *Publisher
	Limit     int
}

// SyncPage reads one bounded page, publishes its signed/hash-chained batch,
// and commits the source cursor only after a valid acknowledgement. A crash at
// any earlier point re-reads the same page and produces deterministic event IDs.
func (c *SyncCoordinator) SyncPage(ctx context.Context, mode ScanMode) (ScanPage, IngestAck, error) {
	if c == nil || !sourceIDPattern.MatchString(c.SourceID) || c.Connector == nil || c.Cursors == nil || c.Publisher == nil {
		return ScanPage{}, IngestAck{}, errors.New("sync coordinator is not configured")
	}
	cursor, err := c.Cursors.Load(ctx, c.SourceID)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	page, err := c.Connector.Scan(ctx, ScanRequest{Mode: mode, Cursor: cursor, Limit: c.Limit})
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	var ack IngestAck
	var targetCursor = page.NextCursor
	var receipt PublishReceipt
	if c.Publisher.Pending != nil {
		receipt, err = c.Publisher.PublishPage(ctx, cursor, page.NextCursor, page.Projections)
		if err != nil {
			return ScanPage{}, IngestAck{}, err
		}
		ack = receipt.Ack
		targetCursor = receipt.CursorAfter
	} else {
		ack, err = c.Publisher.Publish(ctx, page.Projections)
		if err != nil {
			return ScanPage{}, IngestAck{}, err
		}
	}
	committed, err := c.Cursors.CompareAndSwap(ctx, c.SourceID, cursor, targetCursor)
	if err != nil {
		return ScanPage{}, IngestAck{}, err
	}
	if !committed {
		return ScanPage{}, IngestAck{}, errors.New("source cursor changed concurrently")
	}
	if c.Publisher.Pending != nil {
		if err := c.Publisher.FinalizePage(ctx, receipt); err != nil {
			return ScanPage{}, IngestAck{}, err
		}
	}
	page.NextCursor = targetCursor
	return page, ack, nil
}
