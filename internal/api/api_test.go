package api_test

import (
	"encoding/json"
	"strings"
	"testing"

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
