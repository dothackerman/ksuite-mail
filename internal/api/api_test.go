package api_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
)

func TestOKEnvelopeCarriesResultPayload(t *testing.T) {
	report := api.DoctorReport{OK: true, Checks: []api.Check{{Name: "config_parse", Status: api.CheckPass}}}

	env, err := api.OK(report)
	if err != nil {
		t.Fatalf("OK: %v", err)
	}
	if env.Status != api.StatusOK {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOK)
	}

	var got api.DoctorReport
	if err := env.DecodeResult(&got); err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if !got.OK || len(got.Checks) != 1 || got.Checks[0].Name != "config_parse" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestErrEnvelopeHasNoResult(t *testing.T) {
	env := api.Err("daemon_unreachable", "could not connect to the daemon socket")

	if env.Status != api.StatusError {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusError)
	}
	if env.Error == nil || env.Error.Code != "daemon_unreachable" {
		t.Fatalf("error info missing or wrong: %+v", env.Error)
	}
	if env.Result != nil {
		t.Fatalf("error envelope must not carry a result, got %s", env.Result)
	}
}

func TestEmptyOptionalFieldsAreOmitted(t *testing.T) {
	env := api.Envelope{Status: api.StatusOK}

	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{"result", "warnings", "error"} {
		if strings.Contains(s, key) {
			t.Fatalf("expected %q to be omitted, got %s", key, s)
		}
	}
	if !strings.Contains(s, `"status":"ok"`) {
		t.Fatalf("status missing from %s", s)
	}
}

func TestReadStatusMapsRemoteFallbackToStale(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	meta := api.RefreshMeta{
		Attempted:               true,
		RemoteOK:                false,
		LastSuccessfulRefreshAt: &now,
	}
	if got := api.ReadStatus(meta, false, true); got != api.StatusOKStale {
		t.Fatalf("ReadStatus = %q, want %q", got, api.StatusOKStale)
	}
	if got := api.ReadStatus(meta, false, false); got != api.StatusError {
		t.Fatalf("ReadStatus no local = %q, want %q", got, api.StatusError)
	}
}

func TestOKWithStatusHonorsWarnings(t *testing.T) {
	env, err := api.OKWithStatus(api.StatusOKStale, api.ListResponse{
		Refresh: api.RefreshMeta{Attempted: true},
	}, api.Warning{Code: "remote_source_error", Message: "simulated"})
	if err != nil {
		t.Fatalf("OKWithStatus: %v", err)
	}
	if env.Status != api.StatusOKStale {
		t.Fatalf("status = %q, want %q", env.Status, api.StatusOKStale)
	}
	if len(env.Warnings) != 1 || env.Warnings[0].Code != "remote_source_error" {
		t.Fatalf("warning = %#v", env.Warnings)
	}
}
