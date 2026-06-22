// Package daemon is the ksuite-maild HTTP/JSON server spoken over a Unix domain
// socket (ADR-002, ADR-003, ARCH-CON-006). It owns the credential boundary:
// the daemon resolves secrets internally and the CLI only ever sees the safe,
// content-free envelopes defined in internal/api.
//
// This slice serves liveness and diagnostics only — GET /v1/health and
// POST /v1/doctor. There is deliberately no raw IMAP command surface
// (FR-011): mail endpoints arrive in later slices.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/doctor"
)

// systemdListenFDStart is the first file descriptor systemd passes to a
// socket-activated service (SD_LISTEN_FDS_START).
const systemdListenFDStart = 3

// Options configures the daemon's view of the deployment.
type Options struct {
	ConfigPath  string
	SecretsPath string
	StateDir    string
	Logger      *slog.Logger
}

// Server serves the local API.
type Server struct {
	opts Options
	log  *slog.Logger
}

// New builds a server. A nil Options.Logger falls back to the default logger.
func New(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{opts: opts, log: log}
}

// Handler returns the routed HTTP handler. It is exported so tests can drive it
// without a socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/doctor", s.handleDoctor)
	mux.HandleFunc("/", s.handleNotFound)
	return s.logRequests(mux)
}

// Serve runs the HTTP server on ln until ctx is cancelled, then shuts down
// gracefully. Closing a path-based Unix listener unlinks the socket file; a
// systemd-provided listener is left to systemd.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	idle := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		idle <- srv.Shutdown(shutdownCtx)
	}()

	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if shutErr := <-idle; err == nil {
		err = shutErr
	}
	return err
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET for /v1/health")
		return
	}
	s.writeOK(w, api.HealthInfo{Status: "ok"})
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/doctor")
		return
	}
	report := doctor.Run(doctor.Options{
		ConfigPath:  s.opts.ConfigPath,
		SecretsPath: s.opts.SecretsPath,
		StateDir:    s.opts.StateDir,
	})
	s.writeOK(w, report)
}

func (s *Server) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusNotFound, "not_found", "unknown endpoint")
}

// writeOK encodes a success envelope around payload.
func (s *Server) writeOK(w http.ResponseWriter, payload any) {
	env, err := api.OK(payload)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not encode response")
		return
	}
	s.writeJSON(w, http.StatusOK, env)
}

// writeError encodes a structured, content-free error envelope.
func (s *Server) writeError(w http.ResponseWriter, code int, errCode, message string) {
	s.writeJSON(w, code, api.Err(errCode, message))
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, env api.Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(env); err != nil {
		s.log.Error("write response body failed", "err", err)
	}
}

// logRequests records method, path, status, and duration only. Request and
// response bodies are never logged: they could carry credentials or private
// mail content (NFR-SEC-005, NFR-ERR-002).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Listen opens the daemon's listening socket. It prefers a systemd
// socket-activated descriptor; absent that, it listens on socketPath directly
// (clearing any stale socket file first) with 0660 permissions. The boolean
// reports whether the listener came from systemd.
func Listen(socketPath string) (net.Listener, bool, error) {
	if ln, ok, err := systemdListener(); ok || err != nil {
		return ln, true, err
	}
	ln, err := pathListener(socketPath)
	return ln, false, err
}

func systemdListener() (net.Listener, bool, error) {
	pidStr := os.Getenv("LISTEN_PID")
	fdsStr := os.Getenv("LISTEN_FDS")
	if pidStr == "" || fdsStr == "" {
		return nil, false, nil
	}
	if pid, err := strconv.Atoi(pidStr); err != nil || pid != os.Getpid() {
		return nil, false, nil // not addressed to us; fall back to path
	}
	nfds, err := strconv.Atoi(fdsStr)
	if err != nil || nfds < 1 {
		return nil, false, nil
	}

	f := os.NewFile(uintptr(systemdListenFDStart), "ksuite-mail.socket")
	if f == nil {
		return nil, true, errors.New("systemd LISTEN_FDS set but no descriptor present")
	}
	defer func() { _ = f.Close() }()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, true, err
	}
	return ln, true, nil
}

func pathListener(socketPath string) (net.Listener, error) {
	// Remove a stale socket from an unclean shutdown; ignore if absent.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	// Restrict the socket to owner+group; the access group is gated by systemd
	// in production, but a self-bound dev/test socket gets a safe default.
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}
