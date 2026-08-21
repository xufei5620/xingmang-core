package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func main() {
	out := flag.String("out-dir", "", "new directory for the private ingestion PKI bundle")
	serverName := flag.String("server-name", "invoice-ingest.internal", "ingestion TLS DNS name")
	clients := flag.String("clients", "sub2api-agent,newapi-agent", "comma-separated client certificate names")
	validDays := flag.Int("valid-days", 365, "server/client certificate validity (30..825 days)")
	flag.Parse()
	if err := generate(*out, *serverName, strings.Split(*clients, ","), *validDays, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "mtlsgen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("created private ingestion PKI in %s; keep source_agent_ca_key.pem offline\n", *out)
}

func generate(output, serverName string, clientNames []string, validDays int, now time.Time) error {
	if strings.TrimSpace(output) == "" || !filepath.IsAbs(output) {
		return errors.New("--out-dir must be an absolute new directory")
	}
	if strings.TrimSpace(serverName) == "" || strings.ContainsAny(serverName, "/\\\r\n\x00") {
		return errors.New("invalid server name")
	}
	if validDays < 30 || validDays > 825 {
		return errors.New("valid-days must be between 30 and 825")
	}
	cleanClients := make([]string, 0, len(clientNames))
	seen := map[string]struct{}{}
	for _, name := range clientNames {
		name = strings.TrimSpace(name)
		if !safeName.MatchString(name) {
			return fmt.Errorf("invalid client name %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate client name %q", name)
		}
		seen[name] = struct{}{}
		cleanClients = append(cleanClients, name)
	}
	if len(cleanClients) == 0 || len(cleanClients) > 16 {
		return errors.New("between 1 and 16 client names are required")
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("create output directory without overwrite: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			for _, name := range append([]string{
				"source_agent_ca.pem", "source_agent_ca_key.pem", "ingest_server_cert.pem", "ingest_server_key.pem",
			}, clientFileNames(cleanClients...)...) {
				_ = os.Remove(filepath.Join(output, name))
			}
			_ = os.Remove(output)
		}
	}()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "SoloV Invoice Source Agent CA"},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err = writeCertificate(filepath.Join(output, "source_agent_ca.pem"), caDER, 0o644); err != nil {
		return err
	}
	if err = writePrivateKey(filepath.Join(output, "source_agent_ca_key.pem"), caKey); err != nil {
		return err
	}
	if err = issue(output, "ingest_server", serverName, true, caTemplate, caKey, validDays, now); err != nil {
		return err
	}
	for _, client := range cleanClients {
		if err = issue(output, client, client, false, caTemplate, caKey, validDays, now); err != nil {
			return err
		}
	}
	ok = true
	return nil
}

func issue(output, filePrefix, commonName string, server bool, ca *x509.Certificate, caKey *ecdsa.PrivateKey, validDays int, now time.Time) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: commonName},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(time.Duration(validDays) * 24 * time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if server {
		template.DNSNames = []string{commonName}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err = writeCertificate(filepath.Join(output, filePrefix+"_cert.pem"), der, 0o644); err != nil {
		return err
	}
	return writePrivateKey(filepath.Join(output, filePrefix+"_key.pem"), key)
}

func writeCertificate(path string, der []byte, mode os.FileMode) error {
	return writePEM(path, &pem.Block{Type: "CERTIFICATE", Bytes: der}, mode)
}

func writePrivateKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, &pem.Block{Type: "PRIVATE KEY", Bytes: der}, 0o600)
}

func writePEM(path string, block *pem.Block, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err = pem.Encode(file, block); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func randomSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil || serial.Sign() == 0 {
		return big.NewInt(time.Now().UnixNano())
	}
	return serial
}

func clientFileNames(names ...string) []string {
	files := make([]string, 0, len(names)*2)
	for _, name := range names {
		files = append(files, name+"_cert.pem", name+"_key.pem")
	}
	return files
}
