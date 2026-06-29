// Package mail models the normalized mailbox objects used by the daemon-side cache
// and refresh path.
package mail

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSourceUnavailable marks a configured-but-unwired remote mail source.
var ErrSourceUnavailable = errors.New("remote mail source unavailable")

// UID is a local mailbox UID within an IMAP folder.
type UID uint32

// FolderState models cached IMAP folder synchronization metadata.
type FolderState struct {
	AccountID             string
	Folder                string
	UIDVALIDITY           uint64
	UIDNEXT               uint64
	UIDSeen               uint64
	LastRefreshAttempted  time.Time
	LastSuccessfulRefresh time.Time
}

// MessageEnvelope is the normalized message metadata/content shape fetched
// after policy has allowed a UID.
type MessageEnvelope struct {
	UID            UID
	MessageID      string
	ThreadKey      string
	Subject        string
	From           string
	To             string
	Cc             string
	Bcc            string
	Date           time.Time
	Flags          string
	Snippet        string
	BodyPreview    string
	HasAttachments bool
	ContentHash    string
}

// MessageHeaders is the minimal header-only shape used to validate policy
// before fetching snippets, bodies, or other content-derived metadata.
type MessageHeaders struct {
	UID  UID
	From string
	To   string
	Cc   string
	Bcc  string
}

// CachedMessage is the compact cache shape read by API endpoints.
type CachedMessage struct {
	ID                  string
	AccountID           string
	Folder              string
	UIDVALIDITY         uint64
	UID                 UID
	MessageID           string
	ThreadKey           string
	Subject             string
	From                string
	To                  string
	Cc                  string
	Bcc                 string
	Date                time.Time
	Flags               string
	HasAttachments      bool
	Snippet             string
	BodyText            string
	VisibleReason       string
	ContentHash         string
	FirstLoadedAt       time.Time
	LastLoadedOrChecked time.Time
}

// PublicID creates a stable, opaque identifier for message caching and API
// responses. The input identity fields never appear literally in the returned ID.
func PublicID(accountID, folder string, uidvalidity uint64, uid UID) string {
	raw := fmt.Sprintf("%s|%s|%d|%d", accountID, folder, uidvalidity, uid)
	sum := sha256.Sum256([]byte(raw))
	return "msg_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:16]))
}
