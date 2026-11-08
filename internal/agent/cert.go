package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func generateCert(dir string) (string, error) {
	now := time.Now()
	cert := x509.Certificate{
		Subject:               pkix.Name{CommonName: "dagent"},
		DNSNames:              []string{"dagent", "docker-node"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if nil != err {
		return "", err
	}
	cert.SerialNumber = serial
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if nil != err {
		return "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if nil != err {
		return "", err
	}
	fingerprint := sha256.Sum256(publicDER)
	der, err := x509.CreateCertificate(rand.Reader, &cert, &cert, &key.PublicKey, key)
	if nil != err {
		return "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if nil != err {
		return "", err
	}
	if err := os.MkdirAll(dir, 0750); nil != err {
		return "", err
	}
	keyPath := filepath.Join(dir, "node.key")
	if err := writeNewFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); nil != err {
		return "", err
	}
	if err := writeNewFile(filepath.Join(dir, "node.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); nil != err {
		_ = os.Remove(keyPath)
		return "", err
	}
	return base64.StdEncoding.EncodeToString(fingerprint[:]), nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if nil != err {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if nil == err {
		err = closeErr
	}
	if nil != err {
		_ = os.Remove(path)
	}
	return err
}
