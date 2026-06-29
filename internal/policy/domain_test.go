package policy

import (
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

func TestDomainPolicyMatchesOnlyExactHeaderDomains(t *testing.T) {
	acct := config.Account{
		ID:      "acct",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
	}

	t.Run("from exact domain", func(t *testing.T) {
		ok, reason := DomainMatch(acct, mail.MessageEnvelope{
			From: "Alice <alice@regenerativ.ch>",
			To:   "bob@example.com",
		})
		if !ok || reason != "from:regenerativ.ch" {
			t.Fatalf("got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("to exact domain", func(t *testing.T) {
		ok, reason := DomainMatch(acct, mail.MessageEnvelope{
			From: "Alice <alice@other.com>",
			To:   "Bob <bob@regenerativ.ch>",
		})
		if !ok || reason != "to:regenerativ.ch" {
			t.Fatalf("got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("cc exact domain", func(t *testing.T) {
		ok, reason := DomainMatch(acct, mail.MessageEnvelope{
			From: "Alice <alice@other.com>",
			Cc:   "Carol <carol@regenerativ.ch>",
		})
		if !ok || reason != "cc:regenerativ.ch" {
			t.Fatalf("got ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("bcc exact domain", func(t *testing.T) {
		ok, reason := DomainMatch(acct, mail.MessageEnvelope{
			From: "Alice <alice@other.com>",
			Bcc:  "Dave <dave@regenerativ.ch>",
		})
		if !ok || reason != "bcc:regenerativ.ch" {
			t.Fatalf("got ok=%v reason=%q", ok, reason)
		}
	})
}

func TestDomainPolicyRejectsSubdomainsAndBodyTextOnlyMatches(t *testing.T) {
	acct := config.Account{
		ID:      "acct",
		Policy:  config.PolicyDomain,
		Domains: []string{"regenerativ.ch"},
	}

	ok, reason := DomainMatch(acct, mail.MessageEnvelope{
		From: "Alice <alice@fake-regenerativ.ch>",
		To:   "Bob <bob@regenerativ.ch.example.com>",
		Cc:   "",
	})
	if ok {
		t.Fatalf("expected non-match")
	}
	if reason != "" {
		t.Fatalf("expected empty reason for subdomain-like text, got %q", reason)
	}
}

func TestDomainPolicyFullPolicyPassesAlways(t *testing.T) {
	acct := config.Account{
		ID:     "acct",
		Policy: config.PolicyFull,
	}
	ok, reason := DomainMatch(acct, mail.MessageEnvelope{
		From: "Alice <alice@other.com>",
		To:   "Bob <bob@other2.com>",
	})
	if !ok || reason != "policy_full" {
		t.Fatalf("got ok=%v reason=%q", ok, reason)
	}
}
