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

func runInbox(args []string) int {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	socket := fs.String("socket", layout.SocketPath, "path to the daemon Unix socket")
	account := fs.String("account", "", "account identifier (default: all)")
	limit := fs.Int("limit", 0, "maximum number of messages")
	offset := fs.Int("offset", 0, "message offset")
	_ = fs.Bool("brief", false, "reserved for compact output (currently JSON only)")
	_ = fs.Bool("json", false, "emit JSON output")
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
	_ = fs.Bool("json", false, "emit JSON output")
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
	_ = fs.Bool("json", false, "emit JSON output")
	positional, args := extractFirstPositional(args, "socket", "account", "folder", "query", "limit", "offset")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *query == "" {
		*query = positional
	}
	if *query == "" && len(fs.Args()) > 0 {
		*query = fs.Args()[0]
	}
	if *query == "" {
		return readValidationError("query is required")
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
	preview := fs.Bool("preview", false, "include body preview")
	maxChars := 0
	fs.IntVar(&maxChars, "max-chars", 0, "body preview size limit (preview-only)")
	fs.IntVar(&maxChars, "max_chars", 0, "body preview size limit (preview-only)")
	_ = fs.Bool("json", false, "emit JSON output")
	positional, args := extractFirstPositional(args, "socket", "id", "max-chars", "max_chars")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		*id = positional
	}
	if *id == "" && len(fs.Args()) > 0 {
		*id = fs.Args()[0]
	}
	if *id == "" {
		return readValidationError("id is required")
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.Show(ctx, api.ShowRequest{
			ID:       *id,
			Preview:  *preview || maxChars > 0,
			MaxChars: maxChars,
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
	_ = fs.Bool("json", false, "emit JSON output")
	positional, args := extractFirstPositional(args, "socket", "id", "max_messages")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		*id = positional
	}
	if *id == "" && len(fs.Args()) > 0 {
		*id = fs.Args()[0]
	}
	if *id == "" {
		return readValidationError("id is required")
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
	_ = fs.Bool("json", false, "emit JSON output")
	positional, args := extractFirstPositional(args, "socket", "id", "budget")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		*id = positional
	}
	if *id == "" && len(fs.Args()) > 0 {
		*id = fs.Args()[0]
	}
	if *id == "" {
		return readValidationError("id is required")
	}

	return runReadCommand(*socket, func(ctx context.Context, c *udsclient.Client) (api.Envelope, error) {
		return c.Context(ctx, api.ContextRequest{
			ID:     *id,
			Budget: *budget,
		})
	})
}

func extractFirstPositional(args []string, valueFlagNames ...string) (string, []string) {
	valueFlags := make(map[string]struct{}, len(valueFlagNames))
	for _, name := range valueFlagNames {
		valueFlags[name] = struct{}{}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 >= len(args) {
				return "", args
			}
			out := append([]string{}, args[:i]...)
			out = append(out, args[i+2:]...)
			return args[i+1], out
		}
		if strings.HasPrefix(arg, "-") {
			name, hasInlineValue := flagName(arg)
			if _, ok := valueFlags[name]; ok && !hasInlineValue && i+1 < len(args) {
				i++
			}
			continue
		}
		out := append([]string{}, args[:i]...)
		out = append(out, args[i+1:]...)
		return arg, out
	}
	return "", args
}

func flagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		return before, true
	}
	return name, false
}

func readValidationError(message string) int {
	emitJSON(api.Err("bad_request", message))
	return 2
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
