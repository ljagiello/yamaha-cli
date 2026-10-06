package discover

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// yamahaDescPort is the TCP port Yamaha receivers serve their UPnP
// description on (verified on an RX-V583, firmware 2.87, and an R-N800A).
const yamahaDescPort = "49154"

// describeSSDPPort is the UDP port Describe sends its unicast M-SEARCH
// to, and describeFallbackPort the TCP port of the well-known
// description URL it falls back to.
//
// Overridable for tests.
var (
	describeSSDPPort     = 1900
	describeFallbackPort = yamahaDescPort
)

// describeFallbackPath is where Yamaha receivers serve their UPnP
// MediaRenderer description (verified on an RX-V583, firmware 2.87).
const describeFallbackPath = "/MediaRenderer/desc.xml"

// Describe reads the UPnP device description of a single receiver whose
// host is already known, without a LAN-wide multicast scan. It backs
// saving a device by IP: the returned UDN is what lets the
// DHCP-resilience flow (LookupByUDN) find the receiver again after its
// address changes.
//
// It first sends a unicast SSDP M-SEARCH to host (searchSends times, as
// any one can be lost) and fetches the Location from a reply sent by
// that host. If no reply arrives within a quarter of timeout it falls
// back to the well-known Yamaha description URL
// http://<host>:49154/MediaRenderer/desc.xml. A fetch that stalls or
// loses its connection is retried once. The description must identify
// as "Yamaha Corporation" and carry a UDN; otherwise Describe returns an
// error saying why, wrapping ErrNotYamaha when the host answered as
// another manufacturer's device.
//
// timeout bounds the whole call: a quarter for SSDP, then half of what
// is left for the first fetch, so a stalled one leaves time to retry.
func Describe(ctx context.Context, host string, timeout time.Duration) (Device, error) {
	// Honor an already-cancelled context up front, before any I/O.
	if err := ctx.Err(); err != nil {
		return Device{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	location := unicastSearchLocation(ctx, host, timeout/4)
	if err := ctx.Err(); err != nil {
		return Device{}, fmt.Errorf("describe %s: %w", host, err)
	}
	if location == "" {
		location = "http://" + net.JoinHostPort(host, describeFallbackPort) + describeFallbackPath
		tracef(ctx, "→ no Location from unicast ssdp; trying the well-known URL")
	}

	// The contexts carry the deadlines, so the client needs no timeout.
	deadline, _ := ctx.Deadline()
	first, cancelFirst := context.WithTimeout(ctx, time.Until(deadline)/2)
	defer cancelFirst()
	return fetchWithRetry(ctx, first, &http.Client{}, location)
}

// unicastSearchLocation sends a unicast SSDP M-SEARCH to host and
// returns the Location header of the first reply whose source IP is
// host's. It is best-effort: any failure (resolution, socket, no reply
// within wait, ctx done) returns "" and Describe falls back to the
// well-known URL, whose fetch then reports the real error.
//
// Yamaha receivers answer from an ephemeral port rather than 1900, so
// the socket stays unconnected and only the source IP is matched.
func unicastSearchLocation(ctx context.Context, host string, wait time.Duration) string {
	// Resolving and the exchange share wait, so a slow resolver can't
	// eat the budget Describe keeps for the description fetch.
	deadline := time.Now().Add(wait)
	rctx, cancel := context.WithDeadline(ctx, deadline)
	ips, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	cancel()
	if err != nil || len(ips) == 0 {
		tracef(ctx, "← unicast ssdp: resolve %s: %v", host, err)
		return ""
	}
	left := time.Until(deadline)
	if left <= 0 {
		tracef(ctx, "← unicast ssdp: resolving %s used the %v wait", host, wait)
		return ""
	}
	target := &net.UDPAddr{IP: ips[0].IP, Zone: ips[0].Zone, Port: describeSSDPPort}

	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		tracef(ctx, "← unicast ssdp: %v", err)
		return ""
	}
	defer func() { _ = conn.Close() }()

	location := ""
	err = mSearchExchange(ctx, conn, target, mSearch(target.String(), mediaRendererST, searchMX), left, " (unicast)",
		func(from net.Addr, reply []byte) bool {
			if udp, ok := from.(*net.UDPAddr); !ok || !udp.IP.Equal(target.IP) {
				return false
			}
			loc, ok := locationFromSSDPResponse(reply)
			if ok {
				tracef(ctx, "← ssdp %s location=%s", from, loc)
				location = loc
			}
			return ok
		})
	switch {
	case location != "":
	case err != nil:
		tracef(ctx, "← unicast ssdp: %v", err)
	default:
		tracef(ctx, "← no unicast ssdp reply from %s within %v", target.IP, wait)
	}
	return location
}

// locationFromSSDPResponse returns the Location header of an SSDP
// search response datagram, reporting false when msg is not an HTTP
// response or carries no Location.
func locationFromSSDPResponse(msg []byte) (string, bool) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(msg)), nil)
	if err != nil {
		return "", false
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	return loc, loc != ""
}
