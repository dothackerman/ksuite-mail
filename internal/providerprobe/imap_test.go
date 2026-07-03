package providerprobe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/mailfake"
	"github.com/dothackerman/ksuite-mail/internal/providerprobe"
)

func TestRunnerRunIMAPChecklistMatrix(t *testing.T) {
	tests := []struct {
		name                   string
		account                config.Account
		source                 mail.Source
		want                   map[string]api.ProbeCheck
		wantOrder              []string
		wantCalls              []string
		wantStatus             string
		wantReadStatePreserved *bool
	}{
		{
			name:    "capability failure skips dependent checks",
			account: domainAccount("INBOX", "regenerativ.ch"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "capability", account: "rs_info", err: errors.New("NO auth failed with pw and private text")},
			),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"capability":           {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
				"folder_listing":       {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"folder_selection":     {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"uid_behavior":         {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
			},
			wantCalls: []string{"capability"},
		},
		{
			name:    "folder listing failure skips dependent checks",
			account: domainAccount("INBOX", "regenerativ.ch"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "folders", account: "rs_info", err: errors.New("LIST failed with provider text")},
			),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"capability":           {Status: api.ProbeStatusPassed, Code: "capability_ok"},
				"folder_listing":       {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
				"folder_selection":     {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"uid_behavior":         {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
			},
			wantCalls: []string{"capability", "folders"},
		},
		{
			name:       "empty folder list yields fixture required",
			account:    domainAccount("INBOX", "regenerativ.ch"),
			source:     probeAdapter(map[string][]mailfake.Message{}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"folder_listing":       {Status: api.ProbeStatusInconclusive, Code: "fixture_required", Detail: "folder_count=0"},
				"folder_selection":     {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"uid_behavior":         {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
			},
			wantCalls: []string{"capability", "folders"},
		},
		{
			name:    "folder selection failure skips dependent checks",
			account: domainAccount("INBOX", "regenerativ.ch"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "select", account: "rs_info", folder: "INBOX", err: errors.New("EXAMINE failed with private text")},
			),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"folder_selection":     {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
				"uid_behavior":         {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
			},
			wantCalls: []string{"capability", "folders", "select"},
		},
		{
			name:       "nil source returns sanitized unavailable checks",
			account:    domainAccount("INBOX", "regenerativ.ch"),
			source:     nil,
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"capability":           {Status: api.ProbeStatusFailed, Code: "source_unavailable", Detail: "mail source is unavailable"},
				"folder_listing":       {Status: api.ProbeStatusFailed, Code: "source_unavailable", Detail: "mail source is unavailable"},
				"folder_selection":     {Status: api.ProbeStatusFailed, Code: "source_unavailable", Detail: "mail source is unavailable"},
				"uid_behavior":         {Status: api.ProbeStatusFailed, Code: "source_unavailable", Detail: "mail source is unavailable"},
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "fixture_required", Detail: "BODY.PEEK read-state fixture is required"},
			},
		},
		{
			name:       "missing configured folder yields no configured folder outcomes",
			account:    fullAccount(" "),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"folder_selection":     {Status: api.ProbeStatusInconclusive, Code: "no_configured_folder"},
				"uid_behavior":         {Status: api.ProbeStatusInconclusive, Code: "no_configured_folder"},
				"domain_header_search": {Status: api.ProbeStatusNotApplicable, Code: "full_policy"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "no_configured_folder"},
			},
			wantCalls: []string{"capability", "folders"},
		},
		{
			name:       "uid state absence requires fixture state",
			account:    fullAccount("INBOX"),
			source:     uidStateSource(0, 11),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"folder_selection":     {Status: api.ProbeStatusInconclusive, Code: "uid_state_required"},
				"uid_behavior":         {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
				"domain_header_search": {Status: api.ProbeStatusNotApplicable, Code: "full_policy"},
				"read_state":           {Status: api.ProbeStatusInconclusive, Code: "prerequisite_failed"},
			},
		},
		{
			name:       "full policy domain check is not applicable",
			account:    fullAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true, Body: "body"}}}),
			wantStatus: api.ProbeStatusPassed,
			want: map[string]api.ProbeCheck{
				"domain_header_search": {Status: api.ProbeStatusNotApplicable, Code: "full_policy"},
			},
		},
		{
			name:       "domain policy missing fixtures is inconclusive",
			account:    domainAccount("INBOX", "regenerativ.ch"),
			source:     domainFixtureSource(map[string][]mail.UID{"From": {10}}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "fixture_required"},
			},
		},
		{
			name:       "domain policy with no domains is inconclusive",
			account:    domainAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true, Body: "body"}}}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "no_domain", Detail: "domain-policy account has no configured domain"},
			},
		},
		{
			name:       "domain policy with only whitespace domains is inconclusive",
			account:    domainAccount("INBOX", " ", "\t"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true, Body: "body"}}}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"domain_header_search": {Status: api.ProbeStatusInconclusive, Code: "no_domain", Detail: "domain-policy account has no configured domain"},
			},
		},
		{
			name:       "provider errors never expose raw provider text",
			account:    fullAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}}, sourceFailure{method: "list", account: "rs_info", folder: "INBOX", err: errors.New("UID SEARCH leaked info@example.com pw private text")}),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"uid_behavior": {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
			},
		},
		{
			name:    "domain search errors never expose raw provider text",
			account: domainAccount("INBOX", "regenerativ.ch"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "search", account: "rs_info", folder: "INBOX", arg: "from:regenerativ.ch", err: errors.New("UID SEARCH leaked info@example.com pw private text")},
			),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"domain_header_search": {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
			},
		},
		{
			name:       "permission errors map to sanitized permission denied code",
			account:    fullAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}}, sourceFailure{method: "list", account: "rs_info", folder: "INBOX", err: os.ErrPermission}),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"uid_behavior": {Status: api.ProbeStatusFailed, Code: "permission_denied", Detail: "provider probe failed"},
			},
		},
		{
			name:    "uid behavior failures do not force unsupported strategy",
			account: fullAccount("INBOX"),
			source: func() mail.Source {
				source := sourceNoCondstore{
					Adapter: probeAdapter(
						map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
						sourceFailure{method: "list", account: "rs_info", folder: "INBOX", err: errors.New("uid behavior probe failed")},
					),
				}
				source.SetUIDState("rs_info", "INBOX", 777, 21)
				source.SetHighestModSeq("rs_info", "INBOX", 0)
				return source
			}(),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"uid_behavior":     {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
				"refresh_strategy": {Status: api.ProbeStatusInconclusive, Code: "strategy_inconclusive"},
			},
		},
		{
			name:       "failed status outranks inconclusive and passed",
			account:    fullAccount("INBOX"),
			source:     rangeIgnoringSource{Source: probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}})},
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"uid_behavior":         {Status: api.ProbeStatusFailed, Code: "uid_range_mismatch"},
				"domain_header_search": {Status: api.ProbeStatusNotApplicable, Code: "full_policy"},
			},
		},
		{
			name:       "inconclusive status outranks passed",
			account:    fullAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}}}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"uid_behavior": {Status: api.ProbeStatusInconclusive, Code: "fixture_required"},
			},
		},
		{
			name:       "read state requires a fixture uid",
			account:    fullAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {}}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"uid_behavior": {Status: api.ProbeStatusInconclusive, Code: "fixture_required"},
				"read_state":   {Status: api.ProbeStatusInconclusive, Code: "fixture_required", Detail: "BODY.PEEK read-state fixture is required"},
			},
		},
		{
			name:    "read state preview preserves seen state",
			account: fullAccount("INBOX"),
			source: probeAdapter(map[string][]mailfake.Message{
				"INBOX": {
					{UID: 10, VisibleByPolicy: true, Body: "fixture body", MarksSeenOnPreview: false},
					{UID: 20, VisibleByPolicy: true, Body: "fixture body"},
				},
			}),
			wantStatus: api.ProbeStatusPassed,
			want: map[string]api.ProbeCheck{
				"read_state": {Status: api.ProbeStatusPassed, Code: "body_peek_ok"},
			},
			wantCalls:              []string{"capability", "folders", "select", "list", "list", "list", "seen", "seen", "preview"},
			wantReadStatePreserved: boolPtr(true),
		},
		{
			name:    "read state preview marks seen",
			account: fullAccount("INBOX"),
			source: probeAdapter(map[string][]mailfake.Message{
				"INBOX": {
					{UID: 10, VisibleByPolicy: true, Body: "fixture body"},
					{UID: 20, VisibleByPolicy: true, Body: "fixture body", MarksSeenOnPreview: true},
				},
			}),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"read_state": {Status: api.ProbeStatusFailed, Code: "body_marked_seen"},
			},
			wantCalls:              []string{"capability", "folders", "select", "list", "list", "list", "seen", "seen", "preview"},
			wantReadStatePreserved: boolPtr(false),
		},
		{
			name:    "read state skips already seen fixture",
			account: fullAccount("INBOX"),
			source: probeAdapter(map[string][]mailfake.Message{
				"INBOX": {
					{UID: 10, VisibleByPolicy: true, Body: "fixture body", Seen: true},
					{UID: 19, VisibleByPolicy: true, Body: "fixture body", Seen: true},
					{UID: 20, VisibleByPolicy: true, Body: "fixture body"},
				},
			}),
			wantStatus: api.ProbeStatusPassed,
			want: map[string]api.ProbeCheck{
				"read_state": {Status: api.ProbeStatusPassed, Code: "body_peek_ok"},
			},
			wantCalls:              []string{"capability", "folders", "select", "list", "list", "list", "seen", "seen", "seen", "preview"},
			wantReadStatePreserved: boolPtr(true),
		},
		{
			name:    "read state requires an unseen fixture",
			account: fullAccount("INBOX"),
			source: probeAdapter(map[string][]mailfake.Message{
				"INBOX": {
					{UID: 10, VisibleByPolicy: true, Body: "fixture body", Seen: true},
					{UID: 19, VisibleByPolicy: true, Body: "fixture body", Seen: true},
					{UID: 20, VisibleByPolicy: true, Body: "fixture body", Seen: true},
				},
			}),
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"read_state": {Status: api.ProbeStatusInconclusive, Code: "fixture_required", Detail: "BODY.PEEK read-state fixture is required"},
			},
			wantCalls: []string{"capability", "folders", "select", "list", "list", "list", "seen", "seen"},
		},
		{
			name:    "read state unobserved fixture is inconclusive",
			account: fullAccount("INBOX"),
			source: unobservedReadStateSource{Source: probeAdapter(map[string][]mailfake.Message{
				"INBOX": {
					{UID: 10, VisibleByPolicy: true, Body: "fixture body"},
					{UID: 20, VisibleByPolicy: true, Body: "fixture body"},
				},
			})},
			wantStatus: api.ProbeStatusInconclusive,
			want: map[string]api.ProbeCheck{
				"read_state": {Status: api.ProbeStatusInconclusive, Code: "fixture_required", Detail: "BODY.PEEK read-state fixture is required"},
			},
		},
		{
			name:       "read state preview errors are sanitized",
			account:    fullAccount("INBOX"),
			source:     probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}}, sourceFailure{method: "preview", account: "rs_info", folder: "INBOX", err: errors.New("FETCH leaked subject pw private text")}),
			wantStatus: api.ProbeStatusFailed,
			want: map[string]api.ProbeCheck{
				"read_state": {Status: api.ProbeStatusFailed, Code: "remote_failed", Detail: "provider probe failed"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := providerprobe.Runner{}.RunIMAP(context.Background(), tc.source, tc.account)
			if got.Account != "rs_info" {
				t.Fatalf("account = %q, want rs_info", got.Account)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; checks=%+v", got.Status, tc.wantStatus, got.Checks)
			}
			assertCheckOrder(t, got, tc.wantOrder)
			for id, want := range tc.want {
				gotCheck := checkByID(t, got, id)
				if gotCheck.Status != want.Status || gotCheck.Code != want.Code {
					t.Fatalf("%s = %+v, want status=%q code=%q", id, gotCheck, want.Status, want.Code)
				}
				if id == "read_state" && tc.wantReadStatePreserved != nil {
					if gotCheck.Facts == nil || gotCheck.Facts.ReadStatePreserved == nil {
						t.Fatalf("%s facts = %+v, want read_state_preserved=%t", id, gotCheck.Facts, *tc.wantReadStatePreserved)
					}
					if *gotCheck.Facts.ReadStatePreserved != *tc.wantReadStatePreserved {
						t.Fatalf("%s read_state_preserved = %t, want %t", id, *gotCheck.Facts.ReadStatePreserved, *tc.wantReadStatePreserved)
					}
				}
				if want.Detail != "" && gotCheck.Detail != want.Detail {
					t.Fatalf("%s detail = %q, want %q", id, gotCheck.Detail, want.Detail)
				}
			}
			if tc.wantCalls != nil {
				assertCallMethods(t, tc.source, tc.wantCalls)
			}
			assertNoRawProviderLeak(t, got)
		})
	}
}

func TestRunnerRunIMAPBoundsReadStateFixtureScan(t *testing.T) {
	var messages []mailfake.Message
	for uid := mail.UID(1); uid <= 25; uid++ {
		messages = append(messages, mailfake.Message{
			UID:             uid,
			VisibleByPolicy: true,
			Body:            "fixture body",
			Seen:            true,
		})
	}
	source := probeAdapter(map[string][]mailfake.Message{"INBOX": messages})

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, fullAccount("INBOX"))

	check := checkByID(t, got, "read_state")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "fixture_required" {
		t.Fatalf("read_state = %+v, want inconclusive fixture_required", check)
	}
	calls := source.CallsSnapshot()
	var seenCalls int
	var readStateListPayload string
	for _, call := range calls {
		if call.Method == "seen" {
			seenCalls++
		}
		if call.Method == "list" {
			readStateListPayload = call.Payload
		}
	}
	if seenCalls != 10 {
		t.Fatalf("seen calls = %d, want bounded scan of 10; calls=%+v", seenCalls, calls)
	}
	if readStateListPayload != "16:25" {
		t.Fatalf("read-state list range = %q, want 16:25; calls=%+v", readStateListPayload, calls)
	}
}

func TestRunnerRunIMAPSkipsReadStateWhenDomainHeaderSearchFails(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {
			{UID: 20, VisibleByPolicy: false, Body: "private unread fixture"},
		},
	})
	source.SetSearchResult("rs_info", "INBOX", "From", "regenerativ.ch", []mail.UID{20})
	source.SetSearchResult("rs_info", "INBOX", "From", "ksuite-mail-probe.invalid", []mail.UID{20})

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, domainAccount("INBOX", "regenerativ.ch"))

	domainCheck := checkByID(t, got, "domain_header_search")
	if domainCheck.Status != api.ProbeStatusFailed || domainCheck.Code != "header_search_overbroad" {
		t.Fatalf("domain_header_search = %+v, want failed header_search_overbroad", domainCheck)
	}
	readState := checkByID(t, got, "read_state")
	if readState.Status != api.ProbeStatusInconclusive || readState.Code != "prerequisite_failed" {
		t.Fatalf("read_state = %+v, want inconclusive prerequisite_failed", readState)
	}
	calls := source.CallsSnapshot()
	for _, call := range calls {
		if call.Method == "seen" || call.Method == "preview" {
			t.Fatalf("domain read-state probe ran after unsafe header search: calls=%+v", calls)
		}
	}
}

func TestRunnerRunIMAPDomainReadStateUsesOnlySameFolderValidatedHeaders(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {
			{UID: 10, VisibleByPolicy: true, Body: "allowed fixture", Seen: true},
			{UID: 20, VisibleByPolicy: false, Body: "private unread fixture"},
		},
		"Sent": {
			{UID: 30, VisibleByPolicy: true, Body: "sent bcc fixture"},
		},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	source.SetSearchResult("rs_info", "INBOX", "Bcc", "regenerativ.ch", []mail.UID{20})
	source.SetSearchResult("rs_info", "Sent", "Bcc", "regenerativ.ch", []mail.UID{30})
	source.SetSearchResult("rs_info", "Sent", "Bcc", "ksuite-mail-probe.invalid", nil)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	domainCheck := checkByID(t, got, "domain_header_search")
	if domainCheck.Status != api.ProbeStatusPassed || domainCheck.Code != "header_search_ok" {
		t.Fatalf("domain_header_search = %+v, want passed header_search_ok", domainCheck)
	}
	check := checkByID(t, got, "read_state")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "fixture_required" {
		t.Fatalf("read_state = %+v, want inconclusive fixture_required", check)
	}
	calls := source.CallsSnapshot()
	for _, call := range calls {
		if call.Method == "preview" {
			t.Fatalf("domain read-state probe previewed a non-search-approved UID: calls=%+v", calls)
		}
		if call.Method == "seen" && call.Payload != "uid=10" {
			t.Fatalf("domain read-state probe inspected non-search-approved seen state %q: calls=%+v", call.Payload, calls)
		}
	}
}

func TestRunnerRunIMAPDomainReadStateAllowsSafeSameFolderFixtureWhenSentBccMissing(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {
			{UID: 19, VisibleByPolicy: true, Body: "second fixture"},
			{UID: 20, VisibleByPolicy: true, Body: "allowed fixture"},
		},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{20})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX"},
	})

	domainCheck := checkByID(t, got, "domain_header_search")
	if domainCheck.Status != api.ProbeStatusInconclusive || domainCheck.Code != "fixture_required" {
		t.Fatalf("domain_header_search = %+v, want inconclusive fixture_required for missing Sent/Bcc", domainCheck)
	}
	readState := checkByID(t, got, "read_state")
	if readState.Status != api.ProbeStatusPassed || readState.Code != "body_peek_ok" {
		t.Fatalf("read_state = %+v, want passed body_peek_ok from safe same-folder fixture", readState)
	}
	if readState.Facts == nil || readState.Facts.ReadStatePreserved == nil || !*readState.Facts.ReadStatePreserved {
		t.Fatalf("read_state facts = %+v, want read_state_preserved=true", readState.Facts)
	}
	calls := source.CallsSnapshot()
	for _, call := range calls {
		if call.Method == "preview" && call.Payload != "uid=20" {
			t.Fatalf("domain read-state previewed %q, want validated uid=20 only; calls=%+v", call.Payload, calls)
		}
	}
}

func TestRunnerRunIMAPDomainReadStateDoesNotReSearchBeforePreview(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {
			{UID: 20, VisibleByPolicy: true, Body: "allowed fixture", Seen: true},
			{UID: 21, VisibleByPolicy: true, Body: "unvalidated fixture"},
		},
		"Sent": {
			{UID: 30, VisibleByPolicy: true, Body: "sent bcc fixture"},
		},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{20})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	source.SetSearchResult("rs_info", "Sent", "Bcc", "regenerativ.ch", []mail.UID{30})
	source.SetSearchResult("rs_info", "Sent", "Bcc", "ksuite-mail-probe.invalid", nil)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	readState := checkByID(t, got, "read_state")
	if readState.Status != api.ProbeStatusInconclusive || readState.Code != "fixture_required" {
		t.Fatalf("read_state = %+v, want inconclusive fixture_required after only validated uid is already seen", readState)
	}
	calls := source.CallsSnapshot()
	searchesBeforeReadState := 0
	for _, call := range calls {
		switch call.Method {
		case "search":
			searchesBeforeReadState++
		case "seen":
			if searchesBeforeReadState != 10 {
				t.Fatalf("read_state performed extra search before seen check; search count before first seen = %d calls=%+v", searchesBeforeReadState, calls)
			}
			if call.Payload != "uid=20" {
				t.Fatalf("domain read-state inspected %q, want validated uid=20 only; calls=%+v", call.Payload, calls)
			}
			return
		case "preview":
			t.Fatalf("domain read-state previewed unvalidated UID: calls=%+v", calls)
		}
	}
	t.Fatalf("read_state did not inspect validated evidence; calls=%+v", calls)
}

func TestRunnerRunIMAPReportsStructuredFolderStateFacts(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true, Body: "fixture body"}, {UID: 20, VisibleByPolicy: true}},
		"Sent":  {},
	})
	source.SetUIDState("rs_info", "INBOX", 777, 21)
	source.SetHighestModSeq("rs_info", "INBOX", 42)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, fullAccount("INBOX"))

	folderListing := checkByID(t, got, "folder_listing")
	if folderListing.Facts == nil {
		t.Fatalf("folder listing facts missing: %+v", folderListing)
	}
	if folderListing.Facts.FolderCount == nil || *folderListing.Facts.FolderCount != 2 {
		t.Fatalf("folder count facts = %+v, want 2", folderListing.Facts)
	}
	if want := []string{"INBOX", "Sent"}; !stringSlicesEqual(folderListing.Facts.Folders, want) {
		t.Fatalf("folders facts = %v, want %v", folderListing.Facts.Folders, want)
	}

	folderSelection := checkByID(t, got, "folder_selection")
	if folderSelection.Facts == nil {
		t.Fatalf("folder selection facts missing: %+v", folderSelection)
	}
	if folderSelection.Facts.Folder != "INBOX" {
		t.Fatalf("folder fact = %q, want INBOX", folderSelection.Facts.Folder)
	}
	if folderSelection.Facts.ReadOnly == nil || !*folderSelection.Facts.ReadOnly {
		t.Fatalf("read_only fact = %+v, want true", folderSelection.Facts.ReadOnly)
	}
	if folderSelection.Facts.SelectionMode != "examine" {
		t.Fatalf("selection_mode = %q, want examine", folderSelection.Facts.SelectionMode)
	}
	if folderSelection.Facts.UIDVALIDITY == nil || *folderSelection.Facts.UIDVALIDITY != 777 {
		t.Fatalf("uidvalidity fact = %+v, want 777", folderSelection.Facts.UIDVALIDITY)
	}
	if folderSelection.Facts.UIDNEXT == nil || *folderSelection.Facts.UIDNEXT != 21 {
		t.Fatalf("uidnext fact = %+v, want 21", folderSelection.Facts.UIDNEXT)
	}
	if folderSelection.Facts.HighestModSeq == nil || *folderSelection.Facts.HighestModSeq != 42 {
		t.Fatalf("highestmodseq fact = %+v, want 42", folderSelection.Facts.HighestModSeq)
	}

	refreshStrategy := checkByID(t, got, "refresh_strategy")
	if refreshStrategy.Facts == nil {
		t.Fatalf("refresh strategy facts missing: %+v", refreshStrategy)
	}
	if refreshStrategy.Status != api.ProbeStatusPassed || refreshStrategy.Code != "modseq_strategy" {
		t.Fatalf("refresh_strategy check = %+v", refreshStrategy)
	}
	if refreshStrategy.Facts.RefreshStrategy != "modseq" {
		t.Fatalf("refresh strategy = %q, want modseq", refreshStrategy.Facts.RefreshStrategy)
	}
	if refreshStrategy.Facts.UIDRangeSupported == nil || !*refreshStrategy.Facts.UIDRangeSupported {
		t.Fatalf("uid_range_supported should reflect known UID-range behavior: %+v", refreshStrategy.Facts)
	}
	if refreshStrategy.Facts.CondstoreSupported == nil || !*refreshStrategy.Facts.CondstoreSupported {
		t.Fatalf("condstore_supported fact = %+v, want true", refreshStrategy.Facts.CondstoreSupported)
	}
	if refreshStrategy.Facts.HighestModSeqAvailable == nil || !*refreshStrategy.Facts.HighestModSeqAvailable {
		t.Fatalf("highestmodseq_available fact = %+v, want true", refreshStrategy.Facts.HighestModSeqAvailable)
	}
	assertNoRawProviderLeak(t, got)
}

func TestRunnerRunIMAPOmitsUIDFactsWhenStateIsUnavailable(t *testing.T) {
	got := providerprobe.Runner{}.RunIMAP(context.Background(), uidStateSource(0, 21), fullAccount("INBOX"))

	folderSelection := checkByID(t, got, "folder_selection")
	if folderSelection.Facts == nil {
		t.Fatalf("folder selection facts missing: %+v", folderSelection)
	}
	if folderSelection.Facts.ReadOnly == nil || !*folderSelection.Facts.ReadOnly {
		t.Fatalf("read_only fact = %+v, want true", folderSelection.Facts.ReadOnly)
	}
	if folderSelection.Facts.UIDVALIDITY != nil || folderSelection.Facts.UIDNEXT != nil {
		t.Fatalf("uid facts = %+v, want omitted when state is incomplete", folderSelection.Facts)
	}
}

func TestRunnerRunIMAPOmitsHighestModSeqWhenUnavailable(t *testing.T) {
	got := providerprobe.Runner{}.RunIMAP(context.Background(), uidStateSource(777, 21), fullAccount("INBOX"))

	folderSelection := checkByID(t, got, "folder_selection")
	if folderSelection.Facts == nil {
		t.Fatalf("folder selection facts missing: %+v", folderSelection)
	}
	if folderSelection.Facts.UIDVALIDITY == nil || *folderSelection.Facts.UIDVALIDITY != 777 {
		t.Fatalf("uidvalidity fact = %+v, want 777", folderSelection.Facts.UIDVALIDITY)
	}
	if folderSelection.Facts.UIDNEXT == nil || *folderSelection.Facts.UIDNEXT != 21 {
		t.Fatalf("uidnext fact = %+v, want 21", folderSelection.Facts.UIDNEXT)
	}
	if folderSelection.Facts.HighestModSeq != nil {
		t.Fatalf("highestmodseq fact = %+v, want omitted when unavailable", folderSelection.Facts.HighestModSeq)
	}
}

func TestRunnerRunIMAPRefreshStrategyDecision(t *testing.T) {
	t.Run("modseq strategy when condstore and highestmodseq are present", func(t *testing.T) {
		source := probeAdapter(map[string][]mailfake.Message{
			"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
		})
		source.SetUIDState("rs_info", "INBOX", 555, 21)
		source.SetHighestModSeq("rs_info", "INBOX", 99)

		got := providerprobe.Runner{}.RunIMAP(context.Background(), source, fullAccount("INBOX"))
		gotCheck := checkByID(t, got, "refresh_strategy")
		if gotCheck.Status != api.ProbeStatusPassed || gotCheck.Code != "modseq_strategy" {
			t.Fatalf("refresh_strategy check = %+v", gotCheck)
		}
		if gotCheck.Facts == nil || gotCheck.Facts.RefreshStrategy != "modseq" {
			t.Fatalf("refresh strategy facts = %+v", gotCheck.Facts)
		}
		if gotCheck.Facts.UIDRangeSupported == nil || !*gotCheck.Facts.UIDRangeSupported {
			t.Fatalf("uid range supported should be true when uid_behavior passes: %+v", gotCheck.Facts)
		}
	})

	t.Run("uid-range strategy without condstore", func(t *testing.T) {
		source := sourceNoCondstore{
			Adapter: capabilityProbeAdapter(map[string][]mailfake.Message{
				"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
			}),
		}
		source.SetUIDState("rs_info", "INBOX", 555, 21)

		got := providerprobe.Runner{}.RunIMAP(context.Background(), source, fullAccount("INBOX"))
		gotCheck := checkByID(t, got, "refresh_strategy")
		if gotCheck.Status != api.ProbeStatusPassed || gotCheck.Code != "uid_range_strategy" {
			t.Fatalf("refresh_strategy check = %+v", gotCheck)
		}
		if gotCheck.Facts == nil || gotCheck.Facts.RefreshStrategy != "uid_range" {
			t.Fatalf("refresh strategy facts = %+v", gotCheck.Facts)
		}
		if gotCheck.Facts.UIDRangeSupported == nil || !*gotCheck.Facts.UIDRangeSupported {
			t.Fatalf("uid range supported should be true for uid-range strategy: %+v", gotCheck.Facts)
		}
		if gotCheck.Facts.CondstoreSupported == nil || *gotCheck.Facts.CondstoreSupported {
			t.Fatalf("condstore_supported should be false: %+v", gotCheck.Facts)
		}
	})

	t.Run("qresync capability supports modseq strategy", func(t *testing.T) {
		source := sourceCapabilities{
			Adapter: capabilityProbeAdapter(map[string][]mailfake.Message{
				"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
			}),
			caps: []string{"IMAP4rev1", "QRESYNC"},
		}
		source.SetUIDState("rs_info", "INBOX", 555, 21)
		source.SetHighestModSeq("rs_info", "INBOX", 99)

		got := providerprobe.Runner{}.RunIMAP(context.Background(), source, fullAccount("INBOX"))
		gotCheck := checkByID(t, got, "refresh_strategy")
		if gotCheck.Status != api.ProbeStatusPassed || gotCheck.Code != "modseq_strategy" {
			t.Fatalf("refresh_strategy check = %+v", gotCheck)
		}
		if gotCheck.Facts == nil || gotCheck.Facts.RefreshStrategy != "modseq" {
			t.Fatalf("refresh strategy facts = %+v", gotCheck.Facts)
		}
		if gotCheck.Facts.CondstoreSupported == nil || !*gotCheck.Facts.CondstoreSupported {
			t.Fatalf("qresync should imply condstore_supported: %+v", gotCheck.Facts)
		}
	})

	t.Run("unsupported strategy when uid range is missing and modseq unavailable", func(t *testing.T) {
		source := sourceNoCondstore{
			Adapter: capabilityProbeAdapter(map[string][]mailfake.Message{
				"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
			}),
		}
		source.SetUIDState("rs_info", "INBOX", 555, 21)
		source.SetHighestModSeq("rs_info", "INBOX", 0)

		got := providerprobe.Runner{}.RunIMAP(context.Background(), rangeIgnoringSource{Source: source}, fullAccount("INBOX"))
		gotCheck := checkByID(t, got, "refresh_strategy")
		if gotCheck.Status != api.ProbeStatusFailed || gotCheck.Code != "strategy_unsupported" {
			t.Fatalf("refresh_strategy check = %+v", gotCheck)
		}
		if gotCheck.Facts == nil || gotCheck.Facts.RefreshStrategy != "unsupported" {
			t.Fatalf("refresh strategy facts = %+v", gotCheck.Facts)
		}
		if gotCheck.Facts.UIDRangeSupported == nil || *gotCheck.Facts.UIDRangeSupported {
			t.Fatalf("uid range supported should be false on mismatch: %+v", gotCheck.Facts)
		}
	})

	t.Run("inconclusive strategy when uid state is incomplete", func(t *testing.T) {
		got := providerprobe.Runner{}.RunIMAP(context.Background(), uidStateSource(0, 21), fullAccount("INBOX"))
		gotCheck := checkByID(t, got, "refresh_strategy")
		if gotCheck.Status != api.ProbeStatusInconclusive || gotCheck.Code != "prerequisite_failed" {
			t.Fatalf("refresh_strategy check = %+v", gotCheck)
		}
		if gotCheck.Facts == nil || gotCheck.Facts.UIDRangeSupported != nil || gotCheck.Facts.RefreshStrategy != "inconclusive" {
			t.Fatalf("expected only prerequisites in refresh strategy facts, got %+v", gotCheck.Facts)
		}
	})
}

func TestRunnerRunIMAPInconclusiveRefreshStrategyAlwaysHasStructuredFact(t *testing.T) {
	condstoreSupported := true
	tests := []struct {
		name          string
		account       config.Account
		source        mail.Source
		wantCondstore *bool
	}{
		{
			name:    "nil source",
			account: fullAccount("INBOX"),
			source:  nil,
		},
		{
			name:    "capability failure",
			account: fullAccount("INBOX"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "capability", account: "rs_info", err: errors.New("NO auth failed with private text")},
			),
		},
		{
			name:    "folder listing failure",
			account: fullAccount("INBOX"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "folders", account: "rs_info", err: errors.New("LIST failed with private text")},
			),
			wantCondstore: &condstoreSupported,
		},
		{
			name:          "empty folder list",
			account:       fullAccount("INBOX"),
			source:        probeAdapter(map[string][]mailfake.Message{}),
			wantCondstore: &condstoreSupported,
		},
		{
			name:          "no configured folder",
			account:       fullAccount(" "),
			source:        probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}}),
			wantCondstore: &condstoreSupported,
		},
		{
			name:    "folder selection failure",
			account: fullAccount("INBOX"),
			source: probeAdapter(
				map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}},
				sourceFailure{method: "select", account: "rs_info", folder: "INBOX", err: errors.New("EXAMINE failed with private text")},
			),
			wantCondstore: &condstoreSupported,
		},
		{
			name:          "missing uid fixture",
			account:       fullAccount("INBOX"),
			source:        probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}}}),
			wantCondstore: &condstoreSupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := providerprobe.Runner{}.RunIMAP(context.Background(), tc.source, tc.account)
			refreshStrategy := checkByID(t, got, "refresh_strategy")
			if refreshStrategy.Status != api.ProbeStatusInconclusive {
				t.Fatalf("refresh_strategy status = %q, want %q: %+v", refreshStrategy.Status, api.ProbeStatusInconclusive, refreshStrategy)
			}
			if refreshStrategy.Facts == nil {
				t.Fatalf("refresh_strategy facts missing: %+v", refreshStrategy)
			}
			if refreshStrategy.Facts.RefreshStrategy != "inconclusive" {
				t.Fatalf("refresh_strategy fact = %q, want inconclusive: %+v", refreshStrategy.Facts.RefreshStrategy, refreshStrategy.Facts)
			}
			if tc.wantCondstore == nil {
				if refreshStrategy.Facts.CondstoreSupported != nil {
					t.Fatalf("condstore_supported fact = %+v, want omitted", refreshStrategy.Facts.CondstoreSupported)
				}
				return
			}
			if refreshStrategy.Facts.CondstoreSupported == nil || *refreshStrategy.Facts.CondstoreSupported != *tc.wantCondstore {
				t.Fatalf("condstore_supported fact = %+v, want %t", refreshStrategy.Facts.CondstoreSupported, *tc.wantCondstore)
			}
		})
	}
}

type sourceNoCondstore struct {
	*mailfake.Adapter
}

func capabilityProbeAdapter(folders map[string][]mailfake.Message) *mailfake.Adapter {
	return probeAdapter(folders)
}

func (s sourceNoCondstore) Capabilities(ctx context.Context, acct config.Account) ([]string, error) {
	return []string{"IMAP4REV1"}, nil
}

func (s sourceNoCondstore) SetHighestModSeq(account, folder string, hms int64) {
	s.Adapter.SetHighestModSeq(account, folder, hms)
}

type sourceCapabilities struct {
	*mailfake.Adapter
	caps []string
}

func (s sourceCapabilities) Capabilities(ctx context.Context, acct config.Account) ([]string, error) {
	return append([]string(nil), s.caps...), nil
}

func TestRunnerRunIMAPReportsStructuredDomainHeaderSearchFacts(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
		"Sent":  {{UID: 30, VisibleByPolicy: true}},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	source.SetSearchResult("rs_info", "Sent", "Bcc", "regenerativ.ch", []mail.UID{30})
	source.SetSearchResult("rs_info", "Sent", "Bcc", "ksuite-mail-probe.invalid", nil)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	check := checkByID(t, got, "domain_header_search")
	if check.Status != api.ProbeStatusPassed || check.Code != "header_search_ok" {
		t.Fatalf("domain header search check = %+v", check)
	}
	if check.Facts == nil {
		t.Fatalf("domain header search facts missing: %+v", check)
	}
	facts := check.Facts.DomainHeaderSearch
	if len(facts) != 4 {
		t.Fatalf("domain_header_search facts count = %d, want 4", len(facts))
	}

	m := make(map[string]int, len(facts))
	for _, fact := range facts {
		if fact.Domain != "regenerativ.ch" {
			t.Fatalf("unexpected domain = %q, want regenerativ.ch", fact.Domain)
		}
		key := fact.Header + "#" + strconv.Itoa(fact.MatchedUIDCount)
		m[key] = m[key] + 1
		if fact.NonmatchingVisible {
			t.Fatalf("fact should not expose nonmatching_visible=true for %s", fact.Header)
		}
	}
	if gotCount := m["From#1"]; gotCount != 1 {
		t.Fatalf("from fact count = %d, want 1", gotCount)
	}
	if gotCount := m["To#1"]; gotCount != 1 {
		t.Fatalf("to fact count = %d, want 1", gotCount)
	}
	if gotCount := m["Cc#1"]; gotCount != 1 {
		t.Fatalf("cc fact count = %d, want 1", gotCount)
	}
	if gotCount := m["Bcc#1"]; gotCount != 1 {
		t.Fatalf("bcc fact count = %d, want 1", gotCount)
	}
}

func TestRunnerRunIMAPReportsMissingSentBccFixtureFact(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX"},
	})

	check := checkByID(t, got, "domain_header_search")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "fixture_required" {
		t.Fatalf("domain header search check = %+v", check)
	}
	if check.Facts == nil || len(check.Facts.DomainHeaderSearch) != 4 {
		t.Fatalf("domain header search facts missing or incomplete: %+v", check.Facts)
	}
	bcc := check.Facts.DomainHeaderSearch[3]
	if bcc.Domain != "regenerativ.ch" || bcc.Header != "Bcc" {
		t.Fatalf("unexpected Bcc fact order/value: %+v", bcc)
	}
	if bcc.MatchedUIDCount != 0 || bcc.NonmatchingVisible {
		t.Fatalf("unexpected missing Bcc fixture fact: %+v", bcc)
	}
}

func TestRunnerRunIMAPReportsDomainHeaderSearchOverbroadFacts(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {
			{UID: 10, VisibleByPolicy: true},
			{UID: 20, VisibleByPolicy: true},
		},
		"Sent": {
			{UID: 30, VisibleByPolicy: true},
			{UID: 40, VisibleByPolicy: true},
		},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", []mail.UID{20})
	}
	source.SetSearchResult("rs_info", "Sent", "Bcc", "regenerativ.ch", []mail.UID{30})
	source.SetSearchResult("rs_info", "Sent", "Bcc", "ksuite-mail-probe.invalid", []mail.UID{40})

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	check := checkByID(t, got, "domain_header_search")
	if check.Status != api.ProbeStatusFailed || check.Code != "header_search_overbroad" {
		t.Fatalf("domain header search check = %+v", check)
	}
	if check.Facts == nil || len(check.Facts.DomainHeaderSearch) != 1 {
		t.Fatalf("domain header search overbroad facts = %+v", check.Facts)
	}
	for _, fact := range check.Facts.DomainHeaderSearch {
		if fact.Domain != "regenerativ.ch" {
			t.Fatalf("unexpected domain = %q, want regenerativ.ch", fact.Domain)
		}
		if fact.Header == "" {
			t.Fatalf("missing header in fact: %+v", fact)
		}
		if fact.MatchedUIDCount != 1 {
			t.Fatalf("matched count for %q = %d, want 1", fact.Header, fact.MatchedUIDCount)
		}
		if !fact.NonmatchingVisible {
			t.Fatalf("nonmatching_visible must be true for overbroad fixture: %+v", fact)
		}
	}
}

func TestRunnerRunIMAPPreservesOverbroadDomainHeaderSearchAfterLaterFailure(t *testing.T) {
	source := probeAdapter(
		map[string][]mailfake.Message{
			"INBOX": {
				{UID: 10, VisibleByPolicy: true},
				{UID: 20, VisibleByPolicy: true},
			},
			"Sent": {
				{UID: 30, VisibleByPolicy: true},
			},
		},
		sourceFailure{method: "search", account: "rs_info", folder: "INBOX", arg: "to:regenerativ.ch", err: errors.New("transient provider failure")},
	)
	source.SetSearchResult("rs_info", "INBOX", "From", "regenerativ.ch", []mail.UID{10})
	source.SetSearchResult("rs_info", "INBOX", "From", "ksuite-mail-probe.invalid", []mail.UID{20})

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	check := checkByID(t, got, "domain_header_search")
	if check.Status != api.ProbeStatusFailed || check.Code != "header_search_overbroad" {
		t.Fatalf("domain header search check = %+v", check)
	}
	if check.Detail != "header=from domain=regenerativ.ch domain_index_checked=true nonmatching_visible=true" {
		t.Fatalf("domain header search detail = %q", check.Detail)
	}
	if check.Facts == nil || len(check.Facts.DomainHeaderSearch) != 1 {
		t.Fatalf("domain header search overbroad facts = %+v", check.Facts)
	}
	fact := check.Facts.DomainHeaderSearch[0]
	if fact.Header != "From" || !fact.NonmatchingVisible {
		t.Fatalf("unexpected overbroad fact = %+v", fact)
	}
}

func TestRunnerRunIMAPReportsSentBccAvailabilityFacts(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX":        {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
		"Sent Archive": {{UID: 30, VisibleByPolicy: true, Body: "private sent body"}},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	source.SetSearchResult("rs_info", "Sent Archive", "Bcc", "regenerativ.ch", []mail.UID{30})
	source.SetSearchResult("rs_info", "Sent Archive", "Bcc", "ksuite-mail-probe.invalid", nil)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX"},
	})

	check := checkByID(t, got, "sent_bcc_availability")
	if check.Status != api.ProbeStatusPassed || check.Code != "sent_bcc_available" {
		t.Fatalf("sent_bcc_availability = %+v, want passed sent_bcc_available", check)
	}
	if check.Facts == nil || check.Facts.SentBccAvailability == nil {
		t.Fatalf("sent_bcc_availability facts missing: %+v", check.Facts)
	}
	facts := check.Facts.SentBccAvailability
	if facts.SentFolder != "Sent Archive" {
		t.Fatalf("sent folder = %q, want Sent Archive", facts.SentFolder)
	}
	if facts.SentFolderSource != "folder_listing" {
		t.Fatalf("sent folder source = %q, want folder_listing", facts.SentFolderSource)
	}
	if facts.BccAvailable == nil || !*facts.BccAvailable {
		t.Fatalf("bcc available = %+v, want true", facts.BccAvailable)
	}
	if facts.MatchedUIDCount != 1 || facts.NonmatchingVisible {
		t.Fatalf("unexpected sent bcc facts: %+v", facts)
	}
	assertNoRawProviderLeak(t, got)
}

func TestRunnerRunIMAPReportsMissingSentFolderForBccAvailability(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX"},
	})

	check := checkByID(t, got, "sent_bcc_availability")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "sent_folder_required" {
		t.Fatalf("sent_bcc_availability = %+v, want inconclusive sent_folder_required", check)
	}
	if check.Facts == nil || check.Facts.SentBccAvailability == nil {
		t.Fatalf("sent_bcc_availability facts missing: %+v", check.Facts)
	}
	if check.Facts.SentBccAvailability.BccAvailable != nil {
		t.Fatalf("bcc availability should be omitted without sent folder: %+v", check.Facts.SentBccAvailability)
	}
	for _, call := range source.CallsSnapshot() {
		if call.Method == "search" && call.Target == "rs_info@INBOX" && call.Payload == "bcc:regenerativ.ch" {
			t.Fatalf("sent bcc availability searched non-sent folder: calls=%+v", source.CallsSnapshot())
		}
	}
}

func TestRunnerRunIMAPReportsMissingBccFixtureAsInconclusive(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
		"Sent":  {{UID: 30, VisibleByPolicy: true}},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	source.SetSearchResult("rs_info", "Sent", "Bcc", "regenerativ.ch", nil)
	source.SetSearchResult("rs_info", "Sent", "Bcc", "ksuite-mail-probe.invalid", nil)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	check := checkByID(t, got, "sent_bcc_availability")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "bcc_fixture_required" {
		t.Fatalf("sent_bcc_availability = %+v, want inconclusive bcc_fixture_required", check)
	}
	facts := check.Facts.SentBccAvailability
	if facts.BccAvailable == nil || *facts.BccAvailable {
		t.Fatalf("bcc available = %+v, want false", facts.BccAvailable)
	}
	if facts.MatchedUIDCount != 0 || facts.NonmatchingVisible {
		t.Fatalf("unexpected missing bcc facts: %+v", facts)
	}
}

func TestRunnerRunIMAPSentBccAvailabilityChecksAllConfiguredDomains(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
		"Sent":  {{UID: 30, VisibleByPolicy: true}},
	})
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "example.org", []mail.UID{20})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	source.SetSearchResult("rs_info", "Sent", "Bcc", "regenerativ.ch", []mail.UID{30})
	source.SetSearchResult("rs_info", "Sent", "Bcc", "example.org", nil)
	source.SetSearchResult("rs_info", "Sent", "Bcc", "ksuite-mail-probe.invalid", nil)

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch", "example.org"},
		Folders: []string{"INBOX", "Sent"},
	})

	check := checkByID(t, got, "sent_bcc_availability")
	if check.Status != api.ProbeStatusInconclusive || check.Code != "bcc_fixture_required" {
		t.Fatalf("sent_bcc_availability = %+v, want inconclusive bcc_fixture_required", check)
	}
	facts := check.Facts.SentBccAvailability
	if facts.BccAvailable == nil || !*facts.BccAvailable {
		t.Fatalf("bcc available = %+v, want true from partial positive evidence", facts.BccAvailable)
	}
	if facts.MatchedUIDCount != 1 {
		t.Fatalf("matched uid count = %d, want 1", facts.MatchedUIDCount)
	}
	calls := source.CallsSnapshot()
	var bccRegenerativ, bccExample bool
	for _, call := range calls {
		if call.Method == "search" && call.Target == "rs_info@Sent" && call.Payload == "bcc:regenerativ.ch" {
			bccRegenerativ = true
		}
		if call.Method == "search" && call.Target == "rs_info@Sent" && call.Payload == "bcc:example.org" {
			bccExample = true
		}
	}
	if !bccRegenerativ || !bccExample {
		t.Fatalf("sent bcc availability did not search all domains: calls=%+v", calls)
	}
}

func TestRunnerRunIMAPSentBccAvailabilitySanitizesSearchErrors(t *testing.T) {
	source := probeAdapter(
		map[string][]mailfake.Message{
			"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
			"Sent":  {{UID: 30, VisibleByPolicy: true, Body: "private sent body"}},
		},
		sourceFailure{method: "search", account: "rs_info", folder: "Sent", arg: "bcc:regenerativ.ch", err: errors.New("UID SEARCH leaked sent subject pw private text")},
	)
	for _, header := range []string{"From", "To", "Cc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, config.Account{
		ID:      "rs_info",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
		Folders: []string{"INBOX", "Sent"},
	})

	check := checkByID(t, got, "sent_bcc_availability")
	if check.Status != api.ProbeStatusFailed || check.Code != "remote_failed" || check.Detail != "provider probe failed" {
		t.Fatalf("sent_bcc_availability = %+v, want sanitized remote_failed", check)
	}
	assertNoRawProviderLeak(t, got)
}

func TestRunnerRunIMAPSentBccAvailabilityNotApplicableForFullPolicy(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}},
		"Sent":  {{UID: 30, VisibleByPolicy: true}},
	})

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, fullAccount("INBOX", "Sent"))

	check := checkByID(t, got, "sent_bcc_availability")
	if check.Status != api.ProbeStatusNotApplicable || check.Code != "full_policy" {
		t.Fatalf("sent_bcc_availability = %+v, want not_applicable full_policy", check)
	}
	for _, call := range source.CallsSnapshot() {
		if call.Method == "search" && call.Payload == "bcc:regenerativ.ch" {
			t.Fatalf("full policy should not run bcc availability search: calls=%+v", source.CallsSnapshot())
		}
	}
}

func fullAccount(folders ...string) config.Account {
	return config.Account{ID: "rs_info", Policy: config.PolicyFull, Folders: folders}
}

func domainAccount(folder string, domains ...string) config.Account {
	return config.Account{ID: "rs_info", Policy: config.PolicyDomain, Domains: domains, Folders: []string{folder}}
}

type sourceFailure struct {
	method  string
	account string
	folder  string
	arg     string
	err     error
}

func probeAdapter(folders map[string][]mailfake.Message, failures ...sourceFailure) *mailfake.Adapter {
	adapter := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"rs_info": folders})
	for _, failure := range failures {
		adapter.SetFailure(failure.method, failure.account, failure.folder, failure.arg, failure.err)
	}
	return adapter
}

func uidStateSource(uidvalidity, uidnext uint64) mail.Source {
	adapter := probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}})
	adapter.SetUIDState("rs_info", "INBOX", uidvalidity, uidnext)
	return adapter
}

func domainFixtureSource(results map[string][]mail.UID) mail.Source {
	adapter := probeAdapter(map[string][]mailfake.Message{"INBOX": {{UID: 10, VisibleByPolicy: true}, {UID: 20, VisibleByPolicy: true}}})
	for _, header := range []string{"From", "To", "Cc", "Bcc"} {
		adapter.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", results[header])
		adapter.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", nil)
	}
	return adapter
}

type rangeIgnoringSource struct {
	mail.Source
}

func (s rangeIgnoringSource) ListUIDs(ctx context.Context, acct config.Account, folder string, _ mail.UIDRange) ([]mail.UID, error) {
	return s.Source.ListUIDs(ctx, acct, folder, mail.UIDRange{})
}

type unobservedReadStateSource struct {
	mail.Source
}

func (s unobservedReadStateSource) FetchSeenState(context.Context, config.Account, string, mail.UID) (bool, bool, error) {
	return true, false, nil
}

func (s unobservedReadStateSource) FetchBodyPreviewAndSeenState(context.Context, config.Account, string, mail.UID, int) (string, mail.ReadStateProbeResult, error) {
	return "", mail.ReadStateProbeResult{}, nil
}

func checkByID(t *testing.T, probe api.ProbeIMAPResponse, id string) api.ProbeCheck {
	t.Helper()
	for _, check := range probe.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("probe check %q missing from %+v", id, probe.Checks)
	return api.ProbeCheck{}
}

func assertCheckOrder(t *testing.T, probe api.ProbeIMAPResponse, want []string) {
	t.Helper()
	if want == nil {
		want = []string{
			"account_selection",
			"fixed_checklist",
			"capability",
			"folder_listing",
			"folder_selection",
			"uid_behavior",
			"refresh_strategy",
			"domain_header_search",
			"sent_bcc_availability",
			"read_state",
		}
	}
	if len(probe.Checks) != len(want) {
		t.Fatalf("check count = %d, want %d: %+v", len(probe.Checks), len(want), probe.Checks)
	}
	for i, id := range want {
		if probe.Checks[i].ID != id {
			t.Fatalf("check[%d].ID = %q, want %q; checks=%+v", i, probe.Checks[i].ID, id, probe.Checks)
		}
	}
}

func assertCallMethods(t *testing.T, src mail.Source, want []string) {
	t.Helper()
	adapter, ok := src.(*mailfake.Adapter)
	if !ok {
		t.Fatalf("source %T does not expose call methods", src)
	}
	calls := adapter.CallsSnapshot()
	got := make([]string, len(calls))
	for i, call := range calls {
		got[i] = call.Method
	}
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
}

func assertNoRawProviderLeak(t *testing.T, probe api.ProbeIMAPResponse) {
	t.Helper()
	raw, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	for _, leak := range [][]byte{[]byte("pw"), []byte("private text"), []byte("auth failed"), []byte("provider text"), []byte("info@example.com"), []byte("UID SEARCH leaked")} {
		if bytes.Contains(raw, leak) {
			t.Fatalf("probe leaked %q in %s", leak, raw)
		}
	}
}

func stringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func boolPtr(v bool) *bool {
	return &v
}
