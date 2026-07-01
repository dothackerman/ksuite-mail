package mail

import (
	"context"

	"github.com/dothackerman/ksuite-mail/internal/config"
)

// UIDRange scopes adapter queries.
type UIDRange struct {
	Min uint64
	Max uint64
}

// RemoteFolderState carries IMAP metadata returned by SelectFolder.
type RemoteFolderState struct {
	UIDVALIDITY   uint64
	UIDNEXT       uint64
	HighestModSeq int64
	ReadOnly      bool
	SelectionMode string
}

// Source is the narrow adapter interface that hides IMAP details.
type Source interface {
	Capabilities(ctx context.Context, acct config.Account) ([]string, error)
	Folders(ctx context.Context, acct config.Account) ([]string, error)
	SelectFolder(ctx context.Context, acct config.Account, folder string) (RemoteFolderState, error)
	SearchAllowed(ctx context.Context, acct config.Account, folder string, header string, value string, scope UIDRange) ([]UID, error)
	ListUIDs(ctx context.Context, acct config.Account, folder string, scope UIDRange) ([]UID, error)
	FetchHeaders(ctx context.Context, acct config.Account, folder string, uids []UID) ([]MessageHeaders, error)
	FetchEnvelopes(ctx context.Context, acct config.Account, folder string, uids []UID) ([]MessageEnvelope, error)
	FetchBodyPreview(ctx context.Context, acct config.Account, folder string, uid UID, maxBytes int) (string, error)
	FetchBodyPreviewAndSeenState(ctx context.Context, acct config.Account, folder string, uid UID, maxBytes int) (string, bool, error)
}
