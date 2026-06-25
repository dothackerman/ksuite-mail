// Package mailfake provides deterministic fixtures and call-order tracking for unit
// and end-to-end tests.
package mailfake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

// Call captures one adapter call with method and target.
type Call struct {
	Method  string
	Target  string
	Payload string
}

// Message provides a fixture envelope and body payload.
type Message struct {
	UID             mail.UID
	Envelope        mail.MessageEnvelope
	Body            string
	VisibleByPolicy bool
	SearchByHeader  map[string][]mail.UID
}

// FolderData stores per-folder adapter state.
type FolderData struct {
	UIDVALIDITY     uint64
	UIDNEXT         uint64
	HMS             int64
	Messages        map[mail.UID]Message
	SearchResults   map[string][]mail.UID
	FailureByMethod map[string]error
}

type Failure struct {
	When string
	Err  error
}

// Adapter is a deterministic, fixture-backed mail.Source used for tests.
type Adapter struct {
	mu    sync.Mutex
	seed  map[string]map[string]FolderData
	Calls []Call
}

const (
	methodSelect  = "select"
	methodSearch  = "search"
	methodList    = "list"
	methodFetch   = "fetch"
	methodPreview = "preview"
)

const (
	defaultUIDVALIDITY = 123
)

func failureKey(method, account, folder, arg string) string {
	if arg != "" {
		return method + ":" + account + ":" + folder + ":" + arg
	}
	return method + ":" + account + ":" + folder
}

func parseInt(v uint64) string {
	return fmt.Sprintf("%d", v)
}

func rangeLabel(r mail.UIDRange) string {
	return parseInt(r.Min) + ":" + parseInt(r.Max)
}

// NewAdapter builds an in-memory fake source from fixture payload.
func NewAdapter(seed map[string]map[string][]Message, failures ...Failure) *Adapter {
	out := &Adapter{
		seed: map[string]map[string]FolderData{},
	}
	for _, f := range failures {
		parts := strings.SplitN(f.When, ":", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		out.setFailure(parts[0], parts[1], "", f.Err)
	}

	for acctID, folders := range seed {
		out.seed[acctID] = map[string]FolderData{}
		for folder, messages := range folders {
			fd := FolderData{
				UIDVALIDITY:     defaultUIDVALIDITY,
				UIDNEXT:         1,
				HMS:             0,
				Messages:        map[mail.UID]Message{},
				SearchResults:   map[string][]mail.UID{},
				FailureByMethod: map[string]error{},
			}
			for _, m := range messages {
				msg := m
				fd.Messages[m.UID] = msg
				if uint64(msg.UID) >= fd.UIDNEXT {
					fd.UIDNEXT = uint64(msg.UID) + 1
				}
				msg.UID = m.UID
				fd.Messages[m.UID] = msg
			}
			out.seed[acctID][folder] = fd
		}
	}
	return out
}

// SetSearchResult installs explicit UID search results for a header key/value pair.
func (a *Adapter) SetSearchResult(account, folder, header, value string, uids []mail.UID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fd := a.ensureFolderLocked(account, folder)
	key := strings.ToLower(strings.TrimSpace(header)) + ":" + strings.TrimSpace(value)
	clean := append([]mail.UID(nil), uids...)
	sort.Slice(clean, func(i, j int) bool { return clean[i] < clean[j] })
	fd.SearchResults[key] = clean
	a.seed[account][folder] = *fd
}

// SetUIDVALIDITY updates the folder UIDVALIDITY and resets UIDNEXT by convention.
func (a *Adapter) SetUIDVALIDITY(account, folder string, uidvalidity uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fd := a.ensureFolderLocked(account, folder)
	fd.UIDVALIDITY = uidvalidity
	fd.UIDNEXT = 1
	a.seed[account][folder] = *fd
}

// DeleteMessage removes a UID from a folder.
func (a *Adapter) DeleteMessage(account, folder string, uid mail.UID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fd, ok := a.seed[account][folder]
	if !ok {
		return
	}
	delete(fd.Messages, uid)
	a.seed[account][folder] = fd
}

// MoveMessage migrates one UID from source to destination.
func (a *Adapter) MoveMessage(account, fromFolder, toFolder string, uid mail.UID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	src, ok := a.seed[account][fromFolder]
	if !ok {
		return
	}
	msg, ok := src.Messages[uid]
	if !ok {
		return
	}
	delete(src.Messages, uid)
	a.seed[account][fromFolder] = src

	dst := a.ensureFolderLocked(account, toFolder)
	dst.Messages[uid] = msg
	if uint64(uid) >= dst.UIDNEXT {
		dst.UIDNEXT = uint64(uid) + 1
	}
	a.seed[account][toFolder] = *dst
}

// SetFailure assigns a method-specific source error.
func (a *Adapter) SetFailure(method, account, folder, arg string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fd := a.ensureFolderLocked(account, folder)
	fd.FailureByMethod[failureKey(method, account, folder, arg)] = err
	a.seed[account][folder] = *fd
}

// CallsSnapshot returns a point-in-time view of request order.
func (a *Adapter) CallsSnapshot() []Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Call, len(a.Calls))
	copy(out, a.Calls)
	return out
}

func (a *Adapter) resetCallsLocked() {
	a.Calls = nil
}

// ResetCalls clears call history.
func (a *Adapter) ResetCalls() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resetCallsLocked()
}

func (a *Adapter) checkFailure(method, account, folder, arg string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	fd, ok := a.seed[account][folder]
	if !ok {
		return nil
	}
	if fd.FailureByMethod == nil {
		return nil
	}
	key := failureKey(method, account, folder, arg)
	return fd.FailureByMethod[key]
}

func (a *Adapter) appendCall(method, accountID, folder, payload string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls = append(a.Calls, Call{Method: method, Target: accountID + "@" + folder, Payload: payload})
}

func (a *Adapter) seedOrCreate(account string) map[string]FolderData {
	fm, ok := a.seed[account]
	if !ok {
		fm = map[string]FolderData{}
		a.seed[account] = fm
	}
	return fm
}

func (a *Adapter) ensureFolderLocked(account, folder string) *FolderData {
	fm := a.seedOrCreate(account)
	fd, ok := fm[folder]
	if !ok {
		fd = FolderData{
			UIDVALIDITY:     defaultUIDVALIDITY,
			UIDNEXT:         1,
			HMS:             0,
			Messages:        map[mail.UID]Message{},
			SearchResults:   map[string][]mail.UID{},
			FailureByMethod: map[string]error{},
		}
	}
	return &fd
}

func (a *Adapter) setFailure(method, accountFolder string, arg string, err error) {
	parts := strings.SplitN(accountFolder, ":", 2)
	if len(parts) != 2 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	account := strings.TrimSpace(parts[0])
	folder := strings.TrimSpace(parts[1])
	fd := a.ensureFolderLocked(account, folder)
	if fd.FailureByMethod == nil {
		fd.FailureByMethod = map[string]error{}
	}
	fd.FailureByMethod[failureKey(method, account, folder, arg)] = err
	a.seed[account][folder] = *fd
}

// SelectFolder simulates IMAP folder SELECT/EXAMINE and returns folder metadata.
func (a *Adapter) SelectFolder(_ context.Context, acct config.Account, folder string) (mail.RemoteFolderState, error) {
	a.appendCall(methodSelect, acct.ID, folder, "select")
	if err := a.checkFailure(methodSelect, acct.ID, folder, ""); err != nil {
		return mail.RemoteFolderState{}, err
	}

	fd, ok := a.folder(acct, folder)
	if !ok {
		return mail.RemoteFolderState{}, errors.New("folder not found: " + acct.ID + ":" + folder)
	}
	return mail.RemoteFolderState{
		UIDVALIDITY:   fd.UIDVALIDITY,
		UIDNEXT:       fd.UIDNEXT,
		HighestModSeq: fd.HMS,
	}, nil
}

// SearchAllowed simulates UID SEARCH HEADER queries.
func (a *Adapter) SearchAllowed(_ context.Context, acct config.Account, folder string, header string, value string, scope mail.UIDRange) ([]mail.UID, error) {
	key := strings.ToLower(strings.TrimSpace(header)) + ":" + strings.TrimSpace(value)
	a.appendCall(methodSearch, acct.ID, folder, key)
	if err := a.checkFailure(methodSearch, acct.ID, folder, key); err != nil {
		return nil, err
	}
	fd, ok := a.folder(acct, folder)
	if !ok {
		return nil, errors.New("folder not found: " + acct.ID + ":" + folder)
	}
	if explicit, ok := fd.SearchResults[key]; ok {
		out := append([]mail.UID(nil), explicit...)
		return filterUIDRange(out, scope), nil
	}

	var out []mail.UID
	for _, msg := range fd.Messages {
		if !msg.VisibleByPolicy {
			continue
		}
		uid := msg.UID
		if uid == 0 {
			continue
		}
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return filterUIDRange(out, scope), nil
}

// ListUIDs returns all fixture message UIDs in the folder, with optional range cap.
func (a *Adapter) ListUIDs(_ context.Context, acct config.Account, folder string, scope mail.UIDRange) ([]mail.UID, error) {
	a.appendCall(methodList, acct.ID, folder, rangeLabel(scope))
	if err := a.checkFailure(methodList, acct.ID, folder, ""); err != nil {
		return nil, err
	}
	fd, ok := a.folder(acct, folder)
	if !ok {
		return nil, errors.New("folder not found: " + acct.ID + ":" + folder)
	}
	var out []mail.UID
	for uid := range fd.Messages {
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return filterUIDRange(out, scope), nil
}

// FetchEnvelopes returns envelopes for selected UIDs.
func (a *Adapter) FetchEnvelopes(_ context.Context, acct config.Account, folder string, uids []mail.UID) ([]mail.MessageEnvelope, error) {
	a.appendCall(methodFetch, acct.ID, folder, "uids")
	if err := a.checkFailure(methodFetch, acct.ID, folder, ""); err != nil {
		return nil, err
	}
	fd, ok := a.folder(acct, folder)
	if !ok {
		return nil, errors.New("folder not found: " + acct.ID + ":" + folder)
	}
	out := make([]mail.MessageEnvelope, 0, len(uids))
	for _, uid := range uids {
		msg, ok := fd.Messages[uid]
		if !ok {
			continue
		}
		env := msg.Envelope
		env.UID = uid
		out = append(out, env)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// FetchBodyPreview returns a capped body preview for one UID.
func (a *Adapter) FetchBodyPreview(_ context.Context, acct config.Account, folder string, uid mail.UID, maxBytes int) (string, error) {
	a.appendCall(methodPreview, acct.ID, folder, "uid="+fmt.Sprintf("%d", uid))
	if err := a.checkFailure(methodPreview, acct.ID, folder, ""); err != nil {
		return "", err
	}
	fd, ok := a.folder(acct, folder)
	if !ok {
		return "", errors.New("folder not found: " + acct.ID + ":" + folder)
	}
	msg, ok := fd.Messages[uid]
	if !ok {
		return "", errors.New("message not found")
	}
	if maxBytes <= 0 {
		return msg.Body, nil
	}
	body := msg.Body
	if len(body) > maxBytes {
		body = body[:maxBytes]
	}
	return body, nil
}

func (a *Adapter) folder(acct config.Account, folder string) (FolderData, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fm, ok := a.seed[acct.ID]
	if !ok {
		return FolderData{}, false
	}
	fd, ok := fm[folder]
	if !ok {
		return FolderData{}, false
	}
	return fd, true
}

func filterUIDRange(uids []mail.UID, scope mail.UIDRange) []mail.UID {
	if scope == (mail.UIDRange{}) {
		return dedupeAndSort(uids)
	}
	var out []mail.UID
	for _, uid := range uids {
		if scope.Min > 0 && uint64(uid) < scope.Min {
			continue
		}
		if scope.Max > 0 && uint64(uid) > scope.Max {
			continue
		}
		out = append(out, uid)
	}
	return dedupeAndSort(out)
}

func dedupeAndSort(uids []mail.UID) []mail.UID {
	if len(uids) == 0 {
		return []mail.UID{}
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
