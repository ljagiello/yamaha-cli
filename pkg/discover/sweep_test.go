package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	sweptRXV583 = Device{Name: "RX-V583 FBE863", Host: "192.168.1.116", Model: "RX-V583", UDN: sampleYamahaUDN}
	otherRXV583 = Device{Name: "RX-V583 Kitchen", Host: "192.168.1.117", Model: "RX-V583", UDN: "uuid:other"}
)

// stubInterfaces makes sweepTargets see ifaces and a default route
// through defaultRoute (none when it is "").
func stubInterfaces(t *testing.T, defaultRoute string, ifaces ...ifaceAddrs) {
	t.Helper()
	prevIfaces, prevRoute := interfaceAddrsFn, defaultRouteAddrFn
	t.Cleanup(func() { interfaceAddrsFn, defaultRouteAddrFn = prevIfaces, prevRoute })
	interfaceAddrsFn = func() ([]ifaceAddrs, error) { return ifaces, nil }
	defaultRouteAddrFn = func() (netip.Addr, bool) {
		if defaultRoute == "" {
			return netip.Addr{}, false
		}
		return netip.MustParseAddr(defaultRoute), true
	}
}

// iface builds an interface with the given flags and CIDR addresses.
func iface(t *testing.T, name string, flags net.Flags, cidrs ...string) ifaceAddrs {
	t.Helper()
	ia := ifaceAddrs{ifi: net.Interface{Name: name, Flags: flags}}
	for _, c := range cidrs {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", c, err)
		}
		n.IP = ip
		ia.addrs = append(ia.addrs, n)
	}
	return ia
}

const up = net.FlagUp | net.FlagMulticast

func TestSweepTargets(t *testing.T) {
	stubInterfaces(t, "",
		iface(t, "en0", up, "192.168.1.182/24", "192.168.1.183/24", "fe80::1/64"), // one /24, two own addresses
		iface(t, "eth1", up, "10.20.30.40/16"),                                    // wider than /24: only the /24 around us
		iface(t, "p2p", up, "192.168.7.1/30"),                                     // narrower: probed whole
		iface(t, "tun", up, "192.168.9.1/31", "192.168.9.5/32"),                   // no other hosts worth probing
		iface(t, "cgnat", up, "100.64.1.2/24"),                                    // not private (Tailscale, carrier NAT)
		iface(t, "pub", up, "203.0.113.5/24"),                                     // public: never probed
		iface(t, "lo0", up|net.FlagLoopback, "127.0.0.1/8"),
		iface(t, "down", net.FlagMulticast, "192.168.50.2/24"),
		iface(t, "nomcast", net.FlagUp, "192.168.60.2/24"),
		ifaceAddrs{ifi: net.Interface{Name: "nomask", Flags: up}, addrs: []net.Addr{&net.IPAddr{IP: net.ParseIP("192.168.70.2")}}},
	)

	targets, prefixes, err := sweepTargets(netip.Addr{})
	if err != nil {
		t.Fatalf("sweepTargets: %v", err)
	}
	// With no default route, subnets come in address order.
	wantPrefixes := []netip.Prefix{
		netip.MustParsePrefix("10.20.30.0/24"),
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("192.168.7.0/30"),
	}
	if !reflect.DeepEqual(prefixes, wantPrefixes) {
		t.Errorf("prefixes: got %v want %v", prefixes, wantPrefixes)
	}
	// 254 hosts in each /24 minus our own addresses, plus the /30's one peer.
	if want := (254 - 1) + (254 - 2) + 1; len(targets) != want {
		t.Errorf("target count: got %d want %d", len(targets), want)
	}
	have := make(map[netip.Addr]bool, len(targets))
	for _, a := range targets {
		if have[a] {
			t.Errorf("duplicate target %v", a)
		}
		have[a] = true
	}
	for _, a := range []string{
		"192.168.1.182", "192.168.1.183", "10.20.30.40", "192.168.7.1", // ourselves
		"192.168.1.0", "192.168.1.255", "10.20.30.0", "10.20.30.255", "192.168.7.0", "192.168.7.3", // network, broadcast
		"10.20.31.1", "192.168.9.0", "100.64.1.1", "203.0.113.1", "127.0.0.2", "192.168.50.1", "192.168.60.1", "192.168.70.1",
	} {
		if have[netip.MustParseAddr(a)] {
			t.Errorf("%s must not be probed", a)
		}
	}
	for _, a := range []string{"192.168.1.1", "192.168.1.116", "192.168.1.254", "10.20.30.1", "10.20.30.254", "192.168.7.2"} {
		if !have[netip.MustParseAddr(a)] {
			t.Errorf("%s should be probed", a)
		}
	}
}

// A dev box with Docker bridges sorts 172.x before 192.168.x; the
// address cap must not cut the LAN the default route uses.
func TestSweepTargets_DefaultRouteSubnetFirstAndKeptUnderCap(t *testing.T) {
	stubInterfaces(t, "192.168.1.182",
		iface(t, "docker0", up, "172.17.0.1/16"),
		iface(t, "br-a", up, "172.18.0.1/16"),
		iface(t, "br-b", up, "172.19.0.1/16"),
		iface(t, "br-c", up, "172.20.0.1/16"),
		iface(t, "en0", up, "192.168.1.182/24"),
	)

	targets, prefixes, err := sweepTargets(netip.Addr{})
	if err != nil {
		t.Fatalf("sweepTargets: %v", err)
	}
	if home := netip.MustParsePrefix("192.168.1.0/24"); len(prefixes) == 0 || prefixes[0] != home {
		t.Fatalf("prefixes: got %v, want %v first", prefixes, home)
	}
	// sweep keeps targets[:sweepMaxTargets]; 4 bridges + home exceed it.
	if len(targets) <= sweepMaxTargets {
		t.Fatalf("scenario should exceed the %d cap, has %d targets", sweepMaxTargets, len(targets))
	}
	targets = targets[:sweepMaxTargets]
	var home []netip.Addr
	for i := 1; i <= 254; i++ {
		if i != 182 {
			home = append(home, netip.AddrFrom4([4]byte{192, 168, 1, byte(i)}))
		}
	}
	if len(targets) < len(home) || !reflect.DeepEqual(targets[:len(home)], home) {
		t.Fatalf("the home /24 must lead the targets and survive the cap; first targets: %v", targets[:min(5, len(targets))])
	}
}

// DHCP rediscovery probes only the /24 around the receiver's last
// address, and only when that address is on one of our subnets.
func TestSweepTargets_Near(t *testing.T) {
	stubInterfaces(t, "192.168.1.182",
		iface(t, "en0", up, "192.168.1.182/24"),
		iface(t, "eth1", up, "10.20.30.40/16"),
	)
	for _, tt := range []struct {
		near string
		want []netip.Prefix
	}{
		{"192.168.1.116", []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}},
		// On a /16 the receiver may sit in another /24 than ours.
		{"10.20.99.5", []netip.Prefix{netip.MustParsePrefix("10.20.99.0/24")}},
		{"172.30.0.5", nil},  // not on our subnets: we're elsewhere
		{"203.0.113.9", nil}, // public
	} {
		targets, prefixes, err := sweepTargets(netip.MustParseAddr(tt.near))
		if err != nil {
			t.Fatalf("near %s: %v", tt.near, err)
		}
		if !reflect.DeepEqual(prefixes, tt.want) {
			t.Errorf("near %s: prefixes %v, want %v", tt.near, prefixes, tt.want)
		}
		if len(tt.want) == 0 && len(targets) != 0 {
			t.Errorf("near %s: %d targets, want none", tt.near, len(targets))
		}
	}
}

func TestDefaultRouteAddr_NeverFails(t *testing.T) {
	// The result depends on the host; it must just not panic or block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = defaultRouteAddr()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("defaultRouteAddr blocked")
	}
}

// stubSweepTargets points the probe at addrs on port.
func stubSweepTargets(t *testing.T, port string, addrs ...string) {
	t.Helper()
	prevTargets, prevPort := sweepTargetsFn, sweepPort
	t.Cleanup(func() { sweepTargetsFn, sweepPort = prevTargets, prevPort })
	var targets []netip.Addr
	var prefixes []netip.Prefix
	for _, a := range addrs {
		addr := netip.MustParseAddr(a)
		targets = append(targets, addr)
		prefixes = append(prefixes, netip.PrefixFrom(addr, 32))
	}
	sweepTargetsFn = func(netip.Addr) ([]netip.Addr, []netip.Prefix, error) { return targets, prefixes, nil }
	sweepPort = port
}

func TestSweep_FindsReceiverWithOpenPort(t *testing.T) {
	srv := serveDescription(t, sampleYamahaXML)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split httptest addr: %v", err)
	}
	stubSweepTargets(t, port, "127.0.0.1")
	var lines []string
	ctx := WithTrace(context.Background(), func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	devs, err := sweep(ctx, time.Second, netip.Addr{})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(devs) != 1 || devs[0].UDN != sampleYamahaUDN || devs[0].Host != "127.0.0.1" {
		t.Fatalf("devices: got %+v", devs)
	}
	out := strings.Join(lines, "\n")
	for _, want := range []string{
		"→ probe tcp/" + port + " on 127.0.0.1/32 (1 address)",
		"← probe: tcp/" + port + " open on 1 of 1",
		"→ GET http://127.0.0.1:" + port + describeFallbackPath,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trace missing %q:\n%s", want, out)
		}
	}
}

// --debug must not claim a subnet the address cap cut was probed.
func TestCapTargets_NamesOnlyReachedSubnets(t *testing.T) {
	a := netip.MustParsePrefix("192.168.1.0/30")
	b := netip.MustParsePrefix("192.168.2.0/30")
	c := netip.MustParsePrefix("192.168.3.0/30")
	targets := []netip.Addr{
		netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("192.168.1.2"),
		netip.MustParseAddr("192.168.2.1"), netip.MustParseAddr("192.168.2.2"),
		netip.MustParseAddr("192.168.3.1"), netip.MustParseAddr("192.168.3.2"),
	}

	got, prefixes := capTargets(targets, []netip.Prefix{a, b, c}, 3)
	if !reflect.DeepEqual(got, targets[:3]) {
		t.Errorf("targets: got %v want %v", got, targets[:3])
	}
	// b is reached by one address, c not at all.
	if want := []netip.Prefix{a, b}; !reflect.DeepEqual(prefixes, want) {
		t.Errorf("prefixes: got %v want %v", prefixes, want)
	}
}

func TestSweep_ClosedPortFindsNothing(t *testing.T) {
	stubSweepTargets(t, closedFallbackPort, "127.0.0.1")
	devs, err := sweep(context.Background(), time.Second, netip.Addr{})
	if err != nil || len(devs) != 0 {
		t.Fatalf("sweep: got %+v, %v; want nothing", devs, err)
	}
}

func TestSweep_HonorsCancelledContext(t *testing.T) {
	stubSweepTargets(t, closedFallbackPort, "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sweep(ctx, time.Second, netip.Addr{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// The probe is the fallback for a LAN where no SSDP reply reaches us,
// typically because a stateful host firewall drops them.
func TestSearch_ProbesSubnetsWhenSSDPFindsNothing(t *testing.T) {
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) { return nil, nil })
	probe := stubSweep(t, []Device{sweptRXV583})

	devs, err := Search(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if probe.calls != 1 || len(devs) != 1 || devs[0].UDN != sampleYamahaUDN {
		t.Fatalf("got %+v after %d probes; want the swept receiver after 1", devs, probe.calls)
	}
	if probe.near[0].IsValid() {
		t.Errorf("Search must probe all subnets, got near=%v", probe.near[0])
	}
}

func TestSearch_NoProbeWhenSSDPFindsReceiver(t *testing.T) {
	_, loc := startDescServer(t, sampleYamahaXML)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) { return []string{loc}, nil })
	probe := stubSweep(t, []Device{otherRXV583})

	devs, err := Search(context.Background(), time.Second)
	if err != nil || len(devs) != 1 {
		t.Fatalf("Search: got %+v, %v", devs, err)
	}
	if probe.calls != 0 {
		t.Errorf("probe ran %d times although SSDP found a receiver", probe.calls)
	}
}

// With no usable multicast interface SSDP fails outright; the probe may
// still find the receiver, and only an empty probe surfaces the error.
func TestSearch_ProbeAfterSSDPError(t *testing.T) {
	ssdpErr := errors.New("ssdp search: no interfaces")
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) { return nil, ssdpErr })

	stubSweep(t, []Device{sweptRXV583})
	if devs, err := Search(context.Background(), time.Second); err != nil || len(devs) != 1 {
		t.Fatalf("probe hit: got %+v, %v; want the swept receiver", devs, err)
	}

	stubSweep(t, nil)
	if _, err := Search(context.Background(), time.Second); !errors.Is(err, ssdpErr) {
		t.Fatalf("probe miss: got %v, want the SSDP error", err)
	}
}

// DHCP rediscovery behind a firewall: SSDP sees nothing (or only other
// receivers), and the probe of the receiver's last subnet finds the
// saved UDN at its new address.
func TestLookupByUDN_ProbesLastSubnetWhenSSDPMissesTheUDN(t *testing.T) {
	_, loc := startDescServer(t, strings.Replace(sampleYamahaXML, sampleYamahaUDN, otherRXV583.UDN, 1))
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) { return []string{loc}, nil })
	probe := stubSweep(t, []Device{sweptRXV583})

	dev, err := LookupByUDN(context.Background(), sampleYamahaUDN, "192.168.1.50", time.Second)
	if err != nil {
		t.Fatalf("LookupByUDN: %v", err)
	}
	if probe.calls != 1 || dev.Host != sweptRXV583.Host {
		t.Fatalf("got %+v after %d probes; want the swept receiver", dev, probe.calls)
	}
	if want := netip.MustParseAddr("192.168.1.50"); probe.near[0] != want {
		t.Errorf("probe near: got %v want %v (the last saved address)", probe.near[0], want)
	}
}

func TestLookupByUDN_NoProbeWhenSSDPFindsTheUDN(t *testing.T) {
	_, loc := startDescServer(t, sampleYamahaXML)
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) { return []string{loc}, nil })
	probe := stubSweep(t, []Device{sweptRXV583})

	if _, err := LookupByUDN(context.Background(), sampleYamahaUDN, "192.168.1.50", time.Second); err != nil {
		t.Fatalf("LookupByUDN: %v", err)
	}
	if probe.calls != 0 {
		t.Errorf("probe ran %d times although SSDP found the UDN", probe.calls)
	}
}

// A receiver saved by hostname gives no subnet to probe, and probing
// every subnet on each failed command is too broad: SSDP only.
func TestLookupByUDN_SavedHostnameIsSSDPOnly(t *testing.T) {
	withStubbedSearch(t, func(context.Context, string, time.Duration) ([]string, error) { return nil, nil })
	probe := stubSweep(t, []Device{sweptRXV583})

	if _, err := LookupByUDN(context.Background(), sampleYamahaUDN, "ampli.home", time.Second); err == nil {
		t.Fatal("expected not found")
	}
	if probe.calls != 0 {
		t.Errorf("probe ran %d times for a saved hostname", probe.calls)
	}
}
