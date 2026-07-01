// Package imapadapter keeps go-imap/v2 behind the daemon-side mail.Source
// boundary. It never enables go-imap debug logging because that stream can
// contain credentials and raw mailbox data.
package imapadapter

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/mail"
	"github.com/dothackerman/ksuite-mail/internal/secrets"
)

const defaultTimeout = 20 * time.Second

// Source resolves credentials daemon-side and opens short-lived IMAP sessions
// for each read-only operation.
type Source struct {
	secretsPath string
	timeout     time.Duration
	tlsConfig   *tls.Config
}

// New returns a live IMAP source backed by the daemon-owned secrets file.
func New(secretsPath string) *Source {
	return &Source{secretsPath: secretsPath, timeout: defaultTimeout}
}

// Capabilities returns sanitized capability atoms after authenticating.
func (s *Source) Capabilities(ctx context.Context, acct config.Account) ([]string, error) {
	c, err := s.connect(ctx, acct)
	if err != nil {
		return nil, err
	}
	defer c.close()

	caps, err := c.client.Capability().Wait()
	if err != nil {
		return nil, normalizeCommandError(err)
	}
	out := make([]string, 0, len(caps))
	for cap := range caps {
		clean := sanitizeCapability(string(cap))
		if clean != "" {
			out = append(out, clean)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Folders returns sanitized provider folder names.
func (s *Source) Folders(ctx context.Context, acct config.Account) ([]string, error) {
	c, err := s.connect(ctx, acct)
	if err != nil {
		return nil, err
	}
	defer c.close()

	listed, err := c.client.List("", "*", nil).Collect()
	if err != nil {
		return nil, normalizeCommandError(err)
	}
	out := make([]string, 0, len(listed))
	for _, folder := range listed {
		if clean := sanitizeFolder(folder.Mailbox); clean != "" {
			out = append(out, clean)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SelectFolder uses EXAMINE to retrieve read-only folder state.
func (s *Source) SelectFolder(ctx context.Context, acct config.Account, folder string) (mail.RemoteFolderState, error) {
	c, err := s.connect(ctx, acct)
	if err != nil {
		return mail.RemoteFolderState{}, err
	}
	defer c.close()

	caps, _ := c.client.Capability().Wait()
	data, err := c.client.Select(folder, &imap.SelectOptions{
		ReadOnly:  true,
		CondStore: caps.Has(imap.CapCondStore),
	}).Wait()
	if err != nil {
		return mail.RemoteFolderState{}, normalizeCommandError(err)
	}
	return mail.RemoteFolderState{
		UIDVALIDITY:   uint64(data.UIDValidity),
		UIDNEXT:       uint64(data.UIDNext),
		HighestModSeq: int64(data.HighestModSeq),
		ReadOnly:      true,
		SelectionMode: "examine",
	}, nil
}

// SearchAllowed issues UID SEARCH HEADER with optional UID range criteria.
func (s *Source) SearchAllowed(ctx context.Context, acct config.Account, folder string, header string, value string, scope mail.UIDRange) ([]mail.UID, error) {
	c, err := s.connectRaw(ctx, acct)
	if err != nil {
		return nil, err
	}
	defer c.close()

	if err := c.commandOK("EXAMINE", quoteIMAPString(folder)); err != nil {
		return nil, normalizeCommandError(err)
	}
	uids, err := c.uidSearchHeader(header, value, scope)
	if err != nil {
		return nil, normalizeCommandError(err)
	}
	return uids, nil
}

// ListUIDs returns UIDs in a folder, optionally bounded by a UID range.
func (s *Source) ListUIDs(ctx context.Context, acct config.Account, folder string, scope mail.UIDRange) ([]mail.UID, error) {
	c, err := s.connect(ctx, acct)
	if err != nil {
		return nil, err
	}
	defer c.close()
	if _, err := c.client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, normalizeCommandError(err)
	}

	criteria := &imap.SearchCriteria{}
	if uidSet, ok := uidRangeSet(scope); ok {
		criteria.UID = []imap.UIDSet{uidSet}
	}
	data, err := c.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, normalizeCommandError(err)
	}
	return toMailUIDs(data.AllUIDs()), nil
}

func (s *Source) FetchHeaders(context.Context, config.Account, string, []mail.UID) ([]mail.MessageHeaders, error) {
	return nil, mail.ErrSourceUnavailable
}

func (s *Source) FetchEnvelopes(context.Context, config.Account, string, []mail.UID) ([]mail.MessageEnvelope, error) {
	return nil, mail.ErrSourceUnavailable
}

func (s *Source) FetchBodyPreview(ctx context.Context, acct config.Account, folder string, uid mail.UID, maxBytes int) (string, error) {
	preview, _, err := s.FetchBodyPreviewAndSeenState(ctx, acct, folder, uid, maxBytes)
	return preview, err
}

// FetchBodyPreviewAndSeenState returns a bounded body preview with observed
// before/after \\Seen state.
func (s *Source) FetchBodyPreviewAndSeenState(ctx context.Context, acct config.Account, folder string, uid mail.UID, maxBytes int) (string, mail.ReadStateProbeResult, error) {
	c, err := s.connect(ctx, acct)
	if err != nil {
		return "", mail.ReadStateProbeResult{}, err
	}
	defer c.close()
	if _, err := c.client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return "", mail.ReadStateProbeResult{}, normalizeCommandError(err)
	}

	uidSet := imap.UIDSetNum(imap.UID(uid))
	beforeBuf, err := c.client.Fetch(uidSet, &imap.FetchOptions{
		Flags: true,
	}).Collect()
	if err != nil {
		return "", mail.ReadStateProbeResult{}, normalizeCommandError(err)
	}
	if len(beforeBuf) == 0 {
		return "", mail.ReadStateProbeResult{}, nil
	}
	seenBefore := hasSeenFlag(beforeBuf[0].Flags)

	bodySection := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierText, Peek: true}
	if maxBytes > 0 {
		bodySection.Partial = &imap.SectionPartial{Offset: 0, Size: int64(maxBytes)}
	}
	afterBuf, err := c.client.Fetch(uidSet, &imap.FetchOptions{
		Flags:       true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}).Collect()
	if err != nil {
		return "", mail.ReadStateProbeResult{}, normalizeCommandError(err)
	}
	if len(afterBuf) == 0 {
		return "", mail.ReadStateProbeResult{}, nil
	}
	seenAfter := hasSeenFlag(afterBuf[0].Flags)
	preview := string(afterBuf[0].FindBodySection(bodySection))
	return preview, mail.ReadStateProbeResult{
		Observed:   true,
		SeenBefore: seenBefore,
		SeenAfter:  seenAfter,
	}, nil

}

func hasSeenFlag(flags []imap.Flag) bool {
	for _, flag := range flags {
		if flag == imap.FlagSeen {
			return true
		}
	}
	return false
}

type liveConn struct {
	client *imapclient.Client
}

func (c liveConn) close() {
	_ = c.client.Close()
}

func (s *Source) connect(ctx context.Context, acct config.Account) (liveConn, error) {
	if acct.TLS == nil || !*acct.TLS {
		return liveConn{}, mail.ErrSourceUnavailable
	}
	password, err := s.password(acct)
	if err != nil {
		return liveConn{}, err
	}
	tlsConfig := s.connectionTLSConfig(acct)
	opts := &imapclient.Options{
		TLSConfig: tlsConfig,
	}
	conn, err := s.dial(ctx, acct)
	if err != nil {
		return liveConn{}, normalizeCommandError(err)
	}
	client := imapclient.New(conn, opts)
	if err := client.Login(acct.Username, password).Wait(); err != nil {
		_ = client.Close()
		return liveConn{}, normalizeCommandError(err)
	}
	return liveConn{client: client}, nil
}

func (s *Source) connectRaw(ctx context.Context, acct config.Account) (*rawIMAPConn, error) {
	if acct.TLS == nil || !*acct.TLS {
		return nil, mail.ErrSourceUnavailable
	}
	password, err := s.password(acct)
	if err != nil {
		return nil, err
	}
	conn, err := s.dial(ctx, acct)
	if err != nil {
		return nil, normalizeCommandError(err)
	}
	raw := &rawIMAPConn{
		conn: conn,
		r:    bufio.NewReader(conn),
		w:    bufio.NewWriter(conn),
	}
	if err := raw.readGreeting(); err != nil {
		_ = conn.Close()
		return nil, normalizeCommandError(err)
	}
	if err := raw.commandOK("LOGIN", quoteIMAPString(acct.Username), quoteIMAPString(password)); err != nil {
		_ = conn.Close()
		return nil, normalizeCommandError(err)
	}
	return raw, nil
}

func (s *Source) dial(ctx context.Context, acct config.Account) (net.Conn, error) {
	address := net.JoinHostPort(acct.Host, strconv.Itoa(acct.Port))
	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(ctxDeadline); remaining > 0 && remaining < timeout {
			dialer.Timeout = remaining
		}
		if ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	tlsConfig := s.connectionTLSConfig(acct)
	conn, err := tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	if err != nil {
		return nil, err
	}
	boundedConn := deadlineConn{Conn: conn, deadline: deadline}
	if err := boundedConn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return boundedConn, nil
}

type rawIMAPConn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	tag  int
}

func (c *rawIMAPConn) close() {
	_ = c.conn.Close()
}

func (c *rawIMAPConn) readGreeting() error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToUpper(line), "* OK") && !strings.HasPrefix(strings.ToUpper(line), "* PREAUTH") {
		return mail.ErrSourceUnavailable
	}
	return nil
}

func (c *rawIMAPConn) commandOK(name string, args ...string) error {
	_, err := c.command(name, args...)
	return err
}

func (c *rawIMAPConn) uidSearchHeader(header string, value string, scope mail.UIDRange) ([]mail.UID, error) {
	args := []string{"SEARCH"}
	if uidSet, ok := uidRangeSet(scope); ok {
		args = append(args, "UID", uidSet.String())
	}
	args = append(args, "HEADER", quoteIMAPString(header), quoteIMAPString(value))
	lines, err := c.command("UID", args...)
	if err != nil {
		return nil, err
	}
	return parseSearchUIDs(lines), nil
}

func (c *rawIMAPConn) command(name string, args ...string) ([]string, error) {
	c.tag++
	tag := fmt.Sprintf("A%04d", c.tag)
	line := tag + " " + name
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	if _, err := c.w.WriteString(line + "\r\n"); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}

	var untagged []string
	for {
		resp, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(resp, "* ") {
			untagged = append(untagged, resp)
			continue
		}
		if strings.HasPrefix(resp, tag+" ") {
			status := strings.ToUpper(firstField(strings.TrimSpace(strings.TrimPrefix(resp, tag))))
			if status == "OK" {
				return untagged, nil
			}
			return nil, mail.ErrSourceUnavailable
		}
	}
}

func (c *rawIMAPConn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

type deadlineConn struct {
	net.Conn
	deadline time.Time
}

func (c deadlineConn) SetDeadline(t time.Time) error {
	return c.Conn.SetDeadline(capDeadline(t, c.deadline))
}

func (c deadlineConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(capDeadline(t, c.deadline))
}

func (c deadlineConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(capDeadline(t, c.deadline))
}

func capDeadline(next, max time.Time) time.Time {
	if max.IsZero() {
		return next
	}
	if next.IsZero() || max.Before(next) {
		return max
	}
	return next
}

func (s *Source) connectionTLSConfig(acct config.Account) *tls.Config {
	if s.tlsConfig != nil {
		cfg := s.tlsConfig.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = acct.Host
		}
		if cfg.MinVersion == 0 {
			cfg.MinVersion = tls.VersionTLS12
		}
		return cfg
	}
	return &tls.Config{ServerName: acct.Host, MinVersion: tls.VersionTLS12}
}

func (s *Source) password(acct config.Account) (string, error) {
	store, err := secrets.Load(s.secretsPath)
	if err != nil {
		return "", err
	}
	password, ok := store.Resolve(acct.PasswordRef.ID)
	if !ok {
		return "", mail.ErrSourceUnavailable
	}
	return password, nil
}

func sanitizeCapability(cap string) string {
	clean := strings.ToUpper(strings.TrimSpace(cap))
	for _, r := range clean {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '=' {
			continue
		}
		return ""
	}
	return clean
}

func sanitizeFolder(folder string) string {
	clean := strings.TrimSpace(folder)
	if clean == "" || len(clean) > 256 {
		return ""
	}
	for _, r := range clean {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return clean
}

func quoteIMAPString(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\r', '\n':
			continue
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func firstField(value string) string {
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func parseSearchUIDs(lines []string) []mail.UID {
	seen := map[mail.UID]bool{}
	var out []mail.UID
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "*" || strings.ToUpper(fields[1]) != "SEARCH" {
			continue
		}
		for _, field := range fields[2:] {
			uid, err := strconv.ParseUint(field, 10, 64)
			if err != nil || uid == 0 {
				continue
			}
			mailUID := mail.UID(uid)
			if !seen[mailUID] {
				out = append(out, mailUID)
				seen[mailUID] = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uidRangeSet(scope mail.UIDRange) (imap.UIDSet, bool) {
	if scope == (mail.UIDRange{}) {
		return nil, false
	}
	var set imap.UIDSet
	start := imap.UID(scope.Min)
	stop := imap.UID(scope.Max)
	if start == 0 && stop == 0 {
		return nil, false
	}
	if start == 0 {
		start = 1
	}
	set.AddRange(start, stop)
	return set, true
}

func toMailUIDs(uids []imap.UID) []mail.UID {
	out := make([]mail.UID, 0, len(uids))
	for _, uid := range uids {
		out = append(out, mail.UID(uid))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func normalizeCommandError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return context.DeadlineExceeded
	}
	if strings.Contains(strings.ToLower(err.Error()), "i/o timeout") {
		return context.DeadlineExceeded
	}
	return err
}

var _ mail.Source = (*Source)(nil)
