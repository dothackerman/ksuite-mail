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
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

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

const validFullConfig = `[mail]
default_limit = 50

[[mail.accounts]]
id = "acct"
email = "acct@example.com"
host = "imap.example.com"
port = 993
tls = true
username = "acct@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/acct/password" }
policy = "full"
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

func TestReadListOmitsBccFromSummaryJSON(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 1,
					Envelope: mail.MessageEnvelope{
						UID:       1,
						Subject:   "sent",
						From:      "alice@example.com",
						To:        "bob@example.com",
						Bcc:       "private@example.net",
						ThreadKey: "thread",
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
	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	result := raw["result"].(map[string]any)
	results := result["results"].([]any)
	summary := results[0].(map[string]any)
	if _, ok := summary["bcc"]; ok {
		t.Fatalf("summary exposed bcc: %#v", summary)
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

func TestReadShowOmitsReplyChainForFullAccountByDefault(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 3,
					Envelope: mail.MessageEnvelope{
						UID:       3,
						Subject:   "full reply chain",
						From:      "alice@other.example",
						To:        "bob@example.com",
						Snippet:   "full reply chain",
						ThreadKey: "thread",
					},
					Body:            "Current text\nOn Tuesday, reply wrote:\n> quoted text",
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validFullConfig, adapter))
	defer ts.Close()

	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = listRes.Body.Close()
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 1 {
		t.Fatalf("list response decode: %v len=%d", err, len(list.Results))
	}

	showRes := postJSON(t, ts.Client(), ts.URL+"/v1/show", map[string]any{"id": list.Results[0].ID, "preview": true})
	defer func() { _ = showRes.Body.Close() }()
	showEnv := decodeEnvelopeBody(t, showRes.Body)
	var show api.ShowResponse
	if err := showEnv.DecodeResult(&show); err != nil {
		t.Fatalf("decode show payload: %v", err)
	}
	if strings.Contains(show.Result.BodyText, "On Tuesday") {
		t.Fatalf("reply boundary text leaked for full account: %q", show.Result.BodyText)
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

func TestDirectReadsScopeStaleStatusToSeedFolder(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 3,
					Envelope: mail.MessageEnvelope{
						UID:       3,
						Subject:   "fresh",
						From:      "a@example.com",
						ThreadKey: "thread",
						Snippet:   "fresh",
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

	adapter.SetFailure("select", "acct", "Sent", "", fmt.Errorf("remote down"))
	cases := []struct {
		name    string
		path    string
		payload map[string]any
	}{
		{name: "show", path: "/v1/show", payload: map[string]any{"id": list.Results[0].ID, "preview": true}},
		{name: "thread", path: "/v1/thread", payload: map[string]any{"id": list.Results[0].ID}},
		{name: "context", path: "/v1/context", payload: map[string]any{"id": list.Results[0].ID, "budget": 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.Client(), ts.URL+tc.path, tc.payload)
			defer func() { _ = res.Body.Close() }()
			env := decodeEnvelopeBody(t, res.Body)
			if env.Status != api.StatusOK {
				t.Fatalf("status = %q, want ok for fresh seed folder despite unrelated failure", env.Status)
			}
		})
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

func TestReadContextOmitsReplyChainForFullAccount(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 40,
					Envelope: mail.MessageEnvelope{
						UID:       40,
						Subject:   "full context",
						From:      "alice@example.com",
						ThreadKey: "thread",
						Snippet:   "seed",
					},
					Body:            "Current answer\nOn Friday, alice wrote:\n> prior thread line",
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validFullConfig, adapter))
	defer ts.Close()

	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = listRes.Body.Close()
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 1 {
		t.Fatalf("list response decode: %v len=%d", err, len(list.Results))
	}

	ctxRes := postJSON(t, ts.Client(), ts.URL+"/v1/context", map[string]any{"id": list.Results[0].ID, "budget": 200})
	defer func() { _ = ctxRes.Body.Close() }()
	env := decodeEnvelopeBody(t, ctxRes.Body)
	var got api.ContextResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode context result: %v", err)
	}
	if strings.Contains(got.Seed.BodyText, "On Friday") {
		t.Fatalf("context body leaked reply boundary text: %q", got.Seed.BodyText)
	}
}

func TestReadListEmptyScopedPageIgnoresUnrelatedFolderRefreshFailure(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {},
			"Sent":  {},
		},
	})
	adapter.SetFailure("select", "acct", "Sent", "", fmt.Errorf("sent folder down"))
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validFullConfig, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want %q for empty page with unrelated folder failure", env.Status, api.StatusOK)
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

func TestReadListScopesStaleRefreshTimestampToFailedFolder(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {},
			"Sent": {
				{
					UID: 2,
					Envelope: mail.MessageEnvelope{
						UID:     2,
						Subject: "sent",
						From:    "alice@example.com",
						To:      "bob@example.com",
					},
					Body: "sent",
				},
			},
		},
	})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("inbox down"))
	opts := deploymentWithSource(t, validFullConfig, adapter)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	seedDaemonCache(t, opts.StateDir, cachedForDaemon(mail.PublicID("acct", "INBOX", 123, 1), "thread", "alice@example.com", old, 1))
	seedDaemonFolderState(t, opts.StateDir, cache.FolderState{
		AccountID:             "acct",
		Folder:                "INBOX",
		UIDVALIDITY:           123,
		UIDNEXT:               2,
		LastSeenUID:           1,
		PolicyFingerprint:     "full:",
		LastSuccessfulRefresh: &old,
	})
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	if env.Status != api.StatusOKStale {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOKStale)
	}
	var got api.ListResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if got.Refresh.LastSuccessfulRefreshAt == nil || !got.Refresh.LastSuccessfulRefreshAt.Equal(old) {
		t.Fatalf("refresh timestamp = %v, want failed INBOX timestamp %v", got.Refresh.LastSuccessfulRefreshAt, old)
	}
}

func TestReadThreadMarksStaleWhenAnyReturnedFolderFailed(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 1,
					Envelope: mail.MessageEnvelope{
						UID:       1,
						Subject:   "inbox",
						From:      "alice@example.com",
						Snippet:   "inbox",
						ThreadKey: "cross-folder-thread",
					},
					Body: "inbox",
				},
			},
			"Sent": {},
		},
	})
	adapter.SetFailure("select", "acct", "Sent", "", fmt.Errorf("sent folder down"))
	opts := deploymentWithSource(t, validFullConfig, adapter)
	now := time.Now().UTC()
	sent := cachedForDaemon(mail.PublicID("acct", "Sent", 123, 2), "cross-folder-thread", "alice@example.com", now.Add(-time.Hour), 2)
	sent.Folder = "Sent"
	seedDaemonCache(t, opts.StateDir, sent)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/thread", map[string]any{
		"id":           mail.PublicID("acct", "INBOX", 123, 1),
		"max_messages": 10,
	})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	if env.Status != api.StatusOKStale {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOKStale)
	}
	var got api.ThreadResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode thread response: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("thread messages = %#v, want refreshed INBOX and cached Sent rows", got.Messages)
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

func TestReadContextCapsMultibyteSeedBodyByByteBudget(t *testing.T) {
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
					Body:            "éabc",
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

	ctxRes := postJSON(t, ts.Client(), ts.URL+"/v1/context", map[string]any{"id": list.Results[0].ID, "budget": 1})
	defer func() { _ = ctxRes.Body.Close() }()
	env := decodeEnvelopeBody(t, ctxRes.Body)
	var got api.ContextResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if len(got.Seed.BodyText) > 1 {
		t.Fatalf("seed body byte length = %d, want <= 1 for budget", len(got.Seed.BodyText))
	}
	if !utf8.ValidString(got.Seed.BodyText) {
		t.Fatalf("seed body is invalid UTF-8: %q", got.Seed.BodyText)
	}
}

func TestReadContextPreservesZeroRemainingBudget(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 10,
					Envelope: mail.MessageEnvelope{
						UID:       10,
						Subject:   "newer context",
						From:      "alice@example.com",
						ThreadKey: "thread-budget-zero",
						Snippet:   "12345",
						Date:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
					},
					Body:            "newer",
					VisibleByPolicy: true,
				},
				{
					UID: 9,
					Envelope: mail.MessageEnvelope{
						UID:       9,
						Subject:   "seed context",
						From:      "alice@example.com",
						ThreadKey: "thread-budget-zero",
						Snippet:   "seed",
						Date:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
					},
					Body:            strings.Repeat("seed-body", 20),
					VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()
	listRes := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX", "limit": 2})
	var listEnv api.Envelope
	if err := json.NewDecoder(listRes.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = listRes.Body.Close()
	var list api.ListResponse
	if err := listEnv.DecodeResult(&list); err != nil || len(list.Results) != 2 {
		t.Fatalf("decode list payload: %v len=%d", err, len(list.Results))
	}

	ctxRes := postJSON(t, ts.Client(), ts.URL+"/v1/context", map[string]any{"id": list.Results[1].ID, "budget": 5})
	defer func() { _ = ctxRes.Body.Close() }()
	env := decodeEnvelopeBody(t, ctxRes.Body)
	var got api.ContextResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if len(got.Timeline) != 1 || got.Timeline[0].Snippet != "12345" {
		t.Fatalf("timeline = %#v, want exact-budget prior snippet", got.Timeline)
	}
	if got.Seed.BodyText != "" {
		t.Fatalf("seed body = %q, want empty when remaining budget is zero", got.Seed.BodyText)
	}
}

func TestReadShowMissingIDReturnsStructuredRefreshFailure(t *testing.T) {
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, nil))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/show", map[string]any{"id": "missing", "preview": true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want structured 200 envelope", res.StatusCode)
	}
	env := decodeEnvelopeBody(t, res.Body)
	if env.Status != api.StatusError {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusError)
	}
	if len(env.Warnings) != 1 || env.Warnings[0].Code != "remote_source_unavailable" {
		t.Fatalf("warnings = %#v", env.Warnings)
	}
	var got api.ShowResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode show response: %v", err)
	}
	if got.Refresh.Attempted || got.Refresh.RemoteOK {
		t.Fatalf("refresh = %+v, want failed refresh metadata", got.Refresh)
	}
}

func TestReadShowTruncatesBodyByRunes(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 8,
					Envelope: mail.MessageEnvelope{
						UID:       8,
						Subject:   "unicode",
						From:      "alice@example.com",
						ThreadKey: "thread",
					},
					Body:            "éxtra",
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

	showRes := postJSON(t, ts.Client(), ts.URL+"/v1/show", map[string]any{"id": list.Results[0].ID, "preview": true, "max_chars": 1})
	defer func() { _ = showRes.Body.Close() }()
	env := decodeEnvelopeBody(t, showRes.Body)
	var got api.ShowResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if got.Result.BodyText != "é" {
		t.Fatalf("body = %q, want first rune", got.Result.BodyText)
	}
}

func TestReadListRefreshWindowUsesRequestedLimit(t *testing.T) {
	cfg := strings.Replace(validDomainConfig, "default_limit = 50", "default_limit = 1", 1)
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID:      1,
					Envelope: mail.MessageEnvelope{UID: 1, From: "alice@example.com", Subject: "one", ThreadKey: "t1", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
					Body:     "one", VisibleByPolicy: true,
				},
				{
					UID:      2,
					Envelope: mail.MessageEnvelope{UID: 2, From: "alice@example.com", Subject: "two", ThreadKey: "t2", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
					Body:     "two", VisibleByPolicy: true,
				},
				{
					UID:      3,
					Envelope: mail.MessageEnvelope{UID: 3, From: "alice@example.com", Subject: "three", ThreadKey: "t3", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
					Body:     "three", VisibleByPolicy: true,
				},
			},
			"Sent": {},
		},
	})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, cfg, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX", "limit": 2})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ListResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("results = %d, want requested page filled", len(got.Results))
	}
}

func TestReadListRejectsOversizedRefreshWindow(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX", "limit": 1000000})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if calls := adapter.CallsSnapshot(); len(calls) != 0 {
		t.Fatalf("remote refresh ran for oversized request: %+v", calls)
	}
}

func TestReadListClampsConfiguredRefreshFloor(t *testing.T) {
	cfg := strings.Replace(validDomainConfig, "default_limit = 50", "default_limit = 1000000", 1)
	messages := make([]mailfake.Message, 1001)
	for i := range messages {
		uid := mail.UID(i + 1)
		messages[i] = mailfake.Message{
			UID: uid,
			Envelope: mail.MessageEnvelope{
				UID:       uid,
				Subject:   "bulk",
				From:      "alice@example.com",
				ThreadKey: "bulk",
				Date:      time.Date(2026, 1, 1, 0, i%60, 0, 0, time.UTC),
			},
			Body:            "bulk",
			VisibleByPolicy: true,
		}
	}
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": messages, "Sent": {}}})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, cfg, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	previewCalls := 0
	for _, call := range adapter.CallsSnapshot() {
		if call.Method == "preview" && call.Target == "acct@INBOX" {
			previewCalls++
		}
	}
	if previewCalls != maxReadWindow {
		t.Fatalf("preview calls = %d, want clamp at maxReadWindow %d", previewCalls, maxReadWindow)
	}
}

func TestReadListRejectsConfiguredDefaultPlusOffsetWindow(t *testing.T) {
	cfg := strings.Replace(validDomainConfig, "default_limit = 50", "default_limit = 800", 1)
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, cfg, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "folder": "INBOX", "offset": 500})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if calls := adapter.CallsSnapshot(); len(calls) != 0 {
		t.Fatalf("remote refresh ran for oversized configured default window: %+v", calls)
	}
}

func TestReadSearchRejectsOversizedRefreshWindow(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, adapter))
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/search", map[string]any{"account": "acct", "folder": "INBOX", "query": "invoice", "offset": 1000000})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if calls := adapter.CallsSnapshot(); len(calls) != 0 {
		t.Fatalf("remote refresh ran for oversized request: %+v", calls)
	}
}

func TestReadListRejectsCachedRowsFromUnconfiguredFolders(t *testing.T) {
	const fullConfig = `[mail]
default_limit = 50

[[mail.accounts]]
id = "acct"
email = "acct@example.com"
host = "imap.example.com"
port = 993
tls = true
username = "acct@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/acct/password" }
policy = "full"
folders = ["INBOX"]
`
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}}})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	opts := deploymentWithSource(t, fullConfig, adapter)
	now := time.Now().UTC()
	msg := cachedForDaemon("msg_removed_folder", "thread", "alice@example.com", now, 9)
	msg.Folder = "Sent"
	seedDaemonCache(t, opts.StateDir, msg)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/list", map[string]any{"account": "acct", "limit": 10})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ListResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(got.Results) != 0 {
		t.Fatalf("results = %#v, want removed folder row hidden", got.Results)
	}
}

func TestVisibleMessagePageStopsAtRawReadWindow(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:      "acct",
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	loadCalls := 0
	rawRows := 0
	results, err := visibleMessagePage(cfg, func(limit, offset int) ([]mail.CachedMessage, error) {
		loadCalls++
		if offset >= maxReadWindow {
			t.Fatalf("load offset = %d, want bounded below %d", offset, maxReadWindow)
		}
		rawRows += limit
		out := make([]mail.CachedMessage, limit)
		for i := range out {
			out[i] = cachedForDaemon("msg_disallowed", "thread", "alice@example.com", time.Now().UTC(), mail.UID(offset+i+1))
			out[i].Folder = "Removed"
		}
		return out, nil
	}, 1, 0)
	if err != nil {
		t.Fatalf("visibleMessagePage: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %#v, want no visible rows", results)
	}
	if rawRows != maxReadWindow {
		t.Fatalf("raw rows scanned = %d, want cap at %d", rawRows, maxReadWindow)
	}
	if loadCalls == 0 {
		t.Fatalf("load was not called")
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

func TestReadThreadAndContextRejectOversizedBounds(t *testing.T) {
	ts := newLocalHTTPServer(t, deploymentWithSource(t, validDomainConfig, nil))
	defer ts.Close()

	cases := []struct {
		name    string
		path    string
		payload map[string]any
	}{
		{
			name:    "thread max_messages",
			path:    "/v1/thread",
			payload: map[string]any{"id": "msg_any", "max_messages": maxReadWindow + 1},
		},
		{
			name:    "context budget",
			path:    "/v1/context",
			payload: map[string]any{"id": "msg_any", "budget": maxContextBudget + 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.Client(), ts.URL+tc.path, tc.payload)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", res.StatusCode, http.StatusBadRequest)
			}
			env := decodeEnvelopeBody(t, res.Body)
			if env.Status != api.StatusError || env.Error == nil || env.Error.Code != "bad_request" {
				t.Fatalf("envelope = %+v, want bad_request error", env)
			}
		})
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

func TestReadThreadKeepsRequestedSeedWhenBounded(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	opts := deploymentWithSource(t, validFullConfig, adapter)
	now := time.Now().UTC()
	seedDaemonCache(t, opts.StateDir,
		cachedForDaemon("msg_newer", "thread-1", "alice@example.com", now, 1),
		cachedForDaemon("msg_seed", "thread-1", "alice@example.com", now.Add(-time.Hour), 2),
	)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/thread", map[string]any{"id": "msg_seed", "max_messages": 1})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ThreadResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode thread response: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != "msg_seed" {
		t.Fatalf("thread messages = %#v, want requested seed within bound", got.Messages)
	}
}

func TestReadThreadWithEmptyThreadKeyReturnsOnlySeedMessage(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	opts := deploymentWithSource(t, validFullConfig, adapter)
	now := time.Now().UTC()
	seedDaemonCache(t, opts.StateDir,
		cachedForDaemon("msg_seed_empty_thread", "", "alice@example.com", now, 1),
		cachedForDaemon("msg_other_empty_thread", "", "carol@example.com", now.Add(-time.Hour), 2),
	)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/thread", map[string]any{"id": "msg_seed_empty_thread", "max_messages": 10})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ThreadResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode thread response: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != "msg_seed_empty_thread" {
		t.Fatalf("thread messages = %#v, want only seed row for empty thread key", got.Messages)
	}
}

func TestReadContextWithEmptyThreadKeyReturnsNoUnrelatedTimeline(t *testing.T) {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}, "Sent": {}}})
	adapter.SetFailure("select", "acct", "INBOX", "", fmt.Errorf("remote down"))
	opts := deploymentWithSource(t, validFullConfig, adapter)
	now := time.Now().UTC()
	seedDaemonCache(t, opts.StateDir,
		cachedForDaemon("msg_seed_empty_context", "", "alice@example.com", now, 1),
		cachedForDaemon("msg_other_empty_context", "", "carol@example.com", now.Add(-time.Hour), 2),
	)
	ts := newLocalHTTPServer(t, opts)
	defer ts.Close()

	res := postJSON(t, ts.Client(), ts.URL+"/v1/context", map[string]any{"id": "msg_seed_empty_context", "budget": 200})
	defer func() { _ = res.Body.Close() }()
	env := decodeEnvelopeBody(t, res.Body)
	var got api.ContextResponse
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("decode context response: %v", err)
	}
	if len(got.Timeline) != 0 {
		t.Fatalf("timeline = %#v, want no unrelated empty-thread messages", got.Timeline)
	}
}

func TestNormalizeReadPageRejectsOverflowWindow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if _, _, ok := normalizeReadPage(0, maxInt, maxInt); ok {
		t.Fatalf("normalizeReadPage accepted overflowing window")
	}
}

func TestRefreshAndLoadClosesRepositoryOnPostOpenError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("open fd counting uses /proc/self/fd")
	}
	opts := deploymentWithSource(t, validFullConfig, nil)
	cachePath := filepath.Join(opts.StateDir, "mail.db")
	opts.SourceFactory = func(context.Context, *config.Config) (source, error) {
		return nil, fmt.Errorf("source setup failed")
	}
	s := New(opts)

	before := countOpenPathFDs(t, cachePath)
	_, _, _, err := s.refreshAndLoad(context.Background(), defaultLimit)
	if err == nil {
		t.Fatalf("refreshAndLoad error = nil, want source setup failure")
	}
	after := countOpenPathFDs(t, cachePath)
	if after != before {
		t.Fatalf("open cache fds after refreshAndLoad error = %d, want %d", after, before)
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

func seedDaemonFolderState(t *testing.T, stateDir string, state cache.FolderState) {
	t.Helper()
	repo, err := cache.NewRepository(cache.DBOptions{Path: filepath.Join(stateDir, "mail.db")})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer func() { _ = repo.Close() }()
	if err := repo.UpsertFolderState(state); err != nil {
		t.Fatalf("UpsertFolderState(%s/%s): %v", state.AccountID, state.Folder, err)
	}
}

func countOpenPathFDs(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot read /proc/self/fd: %v", err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		if target == path {
			count++
		}
	}
	return count
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
