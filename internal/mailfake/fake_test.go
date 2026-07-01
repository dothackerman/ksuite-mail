package mailfake

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

func TestAdapterRecordsSearchBeforeFetchOrder(t *testing.T) {
	acct := config.Account{
		ID: "acct",
	}
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{
					UID: 1,
					Envelope: mail.MessageEnvelope{
						UID:     1,
						From:    "alice@regenerativ.ch",
						Subject: "hello",
					},
					VisibleByPolicy: true,
				},
			},
		},
	})
	_, _ = a.SelectFolder(context.Background(), acct, "INBOX")
	_, _ = a.SearchAllowed(context.Background(), acct, "INBOX", "From", "regenerativ.ch", mail.UIDRange{})
	_, _ = a.FetchHeaders(context.Background(), acct, "INBOX", []mail.UID{1})
	_, _ = a.FetchEnvelopes(context.Background(), acct, "INBOX", []mail.UID{1})

	calls := a.CallsSnapshot()
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	if calls[1].Method != methodSearch || calls[2].Method != methodHeaders || calls[3].Method != methodFetch {
		t.Fatalf("expected search before headers before fetch, got %#v", calls)
	}
}

func TestAdapterSupportsFailureInjectionAndStateMutations(t *testing.T) {
	acct := config.Account{ID: "acct"}
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{UID: 1, Envelope: mail.MessageEnvelope{UID: 1}, VisibleByPolicy: true},
				{UID: 2, Envelope: mail.MessageEnvelope{UID: 2}, VisibleByPolicy: true},
			},
		},
	}, Failure{When: "list:acct:INBOX", Err: errors.New("remote down")})

	a.SetFailure(methodSelect, "acct", "INBOX", "", errors.New("auth fail"))
	if _, err := a.SelectFolder(context.Background(), acct, "INBOX"); err == nil {
		t.Fatal("expected select failure")
	}

	a.DeleteMessage("acct", "INBOX", 1)
	if _, err := a.FetchEnvelopes(context.Background(), acct, "INBOX", []mail.UID{1}); err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}

	a.MoveMessage("acct", "INBOX", "Sent", 2)
	uids, err := a.ListUIDs(context.Background(), acct, "Sent", mail.UIDRange{})
	if err != nil {
		t.Fatalf("ListUIDs moved folder: %v", err)
	}
	if len(uids) != 1 || uids[0] != 2 {
		t.Fatalf("moved uids = %#v", uids)
	}
}

func TestAdapterSupportsSearchFalsePositives(t *testing.T) {
	acct := config.Account{ID: "acct"}
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{
					UID:             1,
					Body:            "search payload mention",
					VisibleByPolicy: false,
					SearchByHeader: map[string][]mail.UID{
						"from:other.com": {1},
					},
				},
			},
		},
	})
	a.SetSearchResult("acct", "INBOX", "From", "fake.com", []mail.UID{1})
	got, err := a.SearchAllowed(context.Background(), acct, "INBOX", "From", "fake.com", mail.UIDRange{})
	if err != nil {
		t.Fatalf("SearchAllowed: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("search result = %#v", got)
	}

	res, err := a.FetchEnvelopes(context.Background(), acct, "INBOX", got)
	if err != nil {
		t.Fatalf("FetchEnvelopes: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1", len(res))
	}
	if res[0].UID != 1 {
		t.Fatalf("uid = %d, want 1", res[0].UID)
	}
}

func TestAdapterSearchResultCanBeOverriddenByHeader(t *testing.T) {
	acct := config.Account{ID: "acct"}
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{
					UID:             1,
					Envelope:        mail.MessageEnvelope{UID: 1, From: "foo@other.com"},
					VisibleByPolicy: false,
				},
			},
		},
	})
	a.SetSearchResult("acct", "INBOX", "From", "regenerativ.ch", []mail.UID{99})
	got, err := a.SearchAllowed(context.Background(), acct, "INBOX", "From", "regenerativ.ch", mail.UIDRange{})
	if err != nil {
		t.Fatalf("SearchAllowed: %v", err)
	}
	if len(got) != 1 || got[0] != 99 {
		t.Fatalf("got %#v", got)
	}
}

func TestAdapterUIDVALIDITYCanBeReset(t *testing.T) {
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{UID: 10, VisibleByPolicy: true},
			},
		},
	})
	a.SetUIDVALIDITY("acct", "INBOX", 777)
	state, err := a.SelectFolder(context.Background(), config.Account{ID: "acct"}, "INBOX")
	if err != nil {
		t.Fatalf("SelectFolder: %v", err)
	}
	if state.UIDVALIDITY != 777 {
		t.Fatalf("uidvalidity = %d, want 777", state.UIDVALIDITY)
	}
	if !state.ReadOnly {
		t.Fatalf("read-only = false, want true")
	}
	if state.SelectionMode != "examine" {
		t.Fatalf("selection mode = %q, want examine", state.SelectionMode)
	}
}

func TestAdapterResetCallsClearsHistory(t *testing.T) {
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {{UID: 1, VisibleByPolicy: true}},
		},
	})
	a.ResetCalls()
	a.appendCall("x", "a", "b", "p")
	if len(a.CallsSnapshot()) != 1 {
		t.Fatalf("calls = %d, want 1", len(a.CallsSnapshot()))
	}
	a.ResetCalls()
	if len(a.CallsSnapshot()) != 0 {
		t.Fatalf("calls after reset = %d, want 0", len(a.CallsSnapshot()))
	}
}

func TestAdapterFetchBodyRespectsMaxBytes(t *testing.T) {
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{
					UID:             1,
					Body:            "0123456789",
					VisibleByPolicy: true,
					Envelope:        mail.MessageEnvelope{UID: 1},
				},
			},
		},
	})
	body, err := a.FetchBodyPreview(context.Background(), config.Account{ID: "acct"}, "INBOX", 1, 4)
	if err != nil {
		t.Fatalf("FetchBodyPreview: %v", err)
	}
	want := "0123"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestAdapterProvidesDeterministicUIDOrdering(t *testing.T) {
	a := NewAdapter(map[string]map[string][]Message{
		"acct": {
			"INBOX": {
				{UID: 5},
				{UID: 2},
				{UID: 9},
			},
		},
	})
	uids, err := a.ListUIDs(context.Background(), config.Account{ID: "acct"}, "INBOX", mail.UIDRange{})
	if err != nil {
		t.Fatalf("ListUIDs: %v", err)
	}
	if got := fmt.Sprintf("%v", uids); got != "[2 5 9]" {
		t.Fatalf("uids = %s, want [2 5 9]", got)
	}
}
