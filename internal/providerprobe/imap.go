// Package providerprobe runs fixed, sanitized provider capability checks.
package providerprobe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

const probeNegativeValue = "ksuite-mail-probe.invalid"
const readStateFixtureScanLimit = 10

type checkID string

const (
	checkAccountSelection  checkID = "account_selection"
	checkFixedChecklist    checkID = "fixed_checklist"
	checkCapability        checkID = "capability"
	checkFolderListing     checkID = "folder_listing"
	checkFolderSelection   checkID = "folder_selection"
	checkUIDBehavior       checkID = "uid_behavior"
	checkRefreshStrategyID checkID = "refresh_strategy"
	checkDomainHeader      checkID = "domain_header_search"
	checkSentBcc           checkID = "sent_bcc_availability"
	checkReadState         checkID = "read_state"
)

// Runner executes provider probe checklists through the narrow mail.Source port.
type Runner struct{}

// RunIMAP returns sanitized, stable diagnostics for the fixed IMAP checklist.
func (Runner) RunIMAP(ctx context.Context, src mail.Source, acct config.Account) api.ProbeIMAPResponse {
	checks := []api.ProbeCheck{
		probeCheck(checkAccountSelection, api.ProbeStatusPassed, "configured_account", "selected an existing daemon-side account"),
		probeCheck(checkFixedChecklist, api.ProbeStatusPassed, "escape_hatch_unavailable", "probe uses the fixed daemon-side checklist contract"),
	}
	var uidBehaviorCheck api.ProbeCheck
	var folderState *mail.RemoteFolderState

	if src == nil {
		checks = append(checks,
			probeCheck(checkCapability, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			probeCheck(checkFolderListing, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			probeCheck(checkFolderSelection, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			probeCheck(checkUIDBehavior, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			inconclusiveRefreshStrategy(nil, "prerequisite_failed", "refresh strategy requires a live source"),
			domainProbeNotRun(acct),
			sentBccProbeNotRun(acct),
			probeCheck(checkReadState, api.ProbeStatusInconclusive, "fixture_required", "BODY.PEEK read-state fixture is required"),
		)
		return response(acct, checks)
	}

	caps, err := src.Capabilities(ctx, acct)
	if err != nil {
		checks = append(checks, probeFailure(checkCapability, err))
		checks = append(checks,
			probeNotRun(checkFolderListing),
			probeNotRun(checkFolderSelection),
			probeNotRun(checkUIDBehavior),
			inconclusiveRefreshStrategy(nil, "prerequisite_failed", "refresh strategy requires prerequisite checks"),
			domainProbeNotRun(acct),
			sentBccProbeNotRun(acct),
			probeNotRun(checkReadState),
		)
		return response(acct, checks)
	}
	checks = append(checks, probeCheck(checkCapability, api.ProbeStatusPassed, "capability_ok", "capabilities="+strings.Join(caps, ",")))

	folders, err := src.Folders(ctx, acct)
	if err != nil {
		checks = append(checks, probeFailure(checkFolderListing, err))
		checks = append(checks,
			probeNotRun(checkFolderSelection),
			probeNotRun(checkUIDBehavior),
			inconclusiveRefreshStrategy(caps, "prerequisite_failed", "refresh strategy requires prerequisite checks"),
			domainProbeNotRun(acct),
			sentBccProbeNotRun(acct),
			probeNotRun(checkReadState),
		)
		return response(acct, checks)
	}
	if len(folders) == 0 {
		checks = append(checks,
			probeCheck(checkFolderListing, api.ProbeStatusInconclusive, "fixture_required", "folder_count=0"),
			probeNotRun(checkFolderSelection),
			probeNotRun(checkUIDBehavior),
			inconclusiveRefreshStrategy(caps, "prerequisite_failed", "refresh strategy requires prerequisite checks"),
			domainProbeNotRun(acct),
			sentBccProbeNotRun(acct),
			probeNotRun(checkReadState),
		)
		return response(acct, checks)
	}
	checks = append(checks, probeCheckFacts(checkFolderListing, api.ProbeStatusPassed, "list_ok", "folder_count="+strconv.Itoa(len(folders)), &api.ProbeFacts{
		FolderCount: uintPtr(len(folders)),
		Folders:     folders,
	}))

	folder := firstProbeFolder(acct)
	if folder == "" {
		checks = append(checks,
			probeCheck(checkFolderSelection, api.ProbeStatusInconclusive, "no_configured_folder", "selected account has no configured folders"),
			probeCheck(checkUIDBehavior, api.ProbeStatusInconclusive, "no_configured_folder", "selected account has no configured folders"),
			inconclusiveRefreshStrategy(caps, "no_configured_folder", "refresh strategy requires a configured folder"),
			domainProbeNotRun(acct),
			sentBccProbeNotRun(acct),
			probeCheck(checkReadState, api.ProbeStatusInconclusive, "no_configured_folder", "selected account has no configured folders"),
		)
	} else {
		state, err := src.SelectFolder(ctx, acct, folder)
		if err != nil {
			checks = append(checks, probeFailure(checkFolderSelection, err))
			checks = append(checks,
				probeNotRun(checkUIDBehavior),
				inconclusiveRefreshStrategy(caps, "prerequisite_failed", "refresh strategy requires folder selection"),
				domainProbeNotRun(acct),
				sentBccProbeNotRun(acct),
				probeNotRun(checkReadState),
			)
		} else if state.UIDVALIDITY == 0 || state.UIDNEXT == 0 {
			detail := fmt.Sprintf("configured_folder=true uidvalidity_present=%t uidnext_present=%t", state.UIDVALIDITY != 0, state.UIDNEXT != 0)
			checks = append(checks, probeCheckFacts(checkFolderSelection, api.ProbeStatusInconclusive, "uid_state_required", detail, folderStateFacts(folder, state, false)))
			folderState = &state
			uidBehaviorCheck = probeNotRun(checkUIDBehavior)
			checks = append(checks,
				uidBehaviorCheck,
				checkRefreshStrategy(caps, folderState, uidBehaviorCheck),
				domainProbeNotRun(acct),
				sentBccProbeNotRun(acct),
				probeNotRun(checkReadState),
			)
		} else {
			detail := fmt.Sprintf("configured_folder=true uidvalidity=%d uidnext=%d highestmodseq=%d", state.UIDVALIDITY, state.UIDNEXT, state.HighestModSeq)
			checks = append(checks, probeCheckFacts(checkFolderSelection, api.ProbeStatusPassed, "examine_ok", detail, folderStateFacts(folder, state, true)))
			folderState = &state
			uidBehaviorCheck = probeUIDBehavior(ctx, src, acct, folder)
			checks = append(checks, uidBehaviorCheck)
			checks = append(checks, checkRefreshStrategy(caps, folderState, uidBehaviorCheck))
			sentFolder := sentProbeFolderForListing(acct, folders)
			domainHeaderResult := probeDomainHeaders(ctx, src, acct, folder, sentFolder.folder)
			checks = append(checks, domainHeaderResult.check)
			checks = append(checks, probeSentBccAvailability(ctx, src, acct, sentFolder))
			if readStatePrerequisitesMet(acct, domainHeaderResult) {
				checks = append(checks, probeReadState(ctx, src, acct, folder, state, domainHeaderResult.evidence))
			} else {
				checks = append(checks, probeCheck(checkReadState, api.ProbeStatusInconclusive, "prerequisite_failed", "read-state probe requires validated domain-header search"))
			}
		}
	}

	return response(acct, checks)
}

func readStatePrerequisitesMet(acct config.Account, domainHeaderResult domainHeaderProbeResult) bool {
	if acct.Policy != config.PolicyDomain {
		return true
	}
	if domainHeaderResult.check.Status == api.ProbeStatusFailed {
		return false
	}
	return len(domainHeaderResult.evidence) > 0
}

func response(acct config.Account, checks []api.ProbeCheck) api.ProbeIMAPResponse {
	return api.ProbeIMAPResponse{Account: acct.ID, Status: aggregateProbeStatus(checks), Checks: checks}
}

func probeUIDBehavior(ctx context.Context, src mail.Source, acct config.Account, folder string) api.ProbeCheck {
	uids, err := src.ListUIDs(ctx, acct, folder, mail.UIDRange{})
	if err != nil {
		return probeFailure(checkUIDBehavior, err)
	}
	if len(uids) < 2 {
		return probeCheck(checkUIDBehavior, api.ProbeStatusInconclusive, "fixture_required", "at least two fixture UIDs are required")
	}
	scope := mail.UIDRange{Min: uint64(uids[0]), Max: uint64(uids[0])}
	ranged, err := src.ListUIDs(ctx, acct, folder, scope)
	if err != nil {
		return probeFailure(checkUIDBehavior, err)
	}
	if len(ranged) != 1 || ranged[0] != uids[0] {
		facts := &api.ProbeFacts{UIDRangeSupported: boolPtr(false)}
		return probeCheckFacts(checkUIDBehavior, api.ProbeStatusFailed, "uid_range_mismatch", fmt.Sprintf("uid_count=%d range_count=%d", len(uids), len(ranged)), facts)
	}
	facts := &api.ProbeFacts{UIDRangeSupported: boolPtr(true)}
	return probeCheckFacts(checkUIDBehavior, api.ProbeStatusPassed, "uid_range_ok", fmt.Sprintf("uid_count=%d range_count=%d", len(uids), len(ranged)), facts)
}

func checkRefreshStrategy(caps []string, state *mail.RemoteFolderState, uidBehavior api.ProbeCheck) api.ProbeCheck {
	condstoreSupported := boolPtr(supportsCondstore(caps))
	facts := &api.ProbeFacts{
		CondstoreSupported: condstoreSupported,
		UIDRangeSupported:  uidRangeSupported(uidBehavior),
	}

	if state == nil || state.UIDVALIDITY == 0 || state.UIDNEXT == 0 {
		facts.RefreshStrategy = "inconclusive"
		return probeCheckFacts(checkRefreshStrategyID, api.ProbeStatusInconclusive, "prerequisite_failed", "refresh strategy requires complete UID state", facts)
	}
	facts.UIDRangeSupported = uidRangeSupported(uidBehavior)
	facts.HighestModSeqAvailable = boolPtr(state.HighestModSeq > 0)

	if *condstoreSupported && state.HighestModSeq > 0 {
		facts.RefreshStrategy = "modseq"
		return probeCheckFacts(checkRefreshStrategyID, api.ProbeStatusPassed, "modseq_strategy", "MODSEQ strategy is supported", facts)
	}
	if uidBehavior.Status == api.ProbeStatusPassed {
		facts.RefreshStrategy = "uid_range"
		return probeCheckFacts(checkRefreshStrategyID, api.ProbeStatusPassed, "uid_range_strategy", "UID range strategy is supported", facts)
	}
	if uidBehavior.Status == api.ProbeStatusFailed && uidBehavior.Code == "uid_range_mismatch" {
		facts.RefreshStrategy = "unsupported"
		return probeCheckFacts(checkRefreshStrategyID, api.ProbeStatusFailed, "strategy_unsupported", uidBehavior.Detail, facts)
	}
	facts.RefreshStrategy = "inconclusive"
	return probeCheckFacts(checkRefreshStrategyID, api.ProbeStatusInconclusive, "strategy_inconclusive", "missing fixture evidence for strategy selection", facts)
}

func inconclusiveRefreshStrategy(caps []string, code, detail string) api.ProbeCheck {
	facts := &api.ProbeFacts{
		RefreshStrategy: "inconclusive",
	}
	if caps != nil {
		facts.CondstoreSupported = boolPtr(supportsCondstore(caps))
	}
	return probeCheckFacts(checkRefreshStrategyID, api.ProbeStatusInconclusive, code, detail, facts)
}

func uidRangeSupported(check api.ProbeCheck) *bool {
	if check.Status == api.ProbeStatusPassed {
		return boolPtr(true)
	}
	if check.Status == api.ProbeStatusFailed && check.Code == "uid_range_mismatch" {
		return boolPtr(false)
	}
	return nil
}

func hasCapability(caps []string, want string) bool {
	for _, cap := range caps {
		if strings.EqualFold(strings.TrimSpace(cap), want) {
			return true
		}
	}
	return false
}

func supportsCondstore(caps []string) bool {
	return hasCapability(caps, "CONDSTORE") || hasCapability(caps, "QRESYNC")
}

type domainHeaderProbeResult struct {
	check    api.ProbeCheck
	evidence []domainHeaderEvidence
}

type domainHeaderEvidence struct {
	folder string
	header string
	domain string
	uids   []mail.UID
}

func probeDomainHeaders(ctx context.Context, src mail.Source, acct config.Account, folder string, sentFolder string) domainHeaderProbeResult {
	if acct.Policy == config.PolicyFull {
		return domainHeaderProbeResult{check: probeCheck(checkDomainHeader, api.ProbeStatusNotApplicable, "full_policy", "full-policy accounts do not depend on domain-header filtering")}
	}
	if len(acct.Domains) == 0 {
		return domainHeaderProbeResult{check: probeCheck(checkDomainHeader, api.ProbeStatusInconclusive, "no_domain", "domain-policy account has no configured domain")}
	}
	headers := []string{"From", "To", "Cc", "Bcc"}
	facts := &api.ProbeFacts{DomainHeaderSearch: make([]api.DomainHeaderSearch, 0, len(headers)*len(acct.Domains))}
	counts := make([]string, 0, len(headers)*len(acct.Domains))
	evidence := make([]domainHeaderEvidence, 0, len(headers)*len(acct.Domains))
	missingFixture := false
	checkedDomain := false
	for _, domain := range acct.Domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		checkedDomain = true
		for _, header := range headers {
			targetFolder := folder
			if header == "Bcc" {
				if sentFolder == "" {
					missingFixture = true
					counts = append(counts, strings.ToLower(header)+"_count=0")
					facts.DomainHeaderSearch = append(facts.DomainHeaderSearch, api.DomainHeaderSearch{
						Domain:             domain,
						Header:             header,
						MatchedUIDCount:    0,
						NonmatchingVisible: false,
					})
					continue
				}
				targetFolder = sentFolder
			}
			uids, err := src.SearchAllowed(ctx, acct, targetFolder, header, domain, mail.UIDRange{})
			if err != nil {
				return domainHeaderProbeResult{check: probeFailure(checkDomainHeader, err)}
			}
			if len(uids) == 0 {
				missingFixture = true
			}
			negativeUIDs, err := src.SearchAllowed(ctx, acct, targetFolder, header, probeNegativeValue, mail.UIDRange{})
			if err != nil {
				return domainHeaderProbeResult{check: probeFailure(checkDomainHeader, err)}
			}
			nonmatchingVisible := len(negativeUIDs) > 0
			facts.DomainHeaderSearch = append(facts.DomainHeaderSearch, api.DomainHeaderSearch{
				Domain:             domain,
				Header:             header,
				MatchedUIDCount:    len(uids),
				NonmatchingVisible: nonmatchingVisible,
			})
			if len(negativeUIDs) > 0 {
				detail := fmt.Sprintf("header=%s domain=%s domain_index_checked=true nonmatching_visible=true", strings.ToLower(header), domain)
				return domainHeaderProbeResult{check: probeCheckFacts(checkDomainHeader, api.ProbeStatusFailed, "header_search_overbroad", detail, facts)}
			}
			if len(uids) > 0 {
				evidence = append(evidence, domainHeaderEvidence{
					folder: targetFolder,
					header: header,
					domain: domain,
					uids:   append([]mail.UID(nil), uids...),
				})
			}
			counts = append(counts, strings.ToLower(header)+"_count="+strconv.Itoa(len(uids)))
		}
	}
	if !checkedDomain {
		return domainHeaderProbeResult{check: probeCheck(checkDomainHeader, api.ProbeStatusInconclusive, "no_domain", "domain-policy account has no configured domain")}
	}
	if missingFixture {
		return domainHeaderProbeResult{check: probeCheckFacts(checkDomainHeader, api.ProbeStatusInconclusive, "fixture_required", strings.Join(counts, " "), facts), evidence: evidence}
	}
	return domainHeaderProbeResult{check: probeCheckFacts(checkDomainHeader, api.ProbeStatusPassed, "header_search_ok", strings.Join(counts, " "), facts), evidence: evidence}
}

func probeSentBccAvailability(ctx context.Context, src mail.Source, acct config.Account, sentFolder sentProbeFolder) api.ProbeCheck {
	if acct.Policy == config.PolicyFull {
		return probeCheck(checkSentBcc, api.ProbeStatusNotApplicable, "full_policy", "full-policy accounts do not depend on sent Bcc filtering")
	}
	domains := probeDomains(acct)
	if len(domains) == 0 {
		return probeCheck(checkSentBcc, api.ProbeStatusInconclusive, "no_domain", "domain-policy account has no configured domain")
	}
	facts := &api.ProbeFacts{SentBccAvailability: &api.SentBccAvailability{
		SentFolder:       sentFolder.folder,
		SentFolderSource: sentFolder.source,
	}}
	if sentFolder.folder == "" {
		return probeCheckFacts(checkSentBcc, api.ProbeStatusInconclusive, "sent_folder_required", "sent folder fixture is required", facts)
	}

	missingFixture := false
	var matchedUIDCount int
	for _, domain := range domains {
		uids, err := src.SearchAllowed(ctx, acct, sentFolder.folder, "Bcc", domain, mail.UIDRange{})
		if err != nil {
			return probeFailure(checkSentBcc, err)
		}
		negativeUIDs, err := src.SearchAllowed(ctx, acct, sentFolder.folder, "Bcc", probeNegativeValue, mail.UIDRange{})
		if err != nil {
			return probeFailure(checkSentBcc, err)
		}
		if len(uids) == 0 {
			missingFixture = true
		}
		matchedUIDCount += len(uids)
		if len(negativeUIDs) > 0 {
			facts.SentBccAvailability.BccAvailable = boolPtr(matchedUIDCount > 0)
			facts.SentBccAvailability.MatchedUIDCount = matchedUIDCount
			facts.SentBccAvailability.NonmatchingVisible = true
			return probeCheckFacts(checkSentBcc, api.ProbeStatusFailed, "sent_bcc_search_overbroad", "sent_bcc_nonmatching_visible=true", facts)
		}
	}

	facts.SentBccAvailability.MatchedUIDCount = matchedUIDCount
	if missingFixture {
		return probeCheckFacts(checkSentBcc, api.ProbeStatusInconclusive, "bcc_fixture_required", "sent_bcc_count="+strconv.Itoa(matchedUIDCount), facts)
	}
	facts.SentBccAvailability.BccAvailable = boolPtr(matchedUIDCount > 0)
	return probeCheckFacts(checkSentBcc, api.ProbeStatusPassed, "sent_bcc_available", "sent_bcc_count="+strconv.Itoa(matchedUIDCount), facts)
}

func probeReadState(ctx context.Context, src mail.Source, acct config.Account, folder string, state mail.RemoteFolderState, evidence []domainHeaderEvidence) api.ProbeCheck {
	uids, err := readStateFixtureUIDs(ctx, src, acct, folder, state, evidence)
	if err != nil {
		return probeFailure(checkReadState, err)
	}
	if len(uids) > readStateFixtureScanLimit {
		uids = uids[len(uids)-readStateFixtureScanLimit:]
	}
	if len(uids) == 0 {
		return probeCheck(checkReadState, api.ProbeStatusInconclusive, "fixture_required", "BODY.PEEK read-state fixture is required")
	}
	var seenState mail.ReadStateProbeResult
	foundFixture := false
	for _, uid := range uids {
		observed, seenBefore, err := src.FetchSeenState(ctx, acct, folder, uid)
		if err != nil {
			return probeFailure(checkReadState, err)
		}
		if !observed || seenBefore {
			continue
		}
		_, seenState, err = src.FetchBodyPreviewAndSeenState(ctx, acct, folder, uid, 1)
		if err != nil {
			return probeFailure(checkReadState, err)
		}
		if seenState.Observed && !seenState.SeenBefore {
			foundFixture = true
			break
		}
	}
	if !foundFixture {
		return probeCheck(checkReadState, api.ProbeStatusInconclusive, "fixture_required", "BODY.PEEK read-state fixture is required")
	}
	seenChanged := seenState.SeenAfter
	facts := &api.ProbeFacts{
		ReadStatePreserved: boolPtr(!seenChanged),
	}
	if seenChanged {
		return probeCheckFacts(checkReadState, api.ProbeStatusFailed, "body_marked_seen", "seen_state_changed=true", facts)
	}
	return probeCheckFacts(checkReadState, api.ProbeStatusPassed, "body_peek_ok", "body_peek_exercised=true", facts)
}

func readStateFixtureUIDs(ctx context.Context, src mail.Source, acct config.Account, folder string, state mail.RemoteFolderState, evidence []domainHeaderEvidence) ([]mail.UID, error) {
	scope := readStateFixtureScope(state)
	if acct.Policy != config.PolicyDomain {
		return src.ListUIDs(ctx, acct, folder, scope)
	}

	uidsByValue := make(map[mail.UID]struct{})
	for _, item := range evidence {
		if strings.TrimSpace(item.folder) != strings.TrimSpace(folder) {
			continue
		}
		for _, uid := range item.uids {
			if uidInRange(uid, scope) {
				uidsByValue[uid] = struct{}{}
			}
		}
	}

	out := make([]mail.UID, 0, len(uidsByValue))
	for uid := range uidsByValue {
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func readStateFixtureScope(state mail.RemoteFolderState) mail.UIDRange {
	if state.UIDNEXT == 0 {
		return mail.UIDRange{}
	}
	max := state.UIDNEXT - 1
	min := uint64(1)
	if max >= readStateFixtureScanLimit {
		min = max - readStateFixtureScanLimit + 1
	}
	return mail.UIDRange{Min: min, Max: max}
}

func uidInRange(uid mail.UID, scope mail.UIDRange) bool {
	value := uint64(uid)
	if scope.Min != 0 && value < scope.Min {
		return false
	}
	if scope.Max != 0 && value > scope.Max {
		return false
	}
	return true
}

func domainProbeNotRun(acct config.Account) api.ProbeCheck {
	if acct.Policy == config.PolicyFull {
		return probeCheck(checkDomainHeader, api.ProbeStatusNotApplicable, "full_policy", "full-policy accounts do not depend on domain-header filtering")
	}
	return probeNotRun(checkDomainHeader)
}

func probeNotRun(id checkID) api.ProbeCheck {
	return probeCheck(id, api.ProbeStatusInconclusive, "prerequisite_failed", string(id)+" was not run")
}

func firstProbeFolder(acct config.Account) string {
	for _, folder := range acct.Folders {
		if strings.TrimSpace(folder) != "" {
			return folder
		}
	}
	return ""
}

type sentProbeFolder struct {
	folder string
	source string
}

func sentProbeFolderForListing(acct config.Account, folders []string) sentProbeFolder {
	if folder := sentProbeFolderFromList(acct.Folders); folder != "" {
		return sentProbeFolder{folder: folder, source: "configured_folder"}
	}
	if folder := sentProbeFolderFromProviderList(folders); folder != "" {
		return sentProbeFolder{folder: folder, source: "folder_listing"}
	}
	return sentProbeFolder{}
}

func sentProbeFolderFromList(folders []string) string {
	for _, folder := range folders {
		clean := strings.TrimSpace(folder)
		if clean == "" {
			continue
		}
		if isSentFolderAlias(clean) {
			return clean
		}
	}
	return ""
}

func sentProbeFolderFromProviderList(folders []string) string {
	for _, folder := range folders {
		clean := strings.TrimSpace(folder)
		if clean == "" {
			continue
		}
		if isSentFolderAlias(clean) {
			return clean
		}
	}
	return ""
}

func isSentFolderAlias(folder string) bool {
	segments := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(folder)), func(r rune) bool {
		return r == '/' || r == '\\' || r == '.'
	})
	for _, segment := range segments {
		words := strings.Fields(strings.ReplaceAll(segment, "_", " "))
		if len(words) == 0 {
			continue
		}
		if words[0] == "sent" {
			return true
		}
	}
	return false
}

func probeDomains(acct config.Account) []string {
	out := make([]string, 0, len(acct.Domains))
	for _, domain := range acct.Domains {
		clean := strings.TrimSpace(domain)
		if clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

func sentBccProbeNotRun(acct config.Account) api.ProbeCheck {
	if acct.Policy == config.PolicyFull {
		return probeCheck(checkSentBcc, api.ProbeStatusNotApplicable, "full_policy", "full-policy accounts do not depend on sent Bcc filtering")
	}
	return probeNotRun(checkSentBcc)
}

func probeFailure(id checkID, err error) api.ProbeCheck {
	return probeCheck(id, api.ProbeStatusFailed, probeSourceErrorCode(err), "provider probe failed")
}

func probeCheck(id checkID, status, code, detail string) api.ProbeCheck {
	return api.ProbeCheck{ID: string(id), Status: status, Code: code, Detail: detail}
}

func probeCheckFacts(id checkID, status, code, detail string, facts *api.ProbeFacts) api.ProbeCheck {
	check := probeCheck(id, status, code, detail)
	check.Facts = facts
	return check
}

func folderStateFacts(folder string, state mail.RemoteFolderState, includeUIDState bool) *api.ProbeFacts {
	facts := &api.ProbeFacts{
		Folder:        folder,
		ReadOnly:      boolPtr(state.ReadOnly),
		SelectionMode: state.SelectionMode,
	}
	if includeUIDState {
		facts.UIDVALIDITY = uint64Ptr(state.UIDVALIDITY)
		facts.UIDNEXT = uint64Ptr(state.UIDNEXT)
		if state.HighestModSeq > 0 {
			facts.HighestModSeq = int64Ptr(state.HighestModSeq)
		}
	}
	return facts
}

func uintPtr(v int) *int {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}

func probeSourceErrorCode(err error) string {
	switch {
	case errors.Is(err, mail.ErrSourceUnavailable):
		return "source_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "remote_timeout"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		return "remote_failed"
	}
}

func aggregateProbeStatus(checks []api.ProbeCheck) string {
	status := api.ProbeStatusPassed
	for _, check := range checks {
		switch check.Status {
		case api.ProbeStatusFailed:
			return api.ProbeStatusFailed
		case api.ProbeStatusInconclusive:
			status = api.ProbeStatusInconclusive
		}
	}
	return status
}
