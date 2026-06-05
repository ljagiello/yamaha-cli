package ynca

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// This file adds the long-lived, push-observing counterpart to Client.
//
// Client is strictly one-shot request/response and structurally cannot see
// the unsolicited @SUBUNIT:FUNCTION=value reports a receiver pushes when its
// state changes from the front panel, the remote, or another app. The ynca
// reference library's whole design is instead a long-lived reader that
// parses every pushed line and fans it out to callbacks (connection.py /
// protocol.py). Session is the Go equivalent: it owns its own connection
// and a background read loop, and is the foundation for `ynca watch`.
//
// A Session deliberately does NOT share Client's connection or mutex — mixing
// a blocking reader with request/response replies on one socket would
// interleave pushes and command replies. A caller that needs both keeps a
// Client for commands and a Session for observation, on separate sockets.

// defaultKeepAlive is how often a Session pings to keep the connection out
// of the receiver's ~40s YNCA standby timeout. 30s sits comfortably under
// that threshold (the value the ynca reference library uses).
const defaultKeepAlive = 30 * time.Second

// Report is one parsed line from the push stream. For a normal value line
// (@MAIN:VOL=-40.0) Subunit/Function/Value are set and Status is empty; for
// a bare control line (@UNDEFINED/@RESTRICTED, rare unsolicited) Status
// carries the keyword and the other fields are empty. Raw is always the
// verbatim wire line.
type Report struct {
	Subunit  string
	Function string
	Value    string
	Status   string // "", "UNDEFINED", or "RESTRICTED"
	Raw      string
}

// Session is a long-lived YNCA connection that streams the receiver's push
// reports to a handler. Construct with NewSession and drive with Run.
type Session struct {
	addr          string
	timeout       time.Duration
	keepAlive     time.Duration
	wakeOnConnect bool
	commLog       CommLogger
}

// SessionOption configures a Session.
type SessionOption func(*Session)

// WithSessionTimeout sets the dial timeout and the write deadline used for
// keep-alive pings. The read loop itself is unbounded (it blocks for the
// next pushed line); cancel the context to stop it. Default: 3s.
func WithSessionTimeout(d time.Duration) SessionOption {
	return func(s *Session) {
		if d > 0 {
			s.timeout = d
		}
	}
}

// WithKeepAlive overrides the keep-alive ping interval. A non-positive
// duration disables keep-alive entirely (the connection will drop after the
// receiver's standby timeout if no reports arrive). Default: 30s.
func WithKeepAlive(d time.Duration) SessionOption {
	return func(s *Session) { s.keepAlive = d }
}

// WithSessionWake sends a cheap wake ping right after connecting, absorbing
// the first-command drop a standby receiver applies (see Client's
// WithWakeOnConnect). The echo is consumed by the read loop like any other
// line.
func WithSessionWake() SessionOption {
	return func(s *Session) { s.wakeOnConnect = true }
}

// WithSessionCommLog installs a CommLogger for --debug wire tracing.
func WithSessionCommLog(fn CommLogger) SessionOption {
	return func(s *Session) { s.commLog = fn }
}

// NewSession constructs a Session for the given host ("host" or
// "host:port"; default port 50000). The connection is opened by Run.
func NewSession(host string, opts ...SessionOption) (*Session, error) {
	if host == "" {
		return nil, errors.New("ynca: empty host")
	}
	addr := host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, strconv.Itoa(defaultPort))
	}
	s := &Session{
		addr:      addr,
		timeout:   defaultTimeout,
		keepAlive: defaultKeepAlive,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Run dials the receiver and reads its push stream until ctx is cancelled
// or the connection drops, calling handler for each parsed report. It
// returns ctx.Err() on cancellation, a *DialError if the connection can't
// be established, or the underlying read error (often io.EOF) when the peer
// closes the socket — callers that want resilience supervise Run with their
// own reconnect/backoff loop (as `ynca watch` does), mirroring the YXC
// Subscriber model.
//
// handler is called from Run's own goroutine, serially, one report at a
// time; it must not block for long. The keep-alive echo (@SYS:MODELNAME)
// is filtered out before handler sees it, so the 30s ping stays invisible.
func (s *Session) Run(ctx context.Context, handler func(Report)) error {
	d := net.Dialer{Timeout: s.timeout}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return &DialError{Addr: s.addr, Err: err}
	}
	defer func() { _ = conn.Close() }()

	// Closing the conn on ctx cancel is what unblocks the blocking Scan
	// below. stop tears the watchers down when Run returns for any reason.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	if s.wakeOnConnect {
		s.writeLine(conn, "@SYS:MODELNAME=?")
	}
	if s.keepAlive > 0 {
		go s.keepAliveLoop(ctx, conn, stop)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	scanner.Split(splitCRLF)
	for scanner.Scan() {
		raw := scanner.Text()
		s.logLine(false, raw)
		rep, ok := parseReport(raw)
		if !ok {
			continue
		}
		// Suppress the keep-alive echo: the model name never changes
		// mid-session, so a SYS:MODELNAME report is just our own ping coming
		// back — emitting it would surface a phantom "event" every 30s.
		if rep.Subunit == SubunitSystem && rep.Function == FuncModelName {
			continue
		}
		handler(rep)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if serr := scanner.Err(); serr != nil {
		return serr
	}
	// Clean EOF: the receiver closed the connection.
	return io.EOF
}

// keepAliveLoop pings @SYS:MODELNAME=? every keepAlive interval so an idle
// connection doesn't fall into the receiver's standby timeout. It is the
// only writer on the socket, so it needs no lock against the reader
// (concurrent read+write on a net.Conn is safe). It exits when the context
// is cancelled, Run returns (stop), or a write fails.
func (s *Session) keepAliveLoop(ctx context.Context, conn net.Conn, stop <-chan struct{}) {
	t := time.NewTicker(s.keepAlive)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			_ = conn.SetWriteDeadline(time.Now().Add(s.timeout))
			if _, err := io.WriteString(conn, "@SYS:MODELNAME=?\r\n"); err != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Time{})
			s.logLine(true, "@SYS:MODELNAME=?")
		}
	}
}

// writeLine sends one line (CRLF-terminated) under the session timeout,
// best-effort. Used for the optional wake ping.
func (s *Session) writeLine(conn net.Conn, line string) {
	_ = conn.SetWriteDeadline(time.Now().Add(s.timeout))
	if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Time{})
	s.logLine(true, line)
}

// logLine forwards a wire line to the comm logger, if installed.
func (s *Session) logLine(sent bool, line string) {
	if s.commLog != nil {
		s.commLog(sent, line)
	}
}

// parseReport turns a raw push line into a Report. A well-formed
// @SUB:FUNC=value line yields a value report; a bare @UNDEFINED/@RESTRICTED
// line yields a status report; anything else is dropped (ok=false).
func parseReport(line string) (Report, bool) {
	if su, fn, val, err := parseLine(line); err == nil {
		return Report{Subunit: su, Function: fn, Value: val, Raw: line}, true
	}
	switch {
	case strings.HasPrefix(line, "@UNDEFINED"):
		return Report{Status: "UNDEFINED", Raw: line}, true
	case strings.HasPrefix(line, "@RESTRICTED"):
		return Report{Status: "RESTRICTED", Raw: line}, true
	}
	return Report{}, false
}
