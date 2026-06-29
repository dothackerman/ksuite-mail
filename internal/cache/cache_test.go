package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/mail"
)

func TestRepositoryCreatesFileWithSecureMode(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "mail.db")
	repo := mustOpenRepo(t, dbPath)
	defer func() { _ = repo.Close() }()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("db stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want %#o", got, 0o600)
	}
}

func TestRepositoryCanCreateAndQueryFTS5(t *testing.T) {
	t.Parallel()

	repo := mustOpenRepo(t, filepath.Join(t.TempDir(), "mail.db"))
	defer func() { _ = repo.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	msg := mail.CachedMessage{
		ID:                  "msg_match",
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         10,
		UID:                 10,
		MessageID:           "<match@case>",
		ThreadKey:           "thread-a",
		Subject:             "Invoice for regenerative project",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Date:                now,
		Flags:               "\\Seen",
		Snippet:             "renewal reminder",
		BodyText:            "project notes with phrase searchable",
		VisibleReason:       "full",
		ContentHash:         "deadbeef",
		FirstLoadedAt:       now,
		LastLoadedOrChecked: now,
	}
	if err := repo.UpsertMessage(msg); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	results, err := repo.SearchMessages(QueryFilter{Folder: "INBOX", Query: "searchable"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
}

func TestRepositorySerializesWritesAcrossConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mail.db")
	repoA := mustOpenRepo(t, dbPath)
	defer func() { _ = repoA.Close() }()
	repoB := mustOpenRepo(t, dbPath)
	defer func() { _ = repoB.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	repos := []*Repository{repoA, repoB}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			uid := mail.UID(i + 1)
			errs <- repos[i%len(repos)].UpsertMessage(mail.CachedMessage{
				ID:                  fmt.Sprintf("msg_concurrent_%02d", i),
				AccountID:           "acct",
				Folder:              "INBOX",
				UIDVALIDITY:         10,
				UID:                 uid,
				MessageID:           fmt.Sprintf("<msg-%02d>", i),
				ThreadKey:           "thread-concurrent",
				Subject:             "concurrent",
				From:                "alice@example.com",
				To:                  "bob@example.com",
				Date:                now.Add(time.Duration(i) * time.Second),
				Snippet:             "body",
				BodyText:            "body",
				VisibleReason:       "policy_full",
				ContentHash:         fmt.Sprintf("hash-%02d", i),
				FirstLoadedAt:       now,
				LastLoadedOrChecked: now,
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	msgs, err := repoA.ListMessages(QueryFilter{AccountID: "acct", Folder: "INBOX", Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 40 {
		t.Fatalf("messages = %d, want 40", len(msgs))
	}
}

func TestSearchMessagesTreatsPunctuationAsPlainText(t *testing.T) {
	t.Parallel()

	repo := mustOpenRepo(t, filepath.Join(t.TempDir(), "mail.db"))
	defer func() { _ = repo.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  "msg_punctuation",
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         10,
		UID:                 11,
		MessageID:           "<punctuation@case>",
		ThreadKey:           "thread-p",
		Subject:             "invoice #123",
		From:                "alice@example.com",
		To:                  "bob@regenerativ.ch",
		Date:                now,
		Flags:               "\\Seen",
		Snippet:             "foo-bar account",
		BodyText:            "credits for regenerativ.ch",
		VisibleReason:       "full",
		ContentHash:         "h",
		FirstLoadedAt:       now,
		LastLoadedOrChecked: now,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	for _, query := range []string{"alice@example.com", "regenerativ.ch", "foo-bar", "invoice #123"} {
		results, err := repo.SearchMessages(QueryFilter{Query: query})
		if err != nil {
			t.Fatalf("SearchMessages(%q): %v", query, err)
		}
		if len(results) != 1 {
			t.Fatalf("SearchMessages(%q) = %d results, want 1", query, len(results))
		}
	}
}

func TestSearchMessagesDoesNotIndexHiddenBcc(t *testing.T) {
	t.Parallel()

	repo := mustOpenRepo(t, filepath.Join(t.TempDir(), "mail.db"))
	defer func() { _ = repo.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  "msg_hidden_bcc",
		AccountID:           "acct",
		Folder:              "Sent",
		UIDVALIDITY:         10,
		UID:                 12,
		MessageID:           "<hidden-bcc@case>",
		ThreadKey:           "thread-bcc",
		Subject:             "visible sent mail",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Bcc:                 "private-bcc@example.net",
		Date:                now,
		Snippet:             "visible sent mail",
		BodyText:            "visible body",
		VisibleReason:       "domain:from",
		ContentHash:         "h",
		FirstLoadedAt:       now,
		LastLoadedOrChecked: now,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	results, err := repo.SearchMessages(QueryFilter{Query: "private-bcc@example.net"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("hidden bcc was searchable: %#v", results)
	}
}

func TestCleanupUsesLastLoadedOrCheckedForTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoWithTime(t, filepath.Join(t.TempDir(), "mail.db"), now)
	defer func() { _ = repo.Close() }()

	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-48 * time.Hour)

	expired := mail.CachedMessage{
		ID:                  "msg_expired",
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         10,
		UID:                 1,
		MessageID:           "<expired@case>",
		ThreadKey:           "thread-a",
		Subject:             "expired",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Date:                now,
		Snippet:             "old date",
		BodyText:            "kept only by verification",
		VisibleReason:       "full",
		ContentHash:         "c1",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}
	active := mail.CachedMessage{
		ID:                  "msg_active",
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         10,
		UID:                 2,
		MessageID:           "<active@case>",
		ThreadKey:           "thread-b",
		Subject:             "active",
		From:                "carol@example.com",
		To:                  "dan@example.com",
		Date:                old,
		Snippet:             "verified recently",
		BodyText:            "keep because verified now",
		VisibleReason:       "full",
		ContentHash:         "c2",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: recent,
	}

	if err := repo.UpsertMessage(expired); err != nil {
		t.Fatalf("upsert expired: %v", err)
	}
	if err := repo.UpsertMessage(active); err != nil {
		t.Fatalf("upsert active: %v", err)
	}

	if err := repo.CleanupExpired(72 * time.Hour); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	if _, ok, err := repo.GetByPublicID("msg_expired"); err != nil {
		t.Fatalf("GetByPublicID expired: %v", err)
	} else if ok {
		t.Fatalf("expected msg_expired to be purged")
	}
	if _, ok, err := repo.GetByPublicID("msg_active"); err != nil {
		t.Fatalf("GetByPublicID active: %v", err)
	} else if !ok {
		t.Fatalf("expected msg_active to remain")
	}
}

func TestCleanupExpiredInvalidatesFolderStateForPurgedRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	repo := mustOpenRepoWithTime(t, filepath.Join(t.TempDir(), "mail.db"), now)
	defer func() { _ = repo.Close() }()

	old := now.Add(-100 * 24 * time.Hour)
	if err := repo.UpsertMessage(mail.CachedMessage{
		ID:                  "msg_expired_state",
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         10,
		UID:                 9,
		MessageID:           "<expired-state@case>",
		ThreadKey:           "thread-state",
		Subject:             "expired state",
		From:                "alice@example.com",
		To:                  "bob@example.com",
		Date:                now,
		Snippet:             "expired",
		BodyText:            "expired",
		VisibleReason:       "full",
		ContentHash:         "c1",
		FirstLoadedAt:       old,
		LastLoadedOrChecked: old,
	}); err != nil {
		t.Fatalf("upsert expired: %v", err)
	}
	if err := repo.UpsertFolderState(FolderState{
		AccountID:         "acct",
		Folder:            "INBOX",
		UIDVALIDITY:       10,
		UIDNEXT:           10,
		HighestModSeq:     22,
		LastSeenUID:       9,
		PolicyFingerprint: "full:",
	}); err != nil {
		t.Fatalf("upsert state: %v", err)
	}

	if err := repo.CleanupExpired(72 * time.Hour); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	state, err := repo.FolderState("acct", "INBOX")
	if err != nil {
		t.Fatalf("FolderState: %v", err)
	}
	if state != nil {
		t.Fatalf("folder state survived TTL cleanup: %+v", state)
	}
}

func TestDeleteByMessageRefAlsoRemovesFTSIndex(t *testing.T) {
	t.Parallel()

	repo := mustOpenRepo(t, filepath.Join(t.TempDir(), "mail.db"))
	defer func() { _ = repo.Close() }()
	now := time.Now().UTC()

	msg := mail.CachedMessage{
		ID:                  "msg_t1",
		AccountID:           "acct",
		Folder:              "INBOX",
		UIDVALIDITY:         10,
		UID:                 1,
		MessageID:           "<a@case>",
		ThreadKey:           "thread",
		Subject:             "delete me",
		From:                "a@x",
		To:                  "b@x",
		Date:                now,
		Snippet:             "delete me",
		BodyText:            "delete me",
		VisibleReason:       "full",
		ContentHash:         "v",
		FirstLoadedAt:       now,
		LastLoadedOrChecked: now,
	}
	if err := repo.UpsertMessage(msg); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, ok, err := repo.GetByPublicID("msg_t1"); err != nil {
		t.Fatalf("pre-delete get: %v", err)
	} else if !ok {
		t.Fatalf("seed row missing before delete")
	}
	before, err := repo.SearchMessages(QueryFilter{Query: "delete"})
	if err != nil {
		t.Fatalf("search before delete: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("search before delete = %d, want 1", len(before))
	}

	if err := repo.DeleteByMessageRef(MessageRef{AccountID: "acct", Folder: "INBOX", UID: 1}); err != nil {
		t.Fatalf("DeleteByMessageRef: %v", err)
	}
	if _, ok, err := repo.GetByPublicID("msg_t1"); err != nil {
		t.Fatalf("post-delete get: %v", err)
	} else if ok {
		t.Fatalf("row still exists after delete")
	}
	if after, err := repo.SearchMessages(QueryFilter{Query: "delete"}); err != nil {
		t.Fatalf("search after delete: %v", err)
	} else if len(after) != 0 {
		t.Fatalf("search after delete = %d, want 0", len(after))
	}
}

func TestThreadMessagesUsesStableTieBreakerForSameDate(t *testing.T) {
	t.Parallel()

	repo := mustOpenRepo(t, filepath.Join(t.TempDir(), "mail.db"))
	defer func() { _ = repo.Close() }()
	now := time.Now().UTC().Truncate(time.Second)

	for _, msg := range []mail.CachedMessage{
		{
			ID:                  "msg_lower_uid",
			AccountID:           "acct",
			Folder:              "INBOX",
			UIDVALIDITY:         10,
			UID:                 1,
			MessageID:           "<lower@case>",
			ThreadKey:           "thread-tie",
			Subject:             "lower",
			From:                "a@x",
			To:                  "b@x",
			Date:                now,
			Snippet:             "lower",
			BodyText:            "lower",
			VisibleReason:       "full",
			ContentHash:         "lower",
			FirstLoadedAt:       now,
			LastLoadedOrChecked: now,
		},
		{
			ID:                  "msg_higher_uid",
			AccountID:           "acct",
			Folder:              "INBOX",
			UIDVALIDITY:         10,
			UID:                 2,
			MessageID:           "<higher@case>",
			ThreadKey:           "thread-tie",
			Subject:             "higher",
			From:                "a@x",
			To:                  "b@x",
			Date:                now,
			Snippet:             "higher",
			BodyText:            "higher",
			VisibleReason:       "full",
			ContentHash:         "higher",
			FirstLoadedAt:       now,
			LastLoadedOrChecked: now,
		},
	} {
		if err := repo.UpsertMessage(msg); err != nil {
			t.Fatalf("UpsertMessage(%s): %v", msg.ID, err)
		}
	}

	got, err := repo.ThreadMessages("acct", "thread-tie", 100, 0)
	if err != nil {
		t.Fatalf("ThreadMessages: %v", err)
	}
	if len(got) != 2 || got[0].ID != "msg_higher_uid" || got[1].ID != "msg_lower_uid" {
		t.Fatalf("thread order = %#v, want higher UID before lower UID for equal dates", got)
	}
}

func TestThreadMessagesAppliesLimitAndOffsetInQuery(t *testing.T) {
	t.Parallel()

	repo := mustOpenRepo(t, filepath.Join(t.TempDir(), "mail.db"))
	defer func() { _ = repo.Close() }()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 3; i++ {
		msg := mail.CachedMessage{
			ID:                  "msg_page_" + string(rune('0'+i)),
			AccountID:           "acct",
			Folder:              "INBOX",
			UIDVALIDITY:         10,
			UID:                 mail.UID(i),
			MessageID:           "<page@case>",
			ThreadKey:           "thread-page",
			Subject:             "page",
			From:                "a@x",
			To:                  "b@x",
			Date:                now.Add(time.Duration(i) * time.Minute),
			Snippet:             "page",
			BodyText:            "page",
			VisibleReason:       "full",
			ContentHash:         "page",
			FirstLoadedAt:       now,
			LastLoadedOrChecked: now,
		}
		if err := repo.UpsertMessage(msg); err != nil {
			t.Fatalf("UpsertMessage(%s): %v", msg.ID, err)
		}
	}

	got, err := repo.ThreadMessages("acct", "thread-page", 1, 1)
	if err != nil {
		t.Fatalf("ThreadMessages: %v", err)
	}
	if len(got) != 1 || got[0].ID != "msg_page_2" {
		t.Fatalf("paged thread = %#v, want second newest row", got)
	}
}

func TestSeedFromStateDirCreatesPath(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "state")
	if err := SeedFromStateDir(stateDir); err != nil {
		t.Fatalf("SeedFromStateDir: %v", err)
	}
	dbPath := filepath.Join(stateDir, "mail.db")
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want %#o", got, 0o600)
	}
}

func mustOpenRepo(t *testing.T, p string) *Repository {
	t.Helper()
	repo, err := NewRepository(DBOptions{Path: p})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	return repo
}

func mustOpenRepoWithTime(t *testing.T, p string, now time.Time) *Repository {
	t.Helper()
	repo, err := NewRepository(DBOptions{Path: p, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	return repo
}
