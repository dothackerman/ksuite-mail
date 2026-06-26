package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/dothackerman/ksuite-mail/internal/cache"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/mailfake"
)

const validDomainConfig = `[mail]
default_limit = 50

[[mail.accounts]]
id = "acct"
email = "acct@example.com"
host = "imap.example.com"
port = 993
tls = true
username = "acct@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/acct/password" }
policy = "domain"
domains = ["example.com"]
folders = ["INBOX", "Sent"]
`

func deploymentWithSource(t *testing.T, configBody string, sourceAdapter *mailfake.Adapter) Options {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	secPath := filepath.Join(dir, "secrets.json")
	stateDir := filepath.Join(dir, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(configBody), 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(secPath, []byte(`{"version":1,"secrets":{"/ksuite-mail/acct/password":"pw"}}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	opts := Options{ConfigPath: cfgPath, SecretsPath: secPath, StateDir: stateDir}
	if sourceAdapter != nil {
		opts.SourceFactory = func(_ context.Context, _ *config.Config) (source, error) {
			return sourceAdapter, nil
		}
	}
	return opts
}

func postJSON(t *testing.T, client *http.Client, url string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func decodeEnvelopeBody(t *testing.T, r io.Reader) api.Envelope {
	t.Helper()
	var env api.Envelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func newLocalHTTPServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("HTTP test listener denied by environment: %v", err)
		}
		t.Fatalf("listen local tcp: %v", err)
	}
	ts := httptest.NewUnstartedServer(New(opts).Handler())
	ts.Listener = ln
	ts.Start()
	return ts
}

func TestReadListEndpointReturnsMessageSummaries(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 1,
					Envelope: mail.MessageEnvelope{
						UID:       1,
						Subject:   "hello",
						From:      "alice@example.com",
						To:        "bob@example.com",
						Snippet:   "hello summary",
						ThreadKey: "t1",
					},
					VisibleByPolicy: true,
					Body:            "preview",
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX", "limit": 10})
	defer func() { _ = res.Body.Close() }()

	env := decodeEnvelopeBody(t, res.Body)
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want ok", env.Status)
	}
	var got api.ListResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(got.Results))
	}
	if got.Results[0].Account != "acct" || got.Results[0].Folder != "INBOX" {
		t.Fatalf("summary source fields = account %q folder %q", got.Results[0].Account, got.Results[0].Folder)
	}
}

func TestReadShowOmitsReplyChainForDomainAccount(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 2,
					Envelope: mail.MessageEnvelope{
						UID:       2,
						Subject:   "reply chain",
						From:      "alice@example.com",
						To:        "bob@example.com",
						Snippet:   "reply chain",
						ThreadKey: "thread",
					},
					Body:            "Current reply text\nOn Tuesday, reply wrote:\n> quoted text",
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()

	// Prime local cache via list.
	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = listRes.Body.Close() }()
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 1 {
		t.Fatalf("list response decode: %v len=%d", err, len(list.Results))
	}
	id := list.Results[0].ID

	showRes := postJSON(t, ts.Client(), ts.URL+"/v1/show", map[string]any{"id": id, "preview": true})
	defer func() { _ = showRes.Body.Close() }()
	var showEnv api.Envelope
	if err := json.NewDecoder(showRes.Body).Decode(&showEnv); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if showEnv.Status != api.StatusOK {
		t.Fatalf("show status = %q", showEnv.Status)
	}
	var show api.ShowResponse
	if err := showEnv.DecodeResult(&show); err != nil {
		t.Fatalf("decode show payload: %v", err)
	}
	if strings.Contains(show.Result.BodyText, "On Tuesday") {
		t.Fatalf("reply boundary text leaked: %q", show.Result.BodyText)
	}
}

func TestReadContextReturnsStaleStatusOnRemoteFailure(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 3,
					Envelope: mail.MessageEnvelope{
						UID:       3,
						Subject:   "first",
						From:      "a@example.com",
						ThreadKey: "thread",
						Snippet:   "first",
					},
					Body:            "body",
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	opts := deploymentWithSource(t, validDomainConfig, adapter)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	// Seed cache.
	primeRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	_ = primeRes.Body.Close()

	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = res.Body.Close() }()

	var env api.Envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode env: %v", err)
	}
	if env.Status != api.StatusOKStale {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOKStale)
	}
}

func TestReadContextOmitsReplyChainForDomainAccount(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 4,
					Envelope: mail.MessageEnvelope{
						UID:       4,
						Subject:   "thread context",
						From:      "alice@example.com",
						ThreadKey: "thread",
						Snippet:   "seed",
					},
					Body:            "Seed body\nOn Friday, alice wrote:\n> prior thread line",
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()

	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = listRes.Body.Close() }()
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 1 {
		t.Fatalf("list response decode: %v len=%d", err, len(list.Results))
	}

	ctxRes := postJSON(t, ts.Client(), ts.URL+"/v1/context", map[string]any{"id": list.Results[0].ID, "budget": 200})
	defer func() { _ = ctxRes.Body.Close() }()
	var env api.Envelope
	if err := json.NewDecoder(ctxRes.Body).Decode(&env); err != nil {
		t.Fatalf("decode context response: %v", err)
	}
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOK)
	}
	var got api.ContextResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode context result: %v", err)
	}
	if strings.Contains(got.Seed.BodyText, "On Friday") {
		t.Fatalf("context body leaked reply boundary text: %q", got.Seed.BodyText)
	}
}

func TestReadListWarningPropagatesOnRefreshFailure(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 5,
					Envelope: mail.MessageEnvelope{
						UID:       5,
						Subject:   "seed",
						From:      "alice@example.com",
						Snippet:   "seed",
						ThreadKey: "thread",
					},
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	opts := deploymentWithSource(t, validDomainConfig, adapter)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	primeRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	_ = primeRes.Body.Close()

	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = res.Body.Close() }()

	var env api.Envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode env: %v", err)
	}
	if env.Status != api.StatusOKStale {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOKStale)
	}
	if len(env.Warnings) != 1 || env.Warnings[0].Code == "" {
		t.Fatalf("warnings = %#v", env.Warnings)
	}
}

func TestReadSearchRequiresQuery(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {},
			"Sent":  {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/search", map[string]any{"account": "acct", "folder": "INBOX", "query": ""})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	_ = res.Body.Close()
}

func TestReadShowRechecksCachedMessageAgainstCurrentDomainPolicy(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 6,
					Envelope: mail.MessageEnvelope{
						UID:       6,
						Subject:   "policy",
						From:      "alice@example.com",
						To:        "bob@example.com",
						Snippet:   "policy",
						ThreadKey: "thread",
					},
					Body:            "body",
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	opts := deploymentWithSource(t, validDomainConfig, adapter)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = listRes.Body.Close()
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 1 {
		t.Fatalf("decode list payload: %v len=%d", err, len(list.Results))
	}

	cfgPath := opts.ConfigPath
	narrowed := strings.Replace(validDomainConfig, `domains = ["example.com"]`, `domains = ["other.example"]`, 1)
	if err := os.WriteFile(cfgPath, []byte(narrowed), 0o640); err != nil {
		t.Fatalf("narrow config: %v", err)
	}
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))

	showRes := postJSON(t, ts.Client(), ts.URL+"/v1/show", map[string]any{"id": list.Results[0].ID, "preview": true})
	defer func() { _ = showRes.Body.Close() }()
	if showRes.StatusCode != http.StatusNotFound {
		t.Fatalf("show status = %d, want 404", showRes.StatusCode)
	}
}

func TestReadContextCapsSeedBodyByBudget(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 7,
					Envelope: mail.MessageEnvelope{
						UID:       7,
						Subject:   "budget",
						From:      "alice@example.com",
						ThreadKey: "thread",
						Snippet:   "seed",
					},
					Body:            strings.Repeat("x", 200),
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()
	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = listRes.Body.Close()
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 1 {
		t.Fatalf("decode list payload: %v len=%d", err, len(list.Results))
	}

	ctxRes := postJSON(t, ts.Client(), ts.URL+"/v1/context", map[string]any{"id": list.Results[0].ID, "budget": 25})
	defer func() { _ = ctxRes.Body.Close() }()
	env := decodeEnvelopeBody(t, ctxRes.Body)
	var got api.ContextResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if len(got.Seed.BodyText) > 25 {
		t.Fatalf("seed body length = %d, want <= 25", len(got.Seed.BodyText))
	}
}

func TestReadNoSourceReturnsErrorEnvelopeInsteadOfSuccessfulNoop(t *testing.T) {
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, nil))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	if env.Status != api.StatusError {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusError)
	}
	if len(env.Warnings) != 1 || env.Warnings[0].Code != "remote_source_unavailable" {
		t.Fatalf("warnings = %#v", env.Warnings)
	}
	var got api.ListResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if got.Refresh.RemoteOK {
		t.Fatalf("refresh.remote_ok = true, want false")
	}
}

func TestReadListAppliesPolicyBeforePagination(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	opts := deploymentWithSource(t, strings.Replace(validDomainConfig, `domains = ["example.com"]`, `domains = ["other.com"]`, 1), adapter)
	now := time.Now().UTC()
	seedDaemonCache(t, opts.StateDir,
		cachedForDaemon("msg_new_disallowed", "thread-1", "alice@example.com", now, 1),
		cachedForDaemon("msg_old_allowed", "thread-2", "carol@other.com", now.Add(-time.Hour), 2),
	)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX", "limit": 1})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ListResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].ID != "msg_old_allowed" {
		t.Fatalf("results = %#v, want older policy-allowed row", got.Results)
	}
}

func TestReadThreadAppliesLimitAfterPolicy(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	opts := deploymentWithSource(t, strings.Replace(validDomainConfig, `domains = ["example.com"]`, `domains = ["other.com"]`, 1), adapter)
	now := time.Now().UTC()
	seedDaemonCache(t, opts.StateDir,
		cachedForDaemon("msg_new_disallowed", "thread-1", "alice@example.com", now, 1),
		cachedForDaemon("msg_old_allowed", "thread-1", "carol@other.com", now.Add(-time.Hour), 2),
	)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/thread", map[string]any{"id": "msg_old_allowed", "max_messages": 1})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ThreadResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode thread response: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != "msg_old_allowed" {
		t.Fatalf("thread messages = %#v, want policy-allowed row", got.Messages)
	}
}

func seedDaemonCache(t *testing.T, stateDir string, messages ...mail.CachedMessage) {
	t.Helper()
	repo, err := cache.NewRepository(cache.DBOptions{Path: filepath.Join(stateDir, "mail.db")})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer func() { _ = repo.Close() }()
	for _, msg := range messages {
		if err := repo.UpsertMessage(msg); err != nil {
			t.Fatalf("UpsertMessage(%s): %v", msg.ID, err)
		}
	}
}

func cachedForDaemon(id, threadKey, from string, date time.Time, uid mail.UID) mail.CachedMessage {
	return mail.CachedMessage{
		ID:                  id,
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 uid,
		MessageID:           "<" + id + "@case>",
		ThreadKey:           threadKey,
		Subject:             id,
		From:                from,
		To:                  "agent@example.net",
		Date:                date,
		Snippet:             id,
		BodyText:            id,
		VisibleReason:       "seed",
		ContentHash:         id,
		FirstLoadedAt:       date,
		LastLoadedOrChecked: date,
	}
}
