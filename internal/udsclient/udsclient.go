// Package udsclient is the thin CLI-side client for the daemon's HTTP/JSON API
// over a Unix domain socket. It is the only way ksuite-mail talks to
// ksuite-maild; the CLI never resolves credentials or touches IMAP itself
// (NFR-SEC-002, ARCH-CON-002).
package udsclient

import (
	"bytes"
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
	return NewWithTimeout(socket, 10*time.Second)
}

// NewWithTimeout returns a client bound to the daemon socket at path with a
// request timeout chosen by the caller.
func NewWithTimeout(socket string, timeout time.Duration) *Client {
	return &Client{
		socket: socket,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// Timeout returns the HTTP request timeout configured for the client.
func (c *Client) Timeout() time.Duration {
	return c.http.Timeout
}

// Health calls GET /v1/health.
func (c *Client) Health(ctx context.Context) (api.Envelope, error) {
	return c.do(ctx, http.MethodGet, "/v1/health", nil)
}

// Doctor calls POST /v1/doctor.
func (c *Client) Doctor(ctx context.Context) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/doctor", nil)
}

// List calls POST /v1/list.
func (c *Client) List(ctx context.Context, req api.ListRequest) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/list", req)
}

// Search calls POST /v1/search.
func (c *Client) Search(ctx context.Context, req api.SearchRequest) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/search", req)
}

// Show calls POST /v1/show.
func (c *Client) Show(ctx context.Context, req api.ShowRequest) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/show", req)
}

// Thread calls POST /v1/thread.
func (c *Client) Thread(ctx context.Context, req api.ThreadRequest) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/thread", req)
}

// Context calls POST /v1/context.
func (c *Client) Context(ctx context.Context, req api.ContextRequest) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/context", req)
}

// ProbeIMAP calls POST /v1/probe/imap.
func (c *Client) ProbeIMAP(ctx context.Context, req api.ProbeIMAPRequest) (api.Envelope, error) {
	return c.do(ctx, http.MethodPost, "/v1/probe/imap", req)
}

func (c *Client) do(ctx context.Context, method, path string, payload any) (api.Envelope, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return api.Envelope{}, fmt.Errorf("marshal request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		return api.Envelope{}, fmt.Errorf("build request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
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
