// Package api defines the stable v1 local API contract spoken over the Unix
// domain socket (ARCH-CON-006, ADR-003): a JSON envelope plus the safe,
// content-free payload types the CLI decodes.
//
// Envelope intentionally mirrors the documented stable shape in NFR-ERR-002:
// a top-level status, an optional command payload, optional warnings, and an
// optional structured error. The payload is carried in a generic `result`
// container (raw JSON) so each endpoint can return its own typed body without
// the contract growing one field per command. Mail-specific fields such as
// `refresh` and list `results` arrive with the read-only command surface in a
// later slice; this slice serves only health and doctor.
//
// Nothing in this package may carry credentials, message subjects, bodies,
// attachment names, or raw provider text (NFR-SEC-005, NFR-ERR-002).
package api

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/cache"
)

// Allowed high-level envelope statuses (NFR-ERR-002).
const (
	StatusOK      = "ok"
	StatusOKStale = "ok_stale"
	StatusPartial = "partial"
	StatusError   = "error"
)

// Per-check outcomes for diagnostics.
const (
	CheckPass = "pass"
	CheckFail = "fail"
	CheckWarn = "warn"
	CheckSkip = "skip"
)

// Provider probe aggregate statuses and per-check outcomes.
const (
	ProbeStatusPassed        = "passed"
	ProbeStatusFailed        = "failed"
	ProbeStatusInconclusive  = "inconclusive"
	ProbeStatusNotApplicable = "not_applicable"
)

// Warning is a non-fatal, safe-to-display advisory.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is a structured, content-free failure description. It is specific
// enough for the CLI to print a useful diagnostic but must never include
// credentials or private mail content (NFR-ERR-002).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Envelope is the stable wrapper for every daemon response.
type Envelope struct {
	Status   string          `json:"status"`
	Result   json.RawMessage `json:"result,omitempty"`
	Warnings []Warning       `json:"warnings,omitempty"`
	Error    *Error          `json:"error,omitempty"`
}

// OK builds a success envelope carrying result as its payload.
func OK(result any) (Envelope, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode result: %w", err)
	}
	return Envelope{Status: StatusOK, Result: raw}, nil
}

// Err builds an error envelope with a structured, content-free error.
func Err(code, message string) Envelope {
	return Envelope{Status: StatusError, Error: &Error{Code: code, Message: message}}
}

// DecodeResult unmarshals the envelope's result payload into v.
func (e Envelope) DecodeResult(v any) error {
	if len(e.Result) == 0 {
		return fmt.Errorf("envelope has no result payload")
	}
	return json.Unmarshal(e.Result, v)
}

// Check is one diagnostic outcome. Name and Code are stable identifiers; Detail
// is a human-readable, content-free explanation.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Code   string `json:"code,omitempty"`
}

// DoctorReport is the payload of POST /v1/doctor. OK is true only when no check
// failed (skips and warnings do not fail the report).
type DoctorReport struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// HealthInfo is the payload of GET /v1/health: a liveness signal only.
type HealthInfo struct {
	Status string `json:"status"`
}

// RefreshMeta mirrors cache refresh status for command responses.
type RefreshMeta = cache.RefreshMeta

// MessageSummary is a compact, account-safe cache read payload.
type MessageSummary struct {
	ID            string    `json:"id"`
	Account       string    `json:"account"`
	Folder        string    `json:"folder"`
	Subject       string    `json:"subject"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	Cc            string    `json:"cc"`
	Flags         string    `json:"flags"`
	ThreadKey     string    `json:"thread_key"`
	Date          time.Time `json:"date"`
	Snippet       string    `json:"snippet"`
	VisibleReason string    `json:"visible_reason"`
}

// MessageDetail adds preview text to MessageSummary.
type MessageDetail struct {
	MessageSummary
	BodyText        string `json:"body"`
	HasAttachments  bool   `json:"has_attachments"`
	ContentTypeHint string `json:"content_type_hint,omitempty"`
}

// ListRequest defines /v1/list payload.
type ListRequest struct {
	Account string `json:"account"`
	Folder  string `json:"folder"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

// SearchRequest defines /v1/search payload.
type SearchRequest struct {
	Account string `json:"account"`
	Folder  string `json:"folder"`
	Query   string `json:"query"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

// ShowRequest defines /v1/show payload.
type ShowRequest struct {
	ID       string `json:"id"`
	Preview  bool   `json:"preview"`
	MaxChars int    `json:"max_chars"`
}

// ThreadRequest defines /v1/thread payload.
type ThreadRequest struct {
	ID          string `json:"id"`
	Brief       bool   `json:"brief"`
	MaxMessages int    `json:"max_messages"`
}

// ContextRequest defines /v1/context payload.
type ContextRequest struct {
	ID     string `json:"id"`
	Budget int    `json:"budget"`
}

// ProbeIMAPRequest defines /v1/probe/imap payload. Account is mandatory and
// must name an existing daemon-side account.
type ProbeIMAPRequest struct {
	Account string `json:"account"`
}

// ListResponse is returned by /v1/list.
type ListResponse struct {
	Results []MessageSummary `json:"results"`
	Refresh RefreshMeta      `json:"refresh"`
}

// SearchResponse is returned by /v1/search.
type SearchResponse struct {
	Results []MessageSummary `json:"results"`
	Refresh RefreshMeta      `json:"refresh"`
}

// ShowResponse is returned by /v1/show.
type ShowResponse struct {
	Result  MessageDetail `json:"result"`
	Refresh RefreshMeta   `json:"refresh"`
}

// ThreadResponse is returned by /v1/thread.
type ThreadResponse struct {
	ThreadKey string           `json:"thread_key"`
	Messages  []MessageSummary `json:"messages"`
	Refresh   RefreshMeta      `json:"refresh"`
}

// ContextResponse is returned by /v1/context.
type ContextResponse struct {
	Seed          MessageDetail    `json:"seed"`
	Timeline      []MessageSummary `json:"timeline"`
	Refresh       RefreshMeta      `json:"refresh"`
	MessageBudget int              `json:"message_budget"`
}

// ProbeIMAPResponse is returned by /v1/probe/imap. It is intentionally limited
// to sanitized fixed-checklist facts: no credentials, raw IMAP text, provider
// error text, message content, headers, subjects, bodies, or attachment names.
type ProbeIMAPResponse struct {
	Account string       `json:"account"`
	Status  string       `json:"status"`
	Checks  []ProbeCheck `json:"checks"`
}

// ProbeCheck is one fixed provider-probe checklist outcome. Status values are
// stable strings such as passed, failed, inconclusive, and not_applicable.
type ProbeCheck struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Code   string      `json:"code,omitempty"`
	Detail string      `json:"detail,omitempty"`
	Facts  *ProbeFacts `json:"facts,omitempty"`
}

// ProbeFacts carries safe, structured provider-probe facts. It deliberately
// uses typed scalar fields instead of an open map so arbitrary provider text
// cannot be threaded into diagnostics by accident.
type ProbeFacts struct {
	FolderCount            *int     `json:"folder_count,omitempty"`
	Folders                []string `json:"folders,omitempty"`
	Folder                 string   `json:"folder,omitempty"`
	ReadOnly               *bool    `json:"read_only,omitempty"`
	SelectionMode          string   `json:"selection_mode,omitempty"`
	CondstoreSupported     *bool    `json:"condstore_supported,omitempty"`
	HighestModSeqAvailable *bool    `json:"highestmodseq_available,omitempty"`
	UIDRangeSupported      *bool    `json:"uid_range_supported,omitempty"`
	RefreshStrategy        string   `json:"refresh_strategy,omitempty"`
	UIDVALIDITY            *uint64  `json:"uidvalidity,omitempty"`
	UIDNEXT                *uint64  `json:"uidnext,omitempty"`
	HighestModSeq          *int64   `json:"highestmodseq,omitempty"`
}

// ReadStatus determines which top-level status applies to read responses.
func ReadStatus(meta cache.RefreshMeta, partial bool, localFound bool) string {
	if partial {
		if localFound {
			return StatusOKStale
		}
		return StatusError
	}
	if meta.Attempted && !meta.RemoteOK {
		if localFound {
			return StatusOKStale
		}
		return StatusError
	}
	return StatusOK
}

// OKWithStatus builds a success envelope with a custom top-level status.
func OKWithStatus(status string, result any, warnings ...Warning) (Envelope, error) {
	if status == "" {
		status = StatusOK
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode result: %w", err)
	}
	return Envelope{Status: status, Result: raw, Warnings: warnings}, nil
}
