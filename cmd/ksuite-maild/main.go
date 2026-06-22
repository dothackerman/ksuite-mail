// Command ksuite-maild is the privileged daemon that owns mailbox credentials,
// IMAP access, and the local cache. Its full skeleton (Unix-socket API, doctor,
// credential resolver) is built in implementation slice 2.
//
// This slice establishes the executable and the deployment boundary only: the
// binary exists at the documented path so the systemd units rendered by
// `ksuite-mail init` resolve, but it does not yet serve requests.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dothackerman/ksuite-mail/internal/layout"
)

func main() {
	fs := flag.NewFlagSet("ksuite-maild", flag.ExitOnError)
	config := fs.String("config", layout.ConfigFile, "path to config.toml")
	_ = fs.Parse(os.Args[1:])

	fmt.Fprintf(os.Stderr, "ksuite-maild: daemon skeleton arrives in implementation slice 2 (config %s)\n", *config)
}
