package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ensureTLS returns cert/key paths for the configured TLS mode, generating a
// self-signed ECDSA P-256 certificate on first run when needed.
func (s *Server) ensureTLS() (string, string, error) {
	switch s.cfg.TLS.Mode {
	case "custom":
		if s.cfg.TLS.CertFile == "" || s.cfg.TLS.KeyFile == "" {
			return "", "", errors.New("custom TLS mode needs cert_file and key_file")
		}
		return s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile, nil
	case "self-signed":
		dir := filepath.Join(s.dataDir, "certs")
		certPath := filepath.Join(dir, "cert.pem")
		keyPath := filepath.Join(dir, "key.pem")
		if fileExists(certPath) && fileExists(keyPath) {
			return certPath, keyPath, nil
		}
		if err := generateSelfSigned(certPath, keyPath, s.cfg.TLS.ExtraHosts); err != nil {
			return "", "", err
		}
		return certPath, keyPath, nil
	}
	return "", "", errors.New("ensureTLS called with mode " + s.cfg.TLS.Mode)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func generateSelfSigned(certPath, keyPath string, extraHosts []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	for _, h := range extraHosts {
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else if h != "" {
			dnsNames = append(dnsNames, h)
		}
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "BlockPanel", Organization: []string{"BlockPanel self-signed"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certOut, err := os.OpenFile(certPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyOut, err := os.OpenFile(keyPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	return pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
