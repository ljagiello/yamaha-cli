package ynca

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default port and timing for YNCA TCP control.
const (
	defaultPort    = 50000
	defaultTimeout = 3 * time.Second
)

// Client is a YNCA TCP client.
//
// One persistent connection is held per Client; it is reopened lazily
// after any I/O failure. All exported methods are safe for concurrent
// use: a mutex serialises requests so that the request/response stream
// never interleaves.
type Client struct {
	addr    string // host:port
	timeout time.Duration

	wakeOnConnect bool

	mu      sync.Mutex
	conn    net.Conn
	scanner *bufio.Scanner
}

// wakeTimeout bounds the connect-time wake exchange (see WithWakeOnConnect).
// Short on purpose: a healthy receiver replies in milliseconds, and a
// sleeping one that drops the ping just needs us to time out and proceed.
// A var so tests can shrink it.
var wakeTimeout = 600 * time.Millisecond

// Option configures a Client.
type Option func(*Client)

// WithTimeout sets the per-request timeout used for both connect and
// read. Default: 3s.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithWakeOnConnect makes the client send a cheap `@SYS:MODELNAME=?` and
// fully drain its reply immediately after connecting.
//
// A receiver in YNCA standby silently drops the FIRST command on a fresh
// connection while it wakes (YNCA standby is ~40s). Without this, that
// dropped command is often the Probe — so a sleeping-but-supported
// receiver gets misreported as "doesn't support YNCA". The wake exchange
// absorbs that first-command loss so the caller's real command (and the
// probe) land on an awake device. The reply is consumed via a raw read so
// it can never corrupt the next command; a sleeping device that drops the
// ping just makes the short wake read time out. Ported from the ynca
// reference library's connect-time keep-alive.
func WithWakeOnConnect() Option {
	return func(c *Client) { c.wakeOnConnect = true }
}

// WithPort overrides the TCP port (default 50000).
func WithPort(p int) Option {
	return func(c *Client) {
		if p > 0 && p < 65536 {
			c.addr = replacePort(c.addr, p)
		}
	}
}

// New constructs a Client. The host argument is either "host" or
// "host:port"; if no port is given the default 50000 is used. The
// connection is not opened until the first Send or Probe.
func New(host string, opts ...Option) (*Client, error) {
	if host == "" {
		return nil, errors.New("ynca: empty host")
	}
	addr := host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, strconv.Itoa(defaultPort))
	}
	c := &Client{
		addr:    addr,
		timeout: defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Close closes the underlying connection. Subsequent calls to Send or
// Probe will reopen it. Close is idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.scanner = nil
	return err
}

// dialLocked opens a new TCP connection. Caller must hold c.mu.
func (c *Client) dialLocked(ctx context.Context) error {
	d := net.Dialer{Timeout: c.timeout}
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return &DialError{Addr: c.addr, Err: err}
	}
	c.conn = conn
	c.scanner = bufio.NewScanner(conn)
	c.scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	c.scanner.Split(splitCRLF)
	if c.wakeOnConnect {
		c.wakeLocked(ctx)
	}
	return nil
}

// wakeLocked performs a best-effort `@SYS:MODELNAME=?` exchange to wake a
// standby receiver. Caller holds c.mu and c.conn is set. The reply (a
// MODELNAME value, an @UNDEFINED, or nothing if the sleeping device
// dropped the ping) is drained directly off the connection — NOT through
// c.scanner — so the scanner buffer stays pristine for the caller's real
// command and a timed-out drain can't leave the scanner in a terminal
// state. Any error/timeout is ignored.
func (c *Client) wakeLocked(ctx context.Context) {
	deadline := time.Now().Add(wakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return
	}
	if _, err := io.WriteString(c.conn, "@SYS:MODELNAME=?\r\n"); err != nil {
		return
	}
	// Drain up to one reply line straight from the conn. Reading raw
	// (instead of via c.scanner) keeps the scanner untouched for the
	// caller and avoids the dead-scanner problem a timed-out Scan causes.
	buf := make([]byte, 256)
	for {
		n, rerr := c.conn.Read(buf)
		if n > 0 && bytes.IndexByte(buf[:n], '\n') >= 0 {
			break
		}
		if rerr != nil {
			break
		}
	}
	// Clear the temporary deadlines so the caller's own deadline logic
	// starts from a clean slate.
	_ = c.conn.SetReadDeadline(time.Time{})
	_ = c.conn.SetWriteDeadline(time.Time{})
}

// Send issues one YNCA line and returns the receiver's reply. The
// leading `@` is added if missing. The trailing CRLF is added
// automatically. Returns ErrNoReply if no reply arrives within timeout.
//
// Recognised error replies are surfaced as typed errors:
//   - `@UNDEFINED` -> *ErrUndefinedCommand
//   - `@RESTRICTED` -> *ErrRestricted
func (c *Client) Send(ctx context.Context, line string) (string, error) {
	if line == "" {
		return "", errors.New("ynca: empty line")
	}
	if !strings.HasPrefix(line, "@") {
		line = "@" + line
	}
	// Strip any caller-supplied trailing whitespace/CRLF; we add our own.
	line = strings.TrimRight(line, "\r\n")

	c.mu.Lock()
	defer c.mu.Unlock()

	// Try once; on a stale connection (e.g. broken pipe), reconnect and retry.
	reply, err := c.sendOnceLocked(ctx, line)
	if err != nil && isConnReset(err) {
		_ = c.closeLocked()
		reply, err = c.sendOnceLocked(ctx, line)
	}
	if err != nil {
		return "", err
	}
	return classifyReply(reply)
}

// sendOnceLocked performs a single write+read cycle. Caller must hold c.mu.
func (c *Client) sendOnceLocked(ctx context.Context, line string) (reply string, err error) {
	if c.conn == nil {
		if derr := c.dialLocked(ctx); derr != nil {
			return "", derr
		}
	}

	// Any non-nil exit from this function leaves the connection in an
	// undefined protocol state: half-written request, unread reply tail
	// after ctx-cancel, or an in-flight reply still on the wire after
	// ErrNoReply. Closing forces the next Send to redial with a clean
	// socket. Registered before the watchdog-stop defer so it runs
	// AFTER the watchdog has stopped touching c.conn (defers are LIFO).
	defer func() {
		if err != nil {
			_ = c.closeLocked()
		}
	}()

	// Drive read/write deadlines from ctx + timeout. A goroutine
	// watches ctx.Done() and forces an immediate deadline so the
	// blocking read returns. The goroutine always exits when this
	// function returns (no leak).
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if derr := c.conn.SetWriteDeadline(deadline); derr != nil {
		return "", derr
	}
	if derr := c.conn.SetReadDeadline(deadline); derr != nil {
		return "", derr
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			// Force the read/write to abort immediately.
			_ = c.conn.SetReadDeadline(time.Unix(1, 0))
			_ = c.conn.SetWriteDeadline(time.Unix(1, 0))
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	if _, werr := io.WriteString(c.conn, line+"\r\n"); werr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", werr
	}

	if !c.scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if serr := c.scanner.Err(); serr != nil {
			if isTimeout(serr) {
				return "", ErrNoReply
			}
			return "", serr
		}
		// EOF with no error: the peer closed the connection.
		return "", io.EOF
	}
	return c.scanner.Text(), nil
}

// versionSentinel is the end-of-stream fence appended by SendMulti. Every
// receiver answers @SYS:VERSION=? with @SYS:VERSION=<version>, so its echo
// reliably marks "all report lines for the preceding command have now
// arrived" — the technique the ynca reference library uses to drain a
// fan-out GET without guessing how many lines it produces.
const versionSentinel = "@SYS:VERSION=?"

// versionEchoPrefix is what the fence echo looks like coming back.
const versionEchoPrefix = "@SYS:VERSION="

// SendMulti issues one YNCA line that may fan out to several report lines
// (e.g. a `@MAIN:BASIC=?` GET, which a receiver answers with many
// `@MAIN:FUNC=VALUE` lines), then drains every reply up to and including
// the echo of a `@SYS:VERSION=?` fence it appends. It returns the
// intervening report lines; the fence echo is consumed, not returned.
//
// Use this for GETs whose reply length isn't known in advance. Send
// remains the one-line-in/one-line-out path for simple PUTs and
// single-value GETs. Unlike Send, SendMulti does not classify
// @UNDEFINED/@RESTRICTED into typed errors — for a fan-out GET those can
// be legitimate per-field replies, so the caller inspects the returned
// lines (see parseLine).
func (c *Client) SendMulti(ctx context.Context, line string) ([]string, error) {
	if line == "" {
		return nil, errors.New("ynca: empty line")
	}
	if !strings.HasPrefix(line, "@") {
		line = "@" + line
	}
	line = strings.TrimRight(line, "\r\n")

	c.mu.Lock()
	defer c.mu.Unlock()

	lines, err := c.sendMultiOnceLocked(ctx, line)
	if err != nil && isConnReset(err) {
		_ = c.closeLocked()
		lines, err = c.sendMultiOnceLocked(ctx, line)
	}
	return lines, err
}

// sendMultiOnceLocked performs one write(command)+write(fence)+drain
// cycle. Caller must hold c.mu. Mirrors sendOnceLocked's connection and
// ctx-watchdog handling, but reads until the fence echo instead of a
// single line.
func (c *Client) sendMultiOnceLocked(ctx context.Context, line string) (lines []string, err error) {
	if c.conn == nil {
		if derr := c.dialLocked(ctx); derr != nil {
			return nil, derr
		}
	}

	// Any non-nil return leaves the stream in an undefined state (partial
	// drain, half-written fence, unread tail after ctx-cancel). Close so
	// the next call redials clean. Registered before the watchdog-stop
	// defer so it runs after the watchdog has stopped touching c.conn.
	defer func() {
		if err != nil {
			_ = c.closeLocked()
		}
	}()

	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if derr := c.conn.SetWriteDeadline(deadline); derr != nil {
		return nil, derr
	}
	if derr := c.conn.SetReadDeadline(deadline); derr != nil {
		return nil, derr
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = c.conn.SetReadDeadline(time.Unix(1, 0))
			_ = c.conn.SetWriteDeadline(time.Unix(1, 0))
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	if _, werr := io.WriteString(c.conn, line+"\r\n"+versionSentinel+"\r\n"); werr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, werr
	}

	for c.scanner.Scan() {
		reply := c.scanner.Text()
		if strings.HasPrefix(reply, versionEchoPrefix) {
			// Fence reached: every prior report line has been drained.
			return lines, nil
		}
		lines = append(lines, reply)
	}
	// Stream ended before the fence echo.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if serr := c.scanner.Err(); serr != nil {
		if isTimeout(serr) {
			return nil, ErrNoReply
		}
		return nil, serr
	}
	return nil, io.EOF
}

// Probe issues `@SYS:VERSION=?` and returns the firmware version, e.g.
// "2.87/1.81". Used to confirm the receiver speaks YNCA.
func (c *Client) Probe(ctx context.Context) (string, error) {
	reply, err := c.Send(ctx, "@SYS:VERSION=?")
	if err != nil {
		// A receiver that does not speak YNCA at all typically replies
		// with @UNDEFINED or closes the connection without writing.
		if errors.Is(err, io.EOF) {
			return "", ErrUnsupported
		}
		var und *ErrUndefinedCommand
		if errors.As(err, &und) {
			return "", ErrUnsupported
		}
		return "", err
	}
	// Reply: `@SYS:VERSION=2.87/1.81`
	_, _, value, perr := parseLine(reply)
	if perr != nil {
		return "", perr
	}
	return value, nil
}

// classifyReply turns a raw reply line into either a parsed value-bearing
// line (returned as-is) or a typed error for known control replies.
func classifyReply(reply string) (string, error) {
	switch {
	case strings.HasPrefix(reply, "@UNDEFINED"):
		return "", &ErrUndefinedCommand{Line: reply}
	case strings.HasPrefix(reply, "@RESTRICTED"):
		return "", &ErrRestricted{Line: reply}
	}
	return reply, nil
}

// parseLine splits `@SUBUNIT:FUNCTION=value` into its parts. The
// leading `@` is required.
func parseLine(line string) (subunit, function, value string, err error) {
	if !strings.HasPrefix(line, "@") {
		return "", "", "", &ProtocolError{Line: line}
	}
	body := line[1:]
	colon := strings.IndexByte(body, ':')
	eq := strings.IndexByte(body, '=')
	if colon < 0 || eq < 0 || colon > eq {
		return "", "", "", &ProtocolError{Line: line}
	}
	subunit = body[:colon]
	function = body[colon+1 : eq]
	value = body[eq+1:]
	if subunit == "" || function == "" {
		return "", "", "", &ProtocolError{Line: line}
	}
	return subunit, function, value, nil
}

// splitCRLF is a bufio.SplitFunc that yields lines terminated by `\r\n`.
// It also accepts a final unterminated line at EOF.
func splitCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '\r' && data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		// Trim a trailing lone \r if present.
		if data[len(data)-1] == '\r' {
			return len(data), data[:len(data)-1], nil
		}
		return len(data), data, nil
	}
	return 0, nil, nil
}

// isTimeout reports whether err is a net.Error timeout.
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

// isConnReset reports whether err looks like a stale-connection error
// for which a reconnect-and-retry is appropriate.
func isConnReset(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	// net.OpError with a closed/reset underlying error.
	var ne *net.OpError
	if errors.As(err, &ne) {
		if ne.Err != nil && strings.Contains(ne.Err.Error(), "broken pipe") {
			return true
		}
		if ne.Err != nil && strings.Contains(ne.Err.Error(), "connection reset") {
			return true
		}
	}
	return false
}

// replacePort returns addr with its port replaced by p. If addr has no
// port, p is appended.
func replacePort(addr string, p int) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.JoinHostPort(host, strconv.Itoa(p))
}
