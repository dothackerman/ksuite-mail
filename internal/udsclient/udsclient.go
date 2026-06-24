// Package udsclient is the thin CLI-side client for the daemon's HTTP/JSON API
// over a Unix domain socket. It is the only way ksuite-mail talks to
// ksuite-maild; the CLI never resolves credentials or touches IMAP itself
// (NFR-SEC-002, ARCH-CON-002).
package udsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/dothackerman/ksuite-mail/internal/api"
)

// ErrUnreachable indicates the daemon socket could not be dialed — typically
// the service is not running or the socket path is wrong.
var ErrUnreachable = fmt.Errorf("daemon socket is unreachable")

// Client talks to one daemon socket.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client bound to the daemon socket at path.
func New(socket string) *Client {
	return &Client{
		socket: socket,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// Health calls GET /v1/health.
func (c *Client) Health(ctx context.Context) (api.Envelope, error) {
	return c.do(ctx, http.MethodGet, "/v1/health")
}

// Doctor calls POST /v1/doctor.
func (c *Client) Doctor(ctx context.Context) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/doctor")
}

func (c *Client) do(ctx context.Context, method, path string) (api.Envelope, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, nil)
	if err != nil {
		return api.Envelope{}, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Any dial/transport failure to a local socket means the daemon is not
		// answering; surface it as the typed unreachable error.
		return api.Envelope{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var env api.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return api.Envelope{}, fmt.Errorf("decode daemon response: %w", err)
	}
	return env, nil
}
