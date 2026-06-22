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
