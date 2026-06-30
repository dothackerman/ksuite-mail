package imapadapter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

func TestSourceRejectsPlaintextLiveAccount(t *testing.T) {
	acct := testAccount(t)
	tlsEnabled := false
	acct.TLS = &tlsEnabled
	src := New(writeTestSecrets(t))

	_, err := src.Capabilities(context.Background(), acct)
	if !errors.Is(err, mail.ErrSourceUnavailable) {
		t.Fatalf("Capabilities error = %v, want ErrSourceUnavailable", err)
	}
}

func TestSourceTimesOutWhenServerHangsAfterConnect(t *testing.T) {
	ln, roots := newHangingTLSServer(t)
	defer func() { _ = ln.Close() }()

	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("parse port %q: %v", portText, err)
	}
	acct := testAccount(t)
	acct.Host = host
	acct.Port = port

	src := &Source{
		secretsPath: writeTestSecrets(t),
		timeout:     50 * time.Millisecond,
		tlsConfig:   &tls.Config{RootCAs: roots},
	}

	start := time.Now()
	_, err = src.Capabilities(context.Background(), acct)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Capabilities error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want bounded under 1s", elapsed)
	}
}

func testAccount(t *testing.T) config.Account {
	t.Helper()
	tlsEnabled := true
	return config.Account{
		ID:       "rs_info",
		Email:    "info@example.com",
		Host:     "127.0.0.1",
		Port:     993,
		TLS:      &tlsEnabled,
		Username: "info@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/rs_info/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}
}

func writeTestSecrets(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"pw"}}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	return path
}

func newHangingTLSServer(t *testing.T) (net.Listener, *x509.CertPool) {
	t.Helper()
	cert, roots := testCertificate(t)
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("TCP listener denied by environment: %v", err)
		}
		t.Fatalf("listen tcp: %v", err)
	}
	ln := tls.NewListener(base, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("* OK ready\r\n"))
		time.Sleep(2 * time.Second)
	}()
	return ln, roots
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse key pair: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("append root cert")
	}
	return cert, roots
}
