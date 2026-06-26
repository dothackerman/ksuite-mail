// Package refresh coordinates on-demand, read-only mailbox refresh behavior.
package refresh

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/cache"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/policy"
)

// DefaultPreviewBytes is used to cache body previews during refresh.
const DefaultPreviewBytes = 4096

// Warning carries a structured, redacted remote or policy warning for command responses.
type Warning struct {
	Code    string
	Message string
}

const (
	remoteErrorCodePrefix = "remote_"
)

// Result summarizes one refresh cycle.
type Result struct {
	Meta     cache.RefreshMeta
	Warnings []Warning
	Partial  bool
}

type source interface {
	SelectFolder(ctx context.Context, acct config.Account, folder string) (mail.RemoteFolderState, error)
	SearchAllowed(ctx context.Context, acct config.Account, folder string, header string, value string, scope mail.UIDRange) ([]mail.UID, error)
	ListUIDs(ctx context.Context, acct config.Account, folder string, scope mail.UIDRange) ([]mail.UID, error)
	FetchEnvelopes(ctx context.Context, acct config.Account, folder string, uids []mail.UID) ([]mail.MessageEnvelope, error)
	FetchBodyPreview(ctx context.Context, acct config.Account, folder string, uid mail.UID, maxBytes int) (string, error)
}

// RefreshOptions configures the coordinator.
type RefreshOptions struct {
	Now           func() time.Time
	PreviewBytes  int
	MaxCandidates int
}

// Refresh updates local cache for all folders in config.Mail.Accounts.
func Refresh(ctx context.Context, cfg *config.Config, repo *cache.Repository, src source, opts RefreshOptions) (Result, error) {
	now := timestamp(opts.Now)
	if repo == nil {
		return Result{}, errors.New("cache repository is required")
	}
	if cfg == nil {
		return Result{}, errors.New("config is required")
	}
	res := Result{
		Meta: cache.RefreshMeta{
			Attempted:               src != nil,
			RemoteOK:                false,
			LastSuccessfulRefreshAt: nil,
		},
	}
	if src == nil || len(cfg.Mail.Accounts) == 0 {
		res.Meta.Attempted = false
		res.Meta.RemoteOK = true
		return res, nil
	}

	previewBytes := opts.PreviewBytes
	if previewBytes <= 0 {
		previewBytes = DefaultPreviewBytes
	}

	anySuccess := false
	allSuccess := true
	var lastSuccess time.Time
	for i := range cfg.Mail.Accounts {
		account := &cfg.Mail.Accounts[i]
		for _, folder := range account.Folders {
			if err := refreshFolder(ctx, repo, src, account, folder, previewBytes, now, opts.MaxCandidates); err != nil {
				allSuccess = false
				res.Partial = true
				res.Warnings = append(res.Warnings, Warning{
					Code:    remoteErrorCode(err),
					Message: "remote refresh failed for one or more folders",
				})
				continue
			}
			anySuccess = true
			lastSuccess = now
		}
	}
	if anySuccess {
		res.Meta.RemoteOK = allSuccess
		res.Meta.LastSuccessfulRefreshAt = &lastSuccess
	} else {
		res.Meta.RemoteOK = false
		res.Meta.LastSuccessfulRefreshAt = repo.LatestRefreshAt()
	}
	return res, nil
}

func refreshFolder(
	ctx context.Context,
	ctxRepo *cache.Repository,
	src source,
	account *config.Account,
	folder string,
	previewBytes int,
	now time.Time,
	maxCandidates int,
) error {
	state, err := ctxRepo.FolderState(account.ID, folder)
	if err != nil {
		return err
	}
	remote, err := src.SelectFolder(ctx, *account, folder)
	if err != nil {
		return err
	}
	fingerprint := policyFingerprint(account)

	// UIDVALIDITY changed => clear the local folder and restart from scratch.
	if state != nil && state.UIDVALIDITY != remote.UIDVALIDITY {
		if err := ctxRepo.DeleteByFolder(account.ID, folder); err != nil {
			return err
		}
		state = nil
	}

	if state != nil && state.PolicyFingerprint == fingerprint &&
		state.UIDVALIDITY == remote.UIDVALIDITY && state.UIDNEXT == remote.UIDNEXT &&
		state.HighestModSeq != 0 && state.HighestModSeq == remote.HighestModSeq {
		return ctxRepo.UpsertFolderState(cache.FolderState{
			AccountID:             account.ID,
			Folder:                folder,
			UIDVALIDITY:           remote.UIDVALIDITY,
			UIDNEXT:               remote.UIDNEXT,
			HighestModSeq:         remote.HighestModSeq,
			LastSeenUID:           state.LastSeenUID,
			PolicyFingerprint:     fingerprint,
			LastRefreshAttempted:  &now,
			LastSuccessfulRefresh: &now,
		})
	}

	candidates, complete, err := discoverCandidates(ctx, src, account, folder, remote.UIDNEXT, maxCandidates)
	if err != nil {
		return err
	}
	envelopes, err := src.FetchEnvelopes(ctx, *account, folder, candidates)
	if err != nil {
		return err
	}

	keep := cache.UIDSetFromSlice(nil)
	for _, env := range envelopes {
		ok, reason := policy.DomainMatch(*account, env)
		if !ok {
			continue
		}
		keep[env.UID] = struct{}{}

		bodyText, err := src.FetchBodyPreview(ctx, *account, folder, env.UID, previewBytes)
		if err != nil {
			return err
		}
		msg := mail.CachedMessage{
			ID:                  mail.PublicID(account.ID, folder, remote.UIDVALIDITY, env.UID),
			AccountID:           account.ID,
			Folder:              folder,
			UIDVALIDITY:         remote.UIDVALIDITY,
			UID:                 env.UID,
			MessageID:           env.MessageID,
			ThreadKey:           env.ThreadKey,
			Subject:             env.Subject,
			From:                env.From,
			To:                  env.To,
			Cc:                  env.Cc,
			Bcc:                 env.Bcc,
			Date:                env.Date,
			Flags:               env.Flags,
			HasAttachments:      env.HasAttachments,
			Snippet:             env.Snippet,
			BodyText:            bodyText,
			VisibleReason:       reason,
			ContentHash:         env.ContentHash,
			FirstLoadedAt:       now,
			LastLoadedOrChecked: now,
		}
		if err := ctxRepo.UpsertMessage(msg); err != nil {
			return err
		}
	}

	if complete {
		if err := ctxRepo.DeleteMissingByUIDSet(account.ID, folder, keep); err != nil {
			return err
		}
	}

	lastSeenUID := uint64(0)
	if state != nil {
		lastSeenUID = state.LastSeenUID
	}
	for uid := range keep {
		if uint64(uid) > lastSeenUID {
			lastSeenUID = uint64(uid)
		}
	}
	highestModSeq := remote.HighestModSeq
	if !complete {
		highestModSeq = 0
	}
	if err := ctxRepo.UpsertFolderState(cache.FolderState{
		AccountID:             account.ID,
		Folder:                folder,
		UIDVALIDITY:           remote.UIDVALIDITY,
		UIDNEXT:               remote.UIDNEXT,
		HighestModSeq:         highestModSeq,
		LastSeenUID:           lastSeenUID,
		PolicyFingerprint:     fingerprint,
		LastRefreshAttempted:  &now,
		LastSuccessfulRefresh: &now,
	}); err != nil {
		return err
	}
	return nil
}

func discoverCandidates(
	ctx context.Context,
	src source,
	account *config.Account,
	folder string,
	uidNext uint64,
	maxCandidates int,
) ([]mail.UID, bool, error) {
	scope := mail.UIDRange{}
	if uidNext > 0 {
		scope.Max = uidNext
	}

	if account.Policy != config.PolicyDomain {
		uids, err := src.ListUIDs(ctx, *account, folder, scope)
		if err != nil {
			return nil, false, err
		}
		if maxCandidates > 0 && len(uids) > maxCandidates {
			return uids[len(uids)-maxCandidates:], false, nil
		}
		return uids, true, nil
	}

	var out []mail.UID
	for _, header := range []string{"From", "To", "Cc", "Bcc"} {
		for _, domain := range account.Domains {
			domain = strings.TrimSpace(domain)
			if domain == "" {
				continue
			}
			domain = strings.ToLower(domain)
			uids, err := src.SearchAllowed(ctx, *account, folder, header, domain, scope)
			if err != nil {
				return nil, false, err
			}
			out = append(out, uids...)
		}
	}
	out = dedupeUIDs(out)
	if maxCandidates > 0 && len(out) > maxCandidates {
		return out[len(out)-maxCandidates:], false, nil
	}
	return out, true, nil
}

func policyFingerprint(account *config.Account) string {
	if account == nil {
		return ""
	}
	domains := make([]string, 0, len(account.Domains))
	for _, domain := range account.Domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	return account.Policy + ":" + strings.Join(domains, ",")
}

func dedupeUIDs(uids []mail.UID) []mail.UID {
	if len(uids) == 0 {
		return nil
	}
	seen := make(map[mail.UID]struct{}, len(uids))
	uniq := make([]mail.UID, 0, len(uids))
	for _, uid := range uids {
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		uniq = append(uniq, uid)
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i] < uniq[j] })
	return uniq
}

func timestamp(now func() time.Time) time.Time {
	if now != nil {
		t := now()
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func remoteErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return remoteErrorCodePrefix + "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return remoteErrorCodePrefix + "timeout"
	}
	return remoteErrorCodePrefix + "source_error"
}
