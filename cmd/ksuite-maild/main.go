// Command ksuite-maild is the privileged daemon that owns mailbox credentials
// and the local cache. In this slice it serves the health and doctor endpoints
// over a Unix domain socket; mail access arrives in later slices.
//
// In production systemd activates the socket and passes the listening
// descriptor; for development and tests the daemon binds the --socket path
// itself. Either way the CLI never receives credentials (ARCH-CON-002).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/daemon"
	"github.com/dothackerman/ksuite-mail/internal/imapadapter"
	"github.com/dothackerman/ksuite-mail/internal/layout"
	"github.com/dothackerman/ksuite-mail/internal/mail"
)

// version is injected at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	fs := flag.NewFlagSet("ksuite-maild", flag.ExitOnError)
	configPath := fs.String("config", layout.ConfigFile, "path to config.toml")
	secretsPath := fs.String("secrets", layout.SecretsFile, "path to the daemon-readable secrets file")
	stateDir := fs.String("state-dir", layout.StateDir, "path to the daemon state/cache directory")
	socketPath := fs.String("socket", layout.SocketPath, "Unix socket path (ignored under systemd socket activation)")
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ln, fromSystemd, err := daemon.Listen(*socketPath)
	if err != nil {
		log.Error("could not open daemon socket", "err", err)
		os.Exit(1)
	}
	log.Info("ksuite-maild listening", "from_systemd", fromSystemd, "socket", *socketPath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := daemon.New(daemon.Options{
		ConfigPath:  *configPath,
		SecretsPath: *secretsPath,
		StateDir:    *stateDir,
		Logger:      log,
		ProbeSourceFactory: func(context.Context, *config.Config) (mail.Source, error) {
			return imapadapter.New(*secretsPath), nil
		},
	})
	if err := srv.Serve(ctx, ln); err != nil {
		log.Error("daemon stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("ksuite-maild stopped")
}
