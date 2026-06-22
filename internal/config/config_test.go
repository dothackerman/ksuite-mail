package config

import (
	"strings"
	"testing"
	"time"
)

const validDomainAccount = `
[mail]
default_limit = 25
cache_ttl = "90d"

[[mail.accounts]]
id = "rs_info"
email = "info@regenerativ.ch"
host = "mail.infomaniak.com"
port = 993
tls = true
username = "info@regenerativ.ch"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
policy = "domain"
domains = ["regenerativ.ch"]
folders = ["INBOX", "Sent"]
`

func TestLoadValidAccount(t *testing.T) {
	c, err := Load(strings.NewReader(validDomainAccount))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := len(c.Mail.Accounts); got != 1 {
		t.Fatalf("accounts = %d, want 1", got)
	}
	if c.Mail.Accounts[0].PasswordRef.ID != "/ksuite-mail/rs_info/password" {
		t.Fatalf("password_ref.id = %q", c.Mail.Accounts[0].PasswordRef.ID)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	const doc = `
[mail]
default_limit = 25
surprise = true
`
	if _, err := Load(strings.NewReader(doc)); err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestStarterDocumentParsesAndValidates(t *testing.T) {
	doc, err := StarterDocument()
	if err != nil {
		t.Fatalf("StarterDocument: %v", err)
	}
	c, err := Load(strings.NewReader(string(doc)))
	if err != nil {
		t.Fatalf("Load starter: %v\n%s", err, doc)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate starter: %v", err)
	}
	if len(c.Mail.Accounts) != 0 {
		t.Fatalf("starter should have no accounts, got %d", len(c.Mail.Accounts))
	}
	if c.Mail.CacheTTL != DefaultCacheTTL {
		t.Fatalf("cache_ttl = %q, want %q", c.Mail.CacheTTL, DefaultCacheTTL)
	}
}

func TestValidateAccountProblems(t *testing.T) {
	cases := map[string]Account{
		"missing id": {
			Email: "a@b.c", Host: "h", Port: 993, Username: "u",
			PasswordRef: PasswordRef{ID: "x"}, Policy: PolicyFull, Folders: []string{"INBOX"},
		},
		"bad port": {
			ID: "a", Email: "a@b.c", Host: "h", Port: 0, Username: "u",
			PasswordRef: PasswordRef{ID: "x"}, Policy: PolicyFull, Folders: []string{"INBOX"},
		},
		"unsupported policy": {
			ID: "a", Email: "a@b.c", Host: "h", Port: 993, Username: "u",
			PasswordRef: PasswordRef{ID: "x"}, Policy: "weird", Folders: []string{"INBOX"},
		},
		"domain policy without domains": {
			ID: "a", Email: "a@b.c", Host: "h", Port: 993, Username: "u",
			PasswordRef: PasswordRef{ID: "x"}, Policy: PolicyDomain, Folders: []string{"INBOX"},
		},
		"no folders": {
			ID: "a", Email: "a@b.c", Host: "h", Port: 993, Username: "u",
			PasswordRef: PasswordRef{ID: "x"}, Policy: PolicyFull,
		},
		"no password_ref": {
			ID: "a", Email: "a@b.c", Host: "h", Port: 993, Username: "u",
			Policy: PolicyFull, Folders: []string{"INBOX"},
		},
	}
	for name, acct := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Mail: Mail{Accounts: []Account{acct}}}
			if err := Validate(c); err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
}

func TestValidateDuplicateIDs(t *testing.T) {
	c := &Config{Mail: Mail{Accounts: []Account{
		{ID: "dup", Email: "a@b.c", Host: "h", Port: 993, Username: "u", PasswordRef: PasswordRef{ID: "x"}, Policy: PolicyFull, Folders: []string{"INBOX"}},
		{ID: "dup", Email: "a@b.c", Host: "h", Port: 993, Username: "u", PasswordRef: PasswordRef{ID: "y"}, Policy: PolicyFull, Folders: []string{"INBOX"}},
	}}}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "duplicate account id") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}
}

// validAccount returns a fully valid account so a test can invalidate exactly
// one field and assert that field is what validation rejects.
func validAccount() Account {
	tls := true
	return Account{
		ID: "a", Email: "a@b.c", Host: "h", Port: 993, TLS: &tls, Username: "u",
		PasswordRef: PasswordRef{Source: "file", Provider: "local", ID: "/ksuite-mail/a/password"},
		Policy:      PolicyFull, Folders: []string{"INBOX"},
	}
}

// TLS mode is a required account field (FR-002). An omitted `tls` key decodes to
// a nil pointer and must be rejected, so a typo cannot silently disable TLS.
func TestValidateRequiresTLSPresence(t *testing.T) {
	a := validAccount()
	a.TLS = nil
	c := &Config{Mail: Mail{Accounts: []Account{a}}}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatalf("expected tls-required error, got %v", err)
	}
}

// Only the file/local backend that init writes is supported; an unsupported or
// typo'd password_ref backend must fail validation, not defer to the daemon.
func TestValidateRejectsUnknownPasswordRefBackend(t *testing.T) {
	t.Run("bad source", func(t *testing.T) {
		a := validAccount()
		a.PasswordRef.Source = "env"
		c := &Config{Mail: Mail{Accounts: []Account{a}}}
		if err := Validate(c); err == nil || !strings.Contains(err.Error(), "source") {
			t.Fatalf("expected source error, got %v", err)
		}
	})
	t.Run("bad provider", func(t *testing.T) {
		a := validAccount()
		a.PasswordRef.Provider = "loacl"
		c := &Config{Mail: Mail{Accounts: []Account{a}}}
		if err := Validate(c); err == nil || !strings.Contains(err.Error(), "provider") {
			t.Fatalf("expected provider error, got %v", err)
		}
	})
}

func TestParseTTL(t *testing.T) {
	good := map[string]time.Duration{
		"30s": 30 * time.Second,
		"15m": 15 * time.Minute,
		"12h": 12 * time.Hour,
		"90d": 90 * 24 * time.Hour,
		"2w":  2 * 7 * 24 * time.Hour,
	}
	for in, want := range good {
		got, err := ParseTTL(in)
		if err != nil {
			t.Errorf("ParseTTL(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTTL(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "90", "10x", "-5d", "abcd"} {
		if _, err := ParseTTL(bad); err == nil {
			t.Errorf("ParseTTL(%q) expected error", bad)
		}
	}
}
