package agent

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCert(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	fingerprint, err := generateCert(dir)
	if nil != err {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "node.crt"), filepath.Join(dir, "node.key"))
	if nil != err {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if nil != err {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "dagent"}); nil != err {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "docker-node"}); nil != err {
		t.Fatal(err)
	}
	hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	if base64.StdEncoding.EncodeToString(hash[:]) != fingerprint {
		t.Fatal("fingerprint does not match certificate public key")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.example.internal"}); nil == err {
		t.Fatal("accepted wrong hostname")
	}
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "docker-node"}); nil == err {
		t.Fatal("trusted certificate without explicit trust")
	}
	if now := time.Now(); cert.NotAfter.Before(now.AddDate(0, 11, 0)) || cert.NotAfter.After(now.AddDate(1, 0, 1)) {
		t.Fatal("unexpected certificate lifetime")
	}
	info, err := os.Stat(filepath.Join(dir, "node.key"))
	if nil != err {
		t.Fatal(err)
	}
	if 0600 != info.Mode().Perm() {
		t.Fatalf("private key permissions: %o", info.Mode().Perm())
	}
}

func TestCertExists(t *testing.T) {
	for _, existing := range []string{"node.key", "node.crt"} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, existing)
			write(t, path, "keep existing file")
			if _, err := generateCert(dir); nil == err {
				t.Fatal("overwrote existing file")
			}
			data, err := os.ReadFile(path)
			if nil != err || "keep existing file" != string(data) {
				t.Fatalf("existing file changed: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if nil != err || 1 != len(entries) {
				t.Fatalf("left partial certificate pair: %v", err)
			}
		})
	}
}
