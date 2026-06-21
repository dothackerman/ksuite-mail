// Package config defines the non-secret TOML configuration for ksuite-mail and
// the validation rules from NFR-CFG-001..003.
//
// Secrets never live here: accounts reference credentials indirectly through a
// PasswordRef that the daemon resolves at runtime (NFR-SEC-001, NFR-SEC-002).
package config

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Supported account policies (NFR-CFG-003, ARCH-CON-001).
const (
	PolicyFull   = "full"
	PolicyDomain = "domain"
)

// Config is the root of config.toml.
type Config struct {
	Mail Mail `toml:"mail"`
}

// Mail holds global mail defaults and the configured accounts.
type Mail struct {
	DefaultLimit int       `toml:"default_limit"`
	CacheTTL     string    `toml:"cache_ttl"`
	Accounts     []Account `toml:"accounts"`
}

// Account is a single mailbox configuration. Credentials are never stored
// inline; PasswordRef points at a daemon-resolvable secret.
type Account struct {
	ID          string      `toml:"id"`
	Email       string      `toml:"email"`
	Host        string      `toml:"host"`
	Port        int         `toml:"port"`
	TLS         bool        `toml:"tls"`
	Username    string      `toml:"username"`
	PasswordRef PasswordRef `toml:"password_ref"`
	Policy      string      `toml:"policy"`
	Domains     []string    `toml:"domains"`
	Folders     []string    `toml:"folders"`
}

// PasswordRef is an indirect reference to a secret resolved daemon-side.
type PasswordRef struct {
	Source   string `toml:"source"`
	Provider string `toml:"provider"`
	ID       string `toml:"id"`
}

// Load decodes config.toml. It is strict: unknown keys are rejected so typos
// and stale keys surface as errors rather than being silently ignored
// (NFR-CFG-003).
func Load(r io.Reader) (*Config, error) {
	dec := toml.NewDecoder(r)
	dec.DisallowUnknownFields()

	var c Config
	if err := dec.Decode(&c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return nil, fmt.Errorf("config has unknown keys:\n%s", strict.String())
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &c, nil
}

// Validate enforces the rules in NFR-CFG-003. A starter config with zero
// accounts is valid; any account that is present is validated strictly.
func Validate(c *Config) error {
	var problems []error

	if c.Mail.CacheTTL != "" {
		if _, err := ParseTTL(c.Mail.CacheTTL); err != nil {
			problems = append(problems, fmt.Errorf("mail.cache_ttl: %w", err))
		}
	}
	if c.Mail.DefaultLimit < 0 {
		problems = append(problems, errors.New("mail.default_limit must not be negative"))
	}

	seen := make(map[string]bool, len(c.Mail.Accounts))
	for i := range c.Mail.Accounts {
		a := &c.Mail.Accounts[i]
		label := a.ID
		if label == "" {
			label = fmt.Sprintf("account[%d]", i)
		}

		if a.ID == "" {
			problems = append(problems, fmt.Errorf("%s: id is required", label))
		} else if seen[a.ID] {
			problems = append(problems, fmt.Errorf("duplicate account id %q", a.ID))
		} else {
			seen[a.ID] = true
		}

		if a.Email == "" {
			problems = append(problems, fmt.Errorf("%s: email is required", label))
		}
		if a.Host == "" {
			problems = append(problems, fmt.Errorf("%s: host is required", label))
		}
		if a.Port <= 0 || a.Port > 65535 {
			problems = append(problems, fmt.Errorf("%s: port must be in 1..65535", label))
		}
		if a.Username == "" {
			problems = append(problems, fmt.Errorf("%s: username is required", label))
		}
		if a.PasswordRef.ID == "" {
			problems = append(problems, fmt.Errorf("%s: password_ref.id is required", label))
		}

		switch a.Policy {
		case PolicyFull:
			// no domain list required
		case PolicyDomain:
			if len(a.Domains) == 0 {
				problems = append(problems, fmt.Errorf("%s: domain policy requires a non-empty domains list", label))
			}
		case "":
			problems = append(problems, fmt.Errorf("%s: policy is required", label))
		default:
			problems = append(problems, fmt.Errorf("%s: unsupported policy %q (want %q or %q)", label, a.Policy, PolicyFull, PolicyDomain))
		}

		if len(a.Folders) == 0 {
			problems = append(problems, fmt.Errorf("%s: at least one folder is required", label))
		}
	}

	return errors.Join(problems...)
}

// ParseTTL parses a cache TTL such as "90d", "12h", "30m". It accepts a single
// integer followed by a unit suffix: s, m, h, d (days), or w (weeks).
// time.ParseDuration is intentionally not used because it does not understand
// day or week units, which the documented config uses (NFR-CFG-001).
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty ttl")
	}

	unit := s[len(s)-1]
	num := s[:len(s)-1]

	var mult time.Duration
	switch unit {
	case 's':
		mult = time.Second
	case 'm':
		mult = time.Minute
	case 'h':
		mult = time.Hour
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown ttl unit %q (want s, m, h, d, or w)", string(unit))
	}

	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, fmt.Errorf("invalid ttl quantity %q", num)
	}
	if n < 0 {
		return 0, errors.New("ttl must not be negative")
	}
	return time.Duration(n) * mult, nil
}
