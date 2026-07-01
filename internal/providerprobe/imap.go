// Package providerprobe runs fixed, sanitized provider capability checks.
package providerprobe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

const probeNegativeValue = "ksuite-mail-probe.invalid"

type checkID string

const (
	checkAccountSelection checkID = "account_selection"
	checkFixedChecklist   checkID = "fixed_checklist"
	checkCapability       checkID = "capability"
	checkFolderListing    checkID = "folder_listing"
	checkFolderSelection  checkID = "folder_selection"
	checkUIDBehavior      checkID = "uid_behavior"
	checkDomainHeader     checkID = "domain_header_search"
	checkReadState        checkID = "read_state"
)

// Runner executes provider probe checklists through the narrow mail.Source port.
type Runner struct{}

// RunIMAP returns sanitized, stable diagnostics for the fixed IMAP checklist.
func (Runner) RunIMAP(ctx context.Context, src mail.Source, acct config.Account) api.ProbeIMAPResponse {
	checks := []api.ProbeCheck{
		probeCheck(checkAccountSelection, api.ProbeStatusPassed, "configured_account", "selected an existing daemon-side account"),
		probeCheck(checkFixedChecklist, api.ProbeStatusPassed, "escape_hatch_unavailable", "probe uses the fixed daemon-side checklist contract"),
	}

	if src == nil {
		checks = append(checks,
			probeCheck(checkCapability, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			probeCheck(checkFolderListing, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			probeCheck(checkFolderSelection, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			probeCheck(checkUIDBehavior, api.ProbeStatusFailed, "source_unavailable", "mail source is unavailable"),
			domainProbeNotRun(acct),
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
			domainProbeNotRun(acct),
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
			domainProbeNotRun(acct),
			probeNotRun(checkReadState),
		)
		return response(acct, checks)
	}
	if len(folders) == 0 {
		checks = append(checks,
			probeCheck(checkFolderListing, api.ProbeStatusInconclusive, "fixture_required", "folder_count=0"),
			probeNotRun(checkFolderSelection),
			probeNotRun(checkUIDBehavior),
			domainProbeNotRun(acct),
			probeNotRun(checkReadState),
		)
		return response(acct, checks)
	}
	checks = append(checks, probeCheck(checkFolderListing, api.ProbeStatusPassed, "list_ok", "folder_count="+strconv.Itoa(len(folders))))

	folder := firstProbeFolder(acct)
	if folder == "" {
		checks = append(checks,
			probeCheck(checkFolderSelection, api.ProbeStatusInconclusive, "no_configured_folder", "selected account has no configured folders"),
			probeCheck(checkUIDBehavior, api.ProbeStatusInconclusive, "no_configured_folder", "selected account has no configured folders"),
			domainProbeNotRun(acct),
			probeCheck(checkReadState, api.ProbeStatusInconclusive, "no_configured_folder", "selected account has no configured folders"),
		)
	} else {
		state, err := src.SelectFolder(ctx, acct, folder)
		if err != nil {
			checks = append(checks, probeFailure(checkFolderSelection, err))
			checks = append(checks,
				probeNotRun(checkUIDBehavior),
				domainProbeNotRun(acct),
				probeNotRun(checkReadState),
			)
		} else if state.UIDVALIDITY == 0 || state.UIDNEXT == 0 {
			detail := fmt.Sprintf("configured_folder=true uidvalidity_present=%t uidnext_present=%t", state.UIDVALIDITY != 0, state.UIDNEXT != 0)
			checks = append(checks, probeCheck(checkFolderSelection, api.ProbeStatusInconclusive, "uid_state_required", detail))
			checks = append(checks,
				probeNotRun(checkUIDBehavior),
				domainProbeNotRun(acct),
				probeNotRun(checkReadState),
			)
		} else {
			detail := fmt.Sprintf("configured_folder=true uidvalidity=%d uidnext=%d highestmodseq=%d", state.UIDVALIDITY, state.UIDNEXT, state.HighestModSeq)
			checks = append(checks, probeCheck(checkFolderSelection, api.ProbeStatusPassed, "examine_ok", detail))
			checks = append(checks, probeUIDBehavior(ctx, src, acct, folder))
			checks = append(checks, probeDomainHeaders(ctx, src, acct, folder))
			checks = append(checks, probeReadState(ctx, src, acct, folder))
		}
	}

	return response(acct, checks)
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
		return probeCheck(checkUIDBehavior, api.ProbeStatusFailed, "uid_range_mismatch", fmt.Sprintf("uid_count=%d range_count=%d", len(uids), len(ranged)))
	}
	return probeCheck(checkUIDBehavior, api.ProbeStatusPassed, "uid_range_ok", fmt.Sprintf("uid_count=%d range_count=%d", len(uids), len(ranged)))
}

func probeDomainHeaders(ctx context.Context, src mail.Source, acct config.Account, folder string) api.ProbeCheck {
	if acct.Policy == config.PolicyFull {
		return probeCheck(checkDomainHeader, api.ProbeStatusNotApplicable, "full_policy", "full-policy accounts do not depend on domain-header filtering")
	}
	if len(acct.Domains) == 0 {
		return probeCheck(checkDomainHeader, api.ProbeStatusInconclusive, "no_domain", "domain-policy account has no configured domain")
	}
	sentFolder := sentProbeFolder(acct, folder)
	headers := []string{"From", "To", "Cc", "Bcc"}
	counts := make([]string, 0, len(headers)*len(acct.Domains))
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
			if header == "Bcc" && sentFolder != "" {
				targetFolder = sentFolder
			}
			uids, err := src.SearchAllowed(ctx, acct, targetFolder, header, domain, mail.UIDRange{})
			if err != nil {
				return probeFailure(checkDomainHeader, err)
			}
			if len(uids) == 0 {
				missingFixture = true
			}
			negativeUIDs, err := src.SearchAllowed(ctx, acct, targetFolder, header, probeNegativeValue, mail.UIDRange{})
			if err != nil {
				return probeFailure(checkDomainHeader, err)
			}
			if len(negativeUIDs) > 0 {
				detail := fmt.Sprintf("header=%s domain_index_checked=true nonmatching_visible=true", strings.ToLower(header))
				return probeCheck(checkDomainHeader, api.ProbeStatusFailed, "header_search_overbroad", detail)
			}
			counts = append(counts, strings.ToLower(header)+"_count="+strconv.Itoa(len(uids)))
		}
	}
	if !checkedDomain {
		return probeCheck(checkDomainHeader, api.ProbeStatusInconclusive, "no_domain", "domain-policy account has no configured domain")
	}
	if missingFixture {
		return probeCheck(checkDomainHeader, api.ProbeStatusInconclusive, "fixture_required", strings.Join(counts, " "))
	}
	return probeCheck(checkDomainHeader, api.ProbeStatusPassed, "header_search_ok", strings.Join(counts, " "))
}

func probeReadState(ctx context.Context, src mail.Source, acct config.Account, folder string) api.ProbeCheck {
	uids, err := src.ListUIDs(ctx, acct, folder, mail.UIDRange{})
	if err != nil {
		return probeFailure(checkReadState, err)
	}
	if len(uids) == 0 {
		return probeCheck(checkReadState, api.ProbeStatusInconclusive, "fixture_required", "BODY.PEEK read-state fixture is required")
	}
	if _, err := src.FetchBodyPreview(ctx, acct, folder, uids[0], 1); err != nil {
		return probeFailure(checkReadState, err)
	}
	return probeCheck(checkReadState, api.ProbeStatusPassed, "body_peek_ok", "body_peek_exercised=true")
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

func sentProbeFolder(acct config.Account, fallback string) string {
	for _, folder := range acct.Folders {
		clean := strings.TrimSpace(folder)
		if clean == "" {
			continue
		}
		if strings.Contains(strings.ToLower(clean), "sent") {
			return clean
		}
	}
	return fallback
}

func probeFailure(id checkID, err error) api.ProbeCheck {
	return probeCheck(id, api.ProbeStatusFailed, probeSourceErrorCode(err), "provider probe failed")
}

func probeCheck(id checkID, status, code, detail string) api.ProbeCheck {
	return api.ProbeCheck{ID: string(id), Status: status, Code: code, Detail: detail}
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
