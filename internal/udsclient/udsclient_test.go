package udsclient_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/daemon"
	"github.com/dothackerman/ksuite-mail/internal/udsclient"
)

const validConfig = `[mail]
default_limit = 50

[[mail.accounts]]
id = "rs_info"
email = "info@example.com"
host = "imap.example.com"
port = 993
tls = true
username = "info@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
policy = "full"
folders = ["INBOX"]
`

// startDaemon brings up a daemon on a fresh unix socket and returns its path.
func startDaemon(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	secPath := filepath.Join(dir, "secrets.json")
	stateDir := filepath.Join(dir, "state")
	sock := filepath.Join(dir, "d.sock")
	mustWrite(t, cfgPath, validConfig, 0o640)
	mustWrite(t, secPath, `{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"pw"}}`, 0o600)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	ln, _, err := daemon.Listen(sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = daemon.New(daemon.Options{ConfigPath: cfgPath, SecretsPath: secPath, StateDir: stateDir}).Serve(ctx, ln)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitForSocket(t, sock)
	return sock
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func waitForSocket(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon socket %s never became reachable", sock)
}

func TestHealthOverSocket(t *testing.T) {
	c := udsclient.New(startDaemon(t))
	env, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want ok", env.Status)
	}
	var h api.HealthInfo
	if err := env.DecodeResult(&h); err != nil || h.Status != "ok" {
		t.Fatalf("health payload = %+v, err=%v", h, err)
	}
}

func TestDoctorOverSocket(t *testing.T) {
	c := udsclient.New(startDaemon(t))
	env, err := c.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	var r api.DoctorReport
	if err := env.DecodeResult(&r); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if !r.OK {
		t.Fatalf("expected healthy report, got %+v", r.Checks)
	}
}

func TestUnreachableSocketIsTyped(t *testing.T) {
	c := udsclient.New(filepath.Join(t.TempDir(), "absent.sock"))
	_, err := c.Doctor(context.Background())
	if !errors.Is(err, udsclient.ErrUnreachable) {
		t.Fatalf("Doctor against absent socket = %v, want ErrUnreachable", err)
	}
}
