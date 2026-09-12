package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func linked(t *testing.T, backend func(*net.TCPConn)) *net.TCPConn {
	t.Helper()
	address, err := net.ResolveTCPAddr("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.ListenTCP("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	listener, err := net.ListenTCP("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		connection, err := target.AcceptTCP()
		if err == nil {
			defer connection.Close()
			backend(connection)
		}
	}()
	go func() {
		client, err := listener.AcceptTCP()
		if err != nil {
			return
		}
		connection, err := net.DialTCP("tcp4", nil, target.Addr().(*net.TCPAddr))
		if err != nil {
			client.Close()
			return
		}
		bridge(client, connection)
	}()
	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	client.SetDeadline(time.Now().Add(2 * time.Second))
	return client
}

func TestBackendEOFArrivesBeforeClientWriteClose(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 2812)
	client := linked(t, func(connection *net.TCPConn) {
		request := make([]byte, 3)
		if _, err := io.ReadFull(connection, request); err != nil {
			return
		}
		connection.Write(payload)
		connection.CloseWrite()
	})
	if _, err := client.Write([]byte("GET")); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("backend FIN was not propagated while client write stayed open: %v, bytes=%d", err, len(body))
	}
	if !bytes.Equal(body, payload) {
		t.Fatal("response bytes changed")
	}
}

func TestClientHalfClosePermitsCompleteResponse(t *testing.T) {
	payload := make([]byte, 262144)
	for i := range payload {
		payload[i] = byte(i)
	}
	client := linked(t, func(connection *net.TCPConn) {
		body, err := io.ReadAll(connection)
		if err != nil {
			return
		}
		connection.Write(body)
	})
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("upload EOF or response missing: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("opaque binary stream changed: %d bytes", len(body))
	}
}

func TestOnlyLoopbackListenersAndPrivateTargets(t *testing.T) {
	for _, value := range []string{"0.0.0.0:8089", "172.31.243.1:8089", "[::1]:8089", "localhost:8089"} {
		if endpoint(value, true) == nil {
			t.Fatalf("unsafe listen accepted: %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8:80", "203.0.113.9:80", "127.0.0.1:0", "127.0.0.1:65536"} {
		if endpoint(value, false) == nil {
			t.Fatalf("unsafe target accepted: %s", value)
		}
	}
	for _, value := range []string{"172.31.243.20:8081", "127.0.0.1:28480"} {
		if err := endpoint(value, false); err != nil {
			t.Fatal(err)
		}
	}
}
