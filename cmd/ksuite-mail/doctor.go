package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/layout"
	"github.com/dothackerman/ksuite-mail/internal/udsclient"
)

// runDoctor asks the daemon to diagnose the local setup and prints the JSON
// envelope to stdout. It returns the process exit code: 0 when the daemon
// reports a healthy setup, 1 when it reports problems or is unreachable, and 2
// for usage errors. Output is always machine-readable JSON for agent use
// (FR-008); diagnostics never include credentials (NFR-SEC-005).
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	fs.Bool("json", true, "emit compact JSON (currently the only output format)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	env, err := udsclient.New(*socket).Doctor(ctx)
	if err != nil {
		if errors.Is(err, udsclient.ErrUnreachable) {
			emitJSON(api.Err("daemon_unreachable",
				"could not reach ksuite-maild on its socket; is the service running?"))
			return 1
		}
		fmt.Fprintf(os.Stderr, "ksuite-mail doctor: %v\n", err)
		return 1
	}

	emitJSON(env)

	var report api.DoctorReport
	if env.Status == api.StatusOK && env.DecodeResult(&report) == nil && report.OK {
		return 0
	}
	return 1
}

// emitJSON writes a compact JSON envelope line to stdout.
func emitJSON(env api.Envelope) {
	b, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ksuite-mail: encode output: %v\n", err)
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, string(b))
}
