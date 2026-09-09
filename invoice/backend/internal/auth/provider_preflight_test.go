package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const preflightLeakMarker = "do-not-print-this-token"

var preflightTestRSAKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

type providerPreflightFixture struct {
	server       *httptest.Server
	redirectHits atomic.Int32
}

type staticOIDCResolver struct{ addresses []netip.Addr }

func (r staticOIDCResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), nil
}

type recordingOIDCDialer struct {
	mu            sync.Mutex
	calls         []string
	called        chan string
	failures      map[string]error
	actualAddress string
}

type observableOIDCConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *observableOIDCConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type cancelAwareOIDCDialer struct {
	loserStarted  chan struct{}
	loserCanceled chan struct{}
	lateLoser     *observableOIDCConn
}

func (d *cancelAwareOIDCDialer) DialContext(ctx context.Context, _ string, address string) (net.Conn, error) {
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

func (d *recordingOIDCDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
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

func (d *recordingOIDCDialer) callSnapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func newProviderPreflightFixture(t *testing.T, mode string) *providerPreflightFixture {
	t.Helper()
	fixture := &providerPreflightFixture{}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			if mode == "redirect" {
				writer.Header().Set("Location", fixture.server.URL+"/redirected?access_token="+preflightLeakMarker)
				writer.WriteHeader(http.StatusFound)
				return
			}
			if mode == "duplicate_json_key" {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(writer, `{"issuer":%q,"issuer":%q}`, fixture.server.URL, "https://attacker.invalid")
				return
			}
			document := map[string]any{
				"issuer":                                fixture.server.URL,
				"authorization_endpoint":                fixture.server.URL + "/authorize",
				"token_endpoint":                        fixture.server.URL + "/token",
				"userinfo_endpoint":                     fixture.server.URL + "/userinfo",
				"jwks_uri":                              fixture.server.URL + "/jwks",
				"response_types_supported":              []string{"code"},
				"scopes_supported":                      []string{"openid", "profile", "email"},
				"code_challenge_methods_supported":      []string{"S256"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
				"end_session_endpoint":                  fixture.server.URL + "/logout",
				"backchannel_logout_supported":          true,
				"backchannel_logout_session_supported":  true,
			}
			switch mode {
			case "ssrf_endpoint":
				document["token_endpoint"] = "https://169.254.169.254/latest/meta-data"
			case "issuer_mismatch":
				document["issuer"] = "https://attacker.invalid"
			case "missing_authorization":
				delete(document, "authorization_endpoint")
			case "missing_token":
				delete(document, "token_endpoint")
			case "missing_end_session":
				delete(document, "end_session_endpoint")
			case "missing_userinfo":
				delete(document, "userinfo_endpoint")
			case "missing_jwks":
				delete(document, "jwks_uri")
			case "missing_backchannel":
				document["backchannel_logout_supported"] = false
			case "missing_backchannel_session":
				document["backchannel_logout_session_supported"] = false
			case "algorithm_downgrade":
				document["id_token_signing_alg_values_supported"] = []string{"HS256", "none"}
			case "oversized_response":
				document["padding"] = strings.Repeat("x", 70<<10)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(document)
		case "/jwks":
			writer.Header().Set("Content-Type", "application/jwk-set+json")
			if mode == "invalid_jwk" {
				_, _ = writer.Write([]byte(`{"keys":[{"kty":"RSA","kid":"preflight-key","use":"sig","alg":"RS256","e":"AQAB"}]}`))
				return
			}
			if mode == "weak_jwk" {
				_, _ = writer.Write([]byte(`{"keys":[{"kty":"RSA","kid":"preflight-key","use":"sig","alg":"RS256","n":"AQAB","e":"AQAB"}]}`))
				return
			}
			writeFixtureJSON(writer, map[string]any{"keys": []any{rsaPublicJWK(&preflightTestRSAKey.PublicKey)}})
		case "/redirected":
			fixture.redirectHits.Add(1)
			http.Error(writer, "redirect followed", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *providerPreflightFixture) config() OIDCConfig {
	return OIDCConfig{
		IssuerURL:                f.server.URL,
		ClientSecretFile:         "/run/secrets/" + preflightLeakMarker,
		AllowedEndpointHosts:     []string{strings.TrimPrefix(f.server.URL, "https://")},
		AllowedSigningAlgs:       []string{"RS256"},
		TokenEndpointAuthMethod:  "client_secret_basic",
		RequireBackchannelLogout: true,
		MaximumHTTPResponseBytes: 64 << 10,
	}
}

func TestProviderPreflightAcceptsStrictDiscoveryWithoutReadingSecret(t *testing.T) {
	fixture := newProviderPreflightFixture(t, "ok")
	report, err := preflightOIDCProvider(context.Background(), fixture.config(), fixture.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || !report.DiscoveryOnly || !report.ClientSecretFileConfigured ||
		!report.IssuerHTTPS || !report.EndpointHostAllowlist || !report.EndpointAddressPolicy || !report.AuthorizationEndpoint ||
		!report.TokenEndpoint || !report.UserInfoEndpoint || !report.JWKSEndpoint ||
		!report.EndSessionEndpoint || !report.AuthorizationCodeFlow || !report.PKCES256 ||
		!report.BackchannelLogoutSupported || !report.BackchannelSessionSupported ||
		!report.BoundedResponses || !report.HTTPRedirectsRejected ||
		len(report.AllowedSigningAlgorithms) != 1 || report.AllowedSigningAlgorithms[0] != "RS256" {
		t.Fatalf("incomplete preflight report: %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), fixture.server.URL) || strings.Contains(string(encoded), preflightLeakMarker) {
		t.Fatalf("preflight report exposed provider URL or secret-file marker: %s", encoded)
	}
}

func TestProviderPreflightRejectsUnsafeAndIncompleteDiscovery(t *testing.T) {
	tests := []string{
		"ssrf_endpoint",
		"redirect",
		"issuer_mismatch",
		"missing_authorization",
		"missing_token",
		"missing_end_session",
		"missing_userinfo",
		"missing_jwks",
		"missing_backchannel",
		"missing_backchannel_session",
		"algorithm_downgrade",
		"duplicate_json_key",
		"oversized_response",
		"invalid_jwk",
		"weak_jwk",
	}
	for _, mode := range tests {
		t.Run(mode, func(t *testing.T) {
			fixture := newProviderPreflightFixture(t, mode)
			_, err := preflightOIDCProvider(context.Background(), fixture.config(), fixture.server.Client())
			if err == nil {
				t.Fatal("unsafe or incomplete provider metadata accepted")
			}
			if strings.Contains(err.Error(), fixture.server.URL) || strings.Contains(err.Error(), preflightLeakMarker) {
				t.Fatalf("preflight error exposed provider URL or token marker: %v", err)
			}
			if mode == "redirect" && fixture.redirectHits.Load() != 0 {
				t.Fatal("OIDC discovery redirect was followed")
			}
		})
	}
}

func TestProviderPreflightRejectsNonHTTPSIssuer(t *testing.T) {
	for _, issuer := range []string{
		"http://identity.example/realms/invoice",
		"https://identity.example/realms/invoice?",
		"https://identity.example/realms/invoice#",
	} {
		config := OIDCConfig{
			IssuerURL: issuer, AllowedEndpointHosts: []string{"identity.example"},
			AllowedSigningAlgs: []string{"RS256"}, RequireBackchannelLogout: true,
		}
		if _, err := preflightOIDCProvider(context.Background(), config, nil); err == nil {
			t.Fatalf("non-exact HTTPS issuer accepted: %q", issuer)
		}
	}
}

func TestOIDCEndpointIPAddressPolicy(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if address := netip.MustParseAddr(raw); !isPublicOIDCEndpointIP(address) {
			t.Fatalf("public address rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"192.0.2.1", "192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"::1", "::192.0.2.1", "64:ff9b::7f00:1", "100::1", "2001:db8::1",
		"5f00::1", "fc00::1", "fec0::1", "fe80::1",
	} {
		if address := netip.MustParseAddr(raw); isPublicOIDCEndpointIP(address) {
			t.Fatalf("non-public address accepted: %s", raw)
		}
	}
	if _, err := protectedOIDCDialContext([]string{"10.0.0.8"}); err != nil {
		t.Fatalf("exact canonical private IdP exception rejected: %v", err)
	}
	for _, raw := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.0/8", "010.0.0.8", "8.8.8.8"} {
		if _, err := protectedOIDCDialContext([]string{raw}); err == nil {
			t.Fatalf("unsafe private endpoint exception accepted: %q", raw)
		}
	}
}

func TestProtectedOIDCDialRejectsMixedDNSAndUsesNumericPrivateException(t *testing.T) {
	mixedDialer := &recordingOIDCDialer{}
	mixed, err := protectedOIDCDialContextWithDependencies(nil, staticOIDCResolver{addresses: []netip.Addr{
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

	privateDialer := &recordingOIDCDialer{}
	private, err := protectedOIDCDialContextWithDependencies([]string{"10.0.0.8"}, staticOIDCResolver{addresses: []netip.Addr{
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

func TestProtectedOIDCDialUsesConcurrentAddressFallback(t *testing.T) {
	called := make(chan string, 2)
	dialer := &recordingOIDCDialer{
		called:   called,
		failures: map[string]error{"8.8.8.8:443": errors.New("fixture blackhole")},
	}
	dial, err := protectedOIDCDialContextWithDependencies(nil, staticOIDCResolver{addresses: []netip.Addr{
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

func TestProtectedOIDCDialCancelsAndClosesLateLoser(t *testing.T) {
	lateConnection, latePeer := net.Pipe()
	defer latePeer.Close()
	lateLoser := &observableOIDCConn{Conn: lateConnection, closed: make(chan struct{})}
	dialer := &cancelAwareOIDCDialer{
		loserStarted: make(chan struct{}), loserCanceled: make(chan struct{}), lateLoser: lateLoser,
	}
	dial, err := protectedOIDCDialContextWithDependencies(nil, staticOIDCResolver{addresses: []netip.Addr{
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

func TestProtectedOIDCDialPreservesTLSHostname(t *testing.T) {
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
	dialer := &recordingOIDCDialer{actualAddress: server.Listener.Addr().String()}
	dial, err := protectedOIDCDialContextWithDependencies([]string{"10.0.0.8"}, staticOIDCResolver{addresses: []netip.Addr{
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

func TestOIDCJWKAlgorithmAndStrengthPolicy(t *testing.T) {
	strong := jose.JSONWebKey{Key: &preflightTestRSAKey.PublicKey}
	if !publicJWKSupportsAlgorithm(strong, "RS256") || publicJWKSupportsAlgorithm(strong, "ES256") {
		t.Fatal("RSA key type was not bound exclusively to an RSA signing algorithm")
	}
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if publicJWKSupportsAlgorithm(jose.JSONWebKey{Key: &weakKey.PublicKey}, "RS256") {
		t.Fatal("sub-2048-bit RSA signing key accepted")
	}
	ecdsaCases := []struct {
		name      string
		curve     elliptic.Curve
		algorithm string
	}{
		{name: "P-256", curve: elliptic.P256(), algorithm: "ES256"},
		{name: "P-384", curve: elliptic.P384(), algorithm: "ES384"},
		{name: "P-521", curve: elliptic.P521(), algorithm: "ES512"},
	}
	for _, test := range ecdsaCases {
		t.Run(test.name, func(t *testing.T) {
			key, generateErr := ecdsa.GenerateKey(test.curve, rand.Reader)
			if generateErr != nil {
				t.Fatal(generateErr)
			}
			jwk := jose.JSONWebKey{Key: &key.PublicKey}
			for _, algorithm := range []string{"ES256", "ES384", "ES512", "RS256", "EdDSA"} {
				if got, want := publicJWKSupportsAlgorithm(jwk, algorithm), algorithm == test.algorithm; got != want {
					t.Fatalf("curve=%s algorithm=%s got=%v want=%v", test.name, algorithm, got, want)
				}
			}
		})
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edwards := jose.JSONWebKey{Key: public}
	if !publicJWKSupportsAlgorithm(edwards, "EdDSA") || publicJWKSupportsAlgorithm(edwards, "ES256") || publicJWKSupportsAlgorithm(edwards, "RS256") {
		t.Fatal("Ed25519 key was not bound exclusively to EdDSA")
	}
}

func TestProviderPreflightRejectsSensitiveIssuerQueryWithoutEcho(t *testing.T) {
	fixture := newProviderPreflightFixture(t, "ok")
	config := fixture.config()
	config.IssuerURL += "?access_token=" + preflightLeakMarker
	_, err := preflightOIDCProvider(context.Background(), config, fixture.server.Client())
	if err == nil {
		t.Fatal("issuer query accepted")
	}
	if strings.Contains(err.Error(), preflightLeakMarker) || strings.Contains(err.Error(), config.IssuerURL) {
		t.Fatalf("issuer query leaked in error: %v", err)
	}
}

func TestProviderPreflightCannotDisableRequiredBackchannelLogout(t *testing.T) {
	fixture := newProviderPreflightFixture(t, "ok")
	config := fixture.config()
	config.RequireBackchannelLogout = false
	if _, err := preflightOIDCProvider(context.Background(), config, fixture.server.Client()); err == nil {
		t.Fatal("preflight accepted disabled back-channel logout requirement")
	}
}
