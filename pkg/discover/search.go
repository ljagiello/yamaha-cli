package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/net/ipv4"
)

// ssdpGroupAddr is where M-SEARCH requests go: the IPv4 SSDP multicast
// group. Overridable for tests, which point it at a loopback responder.
var ssdpGroupAddr = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

const (
	// searchSends is how many M-SEARCH requests each interface sends.
	// UDP has no retransmission and Wi-Fi drops multicast frames often:
	// with a single request an RX-V583 on Wi-Fi went unfound in 18 of 40
	// scans. UDA 1.1 recommends sending each M-SEARCH more than once.
	searchSends = 3

	// searchMX is the M-SEARCH MX header, the number of seconds a
	// responder may wait before replying. 1 is the minimum UDA allows,
	// which leaves the most room to spread the resends.
	searchMX = 1

	// searchReadBuffer holds any UDP datagram, so an oversized reply
	// reaches the parser (which skips it) instead of failing the read:
	// Windows reports a truncated datagram as a read error.
	searchReadBuffer = 64 << 10
)

// bindAddr is a local IPv4 address to search from and the interface
// that owns it. The zero value binds the wildcard address and leaves
// the choice of multicast interface to the OS.
type bindAddr struct {
	ip  net.IP
	ifi *net.Interface
}

func (b bindAddr) String() string {
	switch {
	case b.ip == nil:
		return "default interface"
	case b.ifi == nil:
		return b.ip.String()
	default:
		return b.ifi.Name + " (" + b.ip.String() + ")"
	}
}

// resendInterval spaces the searchSends requests evenly over the part
// of wait that still leaves responders their MX seconds after the last
// one. Losses come in bursts, so back-to-back resends tend to be lost
// together.
func resendInterval(wait time.Duration) time.Duration {
	return max(0, (wait-searchMX*time.Second)/searchSends)
}

// mSearch builds an SSDP M-SEARCH request for st addressed to host.
func mSearch(host, st string, mx int) []byte {
	return []byte("M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + host + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: " + strconv.Itoa(mx) + "\r\n" +
		"ST: " + st + "\r\n" +
		"\r\n")
}

// searchIface sends the M-SEARCH for st from b, searchSends times spread
// by resendInterval, and returns the Location of every reply received
// before wait elapses, deduplicated. Replies are accepted from any
// source: receivers answer from their own address, not the group's, and
// from an ephemeral port rather than 1900.
//
// It returns early with ctx's error when ctx is done. An error after
// some replies arrived is returned alongside the Locations collected.
func searchIface(ctx context.Context, b bindAddr, st string, wait time.Duration) ([]string, error) {
	host := ""
	if b.ip != nil {
		host = b.ip.String()
	}
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if b.ifi != nil {
		if err := ipv4.NewPacketConn(conn).SetMulticastInterface(b.ifi); err != nil {
			return nil, fmt.Errorf("use %s for multicast: %w", b, err)
		}
	}

	group := ssdpGroupAddr
	var locs []string
	seen := make(map[string]struct{})
	err = mSearchExchange(ctx, conn, group, mSearch(group.String(), st, searchMX), wait, " via "+b.String(),
		func(from net.Addr, reply []byte) bool {
			loc, ok := locationFromSSDPResponse(reply)
			if !ok {
				tracef(ctx, "← ssdp %s: ignored %d bytes (not a search response with a Location)", from, len(reply))
				return false
			}
			tracef(ctx, "← ssdp %s location=%s", from, loc)
			if _, dup := seen[loc]; !dup {
				seen[loc] = struct{}{}
				locs = append(locs, loc)
			}
			return false
		})
	if err == nil && len(locs) == 0 {
		tracef(ctx, "← ssdp no replies via %s within %v", b, wait)
	}
	return locs, err
}

// mSearchExchange sends msg to dst over conn searchSends times, spread by
// resendInterval(wait), and hands every datagram that arrives to onReply
// until wait elapses, ctx is done, or onReply reports it is done. label
// follows the destination in trace lines.
//
// It returns nil when the wait elapses or onReply is done, ctx's error
// when ctx is done first, an error when not even the first send went
// out, and any other read error.
func mSearchExchange(ctx context.Context, conn net.PacketConn, dst net.Addr, msg []byte, wait time.Duration, label string, onReply func(from net.Addr, reply []byte) (done bool)) error {
	// Wakes a blocked read when ctx is done; the loop re-checks ctx after
	// every deadline it sets, so a cancellation is never overwritten.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	interval := resendInterval(wait)
	start := time.Now()
	deadline := start.Add(wait)
	nextSend := func(sent int) time.Time { return start.Add(time.Duration(sent) * interval) }

	buf := make([]byte, searchReadBuffer)
	sent := 0
	for {
		if sent < searchSends && !time.Now().Before(nextSend(sent)) {
			if _, err := conn.WriteTo(msg, dst); err != nil {
				if sent == 0 {
					return fmt.Errorf("send M-SEARCH to %s%s: %w", dst, label, err)
				}
				tracef(ctx, "→ ssdp M-SEARCH %d/%d to %s%s failed: %v", sent+1, searchSends, dst, label, err)
			} else {
				tracef(ctx, "→ ssdp M-SEARCH %d/%d to %s%s", sent+1, searchSends, dst, label)
			}
			sent++
			continue
		}

		readUntil := deadline
		if sent < searchSends && nextSend(sent).Before(readUntil) {
			readUntil = nextSend(sent)
		}
		if err := conn.SetReadDeadline(readUntil); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				return err
			}
			if time.Now().Before(deadline) {
				continue // time for the next send
			}
			return nil
		}
		if onReply(from, buf[:n]) {
			return nil
		}
	}
}
