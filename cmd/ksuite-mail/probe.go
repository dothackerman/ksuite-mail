package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/layout"
	"github.com/dothackerman/ksuite-mail/internal/udsclient"
)

func runProbe(args []string) int {
	if len(args) == 0 {
		return probeValidationError("probe target is required")
	}
	switch args[0] {
	case "imap":
		return runProbeIMAP(args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, "Usage: ksuite-mail probe imap --account <account-ref> --json\n")
		return 0
	default:
		return probeValidationError("unsupported probe target")
	}
}

func runProbeIMAP(args []string) int {
	fs := flag.NewFlagSet("probe imap", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	account := fs.String("account", "", "configured account reference")
	_ = fs.Bool("json", false, "emit JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(fs.Args()) > 0 {
		return probeValidationError("raw IMAP command text is not accepted")
	}
	accountRef := strings.TrimSpace(*account)
	if accountRef == "" {
		return probeValidationError("account is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	env, err := udsclient.New(*socket).ProbeIMAP(ctx, api.ProbeIMAPRequest{Account: accountRef})
	if err != nil {
		if errors.Is(err, udsclient.ErrUnreachable) {
			emitJSON(api.Err("daemon_unreachable",
				"could not reach ksuite-maild on its socket; is the service running?"))
			return 1
		}
		fmt.Fprintf(os.Stderr, "ksuite-mail probe imap: %v\n", err)
		return 1
	}

	emitJSON(env)
	if env.Status == api.StatusOK {
		return 0
	}
	return 1
}

func probeValidationError(message string) int {
	emitJSON(api.Err("bad_request", message))
	return 2
}
