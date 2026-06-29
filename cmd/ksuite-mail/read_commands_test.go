package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/udsclient"
)

func TestReadStatusExitCodeMapsStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		expected int
	}{
		{in: "ok", expected: 0},
		{in: "ok_stale", expected: 0},
		{in: "partial", expected: 1},
		{in: "error", expected: 1},
		{in: "other", expected: 1},
	}
	for _, c := range cases {
		if got := readStatusExitCode(c.in); got != c.expected {
			t.Fatalf("readStatusExitCode(%q) = %d, want %d", c.in, got, c.expected)
		}
	}
}

func TestReadCommandValidationErrorsEmitJSON(t *testing.T) {
	cases := []struct {
		name string
		run  func([]string) int
		want string
	}{
		{name: "search query", run: runSearch, want: "query is required"},
		{name: "show id", run: runShow, want: "id is required"},
		{name: "thread id", run: runThread, want: "id is required"},
		{name: "context id", run: runContext, want: "id is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := captureStdout(t, func() int {
				return tc.run([]string{"--json"})
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			var env api.Envelope
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatalf("validation output is not JSON: %v\n%s", err, out)
			}
			if env.Status != api.StatusError || env.Error == nil ||
				env.Error.Code != "bad_request" || env.Error.Message != tc.want {
				t.Fatalf("validation envelope = %+v", env)
			}
		})
	}
}

func TestProbeCommandValidationErrorsEmitJSON(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing target", args: nil, want: "probe target is required"},
		{name: "missing account", args: []string{"imap", "--json"}, want: "account is required"},
		{name: "raw imap positional", args: []string{"imap", "--account", "rs_info", "--json", "CAPABILITY"}, want: "raw IMAP command text is not accepted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := captureStdout(t, func() int {
				return runProbe(tc.args)
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			var env api.Envelope
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatalf("validation output is not JSON: %v\n%s", err, out)
			}
			if env.Status != api.StatusError || env.Error == nil ||
				env.Error.Code != "bad_request" || env.Error.Message != tc.want {
				t.Fatalf("validation envelope = %+v", env)
			}
		})
	}
}

func TestReadCommandsAcceptDocumentedFlagsAndPositionals(t *testing.T) {
	cases := []struct {
		name      string
		run       func([]string) int
		args      []string
		path      string
		assertReq func(t *testing.T, body map[string]any)
	}{
		{
			name: "inbox json brief",
			run:  runInbox,
			args: []string{"--brief", "--json", "--account", "all"},
			path: "/v1/list",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["folder"] != "INBOX" {
					t.Fatalf("folder = %v, want INBOX", body["folder"])
				}
			},
		},
		{
			name: "search positional query",
			run:  runSearch,
			args: []string{"OpenRouter credits", "--account", "all", "--json"},
			path: "/v1/search",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["query"] != "OpenRouter credits" {
					t.Fatalf("query = %v", body["query"])
				}
			},
		},
		{
			name: "show positional id explicit preview and max chars",
			run:  runShow,
			args: []string{"msg_abc123", "--preview", "--max-chars", "4000", "--json"},
			path: "/v1/show",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["id"] != "msg_abc123" || body["preview"] != true || body["max_chars"] != float64(4000) {
					t.Fatalf("show body = %#v", body)
				}
			},
		},
		{
			name: "show max chars implies bounded preview",
			run:  runShow,
			args: []string{"msg_abc123", "--max-chars", "4000", "--json"},
			path: "/v1/show",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["id"] != "msg_abc123" || body["preview"] != true || body["max_chars"] != float64(4000) {
					t.Fatalf("show body = %#v", body)
				}
			},
		},
		{
			name: "show defaults preview off",
			run:  runShow,
			args: []string{"msg_headers", "--json"},
			path: "/v1/show",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["preview"] != false {
					t.Fatalf("preview = %v, want false", body["preview"])
				}
			},
		},
		{
			name: "thread positional id",
			run:  runThread,
			args: []string{"msg_abc123", "--brief", "--json"},
			path: "/v1/thread",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["id"] != "msg_abc123" {
					t.Fatalf("thread id = %v", body["id"])
				}
			},
		},
		{
			name: "context positional id",
			run:  runContext,
			args: []string{"msg_abc123", "--budget", "1200", "--json"},
			path: "/v1/context",
			assertReq: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["id"] != "msg_abc123" || body["budget"] != float64(1200) {
					t.Fatalf("context body = %#v", body)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket, requests := startReadCommandSocket(t)
			code := tc.run(append([]string{"--socket", socket}, tc.args...))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if len(*requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(*requests))
			}
			req := (*requests)[0]
			if req.path != tc.path {
				t.Fatalf("path = %q, want %q", req.path, tc.path)
			}
			tc.assertReq(t, req.body)
		})
	}
}

func TestProbeCommandReachesDaemonPath(t *testing.T) {
	socket, requests := startReadCommandSocket(t)
	code := runProbe([]string{"imap", "--socket", socket, "--account", "rs_info", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if len(*requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(*requests))
	}
	req := (*requests)[0]
	if req.path != "/v1/probe/imap" {
		t.Fatalf("path = %q, want /v1/probe/imap", req.path)
	}
	if req.body["account"] != "rs_info" {
		t.Fatalf("account = %v, want rs_info", req.body["account"])
	}
}

func TestReadCommandClientTimeoutMatchesReadContext(t *testing.T) {
	client := udsclient.NewWithTimeout("unused.sock", readCommandTimeout)
	if got := client.Timeout(); got != readCommandTimeout {
		t.Fatalf("read client timeout = %s, want %s", got, readCommandTimeout)
	}
	if readCommandTimeout < 20*time.Second {
		t.Fatalf("read command timeout = %s, want at least 20s", readCommandTimeout)
	}
}

type capturedReadRequest struct {
	path string
	body map[string]any
}

func startReadCommandSocket(t *testing.T) (string, *[]capturedReadRequest) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("Unix socket listener denied by environment: %v", err)
		}
		t.Fatalf("listen unix: %v", err)
	}
	requests := []capturedReadRequest{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests = append(requests, capturedReadRequest{path: r.URL.Path, body: body})
		env, err := api.OK(map[string]any{"ok": true})
		if err != nil {
			t.Errorf("api.OK: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env)
	})}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Errorf("serve unix: %v", err)
		}
	}()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
	})
	return socket, &requests
}

func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	code := fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe writer: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close stdout pipe reader: %v", err)
	}
	return code, string(b)
}
