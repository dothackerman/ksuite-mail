package udsclient_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
func startDaemon(t *testing.T, sourceFactory daemon.SourceFactory) string {
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
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || (len(err.Error()) > 0 && strings.Contains(err.Error(), "operation not permitted")) {
			t.Skipf("uds listener denied by environment: %v", err)
		}
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		opts := daemon.Options{ConfigPath: cfgPath, SecretsPath: secPath, StateDir: stateDir}
		if sourceFactory != nil {
			opts.SourceFactory = sourceFactory
		}
		_ = daemon.New(opts).Serve(ctx, ln)
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
	c := udsclient.New(startDaemon(t, nil))
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
	c := udsclient.New(startDaemon(t, nil))
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

func TestListOverSocket(t *testing.T) {
	c := udsclient.New(startDaemon(t, nil))
	env, err := c.List(context.Background(), api.ListRequest{Folder: "INBOX", Limit: 5})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOK)
	}
	var r api.ListResponse
	if err := env.DecodeResult(&r); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
}

func TestSearchOverSocket(t *testing.T) {
	c := udsclient.New(startDaemon(t, nil))
	env, err := c.Search(context.Background(), api.SearchRequest{Folder: "INBOX", Query: "nothing", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOK)
	}
	var r api.SearchResponse
	if err := env.DecodeResult(&r); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
}

func TestShowOverSocket(t *testing.T) {
	c := udsclient.New(startDaemon(t, nil))
	env, err := c.Show(context.Background(), api.ShowRequest{ID: "absent-id", Preview: true, MaxChars: 20})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if env.Status != api.StatusError {
		t.Fatalf("show status = %q", env.Status)
	}
	if env.Error == nil || env.Error.Code != "not_found" {
		t.Fatalf("expected not_found error, got %+v", env.Error)
	}
}

func TestThreadAndContextOverSocket(t *testing.T) {
	c := udsclient.New(startDaemon(t, nil))
	threadEnv, err := c.Thread(context.Background(), api.ThreadRequest{ID: "absent-thread-id"})
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if threadEnv.Status != api.StatusError {
		t.Fatalf("thread status = %q", threadEnv.Status)
	}
	if threadEnv.Error == nil || threadEnv.Error.Code != "not_found" {
		t.Fatalf("expected not_found thread error, got %+v", threadEnv.Error)
	}

	ctxEnv, err := c.Context(context.Background(), api.ContextRequest{ID: "absent-thread-id"})
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	if ctxEnv.Status != api.StatusError {
		t.Fatalf("context status = %q", ctxEnv.Status)
	}
	if ctxEnv.Error == nil || ctxEnv.Error.Code != "not_found" {
		t.Fatalf("expected not_found context error, got %+v", ctxEnv.Error)
	}
}
