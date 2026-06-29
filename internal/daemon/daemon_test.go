package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func newLocalHTTPServer(t *testing.T, opts daemon.Options) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("HTTP test listener denied by environment: %v", err)
		}
		t.Fatalf("listen local tcp: %v", err)
	}
	ts := httptest.NewUnstartedServer(daemon.New(opts).Handler())
	ts.Listener = ln
	ts.Start()
	return ts
}

func skipIfSocketListenerUnsupported(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "operation not permitted") {
		t.Skipf("unix socket listener denied by environment: %v", err)
	}
	t.Fatalf("Listen: %v", err)
}

func TestHealthEndpointReportsLiveness(t *testing.T) {
	ts := newLocalHTTPServer(t, healthyDeployment(t))
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
	ts := newLocalHTTPServer(t, healthyDeployment(t))
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

func TestProbeIMAPEndpointValidatesAccountAndReturnsStubChecklist(t *testing.T) {
	ts := newLocalHTTPServer(t, healthyDeployment(t))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(`{"account":"rs_info"}`))
	if err != nil {
		t.Fatalf("POST probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp.Body)
	var probe api.ProbeIMAPResponse
	if err := env.DecodeResult(&probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if probe.Account != "rs_info" {
		t.Fatalf("account = %q, want rs_info", probe.Account)
	}
	if len(probe.Checks) == 0 {
		t.Fatalf("expected fixed checklist")
	}
	if strings.Contains(mustJSON(t, env), "pw") || strings.Contains(mustJSON(t, env), "CAPABILITY ") {
		t.Fatalf("probe response leaked secret or raw command text: %+v", env)
	}
}

func TestProbeIMAPEndpointRejectsMissingAndUnknownAccount(t *testing.T) {
	ts := newLocalHTTPServer(t, healthyDeployment(t))
	defer ts.Close()

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{name: "missing", body: `{}`, wantCode: http.StatusBadRequest, wantErr: "missing_account"},
		{name: "unknown", body: `{"account":"missing"}`, wantCode: http.StatusNotFound, wantErr: "unknown_account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(tc.body))
			if err != nil {
				t.Fatalf("POST probe: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}
			env := decodeEnvelope(t, resp.Body)
			if env.Status != api.StatusError || env.Error == nil || env.Error.Code != tc.wantErr {
				t.Fatalf("envelope = %+v", env)
			}
		})
	}
}

func TestWrongMethodIsRejected(t *testing.T) {
	ts := newLocalHTTPServer(t, healthyDeployment(t))
	defer ts.Close()

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/doctor"},
		{http.MethodGet, "/v1/probe/imap"},
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

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(b)
}

func TestUnknownPathReturnsErrorEnvelope(t *testing.T) {
	ts := newLocalHTTPServer(t, healthyDeployment(t))
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
		skipIfSocketListenerUnsupported(t, err)
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
