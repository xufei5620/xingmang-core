package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

var dnsNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)

// generateTLS generates only new rehearsal keys. The signing CA private key
// remains in memory and is never written or used outside this invocation.
func generateTLS(directory string, hosts []string) error {
	info, err := os.Lstat(directory)
	if !filepath.IsAbs(directory) || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("fixture TLS output must be an existing private absolute directory")
	}
	seen := map[string]bool{}
	if len(hosts) < 1 || len(hosts) > 4 {
		return errors.New("fixture TLS needs explicit DNS names")
	}
	for _, host := range hosts {
		_, ipErr := netip.ParseAddr(host)
		if len(host) > 253 || !dnsNamePattern.MatchString(host) || ipErr == nil || seen[host] {
			return errors.New("fixture TLS DNS names are invalid or duplicated")
		}
		seen[host] = true
	}
	files := []string{"public-ca.pem", "provider-cert.pem", "provider-key.pem"}
	for _, name := range files {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			return errors.New("fixture TLS output already exists")
		}
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("fixture CA key generation failed")
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("fixture TLS key generation failed")
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	caSerial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return errors.New("fixture CA serial generation failed")
	}
	leafSerial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return errors.New("fixture TLS serial generation failed")
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: caSerial, Subject: pkix.Name{CommonName: "Ephemeral invoice rehearsal CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(6 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return errors.New("fixture CA creation failed")
	}
	leaf := &x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: hosts[0]}, DNSNames: hosts, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(6 * time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return errors.New("fixture TLS certificate creation failed")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return errors.New("fixture TLS private key encoding failed")
	}
	contents := [][]byte{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}
	for i, name := range files {
		// Never replace an earlier successful generation, including a concurrent
		// creator. A partial generation remains visible and fails closed.
		f, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("fixture TLS output could not be created exclusively")
		}
		_, writeErr := f.Write(contents[i])
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return errors.New("fixture TLS output could not be written")
		}
	}
	return nil
}
