package discover

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	ssdp "github.com/koron/go-ssdp"
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

// withStubbedSearch installs a fake searchLocationsFn for the duration
// of a test, returning a cleanup that restores the previous value.
func withStubbedSearch(t *testing.T, fn func(ctx context.Context, st string, timeout time.Duration) ([]string, error)) {
	t.Helper()
	prev := searchLocationsFn
	searchLocationsFn = fn
	t.Cleanup(func() { searchLocationsFn = prev })
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
	dev, err := LookupByUDN(context.Background(), wantUDN, 2*time.Second)
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

	_, err := LookupByUDN(context.Background(), "uuid:nonexistent", 2*time.Second)
	if err == nil {
		t.Fatal("expected error for unknown UDN, got nil")
	}
}

func TestDefaultSearchLocations_BindsConcreteIPv4Addrs(t *testing.T) {
	prevSearch := ssdpSearchFn
	prevAddrs := searchAddrsFn
	t.Cleanup(func() {
		ssdpSearchFn = prevSearch
		searchAddrsFn = prevAddrs
	})

	searchAddrsFn = func() ([]string, error) {
		return []string{"192.168.1.100:0"}, nil
	}
	// The fan-out calls ssdpSearchFn from a goroutine, so guard the
	// recording even though this case binds a single interface.
	var mu sync.Mutex
	var gotAddrs []string
	ssdpSearchFn = func(st string, waitSec int, localAddr string, opts ...ssdp.Option) ([]ssdp.Service, error) {
		mu.Lock()
		gotAddrs = append(gotAddrs, localAddr)
		mu.Unlock()
		if st != mediaRendererST {
			t.Errorf("search type: got %q want %q", st, mediaRendererST)
		}
		if waitSec != 3 {
			t.Errorf("waitSec: got %d want 3", waitSec)
		}
		return []ssdp.Service{{Location: "http://192.168.1.116:49154/MediaRenderer/desc.xml"}}, nil
	}

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("defaultSearchLocations: %v", err)
	}
	if !reflect.DeepEqual(gotAddrs, []string{"192.168.1.100:0"}) {
		t.Fatalf("ssdp localAddr: got %v want concrete interface bind", gotAddrs)
	}
	if len(locs) != 1 || locs[0] != "http://192.168.1.116:49154/MediaRenderer/desc.xml" {
		t.Fatalf("locations: got %+v", locs)
	}
}

func TestDefaultSearchLocations_DedupsAcrossBoundInterfaces(t *testing.T) {
	prevSearch := ssdpSearchFn
	prevAddrs := searchAddrsFn
	t.Cleanup(func() {
		ssdpSearchFn = prevSearch
		searchAddrsFn = prevAddrs
	})

	const (
		locA = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
		locB = "http://10.0.0.5:49154/MediaRenderer/desc.xml"
	)
	searchAddrsFn = func() ([]string, error) {
		return []string{"192.168.1.100:0", "10.0.0.2:0"}, nil
	}
	// Each interface reports a distinct device, and the second also re-sees
	// locA. That duplicate spans two interfaces, so it exercises the
	// cross-interface dedup — not just per-reply dedup — and the recording
	// proves every interface was actually scanned.
	var mu sync.Mutex
	var scanned []string
	ssdpSearchFn = func(_ string, _ int, localAddr string, _ ...ssdp.Option) ([]ssdp.Service, error) {
		mu.Lock()
		scanned = append(scanned, localAddr)
		mu.Unlock()
		switch localAddr {
		case "192.168.1.100:0":
			return []ssdp.Service{{Location: locA}, {Location: locA}}, nil
		case "10.0.0.2:0":
			return []ssdp.Service{{Location: locB}, {Location: locA}}, nil
		default:
			t.Errorf("unexpected localAddr %q", localAddr)
			return nil, nil
		}
	}

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("defaultSearchLocations: %v", err)
	}
	sort.Strings(scanned)
	if !reflect.DeepEqual(scanned, []string{"10.0.0.2:0", "192.168.1.100:0"}) {
		t.Fatalf("expected both interfaces scanned, got %v", scanned)
	}
	// defaultSearchLocations sorts its result; locB sorts before locA.
	if !reflect.DeepEqual(locs, []string{locB, locA}) {
		t.Fatalf("expected deduped union [%q %q], got %+v", locB, locA, locs)
	}
}

func TestDefaultSearchLocations_AggregatesErrorWhenAllInterfacesFail(t *testing.T) {
	prevSearch := ssdpSearchFn
	prevAddrs := searchAddrsFn
	t.Cleanup(func() {
		ssdpSearchFn = prevSearch
		searchAddrsFn = prevAddrs
	})

	searchAddrsFn = func() ([]string, error) {
		return []string{"192.168.1.100:0", "10.0.0.2:0"}, nil
	}
	wantErr := errors.New("boom")
	ssdpSearchFn = func(_ string, _ int, _ string, _ ...ssdp.Option) ([]ssdp.Service, error) {
		return nil, wantErr
	}

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
	prevSearch := ssdpSearchFn
	prevAddrs := searchAddrsFn
	t.Cleanup(func() {
		ssdpSearchFn = prevSearch
		searchAddrsFn = prevAddrs
	})

	const loc = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
	searchAddrsFn = func() ([]string, error) {
		return []string{"192.168.1.100:0", "10.0.0.2:0"}, nil
	}
	ssdpSearchFn = func(_ string, _ int, localAddr string, _ ...ssdp.Option) ([]ssdp.Service, error) {
		if localAddr == "10.0.0.2:0" {
			return nil, errors.New("interface down")
		}
		return []ssdp.Service{{Location: loc}}, nil
	}

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("a partial failure must not error when another interface succeeds: %v", err)
	}
	if len(locs) != 1 || locs[0] != loc {
		t.Fatalf("expected the surviving interface's location, got %+v", locs)
	}
}

func TestDefaultSearchLocations_HonorsCancelledContext(t *testing.T) {
	prevSearch := ssdpSearchFn
	prevAddrs := searchAddrsFn
	t.Cleanup(func() {
		ssdpSearchFn = prevSearch
		searchAddrsFn = prevAddrs
	})

	searchAddrsFn = func() ([]string, error) {
		t.Error("searchAddrsFn must not be called once the context is cancelled")
		return nil, nil
	}
	ssdpSearchFn = func(_ string, _ int, _ string, _ ...ssdp.Option) ([]ssdp.Service, error) {
		t.Error("ssdpSearchFn must not be called once the context is cancelled")
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

func TestSearchAddrs_FiltersToConcreteMulticastIPv4(t *testing.T) {
	prev := interfaceAddrsFn
	t.Cleanup(func() { interfaceAddrsFn = prev })

	up := net.FlagUp | net.FlagMulticast
	interfaceAddrsFn = func() ([]ifaceAddrs, error) {
		return []ifaceAddrs{
			{flags: up | net.FlagLoopback, addrs: []net.Addr{ipNetAddr("127.0.0.1")}}, // loopback iface: skip
			{flags: net.FlagMulticast, addrs: []net.Addr{ipNetAddr("192.168.0.9")}},   // down: skip
			{flags: net.FlagUp, addrs: []net.Addr{ipNetAddr("192.168.0.10")}},         // no multicast: skip
			{flags: up, addrs: []net.Addr{
				ipNetAddr("192.168.1.100"), // kept
				ipNetAddr("fe80::1"),       // IPv6: skip
				ipNetAddr("127.0.0.1"),     // loopback IP: skip
			}},
			{flags: up, addrs: []net.Addr{ipNetAddr("10.0.0.2")}}, // kept
		}, nil
	}

	got, err := searchAddrs()
	if err != nil {
		t.Fatalf("searchAddrs: %v", err)
	}
	want := []string{"192.168.1.100:0", "10.0.0.2:0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bind addrs: got %v want %v", got, want)
	}
}

func TestSearchAddrs_FallsBackToWildcardWhenNoneQualify(t *testing.T) {
	prev := interfaceAddrsFn
	t.Cleanup(func() { interfaceAddrsFn = prev })

	interfaceAddrsFn = func() ([]ifaceAddrs, error) {
		return []ifaceAddrs{
			{flags: net.FlagUp | net.FlagMulticast | net.FlagLoopback, addrs: []net.Addr{ipNetAddr("127.0.0.1")}},
		}, nil
	}

	got, err := searchAddrs()
	if err != nil {
		t.Fatalf("searchAddrs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("expected wildcard fallback [\"\"], got %v", got)
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
