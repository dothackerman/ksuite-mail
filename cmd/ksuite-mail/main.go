// Command ksuite-mail is the thin CLI front end. In this slice it implements
// the privileged `init` setup command only (UC-008). The CLI never resolves or
// transmits mailbox credentials for normal operation; `init` is the documented
// exception that prompts for a credential on the TTY and writes it directly to
// the daemon-owned secrets file (ARCH-CON-002, NFR-OPS-000).
package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

const usage = `ksuite-mail - local Infomaniak K-Mail gateway CLI

Usage:
  ksuite-mail <command> [flags]

Commands:
  init     Prepare the local service boundary (run as root: sudo ksuite-mail init)
  doctor   Diagnose the local setup via the daemon (JSON output)

Run 'ksuite-mail <command> --help' for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ksuite-mail init: %v\n", err)
			os.Exit(1)
		}
	case "doctor":
		os.Exit(runDoctor(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func invokingUser() string {
	if u := strings.TrimSpace(os.Getenv("SUDO_USER")); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func splitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
