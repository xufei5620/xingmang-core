package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type staticEndpointResolver struct{ addresses []netip.Addr }

func (r staticEndpointResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), nil
}

type recordingEndpointDialer struct {
	mu            sync.Mutex
	calls         []string
	called        chan string
	failures      map[string]error
	actualAddress string
}

type observableEndpointConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *observableEndpointConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type cancelAwareEndpointDialer struct {
	loserStarted  chan struct{}
	loserCanceled chan struct{}
	lateLoser     *observableEndpointConn
}

func (d *cancelAwareEndpointDialer) DialContext(ctx context.Context, _ string, address string) (net.Conn, error) {
	if address == "8.8.8.8:443" {
		close(d.loserStarted)
		<-ctx.Done()
		close(d.loserCanceled)
		return d.lateLoser, nil
	}
	<-d.loserStarted
	winner, peer := net.Pipe()
	_ = peer.Close()
	return winner, nil
}

func (d *recordingEndpointDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, address)
	d.mu.Unlock()
	if d.called != nil {
		d.called <- address
	}
	if err := d.failures[address]; err != nil {
		return nil, err
	}
	if d.actualAddress != "" {
		return (&net.Dialer{}).DialContext(ctx, network, d.actualAddress)
	}
	client, peer := net.Pipe()
	go func() {
		<-ctx.Done()
		_ = peer.Close()
	}()
	return client, nil
}

func (d *recordingEndpointDialer) callSnapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func TestEndpointIPAddressPolicy(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if address := netip.MustParseAddr(raw); !isPublicEndpointIP(address) {
			t.Fatalf("public address rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"192.0.2.1", "192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"::1", "::192.0.2.1", "64:ff9b::7f00:1", "100::1", "2001:db8::1",
		"5f00::1", "fc00::1", "fec0::1", "fe80::1",
	} {
		if address := netip.MustParseAddr(raw); isPublicEndpointIP(address) {
			t.Fatalf("non-public address accepted: %s", raw)
		}
	}
	if _, err := protectedEndpointDialContext([]string{"10.0.0.8"}); err != nil {
		t.Fatalf("exact canonical private IdP exception rejected: %v", err)
	}
	for _, raw := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.0/8", "010.0.0.8", "8.8.8.8"} {
		if _, err := protectedEndpointDialContext([]string{raw}); err == nil {
			t.Fatalf("unsafe private endpoint exception accepted: %q", raw)
		}
	}
}

func TestProtectedEndpointDialRejectsMixedDNSAndUsesNumericPrivateException(t *testing.T) {
	mixedDialer := &recordingEndpointDialer{}
	mixed, err := protectedEndpointDialContextWithDependencies(nil, staticEndpointResolver{addresses: []netip.Addr{
		netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.8"),
	}}, mixedDialer)
	if err != nil {
		t.Fatal(err)
	}
	if connection, dialErr := mixed(context.Background(), "tcp", "identity.example:443"); dialErr == nil || connection != nil {
		t.Fatal("mixed public/private DNS answer was accepted")
	}
	if calls := mixedDialer.callSnapshot(); len(calls) != 0 {
		t.Fatalf("mixed DNS answer reached the network dialer: %v", calls)
	}

	privateDialer := &recordingEndpointDialer{}
	private, err := protectedEndpointDialContextWithDependencies([]string{"10.0.0.8"}, staticEndpointResolver{addresses: []netip.Addr{
		netip.MustParseAddr("10.0.0.8"),
	}}, privateDialer)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := private(context.Background(), "tcp", "identity.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if calls := privateDialer.callSnapshot(); len(calls) != 1 || calls[0] != "10.0.0.8:8443" {
		t.Fatalf("private exception did not pin the numeric dial target: %v", calls)
	}
}

func TestProtectedEndpointDialUsesConcurrentAddressFallback(t *testing.T) {
	called := make(chan string, 2)
	dialer := &recordingEndpointDialer{
		called:   called,
		failures: map[string]error{"8.8.8.8:443": errors.New("fixture blackhole")},
	}
	dial, err := protectedEndpointDialContextWithDependencies(nil, staticEndpointResolver{addresses: []netip.Addr{
		netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1"),
	}}, dialer)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), "tcp", "identity.example:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	seen := map[string]bool{}
	for range 2 {
		select {
		case address := <-called:
			seen[address] = true
		case <-time.After(time.Second):
			t.Fatal("multi-address fallback did not start every validated candidate")
		}
	}
	if !seen["8.8.8.8:443"] || !seen["1.1.1.1:443"] {
		t.Fatalf("unexpected numeric fallback targets: %v", seen)
	}
}

func TestProtectedEndpointDialCancelsAndClosesLateLoser(t *testing.T) {
	lateConnection, latePeer := net.Pipe()
	defer latePeer.Close()
	lateLoser := &observableEndpointConn{Conn: lateConnection, closed: make(chan struct{})}
	dialer := &cancelAwareEndpointDialer{
		loserStarted: make(chan struct{}), loserCanceled: make(chan struct{}), lateLoser: lateLoser,
	}
	dial, err := protectedEndpointDialContextWithDependencies(nil, staticEndpointResolver{addresses: []netip.Addr{
		netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1"),
	}}, dialer)
	if err != nil {
		t.Fatal(err)
	}
	winner, err := dial(context.Background(), "tcp", "identity.example:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = winner.Close()
	select {
	case <-dialer.loserCanceled:
	case <-time.After(time.Second):
		t.Fatal("losing address did not receive context cancellation")
	}
	select {
	case <-lateLoser.closed:
	case <-time.After(time.Second):
		t.Fatal("late losing connection was not closed by the result drainer")
	}
}

func TestProtectedEndpointDialPreservesTLSHostname(t *testing.T) {
	sni := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			select {
			case sni <- hello.ServerName:
			default:
			}
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dialer := &recordingEndpointDialer{actualAddress: server.Listener.Addr().String()}
	dial, err := protectedEndpointDialContextWithDependencies([]string{"10.0.0.8"}, staticEndpointResolver{addresses: []netip.Addr{
		netip.MustParseAddr("10.0.0.8"),
	}}, dialer)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}} // test fixture certificate
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get("https://identity.example:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected fixture status: %d", response.StatusCode)
	}
	select {
	case name := <-sni:
		if name != "identity.example" {
			t.Fatalf("TLS SNI=%q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS server did not receive a ClientHello")
	}
	wantTarget := net.JoinHostPort("10.0.0.8", port)
	if calls := dialer.callSnapshot(); len(calls) != 1 || calls[0] != wantTarget {
		t.Fatalf("TLS transport did not dial the validated numeric address: %v", calls)
	}
}
