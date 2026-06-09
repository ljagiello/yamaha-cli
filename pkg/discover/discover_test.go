package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var gotAddr string
	ssdpSearchFn = func(st string, waitSec int, localAddr string, opts ...ssdp.Option) ([]ssdp.Service, error) {
		gotAddr = localAddr
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
	if gotAddr != "192.168.1.100:0" {
		t.Fatalf("ssdp localAddr: got %q want concrete interface bind", gotAddr)
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

	searchAddrsFn = func() ([]string, error) {
		return []string{"192.168.1.100:0", "10.0.0.2:0"}, nil
	}
	ssdpSearchFn = func(_ string, _ int, _ string, _ ...ssdp.Option) ([]ssdp.Service, error) {
		return []ssdp.Service{
			{Location: "http://192.168.1.116:49154/MediaRenderer/desc.xml"},
			{Location: "http://192.168.1.116:49154/MediaRenderer/desc.xml"},
		}, nil
	}

	locs, err := defaultSearchLocations(context.Background(), mediaRendererST, 3*time.Second)
	if err != nil {
		t.Fatalf("defaultSearchLocations: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("expected deduped location, got %+v", locs)
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
