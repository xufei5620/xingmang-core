package connector

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
)

// ErrResponseBodyLimit is stable and deliberately contains no body prefix.
var ErrResponseBodyLimit = errors.New("connector response body exceeds budget")

// LimitedBody enforces a decoded-byte ceiling while preserving Close semantics.
// It is safe to use from a single decoder goroutine; the mutex also makes
// BytesRead/Close observations race-free in instrumentation tests.
type LimitedBody struct {
	mu       sync.Mutex
	r        io.ReadCloser
	max      int64
	read     int64
	exceeded bool
	closed   bool
}

func NewLimitedBody(r io.ReadCloser, maxBytes int64) (*LimitedBody, error) {
	if r == nil || maxBytes <= 0 {
		return nil, ErrResponseBodyLimit
	}
	return &LimitedBody{r: r, max: maxBytes}, nil
}

// NewLimitedReader is the io.Reader counterpart for decoded streams (for
// example a gzip.Reader).  It still returns a reader that reports the same
// stable ErrResponseBodyLimit and closes the underlying reader when possible.
func NewLimitedReader(r io.Reader, maxBytes int64) *LimitedReader {
	return &LimitedReader{r: r, max: maxBytes}
}

type LimitedReader struct {
	r        io.Reader
	max      int64
	read     int64
	exceeded bool
}

func (r *LimitedReader) Read(p []byte) (int, error) {
	if r == nil || r.r == nil || r.max <= 0 {
		return 0, ErrResponseBodyLimit
	}
	if r.exceeded {
		return 0, ErrResponseBodyLimit
	}
	if len(p) == 0 {
		return 0, nil
	}
	remaining := r.max - r.read
	if remaining <= 0 {
		var one [1]byte
		n, err := r.r.Read(one[:])
		if n > 0 {
			r.exceeded = true
			return 0, ErrResponseBodyLimit
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.read += int64(n)
	}
	return n, err
}

// NewContextLimitedReader makes cancellation explicit for slow streams.  It
// does not start a goroutine and therefore cannot leak one when the caller
// abandons a request.
func NewContextLimitedReader(ctx context.Context, r io.Reader, maxBytes int64) *ContextLimitedReader {
	return &ContextLimitedReader{ctx: ctx, r: NewLimitedReader(r, maxBytes)}
}

type ContextLimitedReader struct {
	ctx context.Context
	r   *LimitedReader
}

func (r *ContextLimitedReader) Read(p []byte) (int, error) {
	if r == nil || r.r == nil {
		return 0, ErrResponseBodyLimit
	}
	if r.ctx != nil {
		select {
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		default:
		}
	}
	return r.r.Read(p)
}

// WrapResponseBody performs the cheap Content-Length check before any bytes
// are read, then wraps chunked/unknown responses with the same hard limiter.
func WrapResponseBody(resp *http.Response, maxBytes int64) (io.ReadCloser, error) {
	if resp == nil || resp.Body == nil || maxBytes <= 0 {
		return nil, ErrResponseBodyLimit
	}
	if resp.ContentLength > maxBytes {
		_ = resp.Body.Close()
		return nil, ErrResponseBodyLimit
	}
	body, err := NewLimitedBody(resp.Body, maxBytes)
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return body, nil
}

// LimitResponseBody is a descriptive alias retained for callers that prefer
// the operation name used in the R2-15 evidence checklist.
func LimitResponseBody(resp *http.Response, maxBytes int64) (io.ReadCloser, error) {
	return WrapResponseBody(resp, maxBytes)
}

func (b *LimitedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if b.exceeded {
		return 0, ErrResponseBodyLimit
	}
	if len(p) == 0 {
		return 0, nil
	}
	remaining := b.max - b.read
	if remaining <= 0 {
		// Probe exactly one byte.  This distinguishes a body ending at the
		// boundary from a chunked body that contains one more byte.
		var one [1]byte
		n, err := b.r.Read(one[:])
		if n > 0 {
			b.exceeded = true
			_ = b.r.Close()
			b.closed = true
			return 0, ErrResponseBodyLimit
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.r.Read(p)
	if n > 0 {
		b.read += int64(n)
		if b.read > b.max {
			b.exceeded = true
			_ = b.r.Close()
			b.closed = true
			return 0, ErrResponseBodyLimit
		}
	}
	return n, err
}

func (b *LimitedBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.r.Close()
}

func (b *LimitedBody) BytesRead() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.read }
func (b *LimitedBody) Limit() int64     { b.mu.Lock(); defer b.mu.Unlock(); return b.max }
func (b *LimitedBody) Exceeded() bool   { b.mu.Lock(); defer b.mu.Unlock(); return b.exceeded }
