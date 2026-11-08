package agent

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/arabcoders/dcompose/internal/configutil"
)

func connectionInfo(c config, path string) (token, fingerprint string, err error) {
	token, err = resolveToken(c.Auth.Token, path)
	if nil != err {
		return "", "", err
	}
	data, err := os.ReadFile(configutil.Path(path, c.TLS.CertFile))
	if nil != err {
		return "", "", fmt.Errorf("read TLS certificate: %w", err)
	}
	block, _ := pem.Decode(data)
	if nil == block {
		return "", "", fmt.Errorf("TLS certificate must be PEM encoded")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if nil != err {
		return "", "", fmt.Errorf("read TLS certificate: %w", err)
	}
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return token, base64.StdEncoding.EncodeToString(digest[:]), nil
}
