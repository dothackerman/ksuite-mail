//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const e2eSecret = "TOPSECRET-e2e-do-not-leak"

const e2eConfigTemplate = `[mail]
default_limit = 50

[[mail.accounts]]
id = "rs_info"
email = "info@example.com"
host = "%s"
port = %d
tls = %t
username = "info@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
policy = "full"
folders = ["INBOX"]
`

func e2eConfig(host string, port int, tls bool) string {
	return fmt.Sprintf(e2eConfigTemplate, host, port, tls)
}

func build(t *testing.T, root, pkg, out string) string {
	t.Helper()
	bin := filepath.Join(out, filepath.Base(pkg))
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = root
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, o)
	}
	return bin
}

type daemonRun struct {
	cmd    *exec.Cmd
	done   chan error
	stderr *bytes.Buffer
}

func startDaemon(t *testing.T, cmd *exec.Cmd) *daemonRun {
	t.Helper()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
			return
		case <-time.After(1 * time.Second):
			_ = cmd.Process.Kill()
		}
		<-done
	})
	return &daemonRun{cmd: cmd, done: done, stderr: &stderr}
}

func waitForSocket(t *testing.T, sock string, daemon *daemonRun) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return
		}
		if !daemonIsRunning(daemon) {
			if isSocketPermissionDenied(daemon.stderr.String(), "") {
				t.Skipf("daemon startup denied by environment")
			}
			_ = daemonWaitForErr(daemon)
			t.Fatalf("daemon exited before socket became reachable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !daemonIsRunning(daemon) {
		if isSocketPermissionDenied(daemon.stderr.String(), "") {
			t.Skipf("daemon startup denied by environment")
		}
		_ = daemonWaitForErr(daemon)
		t.Fatalf("daemon exited before socket became reachable")
	}
	t.Fatalf("daemon socket %s never became reachable", sock)
}

func daemonIsRunning(daemon *daemonRun) bool {
	if daemon == nil || daemon.cmd == nil || daemon.cmd.Process == nil {
		return false
	}
	return daemon.cmd.Process.Signal(syscall.Signal(0)) == nil
}

func daemonWaitForErr(daemon *daemonRun) error {
	if daemon == nil {
		return nil
	}
	select {
	case err := <-daemon.done:
		return err
	default:
		return nil
	}
}

func TestDaemonDoneRemainsReadableAfterWaitForErr(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 7")
	daemon := startDaemon(t, cmd)
	deadline := time.Now().Add(2 * time.Second)
	var waitErr error
	for time.Now().Before(deadline) {
		waitErr = daemonWaitForErr(daemon)
		if waitErr != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waitErr == nil {
		t.Fatalf("daemon did not exit before deadline")
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("daemonWaitForErr returned %T, want *exec.ExitError", waitErr)
	}
	select {
	case <-daemon.done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("daemon done channel blocked after daemonWaitForErr consumed exit")
	}
}

func isSocketPermissionDenied(stderrText, errText string) bool {
	if strings.Contains(stderrText, "operation not permitted") {
		return true
	}
	if strings.Contains(errText, "operation not permitted") {
		return true
	}
	return false
}

// exitCode extracts the process exit code from an *exec.ExitError, or fails.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !asExitError(err, &ee) {
		t.Fatalf("expected exit error, got %v", err)
	}
	return ee.ExitCode()
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // direct type is sufficient here
		*target = ee
		return true
	}
	return false
}

// TestDoctorEndToEnd builds both binaries, starts the daemon on a temp socket,
// and runs `ksuite-mail doctor` against a healthy temp deployment. It asserts a
// healthy JSON report, a zero exit code, and that no secret value leaks.
func TestDoctorEndToEnd(t *testing.T) {
	root := moduleRoot(t)
	work := t.TempDir()

	cli := build(t, root, "./cmd/ksuite-mail", work)
	daemonBin := build(t, root, "./cmd/ksuite-maild", work)

	cfgPath := filepath.Join(work, "config.toml")
	secPath := filepath.Join(work, "secrets.json")
	stateDir := filepath.Join(work, "state")
	sock := filepath.Join(work, "d.sock")
	if err := os.WriteFile(cfgPath, []byte(e2eConfig("imap.example.com", 993, true)), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(secPath,
		[]byte(`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"`+e2eSecret+`"}}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	daemonCmd := exec.Command(daemonBin,
		"--config", cfgPath, "--secrets", secPath, "--state-dir", stateDir, "--socket", sock)
	run := startDaemon(t, daemonCmd)
	waitForSocket(t, sock, run)

	out, err := exec.Command(cli, "doctor", "--socket", sock).Output()
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("doctor exit = %d, want 0\noutput: %s", code, out)
	}

	if strings.Contains(string(out), e2eSecret) {
		t.Fatalf("doctor output leaked the secret value:\n%s", out)
	}

	var env struct {
		Status string `json:"status"`
		Result struct {
			OK     bool `json:"ok"`
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("parse doctor JSON: %v\noutput: %s", err, out)
	}
	if env.Status != "ok" || !env.Result.OK {
		t.Fatalf("doctor report not healthy: %s", out)
	}
	if len(env.Result.Checks) == 0 {
		t.Fatalf("doctor report had no checks: %s", out)
	}
	if strings.Contains(string(out), "domain_header_search") || strings.Contains(string(out), "uid_behavior") {
		t.Fatalf("doctor output included provider probe checks:\n%s", out)
	}
}

// TestProbeIMAPEndToEnd verifies the public probe command reaches daemon-owned
// handling over the Unix socket and returns only a sanitized structured result.
func TestProbeIMAPEndToEnd(t *testing.T) {
	root := moduleRoot(t)
	work := t.TempDir()

	cli := build(t, root, "./cmd/ksuite-mail", work)
	daemonBin := build(t, root, "./cmd/ksuite-maild", work)

	cfgPath := filepath.Join(work, "config.toml")
	secPath := filepath.Join(work, "secrets.json")
	stateDir := filepath.Join(work, "state")
	sock := filepath.Join(work, "d.sock")
	imapHost, imapPort, imapCAPEM := startFakeIMAPServer(t)
	imapCAPath := filepath.Join(work, "fake-imap-ca.pem")
	if err := os.WriteFile(imapCAPath, imapCAPEM, 0o600); err != nil {
		t.Fatalf("write fake IMAP CA: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(e2eConfig(imapHost, imapPort, true)), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(secPath,
		[]byte(`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"`+e2eSecret+`"}}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	daemonCmd := exec.Command(daemonBin,
		"--config", cfgPath, "--secrets", secPath, "--state-dir", stateDir, "--socket", sock)
	daemonCmd.Env = append(os.Environ(), "SSL_CERT_FILE="+imapCAPath)
	run := startDaemon(t, daemonCmd)
	waitForSocket(t, sock, run)

	out, err := exec.Command(cli, "probe", "imap", "--socket", sock, "--account", "rs_info", "--json").Output()
	if code := exitCode(t, err); code != 0 {
		t.Fatalf("probe exit = %d, want 0\noutput: %s", code, out)
	}
	if strings.Contains(string(out), e2eSecret) || strings.Contains(string(out), "raw") {
		t.Fatalf("probe output leaked secret or raw-provider detail:\n%s", out)
	}

	var env struct {
		Status string `json:"status"`
		Result struct {
			Account string `json:"account"`
			Status  string `json:"status"`
			Checks  []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("parse probe JSON: %v\noutput: %s", err, out)
	}
	if env.Status != "ok" || env.Result.Account != "rs_info" || env.Result.Status != "inconclusive" {
		t.Fatalf("unexpected probe response: %s", out)
	}
	if len(env.Result.Checks) == 0 {
		t.Fatalf("probe response had no checks: %s", out)
	}
	for _, check := range env.Result.Checks {
		if check.ID == "" {
			t.Fatalf("probe check had no stable id: %s", out)
		}
		if check.Status == "pass" || check.Status == "fail" {
			t.Fatalf("probe check used legacy status vocabulary: %s", out)
		}
	}
}

func startFakeIMAPServer(t *testing.T) (string, int, []byte) {
	t.Helper()
	cert, caPEM := e2eTestCertificate(t)
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("TCP listener denied by environment: %v", err)
		}
		t.Fatalf("listen fake imap: %v", err)
	}
	ln := tls.NewListener(base, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeIMAP(conn)
		}
	}()
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split fake imap address: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("parse fake imap port: %v", err)
	}
	return host, port, caPEM
}

func e2eTestCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate fake IMAP key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create fake IMAP certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse fake IMAP certificate: %v", err)
	}
	return cert, certPEM
}

func serveFakeIMAP(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeIMAP(w, "* OK fake IMAP ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		tag := fields[0]
		cmd := ""
		if len(fields) > 1 {
			cmd = strings.ToUpper(fields[1])
		}
		switch cmd {
		case "LOGIN":
			writeIMAP(w, tag+" OK LOGIN completed")
		case "CAPABILITY":
			writeIMAP(w, "* CAPABILITY IMAP4rev1 UIDPLUS CONDSTORE")
			writeIMAP(w, tag+" OK CAPABILITY completed")
		case "LIST":
			writeIMAP(w, `* LIST () "/" "INBOX"`)
			writeIMAP(w, tag+" OK LIST completed")
		case "SELECT", "EXAMINE":
			writeIMAP(w, "* FLAGS (\\Seen)")
			writeIMAP(w, "* 1 EXISTS")
			writeIMAP(w, "* OK [UIDVALIDITY 123]")
			writeIMAP(w, "* OK [UIDNEXT 11]")
			writeIMAP(w, "* OK [HIGHESTMODSEQ 7]")
			writeIMAP(w, tag+" OK [READ-ONLY] SELECT completed")
		case "UID":
			writeIMAP(w, "* SEARCH 10")
			writeIMAP(w, tag+" OK SEARCH completed")
		case "LOGOUT":
			writeIMAP(w, "* BYE fake IMAP closing")
			writeIMAP(w, tag+" OK LOGOUT completed")
			return
		default:
			writeIMAP(w, tag+" BAD unsupported")
		}
	}
}

func writeIMAP(w *bufio.Writer, line string) {
	_, _ = w.WriteString(line + "\r\n")
	_ = w.Flush()
}

func TestProbeIMAPRejectsMissingAccountBeforeDaemon(t *testing.T) {
	root := moduleRoot(t)
	work := t.TempDir()
	cli := build(t, root, "./cmd/ksuite-mail", work)

	out, err := exec.Command(cli, "probe", "imap", "--json").Output()
	if code := exitCode(t, err); code != 2 {
		t.Fatalf("probe missing account exit = %d, want 2\noutput: %s", code, out)
	}
	var env struct {
		Status string `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("parse probe validation JSON: %v\noutput: %s", err, out)
	}
	if env.Status != "error" || env.Error.Code != "bad_request" || env.Error.Message != "account is required" {
		t.Fatalf("unexpected validation envelope: %s", out)
	}
}

// TestDoctorUnreachableDaemon runs the CLI against a socket with no daemon and
// asserts a structured, agent-readable error and a non-zero exit.
func TestDoctorUnreachableDaemon(t *testing.T) {
	root := moduleRoot(t)
	work := t.TempDir()
	cli := build(t, root, "./cmd/ksuite-mail", work)

	out, err := exec.Command(cli, "doctor", "--socket", filepath.Join(work, "absent.sock")).Output()
	if code := exitCode(t, err); code == 0 {
		t.Fatalf("doctor against absent daemon exit = 0, want non-zero\noutput: %s", out)
	}

	var env struct {
		Status string `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("parse doctor JSON: %v\noutput: %s", err, out)
	}
	if env.Status != "error" || env.Error.Code != "daemon_unreachable" {
		t.Fatalf("unexpected error envelope: %s", out)
	}
}
