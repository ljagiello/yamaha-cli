package discover

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"time"
)

// describeSSDPPort is the UDP port Describe sends its unicast M-SEARCH
// to, and describeFallbackPort the TCP port of the well-known
// description URL it falls back to.
//
// Overridable for tests.
var (
	describeSSDPPort     = 1900
	describeFallbackPort = "49154"
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
// It first sends a unicast SSDP M-SEARCH to host and fetches the
// Location from a reply sent by that host. If no reply arrives within
// half of timeout it falls back to the well-known Yamaha description URL
// http://<host>:49154/MediaRenderer/desc.xml. The description must
// identify as "Yamaha Corporation" and carry a UDN; otherwise Describe
// returns an error saying why, wrapping ErrNotYamaha when the host
// answered as another manufacturer's device.
//
// timeout bounds the whole call, including the description fetch.
func Describe(ctx context.Context, host string, timeout time.Duration) (Device, error) {
	// Honor an already-cancelled context up front, before any I/O.
	if err := ctx.Err(); err != nil {
		return Device{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	location := unicastSearchLocation(ctx, host, timeout/2)
	if location == "" {
		location = "http://" + net.JoinHostPort(host, describeFallbackPort) + describeFallbackPath
	}
	// ctx carries the deadline, so the client needs no timeout of its own.
	return fetchOne(ctx, &http.Client{}, location)
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
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	target := &net.UDPAddr{IP: ips[0].IP, Zone: ips[0].Zone, Port: describeSSDPPort}

	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return ""
	}
	// Registered after the wait deadline so a ctx that is already done
	// overrides it rather than being overwritten by it.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + target.String() + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 1\r\n" +
		"ST: " + mediaRendererST + "\r\n" +
		"\r\n"
	if _, err := conn.WriteTo([]byte(msg), target); err != nil {
		return ""
	}

	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return ""
		}
		if udp, ok := from.(*net.UDPAddr); !ok || !udp.IP.Equal(target.IP) {
			continue
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(buf[:n])), nil)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != "" {
			return loc
		}
	}
}
