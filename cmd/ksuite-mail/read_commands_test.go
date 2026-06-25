package main

import (
	"testing"
)

func TestReadStatusExitCodeMapsStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		expected int
	}{
		{in: "ok", expected: 0},
		{in: "ok_stale", expected: 1},
		{in: "partial", expected: 1},
		{in: "error", expected: 1},
		{in: "other", expected: 1},
	}
	for _, c := range cases {
		if got := readStatusExitCode(c.in); got != c.expected {
			t.Fatalf("readStatusExitCode(%q) = %d, want %d", c.in, got, c.expected)
		}
	}
}

func TestRunSearchRequiresQueryFlag(t *testing.T) {
	t.Parallel()
	if got := runSearch([]string{}); got != 2 {
		t.Fatalf("runSearch() = %d, want 2", got)
	}
}
