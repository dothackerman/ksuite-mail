package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/daemon"
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

func healthyDeployment(t *testing.T) daemon.Options {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	secPath := filepath.Join(dir, "secrets.json")
	stateDir := filepath.Join(dir, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(validConfig), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(secPath,
		[]byte(`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"pw"}}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	return daemon.Options{ConfigPath: cfgPath, SecretsPath: secPath, StateDir: stateDir}
}

func decodeEnvelope(t *testing.T, body io.Reader) api.Envelope {
	t.Helper()
	var env api.Envelope
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func TestHealthEndpointReportsLiveness(t *testing.T) {
	ts := httptest.NewServer(daemon.New(healthyDeployment(t)).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp.Body)
	if env.Status != api.StatusOK {
		t.Fatalf("envelope status = %q, want ok", env.Status)
	}
	var h api.HealthInfo
	if err := env.DecodeResult(&h); err != nil || h.Status != "ok" {
		t.Fatalf("health payload = %+v, err=%v", h, err)
	}
}

func TestDoctorEndpointReturnsReport(t *testing.T) {
	ts := httptest.NewServer(daemon.New(healthyDeployment(t)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/doctor", "application/json", nil)
	if err != nil {
		t.Fatalf("POST doctor: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp.Body)
	var r api.DoctorReport
	if err := env.DecodeResult(&r); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if !r.OK || len(r.Checks) == 0 {
		t.Fatalf("expected healthy report with checks, got %+v", r)
	}
}

func TestWrongMethodIsRejected(t *testing.T) {
	ts := httptest.NewServer(daemon.New(healthyDeployment(t)).Handler())
	defer ts.Close()

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/doctor"},
		{http.MethodPost, "/v1/health"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want 405", c.method, c.path, resp.StatusCode)
		}
		env := decodeEnvelope(t, resp.Body)
		_ = resp.Body.Close()
		if env.Status != api.StatusError || env.Error == nil {
			t.Fatalf("%s %s envelope = %+v, want error", c.method, c.path, env)
		}
	}
}

func TestUnknownPathReturnsErrorEnvelope(t *testing.T) {
	ts := httptest.NewServer(daemon.New(healthyDeployment(t)).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/nope")
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if env := decodeEnvelope(t, resp.Body); env.Status != api.StatusError {
		t.Fatalf("envelope status = %q, want error", env.Status)
	}
}

func TestServeOverUnixSocketAndCleanUp(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, fromSystemd, err := daemon.Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if fromSystemd {
		t.Fatalf("expected path listener, not systemd activation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	srvErr := make(chan error, 1)
	go func() { srvErr <- daemon.New(healthyDeployment(t)).Serve(ctx, ln) }()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}

	resp, err := client.Get("http://unix/v1/health")
	if err != nil {
		t.Fatalf("GET over unix socket: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-srvErr:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not shut down within 3s")
	}

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket file should be removed after shutdown, stat err = %v", err)
	}
}
