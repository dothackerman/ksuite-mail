package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/layout"
	"github.com/dothackerman/ksuite-mail/internal/udsclient"
)

func runInbox(args []string) int {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	account := fs.String("account", "", "account identifier (default: all)")
	limit := fs.Int("limit", 0, "maximum number of messages")
	offset := fs.Int("offset", 0, "message offset")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.List(ctx, api.ListRequest{
			Account: *account,
			Folder:  "INBOX",
			Limit:   *limit,
			Offset:  *offset,
		})
	})
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	account := fs.String("account", "", "account identifier (default: all)")
	folder := fs.String("folder", "", "folder name (default: all configured folders)")
	limit := fs.Int("limit", 0, "maximum number of messages")
	offset := fs.Int("offset", 0, "message offset")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.List(ctx, api.ListRequest{
			Account: *account,
			Folder:  *folder,
			Limit:   *limit,
			Offset:  *offset,
		})
	})
}

func runSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	account := fs.String("account", "", "account identifier (default: all)")
	folder := fs.String("folder", "", "folder name (default: all configured folders)")
	query := fs.String("query", "", "full-text query string")
	limit := fs.Int("limit", 0, "maximum number of matches")
	offset := fs.Int("offset", 0, "result offset")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *query == "" {
		fmt.Println("error: --query is required")
		return 2
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.Search(ctx, api.SearchRequest{
			Account: *account,
			Folder:  *folder,
			Query:   *query,
			Limit:   *limit,
			Offset:  *offset,
		})
	})
}

func runShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	id := fs.String("id", "", "message id from list/search")
	preview := fs.Bool("preview", true, "include body preview")
	maxChars := fs.Int("max_chars", 0, "body preview size limit (preview-only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		fmt.Println("error: --id is required")
		return 2
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.Show(ctx, api.ShowRequest{
			ID:       *id,
			Preview:  *preview,
			MaxChars: *maxChars,
		})
	})
}

func runThread(args []string) int {
	fs := flag.NewFlagSet("thread", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	id := fs.String("id", "", "message id from list/search")
	_ = fs.Bool("brief", false, "reserved for compact thread output (currently ignored)")
	maxMessages := fs.Int("max_messages", 0, "maximum messages to return")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		fmt.Println("error: --id is required")
		return 2
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.Thread(ctx, api.ThreadRequest{
			ID:          *id,
			MaxMessages: *maxMessages,
		})
	})
}

func runContext(args []string) int {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	id := fs.String("id", "", "message id from list/search")
	budget := fs.Int("budget", 0, "timeline byte budget")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		fmt.Println("error: --id is required")
		return 2
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.Context(ctx, api.ContextRequest{
			ID:     *id,
			Budget: *budget,
		})
	})
}

func runReadCommand(socket string, fn func(context.Context, *udsclient.Client) (api.Envelope, error)) int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	env, err := fn(ctx, udsclient.New(socket))
	if err != nil {
		if errors.Is(err, udsclient.ErrUnreachable) {
			emitJSON(api.Err("daemon_unreachable", "could not reach ksuite-maild on its socket; is the service running?"))
			return 1
		}
		fmt.Fprintf(os.Stderr, "ksuite-mail: %v\n", err)
		return 1
	}
	emitJSON(env)
	return readStatusExitCode(env.Status)
}

func readStatusExitCode(status string) int {
	switch status {
	case api.StatusOK:
		return 0
	case api.StatusOKStale, api.StatusPartial, api.StatusError:
		return 1
	default:
		return 1
	}
}
