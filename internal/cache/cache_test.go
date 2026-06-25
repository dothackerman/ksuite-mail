package cache

import (
	"os"
	"path/filepath"
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
