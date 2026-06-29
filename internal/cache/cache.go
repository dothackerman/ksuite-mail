// Package cache stores approved messages and folder synchronization metadata in a
// local SQLite/FTS5 database owned by the daemon.
package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dothackerman/ksuite-mail/internal/layout"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

var writeMu sync.Mutex

// RefreshMeta carries command-level refresh metadata.
type RefreshMeta struct {
	Attempted               bool       `json:"attempted"`
	RemoteOK                bool       `json:"remote_ok"`
	LastSuccessfulRefreshAt *time.Time `json:"last_successful_refresh_at,omitempty"`
}

// MessageRef identifies one mailbox row in local storage.
type MessageRef struct {
	AccountID   string
	Folder      string
	UIDVALIDITY uint64
	UID         mail.UID
}

// FolderState stores IMAP sync metadata.
type FolderState struct {
	AccountID             string
	Folder                string
	UIDVALIDITY           uint64
	UIDNEXT               uint64
	HighestModSeq         int64
	LastSeenUID           uint64
	PolicyFingerprint     string
	LastRefreshAttempted  *time.Time
	LastSuccessfulRefresh *time.Time
}

// QueryFilter scopes cache reads.
type QueryFilter struct {
	AccountID string
	Folder    string
	Query     string
	Limit     int
	Offset    int
}

// UIDSet is a deduping utility used for stale-row removal.
type UIDSet map[mail.UID]struct{}

// Repository owns one open cache connection.
type Repository struct {
	db   *sql.DB
	now  func() time.Time
	path string
}

// DBOptions configures repository initialization.
type DBOptions struct {
	Path string
	Now  func() time.Time
}

// NewRepository opens the cache database and applies the schema.
func NewRepository(opts DBOptions) (*Repository, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("cache path is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := seedDBPath(opts.Path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", opts.Path)
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}
	r := &Repository{db: db, now: opts.Now, path: opts.Path}
	if err := r.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return r, nil
}

// Close releases the underlying DB handle.
func (r *Repository) Close() error { return r.db.Close() }

// Path returns the concrete database path for inspection tests.
func (r *Repository) Path() string { return r.path }

func seedDBPath(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create cache db: %w", err)
	}
	_ = f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod cache db: %w", err)
	}
	return nil
}

func (r *Repository) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			public_id TEXT NOT NULL UNIQUE,
			account_id TEXT NOT NULL,
			folder TEXT NOT NULL,
			uidvalidity INTEGER NOT NULL,
			uid INTEGER NOT NULL,
			message_id TEXT NOT NULL,
			thread_key TEXT NOT NULL,
			subject TEXT NOT NULL,
			from_text TEXT NOT NULL,
			to_text TEXT NOT NULL,
			cc_text TEXT NOT NULL,
			bcc_text TEXT NOT NULL,
			date TEXT NOT NULL,
			flags TEXT NOT NULL,
			has_attachments INTEGER NOT NULL DEFAULT 0,
			snippet TEXT NOT NULL,
			body_text TEXT NOT NULL,
			visible_reason TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			first_loaded_at TEXT NOT NULL,
			last_loaded_or_verified_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS folder_state (
			account_id TEXT NOT NULL,
			folder TEXT NOT NULL,
			uidvalidity INTEGER NOT NULL,
			uidnext INTEGER NOT NULL,
			highestmodseq INTEGER NOT NULL DEFAULT 0,
			last_seen_uid INTEGER NOT NULL DEFAULT 0,
			policy_fingerprint TEXT NOT NULL DEFAULT '',
			last_refresh_attempted_at TEXT,
			last_successful_refresh_at TEXT,
			PRIMARY KEY (account_id, folder)
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS messages_account_folder_uid ON messages(account_id, folder, uidvalidity, uid);`,
		`CREATE INDEX IF NOT EXISTS messages_account_folder_date_idx ON messages(account_id, folder, date);`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			public_id,
			subject,
			from_text,
			to_text,
			cc_text,
			bcc_text,
			snippet,
			body_text
		);`,
	}
	for _, s := range stmts {
		if _, err := r.db.Exec(s); err != nil {
			return fmt.Errorf("cache migration: %w", err)
		}
	}
	if err := r.ensureFolderStatePolicyFingerprintColumn(); err != nil {
		return fmt.Errorf("cache migration: %w", err)
	}
	return nil
}

func (r *Repository) ensureFolderStatePolicyFingerprintColumn() error {
	rows, err := r.db.Query(`PRAGMA table_info(folder_state)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "policy_fingerprint" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = r.db.Exec(`ALTER TABLE folder_state ADD COLUMN policy_fingerprint TEXT NOT NULL DEFAULT ''`)
	return err
}

func rowToMessage(scan fnScanner) (mail.CachedMessage, error) {
	var (
		msg                                     mail.CachedMessage
		dateText, firstLoadedText, verifiedText string
		uid, uidvalidity, hasAttach             uint64
	)
	if err := scan.Scan(
		&msg.ID, &msg.AccountID, &msg.Folder, &uidvalidity, &uid,
		&msg.MessageID, &msg.ThreadKey, &msg.Subject, &msg.From, &msg.To,
		&msg.Cc, &msg.Bcc, &dateText, &msg.Flags, &hasAttach, &msg.Snippet,
		&msg.BodyText, &msg.VisibleReason, &msg.ContentHash,
		&firstLoadedText, &verifiedText,
	); err != nil {
		return msg, err
	}
	msg.UIDVALIDITY = uidvalidity
	msg.UID = mail.UID(uid)
	msg.HasAttachments = hasAttach != 0
	var err error
	msg.Date, err = time.Parse(time.RFC3339Nano, dateText)
	if err != nil {
		return msg, err
	}
	msg.FirstLoadedAt, err = time.Parse(time.RFC3339Nano, firstLoadedText)
	if err != nil {
		return msg, err
	}
	msg.LastLoadedOrChecked, err = time.Parse(time.RFC3339Nano, verifiedText)
	if err != nil {
		return msg, err
	}
	return msg, nil
}

type fnScanner interface {
	Scan(dest ...any) error
}

// UpsertMessage writes a policy-approved message into both content and FTS tables.
func (r *Repository) UpsertMessage(msg mail.CachedMessage) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := r.now().UTC().Format(time.RFC3339Nano)
	if msg.FirstLoadedAt.IsZero() {
		msg.FirstLoadedAt = r.now()
	}
	lastLoadedOrChecked := r.now().UTC()
	if !msg.LastLoadedOrChecked.IsZero() {
		lastLoadedOrChecked = msg.LastLoadedOrChecked.UTC()
	}
	const insert = `
		INSERT INTO messages(
			public_id, account_id, folder, uidvalidity, uid,
			message_id, thread_key, subject, from_text, to_text, cc_text, bcc_text,
			date, flags, has_attachments, snippet, body_text, visible_reason, content_hash,
			first_loaded_at, last_loaded_or_verified_at, updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(public_id) DO UPDATE SET
			message_id = excluded.message_id,
			thread_key = excluded.thread_key,
			subject = excluded.subject,
			from_text = excluded.from_text,
			to_text = excluded.to_text,
			cc_text = excluded.cc_text,
			bcc_text = excluded.bcc_text,
			date = excluded.date,
			flags = excluded.flags,
			has_attachments = excluded.has_attachments,
			snippet = excluded.snippet,
			body_text = excluded.body_text,
			visible_reason = excluded.visible_reason,
			content_hash = excluded.content_hash,
			last_loaded_or_verified_at = excluded.last_loaded_or_verified_at,
			updated_at = excluded.updated_at;`
	if _, err := tx.Exec(insert,
		msg.ID, msg.AccountID, msg.Folder, msg.UIDVALIDITY, msg.UID,
		msg.MessageID, msg.ThreadKey, msg.Subject, msg.From, msg.To, msg.Cc, msg.Bcc,
		msg.Date.UTC().Format(time.RFC3339Nano), msg.Flags, boolToInt(msg.HasAttachments),
		msg.Snippet, msg.BodyText, msg.VisibleReason, msg.ContentHash,
		msg.FirstLoadedAt.UTC().Format(time.RFC3339Nano), lastLoadedOrChecked.Format(time.RFC3339Nano), now,
	); err != nil {
		return rollback(tx, fmt.Errorf("upsert message: %w", err))
	}

	if _, err := tx.Exec(`DELETE FROM messages_fts WHERE public_id = ?`, msg.ID); err != nil {
		return rollback(tx, fmt.Errorf("delete stale fts: %w", err))
	}
	if _, err := tx.Exec(`INSERT INTO messages_fts(public_id, subject, from_text, to_text, cc_text, bcc_text, snippet, body_text)
			VALUES(?,?,?,?,?,?,?,?)`,
		msg.ID, msg.Subject, msg.From, msg.To, msg.Cc, "", msg.Snippet, msg.BodyText,
	); err != nil {
		return rollback(tx, fmt.Errorf("insert fts row: %w", err))
	}
	return tx.Commit()
}

// GetByPublicID returns the cached message if present.
func (r *Repository) GetByPublicID(publicID string) (mail.CachedMessage, bool, error) {
	row := r.db.QueryRow(`SELECT
		public_id, account_id, folder, uidvalidity, uid,
		message_id, thread_key, subject, from_text, to_text, cc_text, bcc_text,
		date, flags, has_attachments, snippet, body_text, visible_reason, content_hash,
		first_loaded_at, last_loaded_or_verified_at
	FROM messages WHERE public_id=?`, publicID)
	msg, err := rowToMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return msg, false, nil
		}
		return msg, false, err
	}
	return msg, true, nil
}

// ListMessages returns messages ordered by date descending.
func (r *Repository) ListMessages(filter QueryFilter) ([]mail.CachedMessage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	stmt := `SELECT
		public_id, account_id, folder, uidvalidity, uid,
		message_id, thread_key, subject, from_text, to_text, cc_text, bcc_text,
		date, flags, has_attachments, snippet, body_text, visible_reason, content_hash,
		first_loaded_at, last_loaded_or_verified_at
	FROM messages`
	args := []any{}
	clauses := []string{}
	if filter.AccountID != "" {
		clauses = append(clauses, "account_id=?")
		args = append(args, filter.AccountID)
	}
	if filter.Folder != "" {
		clauses = append(clauses, "folder=?")
		args = append(args, filter.Folder)
	}
	if len(clauses) > 0 {
		stmt += " WHERE " + strings.Join(clauses, " AND ")
	}
	stmt += " ORDER BY datetime(date) DESC, uid DESC, public_id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, filter.Offset)

	rows, err := r.db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []mail.CachedMessage
	for rows.Next() {
		msg, err := rowToMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// SearchMessages runs FTS5 search across stable fields.
func (r *Repository) SearchMessages(filter QueryFilter) ([]mail.CachedMessage, error) {
	if strings.TrimSpace(filter.Query) == "" {
		return r.ListMessages(filter)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	clauses := []string{"messages_fts MATCH ?"}
	args := []any{plainTextFTSQuery(filter.Query)}
	if filter.AccountID != "" {
		clauses = append(clauses, "m.account_id=?")
		args = append(args, filter.AccountID)
	}
	if filter.Folder != "" {
		clauses = append(clauses, "m.folder=?")
		args = append(args, filter.Folder)
	}
	stmt := `SELECT
		m.public_id, m.account_id, m.folder, m.uidvalidity, m.uid,
		m.message_id, m.thread_key, m.subject, m.from_text, m.to_text, m.cc_text, m.bcc_text,
		m.date, m.flags, m.has_attachments, m.snippet, m.body_text, m.visible_reason, m.content_hash,
		m.first_loaded_at, m.last_loaded_or_verified_at
	FROM messages_fts f
	JOIN messages m ON m.public_id = f.public_id`
	stmt += " WHERE " + strings.Join(clauses, " AND ")
	stmt += " ORDER BY datetime(m.date) DESC, m.uid DESC, m.public_id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, filter.Offset)

	rows, err := r.db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []mail.CachedMessage
	for rows.Next() {
		msg, err := rowToMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func plainTextFTSQuery(query string) string {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return `""`
	}
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}

// ThreadMessages returns cached messages for one thread key.
func (r *Repository) ThreadMessages(accountID, threadKey string, limit, offset int) ([]mail.CachedMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.Query(`SELECT
		public_id, account_id, folder, uidvalidity, uid,
		message_id, thread_key, subject, from_text, to_text, cc_text, bcc_text,
		date, flags, has_attachments, snippet, body_text, visible_reason, content_hash,
		first_loaded_at, last_loaded_or_verified_at
	FROM messages WHERE account_id=? AND thread_key=? ORDER BY datetime(date) DESC, uid DESC, public_id DESC LIMIT ? OFFSET ?`,
		accountID, threadKey, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []mail.CachedMessage
	for rows.Next() {
		msg, err := rowToMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// ListUIDsForFolder returns local UIDs by account and folder.
func (r *Repository) ListUIDsForFolder(accountID, folder string) ([]mail.UID, error) {
	rows, err := r.db.Query(`SELECT uid FROM messages WHERE account_id=? AND folder=?`, accountID, folder)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []mail.UID
	for rows.Next() {
		var uid uint64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, mail.UID(uid))
	}
	return out, rows.Err()
}

// MarkFolderVerified refreshes TTL verification time for cached rows known to
// be covered by a successful no-change folder refresh.
func (r *Repository) MarkFolderVerified(accountID, folder string, uidvalidity uint64, checkedAt time.Time) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	verified := checkedAt.UTC().Format(time.RFC3339Nano)
	_, err := r.db.Exec(`
		UPDATE messages
		SET last_loaded_or_verified_at=?, updated_at=?
		WHERE account_id=? AND folder=? AND uidvalidity=?`,
		verified, r.now().UTC().Format(time.RFC3339Nano), accountID, folder, uidvalidity)
	return err
}

// MarkUIDsVerified refreshes TTL verification time for cached rows observed in
// a bounded refresh window.
func (r *Repository) MarkUIDsVerified(accountID, folder string, uidvalidity uint64, uids UIDSet, checkedAt time.Time) error {
	if len(uids) == 0 {
		return nil
	}
	writeMu.Lock()
	defer writeMu.Unlock()

	verified := checkedAt.UTC().Format(time.RFC3339Nano)
	updated := r.now().UTC().Format(time.RFC3339Nano)
	for uid := range uids {
		if _, err := r.db.Exec(`
			UPDATE messages
			SET last_loaded_or_verified_at=?, updated_at=?
			WHERE account_id=? AND folder=? AND uidvalidity=? AND uid=?`,
			verified, updated, accountID, folder, uidvalidity, uid); err != nil {
			return err
		}
	}
	return nil
}

// DeleteByMessageRef removes one local row including its FTS entry.
func (r *Repository) DeleteByMessageRef(ref MessageRef) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var publicID string
	if err := tx.QueryRow(`SELECT public_id FROM messages WHERE account_id=? AND folder=? AND uid=?`,
		ref.AccountID, ref.Folder, ref.UID).Scan(&publicID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return rollback(tx, err)
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE public_id=?`, publicID); err != nil {
		return rollback(tx, err)
	}
	if _, err := tx.Exec(`DELETE FROM messages_fts WHERE public_id=?`, publicID); err != nil {
		return rollback(tx, err)
	}
	return tx.Commit()
}

// DeleteByFolder deletes all rows in an account/folder and matching FTS rows.
func (r *Repository) DeleteByFolder(accountID, folder string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT public_id FROM messages WHERE account_id=? AND folder=?`, accountID, folder)
	if err != nil {
		return rollback(tx, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return rollback(tx, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return rollback(tx, err)
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE account_id=? AND folder=?`, accountID, folder); err != nil {
		return rollback(tx, err)
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM messages_fts WHERE public_id=?`, id); err != nil {
			return rollback(tx, err)
		}
	}
	return tx.Commit()
}

// DeleteMissingByUIDSet removes local rows that are not present in keep.
func (r *Repository) DeleteMissingByUIDSet(accountID, folder string, keep UIDSet) error {
	local, err := r.ListUIDsForFolder(accountID, folder)
	if err != nil {
		return err
	}
	for _, uid := range local {
		if _, ok := keep[uid]; ok {
			continue
		}
		if err := r.DeleteByMessageRef(MessageRef{
			AccountID:   accountID,
			Folder:      folder,
			UIDVALIDITY: 0,
			UID:         uid,
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteMissingByUIDSetInRange removes local rows inside the observed remote
// UID range that are not present in keep.
func (r *Repository) DeleteMissingByUIDSetInRange(accountID, folder string, keep UIDSet, minUID, maxUID mail.UID) error {
	if minUID > maxUID {
		return nil
	}
	local, err := r.ListUIDsForFolder(accountID, folder)
	if err != nil {
		return err
	}
	for _, uid := range local {
		if uid < minUID || uid > maxUID {
			continue
		}
		if _, ok := keep[uid]; ok {
			continue
		}
		if err := r.DeleteByMessageRef(MessageRef{
			AccountID:   accountID,
			Folder:      folder,
			UIDVALIDITY: 0,
			UID:         uid,
		}); err != nil {
			return err
		}
	}
	return nil
}

// UpsertFolderState records IMAP sync metadata.
func (r *Repository) UpsertFolderState(fs FolderState) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	now := r.now().UTC().Format(time.RFC3339Nano)
	if fs.LastRefreshAttempted == nil {
		t := r.now()
		fs.LastRefreshAttempted = &t
	}
	const upsert = `
		INSERT INTO folder_state(
			account_id, folder, uidvalidity, uidnext, highestmodseq, last_seen_uid, policy_fingerprint,
			last_refresh_attempted_at, last_successful_refresh_at
		) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id, folder) DO UPDATE SET
			uidvalidity = excluded.uidvalidity,
			uidnext = excluded.uidnext,
			highestmodseq = excluded.highestmodseq,
			last_seen_uid = excluded.last_seen_uid,
			policy_fingerprint = excluded.policy_fingerprint,
			last_refresh_attempted_at = excluded.last_refresh_attempted_at,
			last_successful_refresh_at = excluded.last_successful_refresh_at;`
	var attempted, successful any
	if fs.LastRefreshAttempted != nil {
		attempted = fs.LastRefreshAttempted.UTC().Format(time.RFC3339Nano)
	} else {
		attempted = now
	}
	if fs.LastSuccessfulRefresh != nil {
		successful = fs.LastSuccessfulRefresh.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.Exec(upsert,
		fs.AccountID, fs.Folder, fs.UIDVALIDITY, fs.UIDNEXT, fs.HighestModSeq, fs.LastSeenUID, fs.PolicyFingerprint,
		attempted, successful,
	)
	return err
}

// FolderState returns cached sync metadata for one account/folder.
func (r *Repository) FolderState(accountID, folder string) (*FolderState, error) {
	row := r.db.QueryRow(`SELECT
		account_id, folder, uidvalidity, uidnext, highestmodseq, last_seen_uid, policy_fingerprint,
		last_refresh_attempted_at, last_successful_refresh_at
	FROM folder_state WHERE account_id=? AND folder=?`, accountID, folder)
	var (
		fs                         FolderState
		attemptedText, successText sql.NullString
	)
	if err := row.Scan(
		&fs.AccountID, &fs.Folder, &fs.UIDVALIDITY, &fs.UIDNEXT, &fs.HighestModSeq, &fs.LastSeenUID, &fs.PolicyFingerprint,
		&attemptedText, &successText,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if attemptedText.Valid {
		t, err := time.Parse(time.RFC3339Nano, attemptedText.String)
		if err != nil {
			return nil, err
		}
		fs.LastRefreshAttempted = &t
	}
	if successText.Valid {
		t, err := time.Parse(time.RFC3339Nano, successText.String)
		if err != nil {
			return nil, err
		}
		fs.LastSuccessfulRefresh = &t
	}
	return &fs, nil
}

// DeleteAll removes all cached rows and metadata.
func (r *Repository) DeleteAll() error {
	writeMu.Lock()
	defer writeMu.Unlock()

	if _, err := r.db.Exec(`DELETE FROM messages`); err != nil {
		return err
	}
	if _, err := r.db.Exec(`DELETE FROM messages_fts`); err != nil {
		return err
	}
	_, err := r.db.Exec(`DELETE FROM folder_state`)
	return err
}

// CleanupExpired removes rows older than ttl.
func (r *Repository) CleanupExpired(ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	threshold := r.now().Add(-ttl).UTC().Format(time.RFC3339Nano)
	rows, err := r.db.Query(`SELECT public_id, account_id, folder FROM messages WHERE last_loaded_or_verified_at < ?`, threshold)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type expiredRow struct {
		id        string
		accountID string
		folder    string
	}
	var expired []expiredRow
	for rows.Next() {
		var id, accountID, folder string
		if err := rows.Scan(&id, &accountID, &folder); err != nil {
			return err
		}
		expired = append(expired, expiredRow{id: id, accountID: accountID, folder: folder})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(expired) == 0 {
		return nil
	}
	writeMu.Lock()
	defer writeMu.Unlock()

	sort.Slice(expired, func(i, j int) bool { return expired[i].id < expired[j].id })
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	affectedFolders := map[string]struct{}{}
	for _, row := range expired {
		res, err := tx.Exec(`DELETE FROM messages WHERE public_id=? AND last_loaded_or_verified_at < ?`, row.id, threshold)
		if err != nil {
			return rollback(tx, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return rollback(tx, err)
		}
		if affected == 0 {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM messages_fts WHERE public_id=?`, row.id); err != nil {
			return rollback(tx, err)
		}
		affectedFolders[row.accountID+"\x00"+row.folder] = struct{}{}
	}
	for key := range affectedFolders {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		var retained int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE account_id=? AND folder=?`, parts[0], parts[1]).Scan(&retained); err != nil {
			return rollback(tx, err)
		}
		if retained > 0 {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM folder_state WHERE account_id=? AND folder=?`, parts[0], parts[1]); err != nil {
			return rollback(tx, err)
		}
	}
	return tx.Commit()
}

// LatestRefreshAt returns the newest folder_state.last_successful_refresh_at value.
func (r *Repository) LatestRefreshAt() *time.Time {
	row := r.db.QueryRow(`SELECT COALESCE(MAX(last_successful_refresh_at), NULL) FROM folder_state`)
	var raw sql.NullString
	if err := row.Scan(&raw); err != nil || !raw.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil
	}
	return &t
}

// UIDSetFromSlice is a test helper to build a deduping UID set.
func UIDSetFromSlice(uids []mail.UID) UIDSet {
	set := make(UIDSet, len(uids))
	for _, uid := range uids {
		set[uid] = struct{}{}
	}
	return set
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func rollback(tx *sql.Tx, err error) error {
	_ = tx.Rollback()
	return err
}

// SeedFromStateDir creates and chmods the canonical DB path for a given state
// directory. Hidden by cache initialization in production, but useful for tests and
// docs that validate file mode.
func SeedFromStateDir(stateDir string) error {
	if stateDir == "" {
		stateDir = layout.StateDir
	}
	return seedDBPath(filepath.Join(stateDir, "mail.db"))
}
