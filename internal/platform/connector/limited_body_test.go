package connector

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
)

type closeTrackingReader struct {
	*bytes.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error { r.closed = true; return nil }

func TestLimitedBodyExactBoundaryAndOverflow(t *testing.T) {
	r := &closeTrackingReader{Reader: bytes.NewReader([]byte("abcd"))}
	l, err := NewLimitedBody(r, 4)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(l)
	if err != nil || string(data) != "abcd" {
		t.Fatalf("exact read = %q, %v", data, err)
	}
	if err := l.Close(); err != nil || !r.closed {
		t.Fatalf("close err=%v closed=%v", err, r.closed)
	}

	r = &closeTrackingReader{Reader: bytes.NewReader([]byte("abcde"))}
	l, err = NewLimitedBody(r, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(l)
	if !errors.Is(err, ErrResponseBodyLimit) {
		t.Fatalf("overflow error = %v", err)
	}
	if !r.closed {
		t.Fatal("overflow must close body")
	}
}

func TestLimitedBodyRejectsContentLengthBeforeRead(t *testing.T) {
	r := &closeTrackingReader{Reader: bytes.NewReader([]byte("abc"))}
	resp := &http.Response{Body: r, ContentLength: 3}
	if _, err := WrapResponseBody(resp, 2); !errors.Is(err, ErrResponseBodyLimit) {
		t.Fatalf("content length error = %v", err)
	}
	if !r.closed {
		t.Fatal("oversized content-length must close body")
	}
}
