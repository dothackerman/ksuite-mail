package imapadapter

import (
	"bufio"
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

func TestSourceDoesNotWaitForLogoutDuringCleanup(t *testing.T) {
	ln, roots := newLogoutStallingTLSServer(t)
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
		timeout:     750 * time.Millisecond,
		tlsConfig:   &tls.Config{RootCAs: roots},
	}

	start := time.Now()
	caps, err := src.Capabilities(context.Background(), acct)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("Capabilities took %s, cleanup should not wait for LOGOUT", elapsed)
	}
	if got, want := fmt.Sprint(caps), "[IMAP4REV1]"; got != want {
		t.Fatalf("capabilities = %s, want %s", got, want)
	}
}

func TestSearchAllowedUsesHeaderSearchForAddressHeaders(t *testing.T) {
	ln, roots, commands := newHeaderSearchTLSServer(t)
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
		timeout:     time.Second,
		tlsConfig:   &tls.Config{RootCAs: roots},
	}

	uids, err := src.SearchAllowed(context.Background(), acct, "INBOX", "From", "regenerativ.ch", mail.UIDRange{})
	if err != nil {
		t.Fatalf("SearchAllowed: %v", err)
	}
	if got, want := fmt.Sprint(uids), "[7 42]"; got != want {
		t.Fatalf("UIDs = %s, want %s", got, want)
	}

	var gotCommands []string
	for cmd := range commands {
		gotCommands = append(gotCommands, cmd)
	}
	wire := strings.Join(gotCommands, "\n")
	if !strings.Contains(wire, `UID SEARCH HEADER "From" "regenerativ.ch"`) {
		t.Fatalf("wire commands missing HEADER search:\n%s", wire)
	}
	if strings.Contains(wire, "UID SEARCH FROM") {
		t.Fatalf("wire commands used shorthand FROM search:\n%s", wire)
	}
}

func TestSelectFolderUsesReadOnlySelectionAndReturnsUIDState(t *testing.T) {
	ln, roots, commands := newSelectFolderTLSServer(t)
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
		timeout:     time.Second,
		tlsConfig:   &tls.Config{RootCAs: roots},
	}

	state, err := src.SelectFolder(context.Background(), acct, "INBOX")
	if err != nil {
		t.Fatalf("SelectFolder: %v", err)
	}
	if state.UIDVALIDITY != 777 || state.UIDNEXT != 21 || state.HighestModSeq != 42 {
		t.Fatalf("state = %+v, want UIDVALIDITY=777 UIDNEXT=21 HMS=42", state)
	}
	if !state.ReadOnly {
		t.Fatalf("read-only = false, want true")
	}
	if state.SelectionMode != "examine" {
		t.Fatalf("selection mode = %q, want examine", state.SelectionMode)
	}

	var gotCommands []string
	for cmd := range commands {
		gotCommands = append(gotCommands, cmd)
	}
	wire := strings.Join(gotCommands, "\n")
	if !strings.Contains(wire, "EXAMINE INBOX") {
		t.Fatalf("wire commands missing EXAMINE:\n%s", wire)
	}
	if strings.Contains(wire, "SELECT INBOX") {
		t.Fatalf("wire commands used writable SELECT:\n%s", wire)
	}
}

func TestFetchBodyPreviewAndSeenStateFetchesPostPeekFlagsSeparately(t *testing.T) {
	ln, roots, commands := newBodyPeekTLSServer(t)
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
		timeout:     time.Second,
		tlsConfig:   &tls.Config{RootCAs: roots},
	}

	preview, state, err := src.FetchBodyPreviewAndSeenState(context.Background(), acct, "INBOX", 10, 1)
	if err != nil {
		t.Fatalf("FetchBodyPreviewAndSeenState: %v", err)
	}
	if preview != "x" {
		t.Fatalf("preview = %q, want x", preview)
	}
	if !state.Observed || state.SeenBefore || !state.SeenAfter {
		t.Fatalf("state = %+v, want observed unseen before seen after", state)
	}

	var gotCommands []string
	for cmd := range commands {
		gotCommands = append(gotCommands, cmd)
	}
	var uidFetches []string
	for _, cmd := range gotCommands {
		if strings.Contains(cmd, "UID FETCH") {
			uidFetches = append(uidFetches, cmd)
		}
	}
	if len(uidFetches) != 3 {
		t.Fatalf("UID FETCH commands = %#v, want before flags, body peek, after flags", uidFetches)
	}
	if !strings.Contains(uidFetches[0], "FLAGS") || strings.Contains(uidFetches[0], "BODY") {
		t.Fatalf("before flags command = %q, want FLAGS only", uidFetches[0])
	}
	if !strings.Contains(uidFetches[1], "BODY.PEEK") || strings.Contains(uidFetches[1], "FLAGS") {
		t.Fatalf("body command = %q, want BODY.PEEK without FLAGS", uidFetches[1])
	}
	if !strings.Contains(uidFetches[2], "FLAGS") || strings.Contains(uidFetches[2], "BODY") {
		t.Fatalf("after flags command = %q, want FLAGS only", uidFetches[2])
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

func newSelectFolderTLSServer(t *testing.T) (net.Listener, *x509.CertPool, <-chan string) {
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
	commands := make(chan string, 8)
	go func() {
		defer close(commands)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		writeIMAPLine(w, "* OK ready")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			commands <- line
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			tag := fields[0]
			switch strings.ToUpper(fields[1]) {
			case "LOGIN":
				writeIMAPLine(w, tag+" OK LOGIN completed")
			case "CAPABILITY":
				writeIMAPLine(w, "* CAPABILITY IMAP4rev1 CONDSTORE")
				writeIMAPLine(w, tag+" OK CAPABILITY completed")
			case "EXAMINE":
				writeIMAPLine(w, "* FLAGS (\\Seen)")
				writeIMAPLine(w, "* 2 EXISTS")
				writeIMAPLine(w, "* OK [UIDVALIDITY 777] UIDs valid")
				writeIMAPLine(w, "* OK [UIDNEXT 21] next UID")
				writeIMAPLine(w, "* OK [HIGHESTMODSEQ 42] modseq")
				writeIMAPLine(w, tag+" OK [READ-ONLY] EXAMINE completed")
			case "LOGOUT":
				writeIMAPLine(w, "* BYE closing")
				writeIMAPLine(w, tag+" OK LOGOUT completed")
				return
			default:
				writeIMAPLine(w, tag+" BAD unsupported")
			}
		}
	}()
	return ln, roots, commands
}

func newLogoutStallingTLSServer(t *testing.T) (net.Listener, *x509.CertPool) {
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
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		writeIMAPLine(w, "* OK ready")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			fields := strings.Fields(strings.TrimRight(line, "\r\n"))
			if len(fields) < 2 {
				continue
			}
			tag := fields[0]
			switch strings.ToUpper(fields[1]) {
			case "LOGIN":
				writeIMAPLine(w, tag+" OK LOGIN completed")
			case "CAPABILITY":
				writeIMAPLine(w, "* CAPABILITY IMAP4rev1")
				writeIMAPLine(w, tag+" OK CAPABILITY completed")
			case "LOGOUT":
				time.Sleep(2 * time.Second)
				return
			default:
				writeIMAPLine(w, tag+" BAD unsupported")
			}
		}
	}()
	return ln, roots
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

func newHeaderSearchTLSServer(t *testing.T) (net.Listener, *x509.CertPool, <-chan string) {
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
	commands := make(chan string, 8)
	go func() {
		defer close(commands)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		writeIMAPLine(w, "* OK ready")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			commands <- line
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			tag := fields[0]
			switch strings.ToUpper(fields[1]) {
			case "LOGIN":
				writeIMAPLine(w, tag+" OK LOGIN completed")
			case "EXAMINE":
				writeIMAPLine(w, "* FLAGS (\\Seen)")
				writeIMAPLine(w, "* 1 EXISTS")
				writeIMAPLine(w, tag+" OK [READ-ONLY] EXAMINE completed")
			case "UID":
				writeIMAPLine(w, "* SEARCH 42 7")
				writeIMAPLine(w, tag+" OK SEARCH completed")
			case "LOGOUT":
				writeIMAPLine(w, "* BYE closing")
				writeIMAPLine(w, tag+" OK LOGOUT completed")
				return
			default:
				writeIMAPLine(w, tag+" BAD unsupported")
			}
		}
	}()
	return ln, roots, commands
}

func newBodyPeekTLSServer(t *testing.T) (net.Listener, *x509.CertPool, <-chan string) {
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
	commands := make(chan string, 12)
	go func() {
		defer close(commands)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		writeIMAPLine(w, "* OK ready")
		fetchCount := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			commands <- line
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			tag := fields[0]
			switch strings.ToUpper(fields[1]) {
			case "LOGIN":
				writeIMAPLine(w, tag+" OK LOGIN completed")
			case "EXAMINE":
				writeIMAPLine(w, "* FLAGS (\\Seen)")
				writeIMAPLine(w, "* 1 EXISTS")
				writeIMAPLine(w, tag+" OK [READ-ONLY] EXAMINE completed")
			case "UID":
				fetchCount++
				switch fetchCount {
				case 1:
					writeIMAPLine(w, "* 1 FETCH (UID 10 FLAGS ())")
				case 2:
					_, _ = w.WriteString("* 1 FETCH (UID 10 BODY[TEXT]<0> {1}\r\nx)\r\n")
					_ = w.Flush()
				case 3:
					writeIMAPLine(w, "* 1 FETCH (UID 10 FLAGS (\\Seen))")
				default:
					writeIMAPLine(w, tag+" BAD unexpected fetch")
					continue
				}
				writeIMAPLine(w, tag+" OK FETCH completed")
			case "LOGOUT":
				writeIMAPLine(w, "* BYE closing")
				writeIMAPLine(w, tag+" OK LOGOUT completed")
				return
			default:
				writeIMAPLine(w, tag+" BAD unsupported")
			}
		}
	}()
	return ln, roots, commands
}

func writeIMAPLine(w *bufio.Writer, line string) {
	_, _ = w.WriteString(line + "\r\n")
	_ = w.Flush()
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
