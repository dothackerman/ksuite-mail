package main

import (
	"strings"
	"testing"
)

func TestProbeCommandRejectsRawIMAPText(t *testing.T) {
	code, out := captureStdout(t, func() int {
		return runProbe([]string{"imap", "--account", "rs_info", "--json", "CAPABILITY"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if strings.Contains(out, "CAPABILITY") {
		t.Fatalf("probe validation echoed raw IMAP text in %s", out)
	}
}
