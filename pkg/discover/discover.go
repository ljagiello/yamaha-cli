package discover

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"time"

	ssdp "github.com/koron/go-ssdp"
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
// Corporation", and returns the deduplicated set (keyed by UDN).
//
// timeout bounds both the SSDP wait and the per-description HTTP fetch.
// The minimum effective SSDP wait is 1 second; smaller timeouts are
// rounded up because go-ssdp's API is second-granular.
func Search(ctx context.Context, timeout time.Duration) ([]Device, error) {
	locations, err := searchLocations(ctx, mediaRendererST, timeout)
	if err != nil {
		return nil, err
	}
	return fetchAndFilter(ctx, locations, timeout)
}

// LookupByUDN runs Search and returns the single device whose UDN
// matches. It is the entry point for the DHCP-resilience flow: when a
// previously-saved host stops responding, the CLI calls this with the
// UDN it cached at first-discovery time to find the receiver at its new
// IP. Returns an error when no match is found.
func LookupByUDN(ctx context.Context, udn string, timeout time.Duration) (Device, error) {
	devs, err := Search(ctx, timeout)
	if err != nil {
		return Device{}, err
	}
	for _, d := range devs {
		if d.UDN == udn {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("device with UDN %q not found on LAN", udn)
}

// searchLocations runs an SSDP M-SEARCH and returns the unique set of
// description-XML URLs reported in the responders' Location headers.
// Factored out so the per-Location fetch+parse path can be tested
// without driving real multicast traffic.
//
// Overridable via searchLocationsFn for tests.
var searchLocationsFn = defaultSearchLocations
var ssdpSearchFn = ssdp.Search
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
	waitSec := int(timeout / time.Second)
	if waitSec < 1 {
		waitSec = 1
	}
	addrs, err := searchAddrsFn()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var locs []string
	var firstErr error
	type searchResult struct {
		services []ssdp.Service
		err      error
	}
	// The channel is buffered to the goroutine count so every send
	// succeeds without a receiver. ssdp.Search takes no context and blocks
	// for the full waitSec, so on ctx cancellation we return early while
	// these goroutines keep running; the buffer slack lets each one finish
	// its send and exit cleanly rather than leaking.
	results := make(chan searchResult, len(addrs))
	for _, addr := range addrs {
		if err := ctx.Err(); err != nil {
			return locs, err
		}
		go func(addr string) {
			services, err := ssdpSearchFn(st, waitSec, addr)
			results <- searchResult{services: services, err: err}
		}(addr)
	}
	for range addrs {
		select {
		case <-ctx.Done():
			return locs, ctx.Err()
		case result := <-results:
			if result.err != nil {
				if firstErr == nil {
					firstErr = result.err
				}
				continue
			}
			for _, s := range result.services {
				if s.Location == "" {
					continue
				}
				if _, ok := seen[s.Location]; ok {
					continue
				}
				seen[s.Location] = struct{}{}
				locs = append(locs, s.Location)
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
// its flags and its addresses. Pulling net.Interfaces and (*Interface).Addrs
// behind the interfaceAddrsFn seam lets the interface-filtering logic be
// tested without depending on the host's real network configuration.
type ifaceAddrs struct {
	flags net.Flags
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
		out = append(out, ifaceAddrs{flags: ifi.Flags, addrs: addrs})
	}
	return out, nil
}

func searchAddrs() ([]string, error) {
	ifis, err := interfaceAddrsFn()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	var out []string
	for _, ifi := range ifis {
		if ifi.flags&net.FlagUp == 0 ||
			ifi.flags&net.FlagMulticast == 0 ||
			ifi.flags&net.FlagLoopback != 0 {
			continue
		}
		for _, addr := range ifi.addrs {
			ip := ipv4FromAddr(addr)
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			out = append(out, net.JoinHostPort(ip.String(), "0"))
		}
	}
	// Fall back to the wildcard bind ("") when no concrete multicast
	// interface qualifies, so discovery still attempts a default-route scan
	// instead of silently returning zero addresses.
	if len(out) == 0 {
		return []string{""}, nil
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

// fetchAndFilter performs the per-Location description fetch + parse +
// Yamaha-filter + UDN-dedup pipeline. Errors fetching or parsing any
// individual Location are non-fatal: discovery should surface every
// device that responded cleanly even if a peer device's HTTP server is
// flaky.
func fetchAndFilter(ctx context.Context, locations []string, timeout time.Duration) ([]Device, error) {
	client := &http.Client{Timeout: timeout}
	seen := make(map[string]struct{}, len(locations))
	var out []Device
	for _, loc := range locations {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		dev, ok := fetchOne(ctx, client, loc)
		if !ok {
			continue
		}
		if _, dup := seen[dev.UDN]; dup {
			continue
		}
		seen[dev.UDN] = struct{}{}
		out = append(out, dev)
	}
	return out, nil
}

// fetchOne resolves a single SSDP Location to a Device. Returns
// (Device, false) on any error, on non-2xx HTTP status, on parse
// failure, or when the description doesn't identify as Yamaha — the
// caller skips the entry silently in those cases.
func fetchOne(ctx context.Context, client *http.Client, location string) (Device, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return Device{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return Device{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Device{}, false
	}
	desc, err := parseDescriptionXML(io.LimitReader(resp.Body, maxDescriptionBody))
	if err != nil {
		return Device{}, false
	}
	if desc.Manufacturer != yamahaManufacturer {
		return Device{}, false
	}
	if desc.UDN == "" {
		return Device{}, false
	}
	host, err := hostFromLocation(location)
	if err != nil {
		return Device{}, false
	}
	return Device{
		Name:    desc.FriendlyName,
		Host:    host,
		Model:   desc.ModelName,
		BaseURL: fmt.Sprintf("http://%s/YamahaExtendedControl/v1/", host),
		UDN:     desc.UDN,
	}, true
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
