package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxDescriptionBody caps how many bytes we read from a single SSDP
// description XML response. The realistic ceiling for Yamaha receivers
// is a few KiB; 1 MiB leaves room for verbose UPnP descriptions while
// preventing a misbehaving LAN peer from streaming an unbounded XML
// document and OOMing the CLI inside the discovery timeout window.
const maxDescriptionBody = 1 << 20

// mediaRendererST is the SSDP search target used to find UPnP
// MediaRenderer devices, which is the surface Yamaha exposes for
// MusicCast/YXC receivers.
const mediaRendererST = "urn:schemas-upnp-org:device:MediaRenderer:1"

// yamahaManufacturer is the exact manufacturer string Yamaha receivers
// report in their UPnP device description. We match on equality, not
// substring, to avoid accidentally swallowing other vendors that
// reference Yamaha in their text.
const yamahaManufacturer = "Yamaha Corporation"

// ErrNotYamaha is wrapped by the error Describe returns when the host
// answered with the description of another manufacturer's device: the
// host is reachable, but it is not a Yamaha receiver.
var ErrNotYamaha = errors.New("not a Yamaha receiver")

// Device is a discovered Yamaha receiver.
type Device struct {
	// Name is the device's friendlyName (e.g. "RX-V583 FBE863").
	Name string
	// Host is the bare IP address (no scheme, no port) suitable for
	// persisting in the user's config file.
	Host string
	// Model is the device's modelName (e.g. "RX-V583").
	Model string
	// BaseURL is the YXC base URL ("http://<host>/YamahaExtendedControl/v1/").
	// Always derived from the SSDP Location host; the description's
	// yamaha:X_yxcControlURL element is intentionally ignored as the
	// path is fixed across firmware revisions.
	BaseURL string
	// UDN is the persistent unique device name from the description XML
	// (e.g. "uuid:9ab0c000-f668-11de-9976-00a0defbe863"). Survives
	// DHCP renewals and is the key for re-locating a device after its
	// IP changes.
	UDN string
}

// Search performs an SSDP scan for MediaRenderer devices, fetches the
// description XML for each responder, filters to manufacturer == "Yamaha
// Corporation", and returns the deduplicated set (keyed by UDN). When
// that finds no Yamaha receiver it probes this computer's private
// subnets over TCP instead (see sweep), which gets through a stateful
// host firewall that drops the SSDP replies.
//
// timeout bounds the SSDP wait and each description fetch (one retry is
// allowed for a fetch that stalls after connecting). The minimum
// effective SSDP wait is 1 second, so responders always get their full
// MX window; smaller timeouts are rounded up. The probe, when it runs,
// adds about sweepDialTimeout per 256 addresses (at most four /24s).
func Search(ctx context.Context, timeout time.Duration) ([]Device, error) {
	return search(ctx, timeout, func(devs []Device) bool { return len(devs) == 0 }, netip.Addr{})
}

// LookupByUDN runs Search's SSDP scan and returns the device whose UDN
// matches. It is the entry point for the DHCP-resilience flow: when a
// saved receiver stops responding, the CLI calls this with the UDN and
// the address it was saved at (lastHost) to find it at its new address.
//
// When SSDP misses the UDN and lastHost is an IPv4 address on one of
// this computer's subnets, the /24 around lastHost is probed (see
// sweep): a receiver renumbered by DHCP stays on its subnet. When
// lastHost is a hostname, or on a subnet this computer is not on (away
// from home), the lookup is SSDP-only, so no subnet is probed without
// evidence the receiver was on it. Returns an error when no match is
// found.
func LookupByUDN(ctx context.Context, udn, lastHost string, timeout time.Duration) (Device, error) {
	near, err := netip.ParseAddr(lastHost)
	near = near.Unmap()
	probe := err == nil && near.Is4()
	if !probe {
		near = netip.Addr{}
	}
	devs, err := search(ctx, timeout, func(devs []Device) bool {
		_, found := findUDN(devs, udn)
		return probe && !found
	}, near)
	if err != nil {
		return Device{}, err
	}
	if d, found := findUDN(devs, udn); found {
		return d, nil
	}
	tracef(ctx, "← no device with UDN %s among %d found", udn, len(devs))
	return Device{}, fmt.Errorf("device with UDN %q not found on LAN", udn)
}

func findUDN(devs []Device, udn string) (Device, bool) {
	for _, d := range devs {
		if d.UDN == udn {
			return d, true
		}
	}
	return Device{}, false
}

// search runs the SSDP search and, when needProbe says its result falls
// short, the subnet probe around near (all subnets when near is the zero
// Addr), merging both by UDN. An SSDP error (no usable interface, say)
// is returned only when nothing was found either way.
func search(ctx context.Context, timeout time.Duration, needProbe func([]Device) bool, near netip.Addr) ([]Device, error) {
	locations, ssdpErr := searchLocations(ctx, mediaRendererST, timeout)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := fetchAndFilter(ctx, locations, timeout)
	if err != nil {
		return nil, err
	}
	if needProbe(devs) {
		swept, err := sweepFn(ctx, timeout, near)
		if err != nil {
			return nil, err
		}
		for _, d := range swept {
			if _, dup := findUDN(devs, d.UDN); !dup {
				devs = append(devs, d)
			}
		}
	}
	if len(devs) == 0 && ssdpErr != nil {
		return nil, ssdpErr
	}
	return devs, nil
}

// searchLocations runs an SSDP M-SEARCH and returns the unique set of
// description-XML URLs reported in the responders' Location headers.
// Factored out so the per-Location fetch+parse path can be tested
// without driving real multicast traffic.
//
// Overridable via searchLocationsFn for tests.
var searchLocationsFn = defaultSearchLocations
var searchIfaceFn = searchIface
var searchAddrsFn = searchAddrs

func searchLocations(ctx context.Context, st string, timeout time.Duration) ([]string, error) {
	return searchLocationsFn(ctx, st, timeout)
}

func defaultSearchLocations(ctx context.Context, st string, timeout time.Duration) ([]string, error) {
	// Honor an already-cancelled context up front so callers don't pay
	// the multicast wait when they've already been told to stop.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wait := max(timeout, time.Second)
	addrs, err := searchAddrsFn()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var locs []string
	var firstErr error
	type searchResult struct {
		bind bindAddr
		locs []string
		err  error
	}
	// The channel is buffered to the goroutine count so every send
	// succeeds without a receiver: on ctx cancellation we return before
	// draining it, and each search then finishes its send and exits.
	results := make(chan searchResult, len(addrs))
	for _, addr := range addrs {
		if err := ctx.Err(); err != nil {
			return locs, err
		}
		go func(b bindAddr) {
			found, err := searchIfaceFn(ctx, b, st, wait)
			results <- searchResult{bind: b, locs: found, err: err}
		}(addr)
	}
	for range addrs {
		select {
		case <-ctx.Done():
			return locs, ctx.Err()
		case result := <-results:
			if result.err != nil {
				tracef(ctx, "← ssdp search via %s failed: %v", result.bind, result.err)
				if firstErr == nil {
					firstErr = result.err
				}
			}
			for _, loc := range result.locs {
				if _, ok := seen[loc]; ok {
					continue
				}
				seen[loc] = struct{}{}
				locs = append(locs, loc)
			}
		}
	}
	if len(locs) == 0 && firstErr != nil {
		return nil, fmt.Errorf("ssdp search: %w", firstErr)
	}
	// Sort so the result order is stable across runs: locations arrive in
	// non-deterministic goroutine-completion order, and the interactive
	// `--add` picker numbers devices by this order.
	sort.Strings(locs)
	return locs, nil
}

// ifaceAddrs is the subset of a network interface that searchAddrs needs:
// the interface itself (name, index, flags) and its addresses. Pulling
// net.Interfaces and (*Interface).Addrs behind the interfaceAddrsFn seam
// lets the interface-filtering logic be tested without depending on the
// host's real network configuration.
type ifaceAddrs struct {
	ifi   net.Interface
	addrs []net.Addr
}

var interfaceAddrsFn = systemInterfaceAddrs

func systemInterfaceAddrs() ([]ifaceAddrs, error) {
	ifis, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]ifaceAddrs, 0, len(ifis))
	for _, ifi := range ifis {
		addrs, err := ifi.Addrs()
		if err != nil {
			// An interface whose addresses can't be read is unusable for
			// binding; skip it rather than failing the whole scan.
			continue
		}
		out = append(out, ifaceAddrs{ifi: ifi, addrs: addrs})
	}
	return out, nil
}

// searchableIface reports whether an interface with flags f is one
// discovery uses: up, multicast-capable, and not loopback. Both the SSDP
// search and the subnet probe go through the same interfaces.
func searchableIface(f net.Flags) bool {
	return f&net.FlagUp != 0 && f&net.FlagMulticast != 0 && f&net.FlagLoopback == 0
}

// searchAddrs returns one bindAddr per IPv4 address on every up,
// multicast-capable, non-loopback interface, each paired with the
// interface that owns it so the M-SEARCH leaves through that interface.
func searchAddrs() ([]bindAddr, error) {
	ifis, err := interfaceAddrsFn()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	var out []bindAddr
	for i := range ifis {
		ifi := &ifis[i].ifi
		if !searchableIface(ifi.Flags) {
			continue
		}
		for _, addr := range ifis[i].addrs {
			ip := ipv4FromAddr(addr)
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			out = append(out, bindAddr{ip: ip, ifi: ifi})
		}
	}
	// Fall back to the wildcard bind when no concrete multicast interface
	// qualifies, so discovery still attempts a default-route scan instead
	// of silently returning zero addresses.
	if len(out) == 0 {
		return []bindAddr{{}}, nil
	}
	return out, nil
}

func ipv4FromAddr(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.IPNet:
		return a.IP.To4()
	case *net.IPAddr:
		return a.IP.To4()
	}
	return nil
}

// fetchParallel caps concurrent description fetches.
const fetchParallel = 16

// fetchAndFilter performs the per-Location description fetch + parse +
// Yamaha-filter + UDN-dedup pipeline. Fetches run concurrently, so one
// slow or hung responder delays the result by at most its own fetch,
// and the result keeps the order of locations. Errors fetching or
// parsing any individual Location are non-fatal: discovery should
// surface every device that responded cleanly even if a peer device's
// HTTP server is flaky.
func fetchAndFilter(ctx context.Context, locations []string, timeout time.Duration) ([]Device, error) {
	client := &http.Client{Timeout: timeout}
	type result struct {
		dev Device
		err error
	}
	results := make([]result, len(locations))
	sem := make(chan struct{}, fetchParallel)
	var wg sync.WaitGroup
	for i, loc := range locations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			dev, err := fetchWithRetry(ctx, ctx, client, loc)
			if err != nil {
				tracef(ctx, "← skip %s: %v", loc, err)
			}
			results[i] = result{dev, err}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(locations))
	var out []Device
	for _, r := range results {
		if r.err != nil {
			continue
		}
		if _, dup := seen[r.dev.UDN]; dup {
			continue
		}
		tracef(ctx, "← found %s (%s) at %s, %s", r.dev.Name, r.dev.Model, r.dev.Host, r.dev.UDN)
		seen[r.dev.UDN] = struct{}{}
		out = append(out, r.dev)
	}
	return out, nil
}

// fetchWithRetry runs fetchOne under first and, when that stalled after
// connecting (see transientFetchErr) and ctx is still live, once more
// under ctx: over Wi-Fi an RX-V583 stalls on some description fetches
// (3 of 40 with curl alone hit 3s), and a fresh request succeeds. A
// host that accepts the connection but never answers costs two fetches.
func fetchWithRetry(ctx, first context.Context, client *http.Client, location string) (Device, error) {
	dev, err := fetchOne(first, client, location)
	if err != nil && transientFetchErr(err) && ctx.Err() == nil {
		tracef(ctx, "← retry %s: %v", location, err)
		dev, err = fetchOne(ctx, client, location)
	}
	return dev, err
}

// stallError marks a fetch that got a connection and then timed out
// (before or during the response) or lost the body part-way.
type stallError struct{ err error }

func (e *stallError) Error() string { return e.err.Error() }
func (e *stallError) Unwrap() error { return e.err }

// markStall wraps err in a stallError when the request had connected
// and err is a timeout or a cut-short body.
func markStall(err error, connected bool) error {
	var ne net.Error
	if connected && (errors.As(err, &ne) && ne.Timeout() || errors.Is(err, io.ErrUnexpectedEOF)) {
		return &stallError{err}
	}
	return err
}

// transientFetchErr reports whether a description fetch stalled after
// connecting, which a fresh request can cure. A failed connect means the
// host is absent or the address wrong, and an answer (a status, a parse
// error, another vendor) would come back the same.
func transientFetchErr(err error) bool {
	var se *stallError
	return errors.As(err, &se)
}

// fetchOne resolves a single SSDP Location to a Device. It returns an
// error on any transport failure, on non-2xx HTTP status, on parse
// failure, or when the description doesn't identify as a Yamaha receiver
// with a UDN. fetchAndFilter skips such entries silently; Describe
// surfaces the error so the user learns why a known host was rejected.
func fetchOne(ctx context.Context, client *http.Client, location string) (Device, error) {
	tracef(ctx, "→ GET %s", location)
	// Whether a connection was made decides if a timeout is worth a
	// retry, and http.Client drops the dial error from what it returns
	// when its Timeout fires, so record it on the way.
	var connected atomic.Bool
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	})
	req, err := http.NewRequestWithContext(traced, http.MethodGet, location, nil)
	if err != nil {
		return Device{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Device{}, markStall(err, connected.Load())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Device{}, fmt.Errorf("GET %s: unexpected status %s", location, resp.Status)
	}
	desc, err := parseDescriptionXML(io.LimitReader(resp.Body, maxDescriptionBody))
	if err != nil {
		return Device{}, markStall(fmt.Errorf("%s: %w", location, err), true)
	}
	if desc.Manufacturer != yamahaManufacturer {
		return Device{}, fmt.Errorf("%s: %w (manufacturer %q)", location, ErrNotYamaha, desc.Manufacturer)
	}
	if desc.UDN == "" {
		return Device{}, fmt.Errorf("%s: description has no UDN", location)
	}
	return newDevice(location, desc)
}

// newDevice builds the Device for the Yamaha description desc served at
// location. A host with ':' (IPv6) or '%' (an IPv6 zone, or a
// %25-escaped name) is refused: unbracketed in BaseURL it is malformed,
// and saved to the config it is a host neither yxc nor ynca (which reads
// any ':' as a port separator) can use.
func newDevice(location string, desc descDevice) (Device, error) {
	host, err := hostFromLocation(location)
	if err != nil {
		return Device{}, err
	}
	if strings.ContainsAny(host, ":%") {
		return Device{}, fmt.Errorf("%s: host %q is not supported (IPv6 or contains '%%')", location, host)
	}
	return Device{
		Name:    desc.FriendlyName,
		Host:    host,
		Model:   desc.ModelName,
		BaseURL: fmt.Sprintf("http://%s/YamahaExtendedControl/v1/", host),
		UDN:     desc.UDN,
	}, nil
}

// hostFromLocation strips the port and scheme from a Location URL,
// returning just the host portion (IP or hostname). The YXC base URL
// always uses port 80, so we discard whatever port the description
// document was served on (typically 49154 on Yamaha receivers).
func hostFromLocation(location string) (string, error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		// Fall back to SplitHostPort for malformed URLs that still have
		// a usable host:port pair in the opaque portion.
		h, _, splitErr := net.SplitHostPort(u.Host)
		if splitErr != nil || h == "" {
			return "", fmt.Errorf("location %q has no host", location)
		}
		host = h
	}
	return host, nil
}
