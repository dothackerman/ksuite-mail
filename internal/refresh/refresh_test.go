package refresh_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/cache"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/mailfake"
	"github.com/dothackerman/ksuite-mail/internal/refresh"
)

func TestDomainRefreshUsesSearchBeforeFetch(t *testing.T) {
	cfg := &config.Config{
		Mail: config.Mail{
			Accounts: []config.Account{
				{
					ID:       "acct",
					Email:    "acct@example.com",
					Host:     "imap.example.com",
					Port:     993,
					TLS:      boolPtr(true),
					Username: "acct@example.com",
					PasswordRef: config.PasswordRef{
						Source:   config.PasswordSourceFile,
						Provider: config.PasswordProviderLocal,
						ID:       "/ksuite-mail/acct/password",
					},
					Policy:  config.PolicyDomain,
					Domains: []string{"example.com"},
					Folders: []string{"INBOX"},
				},
			},
		},
	}

	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 9,
					Envelope: mail.MessageEnvelope{
						UID:     9,
						From:    "alice@example.com",
						Subject: "hello",
					},
					VisibleByPolicy: true,
				},
			},
		},
	})

	repo := mustOpenRepoForRefresh(t)
	res, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{
		MaxCandidates: 10,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if res.Meta.Attempted != true || res.Meta.RemoteOK != true {
		t.Fatalf("refresh meta = %+v", res.Meta)
	}
	calls := ad.CallsSnapshot()
	if len(calls) < 2 {
		t.Fatalf("calls = %#v, want at least select/search/fetch", calls)
	}
	if calls[0].Method != "select" {
		t.Fatalf("first call = %+v, want select", calls[0])
	}
	if calls[1].Method != "search" {
		t.Fatalf("second call = %+v, want search", calls[1])
	}
	headersSeen := false
	fetchSeen := false
	for _, call := range calls {
		if call.Method == "headers" {
			headersSeen = true
			continue
		}
		if call.Method == "fetch" {
			if !headersSeen {
				t.Fatalf("fetch happened before header validation: calls=%+v", calls)
			}
			fetchSeen = true
			break
		}
		if call.Method == "select" {
			continue
		}
	}
	if !fetchSeen {
		t.Fatalf("fetch not called: calls=%+v", calls)
	}
}

func TestDomainRefreshSkipsNonPolicyMessages(t *testing.T) {
	cfg := &config.Config{
		Mail: config.Mail{
			Accounts: []config.Account{
				{
					ID:       "acct",
					Email:    "acct@example.com",
					Host:     "imap.example.com",
					Port:     993,
					TLS:      boolPtr(true),
					Username: "acct@example.com",
					PasswordRef: config.PasswordRef{
						Source:   config.PasswordSourceFile,
						Provider: config.PasswordProviderLocal,
						ID:       "/ksuite-mail/acct/password",
					},
					Policy:  config.PolicyDomain,
					Domains: []string{"example.com"},
					Folders: []string{"INBOX"},
				},
			},
		},
	}

	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {
				{
					UID: 8,
					Envelope: mail.MessageEnvelope{
						UID:     8,
						From:    "outside@other.com",
						To:      "you@other.com",
						Subject: "no match",
					},
					VisibleByPolicy: true,
				},
			},
		},
	})
	ad.SetSearchResult("acct", "INBOX", "From", "example.com", []mail.UID{8})

	repo := mustOpenRepoForRefresh(t)
	_, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 10})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	msgs, err := repo.ListMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected no cached messages, got %d", len(msgs))
	}
	for _, call := range ad.CallsSnapshot() {
		if call.Method == "fetch" || call.Method == "preview" {
			t.Fatalf("content fetch happened for non-policy UID: calls=%+v", ad.CallsSnapshot())
		}
	}
}

func TestRefreshHandlesMessageMoveAcrossFolders(t *testing.T) {
	cfg := &config.Config{
		Mail: config.Mail{
			Accounts: []config.Account{
				{
					ID:       "acct",
					Email:    "acct@example.com",
					Host:     "imap.example.com",
					Port:     993,
					TLS:      boolPtr(true),
					Username: "acct@example.com",
					PasswordRef: config.PasswordRef{
						Source:   config.PasswordSourceFile,
						Provider: config.PasswordProviderLocal,
						ID:       "/ksuite-mail/acct/password",
					},
					Policy:  config.PolicyFull,
					Folders: []string{"INBOX", "Sent"},
				},
			},
		},
	}

	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<inbox>",
		ThreadKey:           "thread-1",
		Subject:             "moved",
		From:                "a@example.com",
		To:                  "b@example.com",
		Cc:                  "",
		Bcc:                 "",
		Date:                time.Now(),
		Flags:               "",
		HasAttachments:      false,
		Snippet:             "moved",
		BodyText:            "moved",
		VisibleReason:       "policy_full",
		ContentHash:         "h",
		LastLoadedOrChecked: time.Now(),
		FirstLoadedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("seed old message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:             "acct",
		Folder:                "INBOX",
		UIDVALIDITY:           999,
		UIDNEXT:               2,
		LastRefreshAttempted:  ptrTime(time.Now()),
		LastSuccessfulRefresh: ptrTime(time.Now()),
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}

	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {},
			"Sent": {
				{
					UID: 1,
					Envelope: mail.MessageEnvelope{
						UID:       1,
						From:      "a@example.com",
						To:        "b@example.com",
						Subject:   "moved",
						ThreadKey: "thread-1",
						MessageID: "<moved>",
						Date:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
					},
					VisibleByPolicy: true,
				},
			},
		},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 10}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	inbox, err := repo.ListMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX"})
	if err != nil {
		t.Fatalf("ListMessages INBOX: %v", err)
	}
	if len(inbox) != 0 {
		t.Fatalf("expected moved message removed from INBOX, got %d", len(inbox))
	}
	sent, err := repo.ListMessages(cache.QueryFilter{AccountID: "acct", Folder: "Sent"})
	if err != nil {
		t.Fatalf("ListMessages Sent: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected moved message in Sent, got %d", len(sent))
	}
}

func TestDomainRefreshDoesNotDeleteCachedRowsWhenCandidateSearchIsCapped(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyDomain,
		Domains: []string{"example.com"},
		Folders: []string{"INBOX"},
	}}}}
	now := time.Now().UTC()
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<cached>",
		ThreadKey:           "thread-1",
		Subject:             "cached",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Date:                now,
		Snippet:             "cached",
		BodyText:            "cached",
		VisibleReason:       "domain:from",
		ContentHash:         "h1",
		FirstLoadedAt:       now,
		LastLoadedOrChecked: now,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           3,
		HighestModSeq:     0,
		LastSeenUID:       2,
		PolicyFingerprint: "domain:example.com",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {
			{UID: 1, Envelope: mail.MessageEnvelope{UID: 1, From: "alice@example.com", Subject: "old"}, Body: "old", VisibleByPolicy: true},
			{UID: 2, Envelope: mail.MessageEnvelope{UID: 2, From: "alice@example.com", Subject: "new"}, Body: "new", VisibleByPolicy: true},
		}},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 1}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	msgs, err := repo.ListMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("cached rows after capped refresh = %d, want 2", len(msgs))
	}
}

func TestDomainRefreshStripsEmbeddedRepliesBeforeCaching(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyDomain,
		Domains: []string{"example.com"},
		Folders: []string{"INBOX"},
	}}}}
	repo := mustOpenRepoForRefresh(t)
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {{
			UID: 1,
			Envelope: mail.MessageEnvelope{
				UID:       1,
				From:      "alice@example.com",
				Subject:   "reply",
				Snippet:   "Current answer\nOn Friday, bob wrote:\nprivate quoted term",
				ThreadKey: "thread",
			},
			Body:            "Current answer\nOn Friday, bob wrote:\nprivate quoted term",
			VisibleByPolicy: true,
		}}},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 10}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	msgs, err := repo.SearchMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX", Query: "private quoted term", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("quoted reply text was indexed: %#v", msgs)
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1))
	if err != nil {
		t.Fatalf("GetByPublicID: %v", err)
	}
	if !ok || got.BodyText != "Current answer" {
		t.Fatalf("cached body = %q, found=%v; want stripped current answer", got.BodyText, ok)
	}
	if got.Snippet != "Current answer" {
		t.Fatalf("cached snippet = %q, want stripped current answer", got.Snippet)
	}
	msgs, err = repo.SearchMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX", Query: "Current answer", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages current answer: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("current snippet text was not indexed: %#v", msgs)
	}
}

func TestFullRefreshStripsEmbeddedRepliesBeforeCaching(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	repo := mustOpenRepoForRefresh(t)
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {{
			UID: 1,
			Envelope: mail.MessageEnvelope{
				UID:     1,
				From:    "alice@example.com",
				Subject: "reply",
				Snippet: "Current answer\nOn Friday, bob wrote:\nprivate quoted term",
			},
			Body: "Current answer\nOn Friday, bob wrote:\nprivate quoted term",
		}}},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 10}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	msgs, err := repo.SearchMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX", Query: "private quoted term", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("quoted reply text was indexed for full policy: %#v", msgs)
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1))
	if err != nil {
		t.Fatalf("GetByPublicID: %v", err)
	}
	if !ok || got.BodyText != "Current answer" || got.Snippet != "Current answer" {
		t.Fatalf("cached full-policy preview = body %q snippet %q found=%v, want stripped current answer", got.BodyText, got.Snippet, ok)
	}
}

func TestFullRefreshBoundsRemoteUIDListing(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	messages := make([]mailfake.Message, 20)
	for i := range messages {
		uid := mail.UID(i + 1)
		messages[i] = mailfake.Message{
			UID:      uid,
			Envelope: mail.MessageEnvelope{UID: uid, From: "alice@example.com", Subject: "bounded"},
			Body:     "bounded",
		}
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": messages}})
	repo := mustOpenRepoForRefresh(t)

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 5}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var listPayload string
	for _, call := range ad.CallsSnapshot() {
		if call.Method == "list" {
			listPayload = call.Payload
			break
		}
	}
	if listPayload != "16:21" {
		t.Fatalf("list UID range = %q, want bounded 16:21", listPayload)
	}
	state, err := repo.FolderState("acct", "INBOX")
	if err != nil {
		t.Fatalf("FolderState: %v", err)
	}
	if state == nil || state.HighestModSeq != 0 {
		t.Fatalf("folder state = %+v, want incomplete marker after bounded listing", state)
	}
}

func TestBoundedRefreshMarksObservedCachedUIDsVerified(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 16),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 16,
		MessageID:           "<cached>",
		ThreadKey:           "thread",
		Subject:             "cached",
		From:                "a@example.com",
		To:                  "b@example.com",
		Date:                old,
		Snippet:             "cached",
		BodyText:            "cached body",
		VisibleReason:       "policy_full",
		ContentHash:         "h",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           21,
		HighestModSeq:     0,
		LastSeenUID:       20,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	messages := make([]mailfake.Message, 20)
	for i := range messages {
		uid := mail.UID(i + 1)
		messages[i] = mailfake.Message{
			UID:             uid,
			Envelope:        mail.MessageEnvelope{UID: uid, From: "alice@example.com", Subject: "bounded"},
			Body:            "bounded",
			VisibleByPolicy: true,
		}
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": messages}})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{
		MaxCandidates: 5,
		Now:           func() time.Time { return now },
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 16))
	if err != nil {
		t.Fatalf("GetByPublicID: %v", err)
	}
	if !ok || !got.LastLoadedOrChecked.Equal(now) {
		t.Fatalf("LastLoadedOrChecked = %v, found=%v; want bounded verification time %v", got.LastLoadedOrChecked, ok, now)
	}
}

func TestBoundedRefreshRemovesMissingRowsOnlyInsideObservedRange(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	for _, uid := range []mail.UID{10, 17} {
		if err := repo.UpsertMessage(mail.CachedMessage{
			ID:                  mail.PublicID("acct", "INBOX", 123, uid),
			AccountID:           "acct",
			Folder:              "INBOX",
			UIDVALIDITY:         123,
			UID:                 uid,
			MessageID:           fmt.Sprintf("<cached-%d>", uid),
			ThreadKey:           "thread",
			Subject:             "cached",
			From:                "a@example.com",
			To:                  "b@example.com",
			Date:                old,
			Snippet:             "cached",
			BodyText:            "cached body",
			VisibleReason:       "policy_full",
			ContentHash:         fmt.Sprintf("h-%d", uid),
			FirstLoadedAt:       old,
			LastLoadedOrChecked: old,
		}); err != nil {
			t.Fatalf("seed cached UID %d: %v", uid, err)
		}
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           21,
		HighestModSeq:     0,
		LastSeenUID:       20,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {
		{UID: 16, Envelope: mail.MessageEnvelope{UID: 16, From: "alice@example.com", Subject: "bounded"}, Body: "bounded", VisibleByPolicy: true},
		{UID: 18, Envelope: mail.MessageEnvelope{UID: 18, From: "alice@example.com", Subject: "bounded"}, Body: "bounded", VisibleByPolicy: true},
		{UID: 20, Envelope: mail.MessageEnvelope{UID: 20, From: "alice@example.com", Subject: "bounded"}, Body: "bounded", VisibleByPolicy: true},
	}}})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 5}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 17)); err != nil {
		t.Fatalf("GetByPublicID missing range row: %v", err)
	} else if ok {
		t.Fatalf("UID 17 remained cached despite being absent from observed bounded range")
	}
	if _, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 10)); err != nil {
		t.Fatalf("GetByPublicID outside range row: %v", err)
	} else if !ok {
		t.Fatalf("UID 10 was removed outside the observed bounded range")
	}
}

func TestBoundedRefreshUsesObservedScopeForSparseCleanup(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyDomain,
		Domains: []string{"example.com"},
		Folders: []string{"INBOX"},
	}}}}
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 950),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 950,
		MessageID:           "<cached-950>",
		ThreadKey:           "thread",
		Subject:             "cached",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Date:                old,
		Snippet:             "cached",
		BodyText:            "cached body",
		VisibleReason:       "policy_domain",
		ContentHash:         "h-950",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}); err != nil {
		t.Fatalf("seed cached UID: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           1001,
		HighestModSeq:     0,
		LastSeenUID:       1000,
		PolicyFingerprint: "domain:example.com",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {
		{
			UID: 1000,
			Envelope: mail.MessageEnvelope{
				UID:     1000,
				From:    "alice@example.com",
				To:      "bob@example.com",
				Subject: "sparse",
			},
			Body:            "sparse",
			VisibleByPolicy: true,
		},
	}}})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 100}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 950)); err != nil {
		t.Fatalf("GetByPublicID stale row: %v", err)
	} else if ok {
		t.Fatalf("UID 950 remained cached despite being absent from observed remote scope")
	}
}

func TestRefreshPreservesLatestSuccessfulTimestampWhenAllFoldersFail(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	last := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:             "acct",
		Folder:                "INBOX",
		UIDVALIDITY:           123,
		UIDNEXT:               2,
		LastRefreshAttempted:  ptrTime(last),
		LastSuccessfulRefresh: ptrTime(last),
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}}})
	ad.SetFailure("select", "acct", "INBOX", "", context.DeadlineExceeded)

	res, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if res.Meta.RemoteOK {
		t.Fatalf("RemoteOK = true, want false")
	}
	if res.Meta.LastSuccessfulRefreshAt == nil || !res.Meta.LastSuccessfulRefreshAt.Equal(last) {
		t.Fatalf("LastSuccessfulRefreshAt = %v, want %v", res.Meta.LastSuccessfulRefreshAt, last)
	}
}

func TestPartialRefreshKeepsRemoteOKFalse(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX", "Sent"},
	}}}}
	repo := mustOpenRepoForRefresh(t)
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {
			"INBOX": {{UID: 1, Envelope: mail.MessageEnvelope{UID: 1, From: "a@example.com", Subject: "ok"}, Body: "ok"}},
			"Sent":  {},
		},
	})
	ad.SetFailure("select", "acct", "Sent", "", context.DeadlineExceeded)

	res, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !res.Partial {
		t.Fatalf("Partial = false, want true")
	}
	if res.Meta.RemoteOK {
		t.Fatalf("RemoteOK = true, want false")
	}
	if res.Meta.LastSuccessfulRefreshAt == nil {
		t.Fatalf("LastSuccessfulRefreshAt = nil, want successful folder timestamp")
	}
}

func TestIncrementalRefreshRemovesDeletedExistingUIDsWhenDiscoveryComplete(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	now := time.Now().UTC()
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<deleted>",
		ThreadKey:           "thread-delete",
		Subject:             "deleted",
		From:                "a@example.com",
		To:                  "b@example.com",
		Date:                now,
		Snippet:             "deleted",
		BodyText:            "deleted",
		VisibleReason:       "policy_full",
		ContentHash:         "h",
		LastLoadedOrChecked: now,
		FirstLoadedAt:       now,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           3,
		HighestModSeq:     0,
		LastSeenUID:       2,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {
			{UID: 2, Envelope: mail.MessageEnvelope{UID: 2, From: "a@example.com", Subject: "kept"}, Body: "kept"},
		}},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1)); err != nil {
		t.Fatalf("GetByPublicID deleted: %v", err)
	} else if ok {
		t.Fatalf("deleted remote UID remained cached")
	}
}

func TestCompleteRefreshDropsUIDThatVanishesBeforeEnvelopeFetch(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyDomain,
		Domains: []string{"example.com"},
		Folders: []string{"INBOX"},
	}}}}
	now := time.Now().UTC()
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<vanished>",
		ThreadKey:           "thread-vanished",
		Subject:             "vanished",
		From:                "a@example.com",
		To:                  "b@example.com",
		Date:                now,
		Snippet:             "vanished",
		BodyText:            "vanished",
		VisibleReason:       "policy_domain",
		ContentHash:         "h",
		LastLoadedOrChecked: now,
		FirstLoadedAt:       now,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}

	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {}},
	})
	ad.SetSearchResult("acct", "INBOX", "From", "example.com", []mail.UID{1})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1)); err != nil {
		t.Fatalf("GetByPublicID vanished: %v", err)
	} else if ok {
		t.Fatalf("vanished remote UID remained cached")
	}
}

func TestFullRefreshCapsCandidatesWithoutRecordingCompleteState(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	repo := mustOpenRepoForRefresh(t)
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {
			{UID: 1, Envelope: mail.MessageEnvelope{UID: 1, From: "a@example.com", Subject: "one"}, Body: "one"},
			{UID: 2, Envelope: mail.MessageEnvelope{UID: 2, From: "a@example.com", Subject: "two"}, Body: "two"},
			{UID: 3, Envelope: mail.MessageEnvelope{UID: 3, From: "a@example.com", Subject: "three"}, Body: "three"},
		}},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 2}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	msgs, err := repo.ListMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("cached rows after capped refresh = %d, want 2", len(msgs))
	}
	state, err := repo.FolderState("acct", "INBOX")
	if err != nil {
		t.Fatalf("FolderState: %v", err)
	}
	if state == nil || state.HighestModSeq != 0 {
		t.Fatalf("state.HighestModSeq = %+v, want incomplete fast-path marker", state)
	}
}

func TestPolicyFingerprintChangeBypassesRemoteMetadataFastPath(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyDomain,
		Domains: []string{"other.com"},
		Folders: []string{"INBOX"},
	}}}}
	now := time.Now().UTC()
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:             "acct",
		Folder:                "INBOX",
		UIDVALIDITY:           123,
		UIDNEXT:               2,
		HighestModSeq:         99,
		LastSeenUID:           1,
		PolicyFingerprint:     "domain:example.com",
		LastRefreshAttempted:  ptrTime(now),
		LastSuccessfulRefresh: ptrTime(now),
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {
			{
				UID: 1,
				Envelope: mail.MessageEnvelope{
					UID:     1,
					From:    "newly-allowed@other.com",
					Subject: "newly allowed",
				},
				Body:            "newly allowed",
				VisibleByPolicy: true,
			},
		}},
	})
	ad.SetHighestModSeq("acct", "INBOX", 99)

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 10}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	msgs, err := repo.ListMessages(cache.QueryFilter{AccountID: "acct", Folder: "INBOX", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].From != "newly-allowed@other.com" {
		t.Fatalf("messages after policy expansion = %#v", msgs)
	}
	state, err := repo.FolderState("acct", "INBOX")
	if err != nil {
		t.Fatalf("FolderState: %v", err)
	}
	if state == nil || state.PolicyFingerprint != "domain:other.com" {
		t.Fatalf("policy fingerprint = %+v, want updated domain", state)
	}
}

func TestRefreshClearsCachedRowsWhenFolderStateIsMissing(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	now := time.Now().UTC()
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<old>",
		ThreadKey:           "thread",
		Subject:             "old",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Date:                now,
		Snippet:             "old",
		BodyText:            "old body",
		VisibleReason:       "policy_full",
		ContentHash:         "old-hash",
		FirstLoadedAt:       now,
		LastLoadedOrChecked: now,
	}); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {
			{
				UID: 1,
				Envelope: mail.MessageEnvelope{
					UID:       1,
					MessageID: "<new>",
					From:      "alice@example.com",
					Subject:   "new",
				},
				Body: "new body",
			},
		}},
	})
	ad.SetUIDVALIDITY("acct", "INBOX", 456)

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{MaxCandidates: 10}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1)); err != nil {
		t.Fatalf("GetByPublicID old row: %v", err)
	} else if ok {
		t.Fatalf("old UIDVALIDITY row remained cached after state was missing")
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 456, 1))
	if err != nil {
		t.Fatalf("GetByPublicID new row: %v", err)
	}
	if !ok || got.MessageID != "<new>" {
		t.Fatalf("new row = %+v, found=%v; want refreshed UIDVALIDITY row", got, ok)
	}
}

func TestRefreshDoesNotRefetchCachedUIDBodiesWhenUIDNextIsUnchanged(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<cached>",
		ThreadKey:           "thread",
		Subject:             "cached",
		From:                "a@example.com",
		To:                  "b@example.com",
		Date:                old,
		Snippet:             "cached",
		BodyText:            "cached body",
		VisibleReason:       "policy_full",
		ContentHash:         "h",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           2,
		HighestModSeq:     0,
		LastSeenUID:       1,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {{
			UID:             1,
			Envelope:        mail.MessageEnvelope{UID: 1, From: "a@example.com", Subject: "cached"},
			Body:            "remote body should not be fetched",
			VisibleByPolicy: true,
		}}},
	})

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{
		MaxCandidates: 10,
		Now:           func() time.Time { return now },
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for _, call := range ad.CallsSnapshot() {
		if call.Method == "fetch" || call.Method == "preview" {
			t.Fatalf("unexpected remote content fetch for cached UID: %+v", call)
		}
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1))
	if err != nil {
		t.Fatalf("GetByPublicID: %v", err)
	}
	if !ok || got.BodyText != "cached body" {
		t.Fatalf("cached body = %q, found=%v; want existing cached body", got.BodyText, ok)
	}
	if !got.LastLoadedOrChecked.Equal(now) {
		t.Fatalf("LastLoadedOrChecked = %v, want non-HMS verification time %v", got.LastLoadedOrChecked, now)
	}
}

func TestRefreshRefetchesCachedUIDsWhenHighestModSeqChanges(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<cached>",
		ThreadKey:           "thread",
		Subject:             "cached",
		From:                "a@example.com",
		To:                  "b@example.com",
		Date:                old,
		Flags:               "\\Seen",
		Snippet:             "cached",
		BodyText:            "cached body",
		VisibleReason:       "policy_full",
		ContentHash:         "h",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           2,
		HighestModSeq:     99,
		LastSeenUID:       1,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {{
			UID: 1,
			Envelope: mail.MessageEnvelope{
				UID:     1,
				From:    "a@example.com",
				Subject: "remote changed",
				Flags:   "\\Answered",
			},
			Body:            "remote changed body",
			VisibleByPolicy: true,
		}}},
	})
	ad.SetHighestModSeq("acct", "INBOX", 100)

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{
		MaxCandidates: 10,
		Now:           func() time.Time { return now },
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var sawFetch bool
	for _, call := range ad.CallsSnapshot() {
		if call.Method == "fetch" {
			sawFetch = true
			break
		}
	}
	if !sawFetch {
		t.Fatalf("expected fetch for cached UID after HIGHESTMODSEQ changed")
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1))
	if err != nil {
		t.Fatalf("GetByPublicID: %v", err)
	}
	if !ok || got.Flags != "\\Answered" || got.Subject != "remote changed" {
		t.Fatalf("cached message after modseq change = %+v, found=%v", got, ok)
	}
}

func TestRefreshReturnsLocalCacheErrorsInsteadOfRemoteWarnings(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	repo := mustOpenRepoForRefresh(t)
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{"acct": {"INBOX": {}}})

	res, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{})
	if err == nil {
		t.Fatalf("Refresh err = nil, want local cache error")
	}
	if len(res.Warnings) != 0 || res.Partial {
		t.Fatalf("refresh result = %+v, want no remote warning envelope for local cache error", res)
	}
}

func TestFastPathRefreshMarksCachedFolderRowsVerified(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoForRefresh(t)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  mail.PublicID("acct", "INBOX", 123, 1),
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         123,
		UID:                 1,
		MessageID:           "<cached>",
		ThreadKey:           "thread",
		Subject:             "cached",
		From:                "a@example.com",
		To:                  "b@example.com",
		Date:                old,
		Snippet:             "cached",
		BodyText:            "cached body",
		VisibleReason:       "policy_full",
		ContentHash:         "h",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}); err != nil {
		t.Fatalf("seed cached message: %v", err)
	}
	if err := repo.UpsertFolderState(cache.FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       123,
		UIDNEXT:           2,
		HighestModSeq:     99,
		LastSeenUID:       1,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("seed folder state: %v", err)
	}
	ad := mailfake.NewAdapter(map[string]map[string][]mailfake.Message{
		"acct": {"INBOX": {{
			UID:             1,
			Envelope:        mail.MessageEnvelope{UID: 1, From: "a@example.com", Subject: "cached"},
			Body:            "remote body should not be fetched",
			VisibleByPolicy: true,
		}}},
	})
	ad.SetHighestModSeq("acct", "INBOX", 99)

	if _, err := refresh.Refresh(context.Background(), cfg, repo, ad, refresh.RefreshOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, ok, err := repo.GetByPublicID(mail.PublicID("acct", "INBOX", 123, 1))
	if err != nil {
		t.Fatalf("GetByPublicID: %v", err)
	}
	if !ok || !got.LastLoadedOrChecked.Equal(now) {
		t.Fatalf("LastLoadedOrChecked = %v, found=%v; want %v", got.LastLoadedOrChecked, ok, now)
	}
}

func TestRefreshSerializesSameFolderCycles(t *testing.T) {
	cfg := &config.Config{Mail: config.Mail{Accounts: []config.Account{{
		ID:       "acct",
		Email:    "acct@example.com",
		Host:     "imap.example.com",
		Port:     993,
		TLS:      boolPtr(true),
		Username: "acct@example.com",
		PasswordRef: config.PasswordRef{
			Source:   config.PasswordSourceFile,
			Provider: config.PasswordProviderLocal,
			ID:       "/ksuite-mail/acct/password",
		},
		Policy:  config.PolicyFull,
		Folders: []string{"INBOX"},
	}}}}
	repo := mustOpenRepoForRefresh(t)
	src := &trackingSource{delay: 25 * time.Millisecond}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := refresh.Refresh(context.Background(), cfg, repo, src, refresh.RefreshOptions{MaxCandidates: 10})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	if got := atomic.LoadInt32(&src.maxActiveSelects); got != 1 {
		t.Fatalf("concurrent selects for same folder = %d, want serialized", got)
	}
}

type trackingSource struct {
	activeSelects    int32
	maxActiveSelects int32
	delay            time.Duration
}

func (s *trackingSource) Capabilities(context.Context, config.Account) ([]string, error) {
	return nil, nil
}

func (s *trackingSource) Folders(context.Context, config.Account) ([]string, error) {
	return nil, nil
}

func (s *trackingSource) SelectFolder(context.Context, config.Account, string) (mail.RemoteFolderState, error) {
	active := atomic.AddInt32(&s.activeSelects, 1)
	for {
		maxSeen := atomic.LoadInt32(&s.maxActiveSelects)
		if active <= maxSeen || atomic.CompareAndSwapInt32(&s.maxActiveSelects, maxSeen, active) {
			break
		}
	}
	time.Sleep(s.delay)
	atomic.AddInt32(&s.activeSelects, -1)
	return mail.RemoteFolderState{UIDVALIDITY: 123, UIDNEXT: 1}, nil
}

func (s *trackingSource) SearchAllowed(context.Context, config.Account, string, string, string, mail.UIDRange) ([]mail.UID, error) {
	return nil, nil
}

func (s *trackingSource) ListUIDs(context.Context, config.Account, string, mail.UIDRange) ([]mail.UID, error) {
	return nil, nil
}

func (s *trackingSource) FetchHeaders(context.Context, config.Account, string, []mail.UID) ([]mail.MessageHeaders, error) {
	return nil, nil
}

func (s *trackingSource) FetchEnvelopes(context.Context, config.Account, string, []mail.UID) ([]mail.MessageEnvelope, error) {
	return nil, nil
}

func (s *trackingSource) FetchBodyPreview(context.Context, config.Account, string, mail.UID, int) (string, error) {
	return "", nil
}

func (s *trackingSource) FetchBodyPreviewAndSeenState(context.Context, config.Account, string, mail.UID, int) (string, mail.ReadStateProbeResult, error) {
	return "", mail.ReadStateProbeResult{}, nil
}

func mustOpenRepoForRefresh(t *testing.T) *cache.Repository {
	t.Helper()
	repo, err := cache.NewRepository(cache.DBOptions{Path: mustTempDB(t)})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	return repo
}

func mustTempDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir + "/mail.db"
}

func boolPtr(v bool) *bool { return &v }

func ptrTime(t time.Time) *time.Time {
	return &t
}
