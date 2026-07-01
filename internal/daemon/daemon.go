// Package daemon is the ksuite-maild HTTP/JSON server spoken over a Unix domain
// socket (ADR-002, ADR-003, ARCH-CON-006). It owns the credential boundary:
// the daemon resolves secrets internally and the CLI only ever sees the safe,
// content-free envelopes defined in internal/api.
//
// This slice now includes read-only command surfaces used by the CLI: list,
// search, show, thread, and context. It keeps health and doctor behavior
// unchanged.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/cache"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/doctor"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/policy"
	"github.com/dothackerman/ksuite-mail/internal/providerprobe"
	"github.com/dothackerman/ksuite-mail/internal/refresh"
	"github.com/dothackerman/ksuite-mail/internal/secrets"
)

// systemdListenFDStart is the first file descriptor systemd passes to a
// socket-activated service (SD_LISTEN_FDS_START).
const systemdListenFDStart = 3

const (
	defaultLimit       = 100
	defaultBudget      = 1200
	defaultContextMax  = 1200
	maxReadWindow      = 1000
	maxContextBudget   = 12 * 1024
	defaultProbeBudget = 18 * time.Second
)

// source matches the narrow read-only adapter interface.
type source = mail.Source

// SourceFactory builds a read-refresh source from runtime config.
type SourceFactory func(ctx context.Context, cfg *config.Config) (mail.Source, error)

// UnavailableSourceFactory keeps issue #3 production daemons explicit: read
// routes exist for the CLI/API contract, but live IMAP is wired by the later
// provider-probe slice.
func UnavailableSourceFactory() SourceFactory {
	return func(context.Context, *config.Config) (source, error) {
		return unavailableSource{}, nil
	}
}

type unavailableSource struct{}

func (unavailableSource) Capabilities(context.Context, config.Account) ([]string, error) {
	return nil, mail.ErrSourceUnavailable
}

func (unavailableSource) Folders(context.Context, config.Account) ([]string, error) {
	return nil, mail.ErrSourceUnavailable
}

func (unavailableSource) SelectFolder(context.Context, config.Account, string) (mail.RemoteFolderState, error) {
	return mail.RemoteFolderState{}, mail.ErrSourceUnavailable
}

func (unavailableSource) SearchAllowed(context.Context, config.Account, string, string, string, mail.UIDRange) ([]mail.UID, error) {
	return nil, mail.ErrSourceUnavailable
}

func (unavailableSource) ListUIDs(context.Context, config.Account, string, mail.UIDRange) ([]mail.UID, error) {
	return nil, mail.ErrSourceUnavailable
}

func (unavailableSource) FetchHeaders(context.Context, config.Account, string, []mail.UID) ([]mail.MessageHeaders, error) {
	return nil, mail.ErrSourceUnavailable
}

func (unavailableSource) FetchEnvelopes(context.Context, config.Account, string, []mail.UID) ([]mail.MessageEnvelope, error) {
	return nil, mail.ErrSourceUnavailable
}

func (unavailableSource) FetchBodyPreview(context.Context, config.Account, string, mail.UID, int) (string, error) {
	return "", mail.ErrSourceUnavailable
}

func (unavailableSource) FetchBodyPreviewAndSeenState(context.Context, config.Account, string, mail.UID, int) (string, bool, error) {
	return "", false, mail.ErrSourceUnavailable
}

// Options configures the daemon's view of the deployment.
type Options struct {
	ConfigPath         string
	SecretsPath        string
	StateDir           string
	Logger             *slog.Logger
	SourceFactory      SourceFactory
	ProbeSourceFactory SourceFactory
	ProbeTimeout       time.Duration
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
	mux.HandleFunc("/v1/probe/imap", s.handleProbeIMAP)
	mux.HandleFunc("/v1/list", s.handleList)
	mux.HandleFunc("/v1/search", s.handleSearch)
	mux.HandleFunc("/v1/show", s.handleShow)
	mux.HandleFunc("/v1/thread", s.handleThread)
	mux.HandleFunc("/v1/context", s.handleContext)
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

func (s *Server) handleProbeIMAP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/probe/imap")
		return
	}
	var req api.ProbeIMAPRequest
	if err := decodeRequestBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	accountRef := strings.TrimSpace(req.Account)
	if accountRef == "" {
		s.writeError(w, http.StatusBadRequest, "missing_account", "account is required")
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	acct := accountByID(cfg, accountRef)
	if acct == nil {
		s.writeError(w, http.StatusNotFound, "unknown_account", "account not found")
		return
	}
	if err := s.verifyProbeCredential(*acct); err != nil {
		s.writeError(w, http.StatusFailedDependency, probeCredentialErrorCode(err), "selected account credential is unavailable")
		return
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), s.probeTimeout())
	defer cancel()

	src, err := s.probeSourceForConfig(probeCtx, cfg)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "source_unavailable", "mail source is unavailable")
		return
	}

	s.writeOK(w, providerprobe.Runner{}.RunIMAP(probeCtx, src, *acct))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/list")
		return
	}
	var req api.ListRequest
	if err := decodeRequestBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	limit, offset, ok := normalizeReadPage(cfg.Mail.DefaultLimit, req.Limit, req.Offset)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "bad_request", "limit plus offset exceeds maximum read window")
		return
	}
	cfg, repo, refreshRes, err := s.refreshAndLoadConfig(r.Context(), cfg, limit+offset)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer func() { _ = repo.Close() }()

	filter := cache.QueryFilter{
		AccountID: accountFilter(req.Account),
		Folder:    req.Folder,
	}
	results, err := visibleMessagePage(cfg, func(batchLimit, batchOffset int) ([]mail.CachedMessage, error) {
		filter.Limit = batchLimit
		filter.Offset = batchOffset
		return repo.ListMessages(filter)
	}, effectiveReadLimit(cfg.Mail.DefaultLimit, req.Limit), offset)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not query cache")
		return
	}

	resp := api.ListResponse{
		Results: results,
	}
	status, meta, warnings := readEnvelopeForScope(refreshRes, req.Account, req.Folder, len(results) > 0)
	resp.Refresh = meta
	s.writeReadOK(w, status, resp, warnings)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/search")
		return
	}
	var req api.SearchRequest
	if err := decodeRequestBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "query is required")
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	limit, offset, ok := normalizeReadPage(cfg.Mail.DefaultLimit, req.Limit, req.Offset)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "bad_request", "limit plus offset exceeds maximum read window")
		return
	}
	cfg, repo, refreshRes, err := s.refreshAndLoadConfig(r.Context(), cfg, limit+offset)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer func() { _ = repo.Close() }()

	filter := cache.QueryFilter{
		AccountID: accountFilter(req.Account),
		Folder:    req.Folder,
		Query:     req.Query,
	}
	results, err := visibleMessagePage(cfg, func(batchLimit, batchOffset int) ([]mail.CachedMessage, error) {
		filter.Limit = batchLimit
		filter.Offset = batchOffset
		return repo.SearchMessages(filter)
	}, effectiveReadLimit(cfg.Mail.DefaultLimit, req.Limit), offset)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not search cache")
		return
	}

	resp := api.SearchResponse{
		Results: results,
	}
	status, meta, warnings := readEnvelopeForScope(refreshRes, req.Account, req.Folder, len(results) > 0)
	resp.Refresh = meta
	s.writeReadOK(w, status, resp, warnings)
}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/show")
		return
	}
	var req api.ShowRequest
	if err := decodeRequestBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}

	cfg, repo, refreshRes, err := s.refreshAndLoad(r.Context(), defaultLimit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer func() { _ = repo.Close() }()

	msg, found, err := repo.GetByPublicID(req.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not read cache")
		return
	}
	if !found {
		if s.writeRefreshMiss(w, api.ShowResponse{Refresh: refreshRes.Meta}, refreshRes) {
			return
		}
		s.writeError(w, http.StatusNotFound, "not_found", "message not found")
		return
	}

	acct := accountByID(cfg, msg.AccountID)
	if acct == nil || !cachedMessageAllowedForAccount(*acct, msg) {
		s.writeError(w, http.StatusNotFound, "not_found", "message not found")
		return
	}

	body := ""
	includeBody := req.Preview
	if includeBody {
		body = msg.BodyText
		body = stripEmbeddedReplies(body)
		maxChars := req.MaxChars
		if maxChars <= 0 {
			maxChars = refresh.DefaultPreviewBytes
		}
		body = truncateText(body, maxChars)
	}

	resp := api.ShowResponse{
		Result: toMessageDetail(msg, body, includeBody),
	}
	status, meta, warnings := readEnvelopeForScope(refreshRes, msg.AccountID, msg.Folder, true)
	resp.Refresh = meta
	s.writeReadOK(w, status, resp, warnings)
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/thread")
		return
	}
	var req api.ThreadRequest
	if err := decodeRequestBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	maxMessages, ok := normalizeThreadLimit(req.MaxMessages)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "bad_request", "max_messages exceeds maximum read window")
		return
	}

	cfg, repo, refreshRes, err := s.refreshAndLoad(r.Context(), defaultLimit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer func() { _ = repo.Close() }()

	seed, found, err := repo.GetByPublicID(req.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not read cache")
		return
	}
	if !found {
		if s.writeRefreshMiss(w, api.ThreadResponse{Refresh: refreshRes.Meta}, refreshRes) {
			return
		}
		s.writeError(w, http.StatusNotFound, "not_found", "message not found")
		return
	}
	if !cachedMessageAllowed(cfg, seed) {
		s.writeError(w, http.StatusNotFound, "not_found", "message not found")
		return
	}
	loadThread := func(batchLimit, batchOffset int) ([]mail.CachedMessage, error) {
		if strings.TrimSpace(seed.ThreadKey) == "" {
			if batchOffset > 0 {
				return nil, nil
			}
			return []mail.CachedMessage{seed}, nil
		}
		return repo.ThreadMessages(seed.AccountID, seed.ThreadKey, batchLimit, batchOffset)
	}
	threadMessages, err := visibleThreadMessages(cfg, loadThread, maxMessages)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not read thread")
		return
	}
	threadMessages = includeSeedMessage(threadMessages, seed, maxMessages)
	results := make([]api.MessageSummary, 0, len(threadMessages))
	for _, msg := range threadMessages {
		results = append(results, toMessageSummary(msg))
	}

	resp := api.ThreadResponse{
		ThreadKey: seed.ThreadKey,
		Messages:  results,
	}
	status, meta, warnings := readEnvelopeForMessages(refreshRes, threadMessages)
	resp.Refresh = meta
	s.writeReadOK(w, status, resp, warnings)
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST for /v1/context")
		return
	}
	var req api.ContextRequest
	if err := decodeRequestBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	budget, ok := normalizeContextBudget(req.Budget)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "bad_request", "budget exceeds maximum context budget")
		return
	}

	cfg, repo, refreshRes, err := s.refreshAndLoad(r.Context(), defaultLimit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer func() { _ = repo.Close() }()

	seed, found, err := repo.GetByPublicID(req.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not read cache")
		return
	}
	if !found {
		if s.writeRefreshMiss(w, api.ContextResponse{Refresh: refreshRes.Meta}, refreshRes) {
			return
		}
		s.writeError(w, http.StatusNotFound, "not_found", "message not found")
		return
	}

	acct := accountByID(cfg, seed.AccountID)
	if acct == nil || !cachedMessageAllowedForAccount(*acct, seed) {
		s.writeError(w, http.StatusNotFound, "not_found", "message not found")
		return
	}

	msgs, err := boundedThreadContext(cfg, seed.ID, budget, threadMessagesLoader(repo, seed))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not read thread")
		return
	}
	timeline := make([]api.MessageSummary, 0, len(msgs))
	var used int
	for _, msg := range msgs {
		if !cachedMessageAllowed(cfg, msg) {
			continue
		}
		item := toMessageSummary(msg)
		if item.ID == seed.ID {
			continue
		}
		if used+len(item.Snippet) > budget {
			break
		}
		used += len(item.Snippet)
		timeline = append(timeline, item)
	}

	seedBody := ""
	if seed.BodyText != "" {
		remaining := budget - used
		if remaining < 0 {
			remaining = 0
		}
		seedBody = stripEmbeddedReplies(seed.BodyText)
		if remaining > 0 {
			seedBody = truncateTextBytes(seedBody, min(defaultContextMax, remaining))
		} else {
			seedBody = ""
		}
	}
	resp := api.ContextResponse{
		Seed:          toMessageDetail(seed, seedBody, true),
		Timeline:      timeline,
		MessageBudget: budget,
	}
	status, meta, warnings := readEnvelopeForMessages(refreshRes, append([]mail.CachedMessage{seed}, msgs...))
	resp.Refresh = meta
	s.writeReadOK(w, status, resp, warnings)
}

func (s *Server) writeReadOK(w http.ResponseWriter, status string, payload any, warnings []refresh.Warning) {
	apiWarnings := make([]api.Warning, 0, len(warnings))
	for _, w := range warnings {
		apiWarnings = append(apiWarnings, api.Warning{Code: w.Code, Message: w.Message})
	}
	env, err := api.OKWithStatus(status, payload, apiWarnings...)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "could not build response")
		return
	}
	if status == api.StatusError {
		env.Error = &api.Error{Code: "remote_refresh_failed", Message: "remote refresh failed and no local cached result was available"}
	}
	s.writeJSON(w, http.StatusOK, env)
}

func visibleMessagePage(
	cfg *config.Config,
	load func(limit, offset int) ([]mail.CachedMessage, error),
	limit int,
	offset int,
) ([]api.MessageSummary, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if offset < 0 {
		offset = 0
	}
	batchSize := max(defaultLimit, limit+offset)
	results := make([]api.MessageSummary, 0, limit)
	visibleSkipped := 0
	rawOffset := 0
	for rawOffset < maxReadWindow {
		remainingWindow := maxReadWindow - rawOffset
		readLimit := min(batchSize, remainingWindow)
		messages, err := load(readLimit, rawOffset)
		if err != nil {
			return nil, err
		}
		for _, msg := range messages {
			if !cachedMessageAllowed(cfg, msg) {
				continue
			}
			if visibleSkipped < offset {
				visibleSkipped++
				continue
			}
			results = append(results, toMessageSummary(msg))
			if len(results) >= limit {
				return results, nil
			}
		}
		if len(messages) < readLimit {
			return results, nil
		}
		rawOffset += len(messages)
	}
	return results, nil
}

func visibleThreadMessages(
	cfg *config.Config,
	load func(limit, offset int) ([]mail.CachedMessage, error),
	limit int,
) ([]mail.CachedMessage, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	batchSize := max(defaultLimit, limit)
	results := make([]mail.CachedMessage, 0, limit)
	rawOffset := 0
	for rawOffset < maxReadWindow {
		remainingWindow := maxReadWindow - rawOffset
		readLimit := min(batchSize, remainingWindow)
		messages, err := load(readLimit, rawOffset)
		if err != nil {
			return nil, err
		}
		for _, msg := range messages {
			if !cachedMessageAllowed(cfg, msg) {
				continue
			}
			results = append(results, msg)
			if len(results) >= limit {
				return results, nil
			}
		}
		if len(messages) < readLimit {
			return results, nil
		}
		rawOffset += len(messages)
	}
	return results, nil
}

func includeSeedMessage(messages []mail.CachedMessage, seed mail.CachedMessage, limit int) []mail.CachedMessage {
	if limit <= 0 {
		limit = defaultLimit
	}
	for _, msg := range messages {
		if msg.ID == seed.ID {
			return messages
		}
	}
	out := make([]mail.CachedMessage, 0, min(limit, len(messages)+1))
	out = append(out, seed)
	for _, msg := range messages {
		if len(out) >= limit {
			break
		}
		out = append(out, msg)
	}
	return out
}

func boundedThreadContext(
	cfg *config.Config,
	seedID string,
	budget int,
	load func(limit, offset int) ([]mail.CachedMessage, error),
) ([]mail.CachedMessage, error) {
	if budget <= 0 {
		budget = defaultBudget
	}
	batchSize := defaultLimit
	out := make([]mail.CachedMessage, 0, defaultLimit)
	rawOffset := 0
	used := 0
	for rawOffset < maxReadWindow {
		remainingWindow := maxReadWindow - rawOffset
		messages, err := load(min(batchSize, remainingWindow), rawOffset)
		if err != nil {
			return nil, err
		}
		for _, msg := range messages {
			if !cachedMessageAllowed(cfg, msg) || msg.ID == seedID {
				continue
			}
			if used+len(msg.Snippet) > budget {
				return out, nil
			}
			used += len(msg.Snippet)
			out = append(out, msg)
		}
		if len(messages) < batchSize {
			return out, nil
		}
		rawOffset += len(messages)
	}
	return out, nil
}

func normalizeThreadLimit(requestedLimit int) (int, bool) {
	limit := withDefault(0, requestedLimit, defaultLimit)
	if limit > maxReadWindow {
		return 0, false
	}
	return limit, true
}

func normalizeContextBudget(requestedBudget int) (int, bool) {
	budget := withDefault(0, requestedBudget, defaultBudget)
	if budget > maxContextBudget {
		return 0, false
	}
	return budget, true
}

func normalizeReadPage(configDefault, requestedLimit, requestedOffset int) (int, int, bool) {
	limit := effectiveReadLimit(configDefault, requestedLimit)
	offset := max(0, requestedOffset)
	if limit > maxReadWindow || offset > maxReadWindow-limit {
		return 0, 0, false
	}
	return limit, offset, true
}

func threadMessagesLoader(repo *cache.Repository, seed mail.CachedMessage) func(int, int) ([]mail.CachedMessage, error) {
	return func(batchLimit, batchOffset int) ([]mail.CachedMessage, error) {
		if strings.TrimSpace(seed.ThreadKey) == "" {
			if batchOffset > 0 {
				return nil, nil
			}
			return []mail.CachedMessage{seed}, nil
		}
		return repo.ThreadMessages(seed.AccountID, seed.ThreadKey, batchLimit, batchOffset)
	}
}

func readEnvelopeForScope(refreshRes refresh.Result, requestedAccount, requestedFolder string, localFound bool) (string, cache.RefreshMeta, []refresh.Warning) {
	if !refreshRes.Partial || refreshAffectsScope(refreshRes.Warnings, requestedAccount, requestedFolder) {
		warnings := warningsForScope(refreshRes.Warnings, requestedAccount, requestedFolder)
		meta := scopedRefreshMeta(refreshRes.Meta, warnings)
		return api.ReadStatus(meta, refreshRes.Partial, localFound), meta, warnings
	}
	meta := refreshRes.Meta
	meta.RemoteOK = true
	return api.ReadStatus(meta, false, localFound), meta, nil
}

func readEnvelopeForMessages(refreshRes refresh.Result, messages []mail.CachedMessage) (string, cache.RefreshMeta, []refresh.Warning) {
	if len(messages) == 0 {
		return readEnvelopeForScope(refreshRes, "", "", false)
	}
	affected := false
	for _, msg := range messages {
		if refreshAffectsScope(refreshRes.Warnings, msg.AccountID, msg.Folder) {
			affected = true
			break
		}
	}
	if affected {
		warnings := warningsForMessages(refreshRes.Warnings, messages)
		meta := scopedRefreshMeta(refreshRes.Meta, warnings)
		return api.ReadStatus(meta, refreshRes.Partial, true), meta, warnings
	}
	meta := refreshRes.Meta
	meta.RemoteOK = true
	return api.ReadStatus(meta, false, true), meta, nil
}

func scopedRefreshMeta(meta cache.RefreshMeta, warnings []refresh.Warning) cache.RefreshMeta {
	if len(warnings) == 1 && warnings[0].LastSuccessfulRefreshAt != nil {
		meta.LastSuccessfulRefreshAt = warnings[0].LastSuccessfulRefreshAt
	}
	if len(warnings) == 1 && warnings[0].LastSuccessfulRefreshAt == nil {
		meta.LastSuccessfulRefreshAt = nil
	}
	return meta
}

func refreshAffectsScope(warnings []refresh.Warning, requestedAccount, requestedFolder string) bool {
	account := accountFilter(requestedAccount)
	folder := strings.TrimSpace(requestedFolder)
	for _, warning := range warnings {
		if warning.AccountID == "" {
			return true
		}
		if account != "" && warning.AccountID != account {
			continue
		}
		if folder != "" && warning.Folder != "" && warning.Folder != folder {
			continue
		}
		return true
	}
	return len(warnings) == 0
}

func warningsForScope(warnings []refresh.Warning, requestedAccount, requestedFolder string) []refresh.Warning {
	account := accountFilter(requestedAccount)
	folder := strings.TrimSpace(requestedFolder)
	out := make([]refresh.Warning, 0, len(warnings))
	for _, warning := range warnings {
		if warning.AccountID != "" && account != "" && warning.AccountID != account {
			continue
		}
		if warning.Folder != "" && folder != "" && warning.Folder != folder {
			continue
		}
		out = append(out, warning)
	}
	return out
}

func warningsForMessages(warnings []refresh.Warning, messages []mail.CachedMessage) []refresh.Warning {
	out := make([]refresh.Warning, 0, len(warnings))
	for _, warning := range warnings {
		if warning.AccountID == "" {
			out = append(out, warning)
			continue
		}
		for _, msg := range messages {
			if warning.AccountID != msg.AccountID {
				continue
			}
			if warning.Folder != "" && warning.Folder != msg.Folder {
				continue
			}
			out = append(out, warning)
			break
		}
	}
	return out
}

func effectiveReadLimit(configDefault, requestedLimit int) int {
	if requestedLimit > 0 {
		return requestedLimit
	}
	limit := withDefault(configDefault, 0, defaultLimit)
	if limit > maxReadWindow {
		return maxReadWindow
	}
	return limit
}

func (s *Server) refreshAndLoad(ctx context.Context, minCandidates int) (*config.Config, *cache.Repository, refresh.Result, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, nil, refresh.Result{}, err
	}
	return s.refreshAndLoadConfig(ctx, cfg, minCandidates)
}

func (s *Server) refreshAndLoadConfig(ctx context.Context, cfg *config.Config, minCandidates int) (*config.Config, *cache.Repository, refresh.Result, error) {
	repo, err := s.openRepository()
	if err != nil {
		return nil, nil, refresh.Result{}, err
	}
	keepRepo := false
	defer func() {
		if !keepRepo {
			_ = repo.Close()
		}
	}()
	if err := cleanupExpired(repo, cfg); err != nil {
		return nil, nil, refresh.Result{}, err
	}

	src, err := s.sourceForConfig(ctx, cfg)
	if err != nil {
		return nil, nil, refresh.Result{}, err
	}
	if src == nil {
		keepRepo = true
		return cfg, repo, refresh.Result{
			Meta: cache.RefreshMeta{
				Attempted:               false,
				RemoteOK:                false,
				LastSuccessfulRefreshAt: repo.LatestRefreshAt(),
			},
			Partial: true,
			Warnings: []refresh.Warning{{
				Code:    "remote_source_unavailable",
				Message: "remote refresh source is unavailable",
			}},
		}, nil
	}

	res, err := refresh.Refresh(ctx, cfg, repo, src, refresh.RefreshOptions{
		Now:           time.Now,
		PreviewBytes:  refresh.DefaultPreviewBytes,
		MaxCandidates: min(max(effectiveReadLimit(cfg.Mail.DefaultLimit, 0), minCandidates), maxReadWindow),
	})
	if err != nil {
		return nil, nil, refresh.Result{}, err
	}
	keepRepo = true
	return cfg, repo, res, nil
}

func (s *Server) writeRefreshMiss(w http.ResponseWriter, payload any, refreshRes refresh.Result) bool {
	status := api.ReadStatus(refreshRes.Meta, refreshRes.Partial, false)
	if status != api.StatusError {
		return false
	}
	s.writeReadOK(w, status, payload, refreshRes.Warnings)
	return true
}

func (s *Server) sourceForConfig(ctx context.Context, cfg *config.Config) (source, error) {
	if s.opts.SourceFactory == nil {
		return nil, nil
	}
	return s.opts.SourceFactory(ctx, cfg)
}

func (s *Server) probeSourceForConfig(ctx context.Context, cfg *config.Config) (source, error) {
	if s.opts.ProbeSourceFactory == nil {
		return nil, nil
	}
	return s.opts.ProbeSourceFactory(ctx, cfg)
}

func (s *Server) probeTimeout() time.Duration {
	if s.opts.ProbeTimeout > 0 {
		return s.opts.ProbeTimeout
	}
	return defaultProbeBudget
}

func (s *Server) openRepository() (*cache.Repository, error) {
	if err := cache.SeedFromStateDir(s.opts.StateDir); err != nil {
		return nil, err
	}
	path := filepath.Join(s.opts.StateDir, "mail.db")
	repo, err := cache.NewRepository(cache.DBOptions{Path: path})
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func (s *Server) loadConfig() (*config.Config, error) {
	raw, err := os.ReadFile(s.opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
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

func decodeRequestBody(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("missing request body")
	}
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(out); err != nil {
		return errors.New("malformed json")
	}
	return nil
}

func withDefault(cfgLimit, explicit, fallback int) int {
	if explicit > 0 {
		return explicit
	}
	if cfgLimit > 0 {
		return cfgLimit
	}
	return fallback
}

func accountFilter(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "all") {
		return ""
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncateText(s string, maxChars int) string {
	if maxChars <= 0 {
		return s
	}
	for i := range s {
		if maxChars == 0 {
			return s[:i]
		}
		maxChars--
	}
	return s
}

func truncateTextBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	last := 0
	for i := range s {
		if i > maxBytes {
			return s[:last]
		}
		if i == maxBytes {
			return s[:i]
		}
		last = i
	}
	return s[:last]
}

func cleanupExpired(repo *cache.Repository, cfg *config.Config) error {
	ttl := 90 * 24 * time.Hour
	if strings.TrimSpace(cfg.Mail.CacheTTL) != "" {
		parsed, err := config.ParseTTL(cfg.Mail.CacheTTL)
		if err != nil {
			return err
		}
		ttl = parsed
	}
	return repo.CleanupExpired(ttl)
}

func accountByID(cfg *config.Config, id string) *config.Account {
	for i := range cfg.Mail.Accounts {
		if cfg.Mail.Accounts[i].ID == id {
			return &cfg.Mail.Accounts[i]
		}
	}
	return nil
}

var errProbeCredentialMissing = errors.New("probe credential missing")

func (s *Server) verifyProbeCredential(acct config.Account) error {
	store, err := secrets.Load(s.opts.SecretsPath)
	if err != nil {
		return err
	}
	if _, ok := store.Resolve(acct.PasswordRef.ID); !ok {
		return errProbeCredentialMissing
	}
	return nil
}

func probeCredentialErrorCode(err error) string {
	switch {
	case errors.Is(err, errProbeCredentialMissing):
		return "credential_missing"
	case errors.Is(err, os.ErrNotExist):
		return "secrets_missing"
	case errors.Is(err, secrets.ErrUnsafePermissions):
		return "secrets_unsafe_permissions"
	case errors.Is(err, secrets.ErrUnsupportedVersion):
		return "secrets_bad_version"
	case errors.Is(err, secrets.ErrMalformed):
		return "secrets_malformed"
	default:
		return "secrets_unreadable"
	}
}

func toMessageSummary(m mail.CachedMessage) api.MessageSummary {
	return api.MessageSummary{
		ID:            m.ID,
		Account:       m.AccountID,
		Folder:        m.Folder,
		Subject:       m.Subject,
		From:          m.From,
		To:            m.To,
		Cc:            m.Cc,
		Flags:         m.Flags,
		ThreadKey:     m.ThreadKey,
		Date:          m.Date,
		Snippet:       m.Snippet,
		VisibleReason: m.VisibleReason,
	}
}

func cachedMessageAllowed(cfg *config.Config, msg mail.CachedMessage) bool {
	acct := accountByID(cfg, msg.AccountID)
	if acct == nil {
		return false
	}
	return cachedMessageAllowedForAccount(*acct, msg)
}

func cachedMessageAllowedForAccount(acct config.Account, msg mail.CachedMessage) bool {
	if !accountAllowsFolder(acct, msg.Folder) {
		return false
	}
	if acct.Policy != config.PolicyDomain {
		return true
	}
	ok, _ := policy.DomainMatch(acct, mail.MessageEnvelope{
		UID:       msg.UID,
		MessageID: msg.MessageID,
		ThreadKey: msg.ThreadKey,
		Subject:   msg.Subject,
		From:      msg.From,
		To:        msg.To,
		Cc:        msg.Cc,
		Bcc:       msg.Bcc,
		Date:      msg.Date,
		Flags:     msg.Flags,
		Snippet:   msg.Snippet,
	})
	return ok
}

func accountAllowsFolder(acct config.Account, folder string) bool {
	for _, allowed := range acct.Folders {
		if allowed == folder {
			return true
		}
	}
	return false
}

func toMessageDetail(m mail.CachedMessage, body string, includeBody bool) api.MessageDetail {
	item := api.MessageDetail{
		MessageSummary: toMessageSummary(m),
		HasAttachments: m.HasAttachments,
	}
	if includeBody {
		item.BodyText = body
	}
	return item
}

func stripEmbeddedReplies(body string) string {
	if body == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if isReplyBoundary(strings.TrimSpace(line)) {
			break
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isReplyBoundary(line string) bool {
	return strings.HasPrefix(line, "On ") && strings.Contains(line, "wrote:")
}
