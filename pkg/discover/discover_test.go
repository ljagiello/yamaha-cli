package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// sampleYamahaXML mirrors the shape of /tmp/yxc_desc_49154.xml with the
// payload trimmed to what parseDescriptionXML cares about. It exercises
// the dual-namespace gotcha (urn:schemas-upnp-org and
// urn:schemas-yamaha-com) and the dlna:X_DLNADOC sibling that earlier
// versions of this parser tripped over.
const sampleYamahaXML = `<?xml version="1.0" encoding="utf-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0" xmlns:yamaha="urn:schemas-yamaha-com:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <dlna:X_DLNADOC xmlns:dlna="urn:schemas-dlna-org:device-1-0">DMR-1.50</dlna:X_DLNADOC>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>RX-V583 FBE863</friendlyName>
    <manufacturer>Yamaha Corporation</manufacturer>
    <modelName>RX-V583</modelName>
    <UDN>uuid:9ab0c000-f668-11de-9976-00a0defbe863</UDN>
  </device>
  <yamaha:X_device>
    <yamaha:X_URLBase>http://192.168.1.116:80/</yamaha:X_URLBase>
  </yamaha:X_device>
</root>`

// sampleNonYamahaXML is a minimal MediaRenderer description from a
// non-Yamaha vendor, used to verify the manufacturer filter rejects
// foreign devices that share the LAN.
const sampleNonYamahaXML = `<?xml version="1.0" encoding="utf-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <friendlyName>Some Other Renderer</friendlyName>
    <manufacturer>SomeOther Corp</manufacturer>
    <modelName>Model X</modelName>
    <UDN>uuid:11111111-2222-3333-4444-555555555555</UDN>
  </device>
</root>`

func TestParseDescriptionXML(t *testing.T) {
	dev, err := parseDescriptionXML(strings.NewReader(sampleYamahaXML))
	if err != nil {
		t.Fatalf("parseDescriptionXML: %v", err)
	}
	if got, want := dev.Manufacturer, "Yamaha Corporation"; got != want {
		t.Errorf("manufacturer: got %q want %q", got, want)
	}
	if got, want := dev.FriendlyName, "RX-V583 FBE863"; got != want {
		t.Errorf("friendlyName: got %q want %q", got, want)
	}
	if got, want := dev.ModelName, "RX-V583"; got != want {
		t.Errorf("modelName: got %q want %q", got, want)
	}
	if got, want := dev.UDN, "uuid:9ab0c000-f668-11de-9976-00a0defbe863"; got != want {
		t.Errorf("UDN: got %q want %q", got, want)
	}
}

func TestParseDescriptionXML_NonYamaha(t *testing.T) {
	dev, err := parseDescriptionXML(strings.NewReader(sampleNonYamahaXML))
	if err != nil {
		t.Fatalf("parseDescriptionXML: %v", err)
	}
	if dev.Manufacturer == yamahaManufacturer {
		t.Errorf("non-yamaha device unexpectedly matched manufacturer filter")
	}
}

func TestHostFromLocation(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://192.168.1.116:49154/MediaRenderer/desc.xml", "192.168.1.116"},
		{"http://example.local:80/desc.xml", "example.local"},
		{"http://10.0.0.5/desc.xml", "10.0.0.5"},
	}
	for _, tc := range cases {
		got, err := hostFromLocation(tc.in)
		if err != nil {
			t.Errorf("%q: unexpected err: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestNewDevice_RejectsIPv6Host covers a Location whose host is IPv6
// or holds a '%': unbracketed in BaseURL it is malformed, and saved to
// the config it is a host yxc and ynca cannot use, so the device is
// refused.
func TestNewDevice_RejectsIPv6Host(t *testing.T) {
	desc := descDevice{FriendlyName: "RX-V583 FBE863", Manufacturer: yamahaManufacturer, ModelName: "RX-V583", UDN: sampleYamahaUDN}
	for _, loc := range []string{
		"http://[::1]:49154/MediaRenderer/desc.xml",
		"http://[fe80::1%25en0]:49154/MediaRenderer/desc.xml",
		"http://rx%25v583:49154/MediaRenderer/desc.xml",
	} {
		if dev, err := newDevice(loc, desc); err == nil {
			t.Errorf("newDevice(%q) = %+v, want an error", loc, dev)
		}
	}

	dev, err := newDevice("http://192.168.1.116:49154/MediaRenderer/desc.xml", desc)
	want := Device{
		Name:    "RX-V583 FBE863",
		Host:    "192.168.1.116",
		Model:   "RX-V583",
		BaseURL: "http://192.168.1.116/YamahaExtendedControl/v1/",
		UDN:     sampleYamahaUDN,
	}
	if err != nil || dev != want {
		t.Errorf("newDevice(IPv4) = %+v, %v; want %+v", dev, err, want)
	}
}

// withStubbedSearch installs a fake searchLocationsFn for the duration
// of a test. It also stubs the subnet probe to find nothing, so a test
// whose SSDP stub comes back empty never probes the real network; tests
// that exercise the probe install their own with stubSweep.
func withStubbedSearch(t *testing.T, fn func(ctx context.Context, st string, timeout time.Duration) ([]string, error)) {
	t.Helper()
	prev := searchLocationsFn
	searchLocationsFn = fn
	t.Cleanup(func() { searchLocationsFn = prev })
	stubSweep(t, nil)
}

// sweepStub records the subnet probes a test triggered.
type sweepStub struct {
	calls int
	near  []netip.Addr // the near argument of each call
}

// stubSweep replaces the subnet probe with one that finds devs.
func stubSweep(t *testing.T, devs []Device) *sweepStub {
	t.Helper()
	s := &sweepStub{}
	prev := sweepFn
	sweepFn = func(_ context.Context, _ time.Duration, near netip.Addr) ([]Device, error) {
		s.calls++
		s.near = append(s.near, near)
		return devs, nil
	}
	t.Cleanup(func() { sweepFn = prev })
	return s
}

// startDescServer serves the supplied XML body on /desc.xml and returns
// the full URL. Verifies the test path in fetchAndFilter against a real
// HTTP server without touching the SSDP machinery.
func startDescServer(t *testing.T, body string) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/desc.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/desc.xml"
}

func TestSearch_FiltersAndDedups(t *testing.T) {
	yamahaSrv, yamahaLoc := startDescServer(t, sampleYamahaXML)
	_, otherLoc := startDescServer(t, sampleNonYamahaXML)

	// Same Yamaha device responds twice (e.g. multiple SSDP echoes from
	// different interfaces) — dedup by UDN must collapse to one entry.
	withStubbedSearch(t, func(ctx context.Context, st string, timeout time.Duration) ([]string, error) {
		return []string{yamahaLoc, otherLoc, yamahaLoc}, nil
	})

	devs, err := Search(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 yamaha device, got %d (%+v)", len(devs), devs)
	}
	d := devs[0]
	if d.Name != "RX-V583 FBE863" {
		t.Errorf("Name: got %q", d.Name)
	}
	if d.Model != "RX-V583" {
		t.Errorf("Model: got %q", d.Model)
	}
	if d.UDN != "uuid:9ab0c000-f668-11de-9976-00a0defbe863" {
		t.Errorf("UDN: got %q", d.UDN)
	}
	// Host should be the bare hostname/IP from the test server URL,
	// without scheme or port — that's what the config persists.
	wantHost, err := hostFromLocation(yamahaSrv.URL)
	if err != nil {
		t.Fatalf("hostFromLocation: %v", err)
	}
	if d.Host != wantHost {
		t.Errorf("Host: got %q want %q", d.Host, wantHost)
	}
	wantBase := "http://" + wantHost + "/YamahaExtendedControl/v1/"
	if d.BaseURL != wantBase {
		t.Errorf("BaseURL: got %q want %q", d.BaseURL, wantBase)
	}
}

func TestLookupByUDN_ReturnsMatch(t *testing.T) {
	_, yamahaLoc := startDescServer(t, sampleYamahaXML)

	withStubbedSearch(t, func(ctx context.Context, st string, timeout time.Duration) ([]string, error) {
		return []string{yamahaLoc}, nil
	})

	const wantUDN = "uuid:9ab0c000-f668-11de-9976-00a0defbe863"
	dev, err := LookupByUDN(context.Background(), wantUDN, "", 2*time.Second)
	if err != nil {
		t.Fatalf("LookupByUDN: %v", err)
	}
	if dev.UDN != wantUDN {
		t.Errorf("UDN: got %q want %q", dev.UDN, wantUDN)
	}
	if dev.Model != "RX-V583" {
		t.Errorf("Model: got %q", dev.Model)
	}
}

func TestLookupByUDN_NoMatch(t *testing.T) {
	_, yamahaLoc := startDescServer(t, sampleYamahaXML)

	withStubbedSearch(t, func(ctx context.Context, st string, timeout time.Duration) ([]string, error) {
		return []string{yamahaLoc}, nil
	})

	_, err := LookupByUDN(context.Background(), "uuid:nonexistent", "", 2*time.Second)
	if err == nil {
		t.Fatal("expected error for unknown UDN, got nil")
	}
}

// stubSearchFanout swaps the bind-address list and the per-interface
// search for the duration of a test, so the fan-out can be exercised
// without real multicast traffic.
func stubSearchFanout(t *testing.T, addrs []bindAddr, search func(ctx context.Context, b bindAddr, st string, wait time.Duration) ([]string, error)) {
	t.Helper()
	prevSearch, prevAddrs := searchIfaceFn, searchAddrsFn
	t.Cleanup(func() {
		searchIfaceFn = prevSearch
		searchAddrsFn = prevAddrs
	})
	searchAddrsFn = func() ([]bindAddr, error) { return addrs, nil }
	searchIfaceFn = search
}

func bindIP(ip string) bindAddr { return bindAddr{ip: net.ParseIP(ip).To4()} }

func TestDefaultSearchLocations_BindsConcreteIPv4Addrs(t *testing.T) {
	// The fan-out calls searchIfaceFn from a goroutine, so guard the
	// recording even though this case binds a single interface.
	var mu sync.Mutex
	var gotBinds []string
	stubSearchFanout(t, []bindAddr{bindIP("192.168.1.100")}, func(_ context.Context, b bindAddr, st string, wait time.Duration) ([]string, error) {
		mu.Lock()
		gotBinds = append(gotBinds, b.ip.String())
		mu.Unlock()
		if st != mediaRendererST {
			t.Errorf("search type: got %q want %q", st, mediaRendererST)
		}
		if wait != 3*time.Second {
			t.Errorf("wait: got %v want 3s", wait)
		}
		return []string{"http://192.168.1.116:49154/MediaRenderer/desc.xml"}, nil
	})

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("defaultSearchLocations: %v", err)
	}
	if !reflect.DeepEqual(gotBinds, []string{"192.168.1.100"}) {
		t.Fatalf("bind addrs: got %v want concrete interface bind", gotBinds)
	}
	if len(locs) != 1 || locs[0] != "http://192.168.1.116:49154/MediaRenderer/desc.xml" {
		t.Fatalf("locations: got %+v", locs)
	}
}

// A sub-second timeout still gives responders a full second to answer.
func TestDefaultSearchLocations_RoundsWaitUpToOneSecond(t *testing.T) {
	stubSearchFanout(t, []bindAddr{bindIP("192.168.1.100")}, func(_ context.Context, _ bindAddr, _ string, wait time.Duration) ([]string, error) {
		if wait != time.Second {
			t.Errorf("wait: got %v want 1s", wait)
		}
		return nil, nil
	})
	if _, err := defaultSearchLocations(context.Background(), mediaRendererST, 200*time.Millisecond); err != nil {
		t.Fatalf("defaultSearchLocations: %v", err)
	}
}

func TestDefaultSearchLocations_DedupsAcrossBoundInterfaces(t *testing.T) {
	const (
		locA = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
		locB = "http://10.0.0.5:49154/MediaRenderer/desc.xml"
	)
	// Each interface reports a distinct device, and the second also re-sees
	// locA. That duplicate spans two interfaces, so it exercises the
	// cross-interface dedup, and the recording proves every interface was
	// actually scanned.
	var mu sync.Mutex
	var scanned []string
	stubSearchFanout(t, []bindAddr{bindIP("192.168.1.100"), bindIP("10.0.0.2")}, func(_ context.Context, b bindAddr, _ string, _ time.Duration) ([]string, error) {
		mu.Lock()
		scanned = append(scanned, b.ip.String())
		mu.Unlock()
		switch b.ip.String() {
		case "192.168.1.100":
			return []string{locA}, nil
		case "10.0.0.2":
			return []string{locB, locA}, nil
		default:
			t.Errorf("unexpected bind %v", b.ip)
			return nil, nil
		}
	})

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("defaultSearchLocations: %v", err)
	}
	sort.Strings(scanned)
	if !reflect.DeepEqual(scanned, []string{"10.0.0.2", "192.168.1.100"}) {
		t.Fatalf("expected both interfaces scanned, got %v", scanned)
	}
	// defaultSearchLocations sorts its result; locB sorts before locA.
	if !reflect.DeepEqual(locs, []string{locB, locA}) {
		t.Fatalf("expected deduped union [%q %q], got %+v", locB, locA, locs)
	}
}

func TestDefaultSearchLocations_AggregatesErrorWhenAllInterfacesFail(t *testing.T) {
	wantErr := errors.New("boom")
	stubSearchFanout(t, []bindAddr{bindIP("192.168.1.100"), bindIP("10.0.0.2")}, func(context.Context, bindAddr, string, time.Duration) ([]string, error) {
		return nil, wantErr
	})

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err == nil {
		t.Fatalf("expected error when all interfaces fail, got locs=%+v", locs)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error should wrap the ssdp failure: got %v", err)
	}
	if !strings.Contains(err.Error(), "ssdp search") {
		t.Fatalf("error should carry the ssdp search prefix: got %v", err)
	}
}

func TestDefaultSearchLocations_ReturnsLocationWhenSomeInterfacesError(t *testing.T) {
	const loc = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
	stubSearchFanout(t, []bindAddr{bindIP("192.168.1.100"), bindIP("10.0.0.2")}, func(_ context.Context, b bindAddr, _ string, _ time.Duration) ([]string, error) {
		if b.ip.String() == "10.0.0.2" {
			return nil, errors.New("interface down")
		}
		return []string{loc}, nil
	})

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("a partial failure must not error when another interface succeeds: %v", err)
	}
	if len(locs) != 1 || locs[0] != loc {
		t.Fatalf("expected the surviving interface's location, got %+v", locs)
	}
}

// An interface that failed mid-scan (a read error after replies came
// in) still contributes the locations it collected.
func TestDefaultSearchLocations_KeepsLocationsFromFailedInterface(t *testing.T) {
	const loc = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
	stubSearchFanout(t, []bindAddr{bindIP("192.168.1.100")}, func(context.Context, bindAddr, string, time.Duration) ([]string, error) {
		return []string{loc}, errors.New("read: connection reset")
	})

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("collected locations must win over the late error: %v", err)
	}
	if len(locs) != 1 || locs[0] != loc {
		t.Fatalf("locations: got %+v want [%s]", locs, loc)
	}
}

func TestDefaultSearchLocations_HonorsCancelledContext(t *testing.T) {
	prevSearch, prevAddrs := searchIfaceFn, searchAddrsFn
	t.Cleanup(func() {
		searchIfaceFn = prevSearch
		searchAddrsFn = prevAddrs
	})
	searchAddrsFn = func() ([]bindAddr, error) {
		t.Error("searchAddrsFn must not be called once the context is cancelled")
		return nil, nil
	}
	searchIfaceFn = func(context.Context, bindAddr, string, time.Duration) ([]string, error) {
		t.Error("searchIfaceFn must not be called once the context is cancelled")
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := defaultSearchLocations(ctx, mediaRendererST, 3*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ipNetAddr builds the *net.IPNet that (*net.Interface).Addrs reports for a
// concrete interface address, so the searchAddrs filter can be exercised
// without touching the host's real network configuration.
func ipNetAddr(ip string) *net.IPNet {
	return &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(24, 32)}
}

// fakeAddr is a net.Addr whose concrete type ipv4FromAddr does not handle,
// covering the default (return nil) branch.
type fakeAddr struct{ s string }

func (f fakeAddr) Network() string { return "fake" }
func (f fakeAddr) String() string  { return f.s }

// bindStrings renders bind addresses as "ip%iface" ("%" alone for the
// wildcard) so tests can compare them with reflect.DeepEqual.
func bindStrings(bs []bindAddr) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		s := ""
		if b.ip != nil {
			s = b.ip.String()
		}
		s += "%"
		if b.ifi != nil {
			s += b.ifi.Name
		}
		out = append(out, s)
	}
	return out
}

func TestSearchAddrs_FiltersToConcreteMulticastIPv4(t *testing.T) {
	prev := interfaceAddrsFn
	t.Cleanup(func() { interfaceAddrsFn = prev })

	up := net.FlagUp | net.FlagMulticast
	iface := func(name string, flags net.Flags) net.Interface { return net.Interface{Name: name, Flags: flags} }
	interfaceAddrsFn = func() ([]ifaceAddrs, error) {
		return []ifaceAddrs{
			{ifi: iface("lo0", up|net.FlagLoopback), addrs: []net.Addr{ipNetAddr("127.0.0.1")}}, // loopback iface: skip
			{ifi: iface("en1", net.FlagMulticast), addrs: []net.Addr{ipNetAddr("192.168.0.9")}}, // down: skip
			{ifi: iface("en2", net.FlagUp), addrs: []net.Addr{ipNetAddr("192.168.0.10")}},       // no multicast: skip
			{ifi: iface("en0", up), addrs: []net.Addr{
				ipNetAddr("192.168.1.100"), // kept
				ipNetAddr("fe80::1"),       // IPv6: skip
				ipNetAddr("127.0.0.1"),     // loopback IP: skip
			}},
			{ifi: iface("eth1", up), addrs: []net.Addr{ipNetAddr("10.0.0.2")}}, // kept
		}, nil
	}

	got, err := searchAddrs()
	if err != nil {
		t.Fatalf("searchAddrs: %v", err)
	}
	// Each address carries the interface that owns it, so the M-SEARCH
	// leaves through that interface only.
	want := []string{"192.168.1.100%en0", "10.0.0.2%eth1"}
	if !reflect.DeepEqual(bindStrings(got), want) {
		t.Fatalf("bind addrs: got %v want %v", bindStrings(got), want)
	}
}

func TestSearchAddrs_FallsBackToWildcardWhenNoneQualify(t *testing.T) {
	prev := interfaceAddrsFn
	t.Cleanup(func() { interfaceAddrsFn = prev })

	interfaceAddrsFn = func() ([]ifaceAddrs, error) {
		return []ifaceAddrs{
			{ifi: net.Interface{Name: "lo0", Flags: net.FlagUp | net.FlagMulticast | net.FlagLoopback}, addrs: []net.Addr{ipNetAddr("127.0.0.1")}},
		}, nil
	}

	got, err := searchAddrs()
	if err != nil {
		t.Fatalf("searchAddrs: %v", err)
	}
	if !reflect.DeepEqual(bindStrings(got), []string{"%"}) {
		t.Fatalf("expected the wildcard fallback, got %v", bindStrings(got))
	}
}

func TestSearchAddrs_WrapsInterfaceListError(t *testing.T) {
	prev := interfaceAddrsFn
	t.Cleanup(func() { interfaceAddrsFn = prev })

	interfaceAddrsFn = func() ([]ifaceAddrs, error) {
		return nil, errors.New("no interfaces")
	}

	if _, err := searchAddrs(); err == nil || !strings.Contains(err.Error(), "list network interfaces") {
		t.Fatalf("expected wrapped interface-list error, got %v", err)
	}
}

func TestIPv4FromAddr(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want string // "" means a nil result
	}{
		{"IPNet IPv4", ipNetAddr("192.168.1.5"), "192.168.1.5"},
		{"IPNet IPv6 only", ipNetAddr("fe80::1"), ""},
		{"IPAddr IPv4", &net.IPAddr{IP: net.ParseIP("10.0.0.7")}, "10.0.0.7"},
		{"unhandled net.Addr type", fakeAddr{s: "whatever"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := ipv4FromAddr(tt.addr)
			if tt.want == "" {
				if ip != nil {
					t.Fatalf("got %v want nil", ip)
				}
				return
			}
			if ip == nil || ip.String() != tt.want {
				t.Fatalf("got %v want %s", ip, tt.want)
			}
		})
	}
}

// TestSearch_DescriptionBodyCappedAtLimit guards the io.LimitReader
// around parseDescriptionXML in fetchOne. A malicious / misbehaving
// LAN peer that streams an unbounded UPnP description body must not
// hang the discovery scan or OOM the CLI.
//
// The server here streams `<friendlyName>aaaa...` forever. With the
// cap the decoder hits EOF after maxDescriptionBody bytes, the XML
// parse fails on the truncated element, fetchOne silently skips, and
// Search returns 0 devices in milliseconds. Without the cap, the
// decoder blocks until Client.Timeout fires (whatever we pass to
// Search) — so we assert on elapsed time relative to the timeout.
func TestSearch_DescriptionBodyCappedAtLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		// Open the XML, then stream into an unterminated element body
		// forever. The LimitReader cap should fire well before the
		// stream ends.
		if _, err := w.Write([]byte(`<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device><manufacturer>Yamaha Corporation</manufacturer><friendlyName>`)); err != nil {
			return
		}
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 64*1024)
		for i := range chunk {
			chunk[i] = 'a'
		}
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	loc := srv.URL + "/desc.xml"
	withStubbedSearch(t, func(_ context.Context, _ string, _ time.Duration) ([]string, error) {
		return []string{loc}, nil
	})

	// Generous timeout: with the cap the call returns in ms; without
	// the cap it would block until Client.Timeout (= this value).
	const searchTimeout = 5 * time.Second
	start := time.Now()
	devs, err := Search(context.Background(), searchTimeout)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Truncated XML is malformed, so fetchOne skips → 0 devices.
	if len(devs) != 0 {
		t.Errorf("expected 0 devices from malformed stream, got %d", len(devs))
	}
	// Soft cap: with the LimitReader fix, elapsed is well under 1s.
	// Without the cap it would be ~searchTimeout. 2s leaves headroom
	// for slow CI while still failing loudly on a regression.
	if elapsed > 2*time.Second {
		t.Errorf("Search took %v with body cap — expected sub-second short-circuit (cap=%d bytes, timeout=%v)",
			elapsed, maxDescriptionBody, searchTimeout)
	}
}

// Over Wi-Fi an RX-V583 stalls mid-body on some description fetches. One
// stall must not drop a receiver that answered SSDP.
func TestSearch_RetriesStalledDescriptionFetch(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		if n == 1 {
			// Headers and half the body, then nothing until the client
			// gives up, as the stalled receiver did.
			_, _ = w.Write([]byte(sampleYamahaXML[:len(sampleYamahaXML)/2]))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(sampleYamahaXML))
	}))
	t.Cleanup(srv.Close)
	loc := srv.URL + "/desc.xml"
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
		return []string{loc}, nil
	})
	var traced []string
	ctx := WithTrace(context.Background(), func(format string, args ...any) {
		traced = append(traced, fmt.Sprintf(format, args...))
	})

	devs, err := Search(ctx, time.Second)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected the receiver after one retry, got %d devices", len(devs))
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("description requests: got %d want 2", requests)
	}
	if !strings.Contains(strings.Join(traced, "\n"), "← retry "+loc) {
		t.Errorf("trace should record the retry:\n%s", strings.Join(traced, "\n"))
	}
}

// A non-Yamaha answer is final: retrying it can't change the vendor.
func TestSearch_DoesNotRetryNonYamaha(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		_, _ = w.Write([]byte(sampleNonYamahaXML))
	}))
	t.Cleanup(srv.Close)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
		return []string{srv.URL + "/desc.xml"}, nil
	})

	devs, err := Search(context.Background(), time.Second)
	if err != nil || len(devs) != 0 {
		t.Fatalf("Search: got %d devices, err %v; want none", len(devs), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Errorf("description requests: got %d want 1", requests)
	}
}

// Answers that would come back the same (a non-2xx status, a broken
// description) are not retried; only stalls are.
func TestSearch_DoesNotRetryDeterministicFailures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status 404": func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"bad xml":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<root><device>")) },
		"no UDN":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sampleNoUDNXML)) },
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				mu.Unlock()
				handler(w, r)
			}))
			t.Cleanup(srv.Close)
			withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
				return []string{srv.URL + "/desc.xml"}, nil
			})

			if devs, err := Search(context.Background(), time.Second); err != nil || len(devs) != 0 {
				t.Fatalf("Search: got %d devices, err %v; want none", len(devs), err)
			}
			mu.Lock()
			defer mu.Unlock()
			if requests != 1 {
				t.Errorf("description requests: got %d want 1", requests)
			}
		})
	}
}

// Trace lines carry strings from the network (Location headers, names
// from description XML). A newline or terminal control code in one must
// not forge extra trace lines or reach the terminal raw.
func TestSearch_TraceEscapesControlCharacters(t *testing.T) {
	forged := strings.Replace(sampleYamahaXML, "<friendlyName>RX-V583 FBE863</friendlyName>",
		"<friendlyName>RX&#10;← found fake\u009b31m</friendlyName>", 1)
	_, loc := startDescServer(t, forged)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
		return []string{loc}, nil
	})
	var traced []string
	ctx := WithTrace(context.Background(), func(format string, args ...any) {
		traced = append(traced, fmt.Sprintf(format, args...))
	})

	if _, err := Search(ctx, time.Second); err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, line := range traced {
		if strings.ContainsAny(line, "\n\u009b") {
			t.Errorf("trace line carries a raw control character: %q", line)
		}
	}
	if !strings.Contains(strings.Join(traced, "\n"), `RX\x0a← found fake\u009b31m`) {
		t.Errorf("trace should show the name with escapes:\n%s", strings.Join(traced, "\n"))
	}
}

// A receiver reached through two Locations is one device, and --debug
// says "found" once.
func TestSearch_TracesFoundOncePerDevice(t *testing.T) {
	_, locA := startDescServer(t, sampleYamahaXML)
	_, locB := startDescServer(t, sampleYamahaXML)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
		return []string{locA, locB}, nil
	})
	found := 0
	ctx := WithTrace(context.Background(), func(format string, args ...any) {
		if strings.HasPrefix(fmt.Sprintf(format, args...), "← found ") {
			found++
		}
	})

	devs, err := Search(ctx, time.Second)
	if err != nil || len(devs) != 1 {
		t.Fatalf("Search: got %d devices, err %v; want 1", len(devs), err)
	}
	if found != 1 {
		t.Errorf("'← found' traced %d times, want 1", found)
	}
}

// Only a fetch that got a connection and then stalled or lost it is
// worth retrying; a host that never accepted the connection is absent.
func TestMarkStall(t *testing.T) {
	timeout := os.ErrDeadlineExceeded
	for _, tt := range []struct {
		name      string
		err       error
		connected bool
		want      bool
	}{
		// http.Client drops the dial error when its Timeout fires, so a
		// connect timeout looks like any other timeout: only the
		// connected flag tells them apart.
		{"timeout before connecting", &url.Error{Op: "Get", URL: "http://h", Err: context.DeadlineExceeded}, false, false},
		{"dial refused", &url.Error{Op: "Get", URL: "http://h", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, false, false},
		{"timeout awaiting headers", &url.Error{Op: "Get", URL: "http://h", Err: context.DeadlineExceeded}, true, true},
		{"body read deadline", fmt.Errorf("http://h: parse description xml: %w", timeout), true, true},
		{"body cut short", fmt.Errorf("http://h: parse description xml: %w", io.ErrUnexpectedEOF), true, true},
		{"non-2xx", errors.New("GET http://h: unexpected status 404 Not Found"), true, false},
		{"other vendor", fmt.Errorf("http://h: %w (manufacturer %q)", ErrNotYamaha, "Sonos"), true, false},
	} {
		err := markStall(tt.err, tt.connected)
		if got := transientFetchErr(err); got != tt.want {
			t.Errorf("%s: transient = %v, want %v", tt.name, got, tt.want)
		}
		if err.Error() != tt.err.Error() {
			t.Errorf("%s: marking changed the message to %q", tt.name, err)
		}
	}
}

// A host that never completes the TCP handshake is absent or the address
// is wrong: one attempt is enough. The client's own Timeout fires here,
// which hides the dial error from the returned error.
func TestFetchOne_ConnectTimeoutIsNotTransient(t *testing.T) {
	client := &http.Client{
		Timeout: 200 * time.Millisecond,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	}
	_, err := fetchOne(context.Background(), client, "http://192.0.2.1:49154/MediaRenderer/desc.xml")
	if err == nil {
		t.Fatal("expected an error")
	}
	if transientFetchErr(err) {
		t.Errorf("a connect timeout must not be retried: %v", err)
	}
}

// A receiver that accepts the connection but stalls before sending
// headers is retried like one that stalls mid-body.
func TestSearch_RetriesHeaderStall(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			<-r.Context().Done() // no headers until the client gives up
			return
		}
		_, _ = w.Write([]byte(sampleYamahaXML))
	}))
	t.Cleanup(srv.Close)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
		return []string{srv.URL + "/desc.xml"}, nil
	})

	devs, err := Search(context.Background(), time.Second)
	if err != nil || len(devs) != 1 {
		t.Fatalf("Search: got %+v, %v; want the receiver after one retry", devs, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("description requests: got %d want 2", requests)
	}
}

// Descriptions are fetched concurrently, so one slow responder doesn't
// hold up the rest, and the result keeps the input order the --add
// picker numbers devices by.
func TestSearch_FetchesDescriptionsConcurrentlyInOrder(t *testing.T) {
	slowDesc := func(body string, delay time.Duration) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(delay)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL + "/desc.xml"
	}
	const delay = 600 * time.Millisecond
	first := slowDesc(sampleYamahaXML, delay)
	second := slowDesc(strings.Replace(sampleYamahaXML, sampleYamahaUDN, "uuid:second", 1), delay/2)
	other := slowDesc(sampleNonYamahaXML, delay)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) {
		return []string{first, other, second}, nil
	})

	start := time.Now()
	devs, err := Search(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 2*delay {
		t.Errorf("Search took %v; three fetches of up to %v each should overlap", elapsed, delay)
	}
	if len(devs) != 2 || devs[0].UDN != sampleYamahaUDN || devs[1].UDN != "uuid:second" {
		t.Fatalf("devices: got %+v, want the first then the second Location's receiver", devs)
	}
}

func TestSearch_SkipsBadLocations(t *testing.T) {
	_, yamahaLoc := startDescServer(t, sampleYamahaXML)

	// Mix in an unreachable location and a malformed one. Both should
	// be silently skipped while the good one still surfaces.
	withStubbedSearch(t, func(ctx context.Context, st string, timeout time.Duration) ([]string, error) {
		return []string{
			"http://127.0.0.1:1/never-listens", // closed port
			"::not a url::",
			yamahaLoc,
		}, nil
	})

	devs, err := Search(context.Background(), 1*time.Second)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devs))
	}
}
