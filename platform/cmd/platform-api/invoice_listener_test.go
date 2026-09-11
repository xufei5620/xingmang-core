package main

import (
	"context"
	"net"
	"testing"
)

func TestUnifiedListenerFailureDoesNotStartInvoiceWorkers(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	started := 0
	listener, err := startUnifiedListener(context.Background(), occupied.Addr().String(), func(context.Context) error { started++; return nil })
	if listener != nil {
		listener.Close()
		t.Fatal("occupied port unexpectedly usable")
	}
	if err == nil || started != 0 {
		t.Fatalf("listen failure started workers: started=%d err=%v", started, err)
	}
}

func TestUnifiedListenerCancellationDoesNotStartWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := 0
	listener, err := startUnifiedListener(ctx, "127.0.0.1:0", func(context.Context) error { started++; return nil })
	if listener != nil {
		listener.Close()
	}
	if err == nil || started != 0 {
		t.Fatalf("canceled startup continued: started=%d err=%v", started, err)
	}
}
