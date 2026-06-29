// Package policy applies address-domain restrictions for private accounts.
package policy

import (
	mailpkg "net/mail"
	"strings"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

// DomainMatch reports whether a message is allowed for a domain policy and which
// header provided the first matching reason.
func DomainMatch(acct config.Account, env mail.MessageEnvelope) (bool, string) {
	if acct.Policy != config.PolicyDomain {
		return true, "policy_full"
	}
	allowed := normalize(acct.Domains)
	for _, d := range exactMatch([]string{env.From}, allowed) {
		return true, "from:" + d
	}
	for _, d := range exactMatch([]string{env.To}, allowed) {
		return true, "to:" + d
	}
	for _, d := range exactMatch([]string{env.Cc}, allowed) {
		return true, "cc:" + d
	}
	for _, d := range exactMatch([]string{env.Bcc}, allowed) {
		return true, "bcc:" + d
	}
	return false, ""
}

func exactMatch(values []string, allowed map[string]struct{}) (matches []string) {
	for _, value := range values {
		for _, address := range splitAddresses(value) {
			d := strings.ToLower(strings.TrimSpace(addressDomain(address)))
			if d == "" {
				continue
			}
			if _, ok := allowed[d]; ok {
				matches = append(matches, d)
			}
		}
	}
	return
}

func splitAddresses(value string) []string {
	var out []string
	if value == "" {
		return out
	}
	addrs, err := mailpkg.ParseAddressList(value)
	if err == nil {
		for _, addr := range addrs {
			out = append(out, addr.Address)
		}
		return out
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ',', ';':
			return true
		default:
			return false
		}
	})
	out = make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func addressDomain(value string) string {
	at := strings.LastIndex(value, "@")
	if at < 0 || at == len(value)-1 {
		return ""
	}
	return value[at+1:]
}

func normalize(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[strings.ToLower(strings.TrimSpace(v))] = struct{}{}
	}
	return set
}
