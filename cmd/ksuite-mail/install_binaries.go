package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dothackerman/ksuite-mail/internal/layout"
)

// runInstallBinaries implements `ksuite-mail install`. It copies itself and the
// sibling ksuite-maild binary to their canonical installed paths
// (layout.InstalledCLI and layout.InstalledDaem). The command is idempotent:
// re-running it with a newer release overwrites the previous binaries in place.
//
// The sibling binary is located relative to the current executable so that the
// command works correctly when invoked from an extracted release tarball before
// either binary is on PATH.
func runInstallBinaries(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sudo ./ksuite-mail install")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Copies ksuite-mail and ksuite-maild to their installed paths.")
		fmt.Fprintln(os.Stderr, "Must be run as root. Re-running is safe and upgrades existing binaries.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	// Resolve symlinks so we operate on the real file, not a link.
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	sibling := filepath.Join(filepath.Dir(self), "ksuite-maild")
	if _, err := os.Stat(sibling); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ksuite-maild not found at %s — extract both binaries from the release tarball into the same directory before running install", sibling)
	} else if err != nil {
		return fmt.Errorf("check ksuite-maild: %w", err)
	}

	if err := copyBinary(self, layout.InstalledCLI); err != nil {
		return fmt.Errorf("install ksuite-mail: %w", err)
	}
	fmt.Printf("installed %s\n", layout.InstalledCLI)

	if err := copyBinary(sibling, layout.InstalledDaem); err != nil {
		return fmt.Errorf("install ksuite-maild: %w", err)
	}
	fmt.Printf("installed %s\n", layout.InstalledDaem)

	fmt.Println("done — run: sudo ksuite-mail init --install-units --account-id=...")
	return nil
}

// copyBinary copies src to dst atomically by writing to a temp file in the
// same directory and renaming. This prevents a running daemon from reading a
// half-written binary during an upgrade.
func copyBinary(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is the executable path from os.Executable
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	// Write to a temp file in the destination directory so the rename is atomic
	// on the same filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".ksuite-install-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after successful rename

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
