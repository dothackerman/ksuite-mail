package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/api"
)

func TestReadStatusExitCodeMapsStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		expected int
	}{
		{in: "ok", expected: 0},
		{in: "ok_stale", expected: 1},
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

func TestRunSearchRequiresQueryFlag(t *testing.T) {
	t.Parallel()
	if got := runSearch([]string{}); got != 2 {
		t.Fatalf("runSearch() = %d, want 2", got)
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
