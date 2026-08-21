package document

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

const (
	defaultClamAVDialTimeout = 3 * time.Second
	defaultClamAVIOTimeout   = 30 * time.Second
	clamAVChunkBytes         = 64 << 10
	clamAVMaxResponseBytes   = 4 << 10
)

var ErrMalwareFound = errors.New("malware detected")

// ClamAVScanner streams the quarantined file to clamd using the INSTREAM
// protocol. It does not require clamd to share or trust the document path.
type ClamAVScanner struct {
	Address     string
	DialTimeout time.Duration
	IOTimeout   time.Duration
}

func (s ClamAVScanner) Ping(ctx context.Context) error {
	address := strings.TrimSpace(s.Address)
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid ClamAV address: %w", err)
	}
	timeout := s.DialTimeout
	if timeout <= 0 {
		timeout = defaultClamAVDialTimeout
	}
	connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to ClamAV: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	if err = writeAll(connection, []byte("zPING\x00")); err != nil {
		return err
	}
	response, err := bufio.NewReader(io.LimitReader(connection, 64)).ReadString('\x00')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(strings.TrimSuffix(response, "\x00")) != "PONG" {
		return errors.New("ClamAV PING did not return PONG")
	}
	return nil
}

func (s ClamAVScanner) Scan(ctx context.Context, path string) error {
	address := strings.TrimSpace(s.Address)
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid ClamAV address: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open quarantined document: %w", err)
	}
	defer file.Close()

	dialTimeout := s.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultClamAVDialTimeout
	}
	ioTimeout := s.IOTimeout
	if ioTimeout <= 0 {
		ioTimeout = defaultClamAVIOTimeout
	}
	connection, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to ClamAV: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(ioTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err = connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set ClamAV deadline: %w", err)
	}
	if err = writeAll(connection, []byte("zINSTREAM\x00")); err != nil {
		return fmt.Errorf("start ClamAV stream: %w", err)
	}

	chunk := make([]byte, clamAVChunkBytes)
	var size [4]byte
	for {
		read, readErr := file.Read(chunk)
		if read > 0 {
			binary.BigEndian.PutUint32(size[:], uint32(read))
			if err = writeAll(connection, size[:]); err != nil {
				return fmt.Errorf("write ClamAV chunk size: %w", err)
			}
			if err = writeAll(connection, chunk[:read]); err != nil {
				return fmt.Errorf("write ClamAV chunk: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read quarantined document: %w", readErr)
		}
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if err = writeAll(connection, size[:]); err != nil {
		return fmt.Errorf("finish ClamAV stream: %w", err)
	}

	response, err := bufio.NewReader(io.LimitReader(connection, clamAVMaxResponseBytes)).ReadString('\x00')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read ClamAV response: %w", err)
	}
	response = strings.TrimSpace(strings.TrimSuffix(response, "\x00"))
	switch {
	case strings.HasSuffix(response, ": OK"):
		return nil
	case strings.HasSuffix(response, " FOUND"):
		return ErrMalwareFound
	case response == "":
		return errors.New("ClamAV returned an empty response")
	default:
		if len(response) > 256 {
			response = response[:256]
		}
		return fmt.Errorf("ClamAV rejected scan: %s", response)
	}
}

func writeAll(writer io.Writer, body []byte) error {
	for len(body) > 0 {
		written, err := writer.Write(body)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		body = body[written:]
	}
	return nil
}
