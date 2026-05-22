package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Event is one push notification received from a Yamaha receiver over
// UDP. The receiver sends a JSON object whose top-level keys are zone
// names ("main", "zone2") or subsystem names ("system", "netusb",
// "tuner", "dist", "main_input"); values are partial state deltas. We
// expose the raw bytes — callers parse what they need.
//
// Control events surfaced by the Subscriber itself (reconnect attempts,
// shutdown) carry Kind != "" and Raw == nil.
type Event struct {
	// Raw is the verbatim JSON document sent by the receiver. nil for
	// control events.
	Raw json.RawMessage
	// From is the UDP sender's address. Useful when one Subscriber
	// listens for events from multiple receivers (not supported by this
	// API but kept on the type so callers can disambiguate).
	From netip.AddrPort
	// Kind classifies control events ("subscribe", "renew", "shutdown",
	// "reconnect"). Empty for ordinary device events.
	Kind string
	// Err carries the error that triggered a control event, if any.
	Err error
}

// Default Subscriber timing values. The user-tunable ones are exposed
// via Subscriber fields; the rest stay as package constants.
const (
	subscribeRenewIntervalDef = 8 * time.Minute
	subscribeBackoffMinDef    = 1 * time.Second
	subscribeBackoffMaxDef    = 60 * time.Second
	subscribeSilentAfter      = 30 * time.Second
	// subscribeReadDeadline bounds how long PacketConn.ReadFrom blocks
	// before returning so the loop can check ctx and silence.
	subscribeReadDeadline = 500 * time.Millisecond
)

// Subscriber configures a UDP event subscription.
//
// The zero value is valid; defaults kick in for unset fields.
type Subscriber struct {
	// Logger receives subscriber lifecycle messages. nil = silent.
	Logger *slog.Logger
	// BackoffMin is the initial reconnect backoff. Default: 1s.
	BackoffMin time.Duration
	// BackoffMax is the cap on reconnect backoff. Default: 60s.
	BackoffMax time.Duration
	// SilentAfter is the duration of UDP silence after which the
	// subscription is considered dead and reconnect is triggered.
	// Default: 30s.
	SilentAfter time.Duration
	// RenewInterval is how often the subscriber re-issues the
	// subscription HTTP call to refresh the receiver's event registration.
	// The receiver expires registrations at ~10 min, so anything
	// noticeably less is fine. Default: 8 min.
	RenewInterval time.Duration
}

// Subscribe binds a UDP socket on 127.0.0.1, registers the event
// subscription with the receiver via EventDo, and pumps events into the
// returned channel until ctx is cancelled.
//
// The receiver expires push subscriptions after ~10 minutes; Subscribe
// re-issues the subscription every 8 minutes to renew it. On any of
// these conditions Subscribe attempts to reconnect with exponential
// backoff bounded by BackoffMin/BackoffMax:
//
//   - Initial subscription error
//   - Renewal failure
//   - SilentAfter of UDP silence
//
// Reconnect attempts and shutdown are surfaced as control events
// (Event.Kind != "").
//
// The supplied Client's event port is overwritten with the Subscriber's
// bound UDP port, so the caller does not need to pre-configure
// WithEventPort. The client may continue to be used after Subscribe
// returns — the port remains set.
//
// zones are the zone IDs to subscribe (e.g. "main", "zone2"); each is
// subscribed by issuing `<zone>/getStatus` with the event headers.
//
// The returned channel is closed after a final shutdown event when ctx
// is cancelled.
func (s *Subscriber) Subscribe(ctx context.Context, c *Client, zones []string) (<-chan Event, error) {
	if c == nil {
		return nil, errors.New("yxc: Subscribe: nil client")
	}
	if len(zones) == 0 {
		return nil, errors.New("yxc: Subscribe: at least one zone required")
	}
	for _, z := range zones {
		if _, err := validZone(z); err != nil {
			return nil, err
		}
	}

	// Apply defaults.
	backoffMin := s.BackoffMin
	if backoffMin <= 0 {
		backoffMin = subscribeBackoffMinDef
	}
	backoffMax := s.BackoffMax
	if backoffMax <= 0 {
		backoffMax = subscribeBackoffMaxDef
	}
	if backoffMax < backoffMin {
		backoffMax = backoffMin
	}
	silentAfter := s.SilentAfter
	if silentAfter <= 0 {
		silentAfter = subscribeSilentAfter
	}
	renewInterval := s.RenewInterval
	if renewInterval <= 0 {
		renewInterval = subscribeRenewIntervalDef
	}

	// Bind a UDP socket. We bind on 0.0.0.0 so the receiver can reach
	// us regardless of which local interface the request originated on.
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("yxc: Subscribe: bind UDP: %w", err)
	}
	udpAddr, ok := pc.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("yxc: Subscribe: unexpected LocalAddr type %T", pc.LocalAddr())
	}

	// Wire the bound port into the client so EventDo emits it. Routed
	// through the locked accessor so concurrent Do/EventDo callers
	// don't race on the field.
	c.setEventPort(udpAddr.Port)

	// Resolve the receiver's host once so the reader can drop UDP
	// packets that don't originate from it. A LAN attacker who knows
	// the bound port could otherwise inject crafted state — this filter
	// closes that hatch. Best-effort: if resolution fails, keep going
	// without filtering (log via s.log so debug users notice).
	//
	// Indirected via a package-level var so tests can substitute a
	// fake set without driving real DNS or rebuilding the client URL.
	expected := resolveExpectedAddrsFn(c)
	if len(expected) == 0 {
		s.log("UDP source filter disabled: failed to resolve receiver host", "url", c.BaseURL())
	}

	out := make(chan Event, 32)

	go s.run(ctx, c, pc, zones, backoffMin, backoffMax, silentAfter, renewInterval, expected, out)

	return out, nil
}

// resolveExpectedAddrsFn is the seam Subscribe consults to learn the
// source-IP allow-set. Tests stub it to drive the drop branch without
// driving real DNS resolution.
var resolveExpectedAddrsFn = resolveExpectedAddrs

// resolveExpectedAddrs returns the set of source IPs we should accept
// UDP packets from for this client. We resolve the host (which may be
// a hostname or a literal IP) at subscribe time. An empty set means
// "no filtering"; the reader will accept any source.
func resolveExpectedAddrs(c *Client) map[netip.Addr]struct{} {
	host := c.baseURL.Hostname()
	if host == "" {
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return map[netip.Addr]struct{}{a.Unmap(): {}}
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	out := make(map[netip.Addr]struct{}, len(ips))
	for _, ip := range ips {
		if a, ok := netip.AddrFromSlice(ip); ok {
			out[a.Unmap()] = struct{}{}
		}
	}
	return out
}

// run is the main subscription pump. It owns pc and closes it on exit.
//
// expected is the set of source IPs the receiver is allowed to send
// from. An empty set disables filtering (e.g. when host resolution
// failed at subscribe time).
func (s *Subscriber) run(
	ctx context.Context,
	c *Client,
	pc net.PacketConn,
	zones []string,
	backoffMin, backoffMax, silentAfter, renewInterval time.Duration,
	expected map[netip.Addr]struct{},
	out chan<- Event,
) {
	defer close(out)
	defer func() { _ = pc.Close() }()

	// Reader goroutine pumps UDP packets into a local channel. We use a
	// channel so the supervisor goroutine can multiplex over reads,
	// renewal ticks, ctx cancel, and silence.
	type pkt struct {
		data []byte
		from netip.AddrPort
		err  error
	}
	pkts := make(chan pkt, 32)
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		buf := make([]byte, 64*1024)
		for {
			_ = pc.SetReadDeadline(time.Now().Add(subscribeReadDeadline))
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					// Read deadline tick — check ctx and continue.
					select {
					case <-ctx.Done():
						return
					default:
						continue
					}
				}
				// Permanent error (likely closed socket on shutdown).
				select {
				case pkts <- pkt{err: err}:
				case <-ctx.Done():
				}
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			ap := netip.AddrPort{}
			if ua, ok := addr.(*net.UDPAddr); ok {
				if a, ok := netip.AddrFromSlice(ua.IP); ok {
					ap = netip.AddrPortFrom(a.Unmap(), uint16(ua.Port))
				}
			}
			select {
			case pkts <- pkt{data: data, from: ap}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Try the first subscription. On success, enter the steady-state
	// loop. On failure, enter the backoff loop until we succeed or ctx
	// is cancelled.
	backoff := backoffMin
	for {
		err := s.subscribe(ctx, c, zones)
		if err == nil {
			s.emit(ctx, out, &Event{Kind: "subscribe"})
			break
		}
		s.log("subscribe failed", "err", err)
		s.emit(ctx, out, &Event{Kind: "reconnect", Err: err})
		select {
		case <-ctx.Done():
			s.emit(ctx, out, &Event{Kind: "shutdown", Err: ctx.Err()})
			readerWG.Wait()
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff, backoffMax)
	}
	backoff = backoffMin

	// Steady-state loop.
	renewT := time.NewTicker(renewInterval)
	defer renewT.Stop()
	silenceT := time.NewTimer(silentAfter)
	defer silenceT.Stop()

	resetSilence := func() {
		if !silenceT.Stop() {
			// Drain if a fire is pending.
			select {
			case <-silenceT.C:
			default:
			}
		}
		silenceT.Reset(silentAfter)
	}

	for {
		select {
		case <-ctx.Done():
			s.emit(ctx, out, &Event{Kind: "shutdown", Err: ctx.Err()})
			readerWG.Wait()
			return

		case p, ok := <-pkts:
			if !ok {
				return
			}
			if p.err != nil {
				// Reader stopped — typically socket closed. Treat as
				// terminal.
				s.log("reader error", "err", p.err)
				s.emit(ctx, out, &Event{Kind: "shutdown", Err: p.err})
				readerWG.Wait()
				return
			}
			// Drop packets whose source isn't the registered receiver.
			// Without this filter, any LAN host can craft a UDP packet
			// to our bound port and the CLI would surface it as device
			// state. Best-effort: empty `expected` (resolution failed)
			// disables filtering, in which case we accept anything and
			// expose `From` in the event so consumers can validate.
			if len(expected) > 0 {
				if _, ok := expected[p.from.Addr()]; !ok {
					s.log("dropped packet from unexpected source",
						"from", p.from.String())
					continue
				}
			}
			resetSilence()
			s.emit(ctx, out, &Event{Raw: json.RawMessage(p.data), From: p.from})

		case <-renewT.C:
			if err := s.subscribe(ctx, c, zones); err != nil {
				s.log("renewal failed", "err", err)
				s.emit(ctx, out, &Event{Kind: "reconnect", Err: err})
				if !s.recover(ctx, c, zones, &backoff, backoffMin, backoffMax, out) {
					readerWG.Wait()
					return
				}
				resetSilence()
				continue
			}
			// Renewal succeeded. Emit a "renew" control event so
			// consumers can distinguish a periodic renewal from the
			// initial bind ("subscribe") and from recovery
			// ("reconnect"). Kind is set to "renew"; otherwise the
			// event carries no payload.
			s.emit(ctx, out, &Event{Kind: "renew"})

		case <-silenceT.C:
			err := fmt.Errorf("yxc: no events for %s", silentAfter)
			s.log("silence trigger", "after", silentAfter)
			s.emit(ctx, out, &Event{Kind: "reconnect", Err: err})
			if !s.recover(ctx, c, zones, &backoff, backoffMin, backoffMax, out) {
				readerWG.Wait()
				return
			}
			resetSilence()
		}
	}
}

// recover repeatedly tries to re-subscribe with exponential backoff.
// Returns true on success, false on ctx cancellation. *backoff is
// mutated to track the current value across calls.
func (s *Subscriber) recover(
	ctx context.Context,
	c *Client,
	zones []string,
	backoff *time.Duration,
	backoffMin, backoffMax time.Duration,
	out chan<- Event,
) bool {
	for {
		select {
		case <-ctx.Done():
			s.emit(ctx, out, &Event{Kind: "shutdown", Err: ctx.Err()})
			return false
		case <-time.After(*backoff):
		}
		err := s.subscribe(ctx, c, zones)
		if err == nil {
			*backoff = backoffMin
			s.emit(ctx, out, &Event{Kind: "subscribe"})
			return true
		}
		s.log("recover failed", "err", err)
		s.emit(ctx, out, &Event{Kind: "reconnect", Err: err})
		*backoff = nextBackoff(*backoff, backoffMax)
	}
}

// subscribe issues one EventDo call per zone to (re)register the
// subscription with the receiver. Any non-nil error short-circuits.
func (s *Subscriber) subscribe(ctx context.Context, c *Client, zones []string) error {
	for _, z := range zones {
		zn, err := validZone(z)
		if err != nil {
			return err
		}
		if _, err := c.EventDo(ctx, zn+"/getStatus", nil); err != nil {
			return err
		}
	}
	return nil
}

// emit sends ev on out, dropping the send if ctx is cancelled. The
// channel is buffered so this rarely blocks; under sustained back
// pressure we'd rather drop than wedge the supervisor.
//
// The terminal "shutdown" event is special-cased: we always wait for
// it to land (or for a brief grace period) so callers reliably observe
// the lifecycle.
func (s *Subscriber) emit(ctx context.Context, out chan<- Event, ev *Event) {
	if ev.Kind == "shutdown" {
		select {
		case out <- *ev:
		case <-time.After(500 * time.Millisecond):
			// Channel is full and no consumer — give up rather than
			// wedge the goroutine forever.
		}
		return
	}
	select {
	case out <- *ev:
	case <-ctx.Done():
	}
}

// log emits a debug-level message via Logger if configured.
func (s *Subscriber) log(msg string, kv ...any) {
	if s.Logger == nil {
		return
	}
	s.Logger.Debug("yxc.subscribe: "+msg, kv...)
}

// nextBackoff doubles cur up to maxDur.
func nextBackoff(cur, maxDur time.Duration) time.Duration {
	next := cur * 2
	if next > maxDur {
		return maxDur
	}
	if next <= 0 {
		return maxDur
	}
	return next
}
