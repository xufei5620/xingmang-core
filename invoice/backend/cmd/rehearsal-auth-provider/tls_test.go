package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSGenerationTrustsOnlyNamedFixtureHostsAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := generateTLS(dir, []string{"sub.example.invalid", "new.example.invalid"}); err != nil {
		t.Fatal(err)
	}
	caRaw, err := os.ReadFile(filepath.Join(dir, "public-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := pem.Decode(caRaw)
	if caPEM == nil {
		t.Fatal("missing CA PEM")
	}
	ca, err := x509.ParseCertificate(caPEM.Bytes)
	if err != nil || !ca.IsCA {
		t.Fatal("invalid generated CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "provider-cert.pem"), filepath.Join(dir, "provider-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"sub.example.invalid", "new.example.invalid"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
			t.Fatal("generated fixture chain or SAN invalid")
		}
	}
	// Exercise actual TLS, using a test-only transport with the generated CA
	// and exact SAN. Production endpoint dial/SSRF policy is unchanged.
	h := newProvider(testFixtures())
	h.owner = testOwner
	server := httptest.NewUnstartedServer(h)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "sub.example.invalid", MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal("fixture HTTPS health probe failed")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("fixture HTTPS unhealthy")
	}
	if leaf.VerifyHostname("other.example.invalid") == nil {
		t.Fatal("unlisted hostname accepted")
	}
	if leaf.NotAfter.Sub(time.Now()) > 7*time.Hour {
		t.Fatal("fixture certificate outlives rehearsal")
	}
	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); !os.IsNotExist(err) {
		t.Fatal("CA private key persisted")
	}
	if err := generateTLS(dir, []string{"sub.example.invalid"}); err == nil {
		t.Fatal("existing fixture TLS overwritten")
	}
	after, err := os.ReadFile(filepath.Join(dir, "public-ca.pem"))
	if err != nil || string(after) != string(caRaw) {
		t.Fatal("failed generation replaced existing CA")
	}
}

func TestTLSGenerationRejectsUnsafeNamesAndPaths(t *testing.T) {
	for _, hosts := range [][]string{{}, {""}, {"*.example.invalid"}, {"https://sub.example.invalid"}, {"127.0.0.1"}, {"sub.example.invalid", "sub.example.invalid"}} {
		if err := generateTLS(t.TempDir(), hosts); err == nil {
			t.Fatal("unsafe TLS name accepted")
		}
	}
	if err := generateTLS("relative", []string{"sub.example.invalid"}); err == nil {
		t.Fatal("relative private output accepted")
	}
}
