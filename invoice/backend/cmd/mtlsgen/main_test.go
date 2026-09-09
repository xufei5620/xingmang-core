package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateMutualTLSBundle(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pki")
	if err := generate(out, "invoice-ingest.internal", []string{"sub2api-agent", "newapi-agent"}, 365, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ca := readCertificate(t, filepath.Join(out, "source_agent_ca.pem"))
	server := readCertificate(t, filepath.Join(out, "ingest_server_cert.pem"))
	client := readCertificate(t, filepath.Join(out, "sub2api-agent_cert.pem"))
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, DNSName: "invoice-ingest.internal", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if err := generate(out, "invoice-ingest.internal", []string{"other"}, 365, time.Now().UTC()); err == nil {
		t.Fatal("generator overwrote an existing PKI directory")
	}
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		t.Fatal("certificate PEM missing")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
