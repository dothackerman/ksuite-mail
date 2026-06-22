package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dothackerman/ksuite-mail/internal/bootstrap"
)

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var (
		root        = fs.String("root", "", "filesystem prefix for all paths (staging/testing; default is the real root)")
		accessGroup = fs.String("access-group", "", "socket access group (default: invoking user's primary group)")
		install     = fs.Bool("install-units", false, "install systemd units to /etc/systemd/system instead of printing them")
		accountID   = fs.String("account-id", "", "optional: add and credential one account during init")
		email       = fs.String("email", "", "account email address")
		host        = fs.String("host", "mail.infomaniak.com", "IMAP host")
		port        = fs.Int("port", 993, "IMAP port")
		tls         = fs.Bool("tls", true, "use implicit TLS")
		username    = fs.String("username", "", "IMAP username (defaults to email)")
		policy      = fs.String("policy", "full", "account policy: full or domain")
		domains     = fs.String("domains", "", "comma-separated domains (required for domain policy)")
		folders     = fs.String("folders", "INBOX,Sent", "comma-separated folders")
	)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	opts := bootstrap.Options{
		Root:         *root,
		AccessGroup:  *accessGroup,
		InvokingUser: invokingUser(),
		Out:          os.Stdout,
	}
	if *install {
		opts.Units = bootstrap.UnitsInstall
	}

	if *accountID != "" {
		user := *username
		if user == "" {
			user = *email
		}
		opts.Account = &bootstrap.AccountSeed{
			ID:       *accountID,
			Email:    *email,
			Host:     *host,
			Port:     *port,
			TLS:      *tls,
			Username: user,
			Policy:   *policy,
			Domains:  splitList(*domains),
			Folders:  splitList(*folders),
		}
	}

	deps := bootstrap.RealDeps()
	if *root != "" {
		// A staging prefix is unprivileged: prepare the filesystem boundary
		// without OS user creation or ownership changes.
		deps = bootstrap.StagingDeps()
	} else if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "warning: not running as root; privileged steps (useradd, chown, /etc writes) will fail. Use sudo.")
	}

	_, err := bootstrap.Run(opts, deps)
	return err
}
