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
		name       string
		account    config.Account
		source     mail.Source
		want       map[string]api.ProbeCheck
		wantOrder  []string
		wantCalls  []string
		wantStatus string
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

func TestRunnerRunIMAPReportsDomainHeaderSearchOverbroadFacts(t *testing.T) {
	source := probeAdapter(map[string][]mailfake.Message{
		"INBOX": {
			{UID: 10, VisibleByPolicy: true},
			{UID: 20, VisibleByPolicy: true},
		},
	})
	for _, header := range []string{"From", "To", "Cc", "Bcc"} {
		source.SetSearchResult("rs_info", "INBOX", header, "regenerativ.ch", []mail.UID{10})
		source.SetSearchResult("rs_info", "INBOX", header, "ksuite-mail-probe.invalid", []mail.UID{20})
	}

	got := providerprobe.Runner{}.RunIMAP(context.Background(), source, domainAccount("INBOX", "regenerativ.ch"))

	check := checkByID(t, got, "domain_header_search")
	if check.Status != api.ProbeStatusFailed || check.Code != "header_search_overbroad" {
		t.Fatalf("domain header search check = %+v", check)
	}
	if check.Facts == nil || len(check.Facts.DomainHeaderSearch) != 4 {
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
			"domain_header_search",
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
