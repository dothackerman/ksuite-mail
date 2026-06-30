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
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/daemon"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/mailfake"
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

const domainConfig = `[mail]
default_limit = 50

[[mail.accounts]]
id = "rs_info"
email = "info@example.com"
host = "imap.example.com"
port = 993
tls = true
username = "info@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
policy = "domain"
domains = ["regenerativ.ch"]
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

func probeDeployment(t *testing.T, adapter *mailfake.Adapter) daemon.Options {
	t.Helper()
	opts := healthyDeployment(t)
	opts.ProbeSourceFactory = func(context.Context, *config.Config) (mail.Source, error) {
		return adapter, nil
	}
	return opts
}

func decodeEnvelope(t *testing.T, body io.Reader) api.Envelope {
	t.Helper()
	var env api.Envelope
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func probeCheckByID(t *testing.T, probe api.ProbeIMAPResponse, id string) api.ProbeCheck {
	t.Helper()
	for _, check := range probe.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("probe check %q missing from %+v", id, probe.Checks)
	return api.ProbeCheck{}
}

type rangeIgnoringSource struct {
	mail.Source
}

func (s rangeIgnoringSource) ListUIDs(ctx context.Context, acct config.Account, folder string, _ mail.UIDRange) ([]mail.UID, error) {
	return s.Source.ListUIDs(ctx, acct, folder, mail.UIDRange{})
}

type timeoutProbeSource struct{}

func (timeoutProbeSource) Capabilities(context.Context, config.Account) ([]string, error) {
	return []string{"IMAP4rev1"}, nil
}

func (timeoutProbeSource) Folders(ctx context.Context, _ config.Account) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (timeoutProbeSource) SelectFolder(context.Context, config.Account, string) (mail.RemoteFolderState, error) {
	return mail.RemoteFolderState{}, errors.New("unexpected select")
}

func (timeoutProbeSource) SearchAllowed(context.Context, config.Account, string, string, string, mail.UIDRange) ([]mail.UID, error) {
	return nil, errors.New("unexpected search")
}

func (timeoutProbeSource) ListUIDs(context.Context, config.Account, string, mail.UIDRange) ([]mail.UID, error) {
	return nil, errors.New("unexpected list")
}

func (timeoutProbeSource) FetchHeaders(context.Context, config.Account, string, []mail.UID) ([]mail.MessageHeaders, error) {
	return nil, errors.New("unexpected headers")
}

func (timeoutProbeSource) FetchEnvelopes(context.Context, config.Account, string, []mail.UID) ([]mail.MessageEnvelope, error) {
	return nil, errors.New("unexpected fetch")
}

func (timeoutProbeSource) FetchBodyPreview(context.Context, config.Account, string, mail.UID, int) (string, error) {
	return "", errors.New("unexpected preview")
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

func TestProbeIMAPEndpointValidatesAccountAndRunsLiveChecklist(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"rs_info": {
			"INBOX":                  {{UID: 10, VisibleByPolicy: true}},
			"private-folder-name-pw": {},
		},
	})
	ts := newLocalHTTPServer(t, probeDeployment(t, adapter))
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
	if probe.Status != api.ProbeStatusInconclusive {
		t.Fatalf("status = %q, want %q", probe.Status, api.ProbeStatusInconclusive)
	}
	if len(probe.Checks) == 0 {
		t.Fatalf("expected fixed checklist")
	}
	checks := map[string]string{}
	for _, check := range probe.Checks {
		checks[check.ID] = check.Status
		if check.ID == "" {
			t.Fatalf("probe check missing stable id: %+v", check)
		}
		if check.Status == "pass" || check.Status == "fail" {
			t.Fatalf("probe check used legacy status vocabulary: %+v", check)
		}
	}
	if checks["account_selection"] != api.ProbeStatusPassed {
		t.Fatalf("account_selection status = %q, want %q", checks["account_selection"], api.ProbeStatusPassed)
	}
	if checks["capability"] != api.ProbeStatusPassed {
		t.Fatalf("capability status = %q, want %q", checks["capability"], api.ProbeStatusPassed)
	}
	if checks["folder_listing"] != api.ProbeStatusPassed {
		t.Fatalf("folder_listing status = %q, want %q", checks["folder_listing"], api.ProbeStatusPassed)
	}
	if checks["folder_selection"] != api.ProbeStatusPassed {
		t.Fatalf("folder_selection status = %q, want %q", checks["folder_selection"], api.ProbeStatusPassed)
	}
	if checks["uid_behavior"] != api.ProbeStatusPassed {
		t.Fatalf("uid_behavior status = %q, want %q", checks["uid_behavior"], api.ProbeStatusPassed)
	}
	if checks["domain_header_search"] != api.ProbeStatusNotApplicable {
		t.Fatalf("domain_header_search status = %q, want %q", checks["domain_header_search"], api.ProbeStatusNotApplicable)
	}
	if strings.Contains(mustJSON(t, env), "pw") ||
		strings.Contains(mustJSON(t, env), "private-folder-name") ||
		strings.Contains(mustJSON(t, env), "CAPABILITY ") {
		t.Fatalf("probe response leaked secret or raw command text: %+v", env)
	}
	calls := adapter.CallsSnapshot()
	if len(calls) == 0 || calls[0].Method != "capability" {
		t.Fatalf("expected probe to call live adapter path, calls=%#v", calls)
	}
}

func TestProbeIMAPEndpointUsesProbeSourceWithoutReadSource(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"rs_info": {"INBOX": {{UID: 10, VisibleByPolicy: true}}},
	})
	opts := healthyDeployment(t)
	opts.SourceFactory = func(context.Context, *config.Config) (mail.Source, error) {
		return nil, errors.New("read source should not be used by probe")
	}
	opts.ProbeSourceFactory = func(context.Context, *config.Config) (mail.Source, error) {
		return adapter, nil
	}
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(`{"account":"rs_info"}`))
	if err != nil {
		t.Fatalf("POST probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	env := decodeEnvelope(t, resp.Body)
	if env.Status != api.StatusOK {
		t.Fatalf("envelope status = %q, want ok", env.Status)
	}
	if calls := adapter.CallsSnapshot(); len(calls) == 0 || calls[0].Method != "capability" {
		t.Fatalf("probe source was not used, calls=%#v", calls)
	}
}

func TestProbeIMAPEndpointSanitizesLiveAdapterFailure(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"rs_info": {"INBOX": {}},
	})
	adapter.SetFailure("capability", "rs_info", "", "", errors.New("NO auth failed for info@example.com with pw and private text"))
	ts := newLocalHTTPServer(t, probeDeployment(t, adapter))
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
	if probe.Status != api.ProbeStatusFailed {
		t.Fatalf("probe status = %q, want failed", probe.Status)
	}
	raw := mustJSON(t, env)
	for _, leak := range []string{"pw", "info@example.com", "private text", "auth failed"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("probe failure leaked %q in %s", leak, raw)
		}
	}
	found := false
	for _, check := range probe.Checks {
		if check.ID == "capability" {
			found = true
			if check.Code != "remote_failed" || check.Detail != "provider probe failed" {
				t.Fatalf("capability check = %+v", check)
			}
		}
	}
	if !found {
		t.Fatal("capability check missing")
	}
	if calls := adapter.CallsSnapshot(); len(calls) != 1 || calls[0].Method != "capability" {
		t.Fatalf("probe should stop after first source failure, calls=%#v", calls)
	}
	for _, check := range probe.Checks {
		switch check.ID {
		case "folder_listing", "folder_selection", "uid_behavior", "read_state":
			if check.Code != "prerequisite_failed" {
				t.Fatalf("%s code = %q, want prerequisite_failed", check.ID, check.Code)
			}
		}
	}
}

func TestProbeIMAPEndpointBoundsEntireProbeBeforeClientTimeout(t *testing.T) {
	opts := healthyDeployment(t)
	opts.ProbeTimeout = 30 * time.Millisecond
	opts.ProbeSourceFactory = func(context.Context, *config.Config) (mail.Source, error) {
		return timeoutProbeSource{}, nil
	}
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	client := &http.Client{Timeout: time.Second}
	start := time.Now()
	resp, err := client.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(`{"account":"rs_info"}`))
	if err != nil {
		t.Fatalf("POST probe: %v", err)
	}
	elapsed := time.Since(start)
	defer func() { _ = resp.Body.Close() }()
	if elapsed > 500*time.Millisecond {
		t.Fatalf("probe took %s, want daemon-side timeout before client timeout", elapsed)
	}
	env := decodeEnvelope(t, resp.Body)
	var probe api.ProbeIMAPResponse
	if err := env.DecodeResult(&probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if probe.Status != api.ProbeStatusFailed {
		t.Fatalf("probe status = %q, want failed", probe.Status)
	}
	check := probeCheckByID(t, probe, "folder_listing")
	if check.Status != api.ProbeStatusFailed || check.Code != "remote_timeout" || check.Detail != "provider probe failed" {
		t.Fatalf("folder_listing check = %+v", check)
	}
}

func TestProbeIMAPEndpointFailsUIDRangeMismatch(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"rs_info": {"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
	})
	opts := healthyDeployment(t)
	opts.ProbeSourceFactory = func(context.Context, *config.Config) (mail.Source, error) {
		return rangeIgnoringSource{Source: adapter}, nil
	}
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(`{"account":"rs_info"}`))
	if err != nil {
		t.Fatalf("POST probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	env := decodeEnvelope(t, resp.Body)
	var probe api.ProbeIMAPResponse
	if err := env.DecodeResult(&probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if probe.Status != api.ProbeStatusFailed {
		t.Fatalf("probe status = %q, want failed", probe.Status)
	}
	check := probeCheckByID(t, probe, "uid_behavior")
	if check.Status != api.ProbeStatusFailed || check.Code != "uid_range_mismatch" {
		t.Fatalf("uid_behavior check = %+v", check)
	}
}

func TestProbeIMAPEndpointRequiresEachDomainHeaderFixture(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"rs_info": {"INBOX": {{UID: 10, VisibleByPolicy: true}}},
	})
	adapter.SetSearchResult("rs_info", "INBOX", "From", "regenerativ.ch", []mail.UID{10})
	adapter.SetSearchResult("rs_info", "INBOX", "To", "regenerativ.ch", nil)
	adapter.SetSearchResult("rs_info", "INBOX", "Cc", "regenerativ.ch", nil)
	adapter.SetSearchResult("rs_info", "INBOX", "Bcc", "regenerativ.ch", nil)
	opts := probeDeployment(t, adapter)
	if err := os.WriteFile(opts.ConfigPath, []byte(domainConfig), 0o640); err != nil {
		t.Fatalf("write domain config: %v", err)
	}
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(`{"account":"rs_info"}`))
	if err != nil {
		t.Fatalf("POST probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	env := decodeEnvelope(t, resp.Body)
	var probe api.ProbeIMAPResponse
	if err := env.DecodeResult(&probe); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	check := probeCheckByID(t, probe, "domain_header_search")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "fixture_required" {
		t.Fatalf("domain_header_search check = %+v", check)
	}
	if !strings.Contains(check.Detail, "from_count=1") || !strings.Contains(check.Detail, "to_count=0") {
		t.Fatalf("domain_header_search detail = %q", check.Detail)
	}
}

func TestProbeIMAPEndpointRejectsMissingCredential(t *testing.T) {
	opts := healthyDeployment(t)
	if err := os.WriteFile(opts.SecretsPath, []byte(`{"version":1,"secrets":{}}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/probe/imap", "application/json", bytes.NewBufferString(`{"account":"rs_info"}`))
	if err != nil {
		t.Fatalf("POST probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFailedDependency {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFailedDependency)
	}
	env := decodeEnvelope(t, resp.Body)
	if env.Status != api.StatusError || env.Error == nil || env.Error.Code != "credential_missing" {
		t.Fatalf("envelope = %+v", env)
	}
	if strings.Contains(mustJSON(t, env), "pw") || strings.Contains(mustJSON(t, env), opts.SecretsPath) {
		t.Fatalf("probe credential error leaked secret detail: %+v", env)
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
