// Local synthetic TCP transport; it never parses HTTP, TLS or PROXY headers.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

func endpoint(value string, listener bool) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || ip == nil || ip.To4() == nil {
		return fmt.Errorf("literal IPv4 endpoint required")
	}
	if listener && host != "127.0.0.1" {
		return fmt.Errorf("listener must be exact IPv4 loopback")
	}
	if !listener && !ip.IsPrivate() && !ip.IsLoopback() {
		return fmt.Errorf("only private or local fixture targets are allowed")
	}
	return nil
}

func bridge(client, backend *net.TCPConn) {
	defer client.Close()
	defer backend.Close()
	uploaded := make(chan struct{})
	go func() {
		defer close(uploaded)
		_, _ = io.Copy(backend, client)
		_ = backend.CloseWrite()
	}()
	_, _ = io.Copy(client, backend)
	_ = client.CloseWrite()
	<-uploaded
}

func serve(listen, target string) error {
	if err := endpoint(listen, true); err != nil {
		return err
	}
	if err := endpoint(target, false); err != nil {
		return err
	}
	address, err := net.ResolveTCPAddr("tcp4", listen)
	if err != nil {
		return err
	}
	listener, err := net.ListenTCP("tcp4", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	for {
		client, err := listener.AcceptTCP()
		if err != nil {
			return err
		}
		go func() {
			// Matches the former nc -w 30 connect timeout; no application/read timeout.
			dialer := net.Dialer{Timeout: 30 * time.Second}
			connection, err := dialer.Dial("tcp4", target)
			if err != nil {
				client.Close()
				return
			}
			bridge(client, connection.(*net.TCPConn))
		}()
	}
}

func main() {
	listen := flag.String("listen", "", "exact loopback listener")
	target := flag.String("target", "", "reviewed private target")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if err := serve(*listen, *target); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
