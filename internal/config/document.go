package config

import (
	"bytes"
	"fmt"

	toml "github.com/pelletier/go-toml/v2"
)

// Default global mail settings used when seeding a fresh config.
const (
	DefaultLimit    = 25
	DefaultCacheTTL = "90d"
)

// Starter returns the default configuration used to seed a fresh
// /etc/ksuite-mail/config.toml. It contains no accounts: init prepares the
// boundary, and accounts are added explicitly so the daemon never starts with
// an unintended mailbox configured.
func Starter() *Config {
	return &Config{
		Mail: Mail{
			DefaultLimit: DefaultLimit,
			CacheTTL:     DefaultCacheTTL,
		},
	}
}

// Marshal encodes a config to TOML.
func Marshal(c *Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.SetIndentTables(true)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return buf.Bytes(), nil
}

// starterHeader documents the format and shows a commented example account so
// an operator can complete the file by hand. The comments are inert to the
// parser and the rendered document validates as-is (zero accounts).
const starterHeader = `# ksuite-mail configuration (NFR-CFG-001).
#
# Secrets never appear here. Each account references its password indirectly
# through password_ref; the daemon resolves it from the protected secrets file.
#
# Example account (uncomment and adjust, then add the secret with 'init'):
#
# [[mail.accounts]]
# id = "rs_info"
# email = "info@example.com"
# host = "mail.infomaniak.com"
# port = 993
# tls = true
# username = "info@example.com"
# password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
# policy = "full"
# folders = ["INBOX", "Sent"]

`

// StarterDocument renders the full seed document: a commented header followed
// by the encoded defaults. The result parses strictly and validates.
func StarterDocument() ([]byte, error) {
	body, err := Marshal(Starter())
	if err != nil {
		return nil, err
	}
	return append([]byte(starterHeader), body...), nil
}
