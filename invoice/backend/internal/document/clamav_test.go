package document

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeClamAV(t *testing.T, response string) (string, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan []byte, 1)
	go func() {
		defer listener.Close()
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		command := make([]byte, len("zINSTREAM\x00"))
		if _, acceptErr = io.ReadFull(connection, command); acceptErr != nil {
			return
		}
		var body []byte
		var size [4]byte
		for {
			if _, acceptErr = io.ReadFull(connection, size[:]); acceptErr != nil {
				return
			}
			length := binary.BigEndian.Uint32(size[:])
			if length == 0 {
				break
			}
			chunk := make([]byte, length)
			if _, acceptErr = io.ReadFull(connection, chunk); acceptErr != nil {
				return
			}
			body = append(body, chunk...)
		}
		seen <- body
		_, _ = connection.Write([]byte(response + "\x00"))
	}()
	return listener.Addr().String(), seen
}

func TestClamAVScannerStreamsDocument(t *testing.T) {
	address, seen := fakeClamAV(t, "stream: OK")
	path := filepath.Join(t.TempDir(), "invoice.pdf")
	want := []byte("%PDF-1.7\ncontent")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (ClamAVScanner{Address: address, IOTimeout: 2 * time.Second}).Scan(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; string(got) != string(want) {
		t.Fatalf("streamed body=%q", got)
	}
}

func TestClamAVScannerRejectsMalwareAndInvalidAddress(t *testing.T) {
	address, _ := fakeClamAV(t, "stream: Eicar-Signature FOUND")
	path := filepath.Join(t.TempDir(), "invoice.pdf")
	if err := os.WriteFile(path, []byte("%PDF-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (ClamAVScanner{Address: address}).Scan(context.Background(), path); !errors.Is(err, ErrMalwareFound) {
		t.Fatalf("malware error=%v", err)
	}
	if err := (ClamAVScanner{Address: "clamav"}).Scan(context.Background(), path); err == nil {
		t.Fatal("invalid ClamAV address accepted")
	}
}

func TestClamAVScannerPing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		command := make([]byte, len("zPING\x00"))
		if _, acceptErr = io.ReadFull(connection, command); acceptErr == nil && string(command) == "zPING\x00" {
			_, _ = connection.Write([]byte("PONG\x00"))
		}
	}()
	if err = (ClamAVScanner{Address: listener.Addr().String()}).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
