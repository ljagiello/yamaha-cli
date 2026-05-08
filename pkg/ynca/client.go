package ynca

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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

	mu      sync.Mutex
	conn    net.Conn
	scanner *bufio.Scanner
}

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
		return fmt.Errorf("ynca: dial %s: %w", c.addr, err)
	}
	c.conn = conn
	c.scanner = bufio.NewScanner(conn)
	c.scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	c.scanner.Split(splitCRLF)
	return nil
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
func (c *Client) sendOnceLocked(ctx context.Context, line string) (string, error) {
	if c.conn == nil {
		if err := c.dialLocked(ctx); err != nil {
			return "", err
		}
	}

	// Drive read/write deadlines from ctx + timeout. A goroutine
	// watches ctx.Done() and forces an immediate deadline so the
	// blocking read returns. The goroutine always exits when this
	// function returns (no leak).
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return "", err
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return "", err
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

	if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}

	if !c.scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err := c.scanner.Err(); err != nil {
			if isTimeout(err) {
				return "", ErrNoReply
			}
			return "", err
		}
		// EOF with no error: the peer closed the connection.
		return "", io.EOF
	}
	return c.scanner.Text(), nil
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
