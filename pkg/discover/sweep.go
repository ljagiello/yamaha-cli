package discover

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// The subnet probe is the fallback when SSDP finds no receiver. A
// stateful host firewall drops SSDP replies, which come from the
// receiver's ephemeral port and so look unsolicited, but lets the
// answers to our own TCP connections back in. The probe dials the port
// Yamaha receivers serve their description on and hands each host that
// accepts the connection to fetchAndFilter.
const (
	// sweepDialTimeout bounds each TCP connect. Absent hosts never answer
	// and use all of it; it sits above the 1s TCP SYN and ARP retry
	// interval so one lost SYN or ARP reply doesn't hide the receiver.
	sweepDialTimeout = 2 * time.Second

	// sweepParallel caps concurrent connects. 256 covers a /24 in one
	// wave, so probing a /24 takes about one sweepDialTimeout.
	sweepParallel = 256

	// sweepMaxTargets caps the addresses probed: four /24s.
	sweepMaxTargets = 1024

	// sweepWidestBits is the widest subnet probed whole; on a wider one
	// only a /24 is probed.
	sweepWidestBits = 24
)

// Overridable for tests.
var (
	sweepFn            = sweep
	sweepTargetsFn     = sweepTargets
	defaultRouteAddrFn = defaultRouteAddr
	sweepPort          = yamahaDescPort
)

// sweep probes sweepPort on the addresses sweepTargetsFn(near) returns
// and returns the Yamaha receivers among the hosts that accepted the
// connection. timeout bounds each description fetch, as in Search.
func sweep(ctx context.Context, timeout time.Duration, near netip.Addr) ([]Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets, prefixes, err := sweepTargetsFn(near)
	if err != nil {
		tracef(ctx, "← probe: %v", err)
		return nil, nil
	}
	if len(targets) == 0 {
		if near.IsValid() {
			tracef(ctx, "→ probe: %s is not on this computer's private subnets; not probing", near)
		} else {
			tracef(ctx, "→ probe: no private IPv4 subnet to probe")
		}
		return nil, nil
	}
	if len(targets) > sweepMaxTargets {
		tracef(ctx, "→ probe: capped at %d of %d addresses", sweepMaxTargets, len(targets))
		targets, prefixes = capTargets(targets, prefixes, sweepMaxTargets)
	}
	tracef(ctx, "→ probe tcp/%s on %s (%d %s)", sweepPort, joinPrefixes(prefixes), len(targets), plural(len(targets), "address", "addresses"))

	var (
		mu   sync.Mutex
		open []netip.Addr
		wg   sync.WaitGroup
	)
	start := time.Now()
	sem := make(chan struct{}, sweepParallel)
	dialer := net.Dialer{Timeout: sweepDialTimeout}
dial:
	for _, addr := range targets {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dial
		}
		wg.Add(1)
		go func(addr netip.Addr) {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), sweepPort))
			if err != nil {
				return
			}
			_ = conn.Close()
			mu.Lock()
			open = append(open, addr)
			mu.Unlock()
		}(addr)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(open, netip.Addr.Compare)
	tracef(ctx, "← probe: tcp/%s open on %d of %d in %v", sweepPort, len(open), len(targets), time.Since(start).Round(time.Millisecond))

	locs := make([]string, 0, len(open))
	for _, addr := range open {
		locs = append(locs, "http://"+net.JoinHostPort(addr.String(), sweepPort)+describeFallbackPath)
	}
	return fetchAndFilter(ctx, locs, timeout)
}

// sweepTargets returns the addresses to probe and the subnets they come
// from, the subnet on this computer's default route first. Only private
// IPv4 subnets of the interfaces Search sends M-SEARCH from count; /31
// and /32 have no other hosts worth probing.
//
// Without near, every such subnet is probed: whole up to a /24, else
// the /24 around this computer's address. With near (the last address
// of a receiver being looked for), only the /24 around near is probed,
// and only when near lies inside one of those subnets. This computer's
// own addresses are never probed.
func sweepTargets(near netip.Addr) ([]netip.Addr, []netip.Prefix, error) {
	ifis, err := interfaceAddrsFn()
	if err != nil {
		return nil, nil, fmt.Errorf("list network interfaces: %w", err)
	}
	self := make(map[netip.Addr]bool)
	var prefixes []netip.Prefix
	add := func(p netip.Prefix) {
		if !slices.Contains(prefixes, p) {
			prefixes = append(prefixes, p)
		}
	}
	for _, ia := range ifis {
		if !searchableIface(ia.ifi.Flags) {
			continue
		}
		for _, a := range ia.addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue // no mask, so no subnet
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			ones, bits := ipNet.Mask.Size()
			if bits != 8*net.IPv4len || ones > 30 {
				continue
			}
			addr, _ := netip.AddrFromSlice(ip4)
			self[addr] = true
			bits24 := max(ones, sweepWidestBits)
			switch {
			case !near.IsValid():
				add(netip.PrefixFrom(addr, bits24).Masked())
			case netip.PrefixFrom(addr, ones).Masked().Contains(near):
				add(netip.PrefixFrom(near, bits24).Masked())
			}
		}
	}
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	// The default route's subnet is where a home receiver almost always
	// is; first, it can't fall to the sweepMaxTargets cap behind, say,
	// Docker bridges on 172.17-20.
	if route, ok := defaultRouteAddrFn(); ok {
		if i := slices.IndexFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(route) }); i > 0 {
			p := prefixes[i]
			prefixes = slices.Insert(slices.Delete(prefixes, i, i+1), 0, p)
		}
	}

	var targets []netip.Addr
	seen := make(map[netip.Addr]bool)
	for _, p := range prefixes {
		// Skip the network address (p.Addr()) and stop before the
		// broadcast address, the last one in p.
		for a := p.Addr().Next(); p.Contains(a.Next()); a = a.Next() {
			if self[a] || seen[a] {
				continue
			}
			seen[a] = true
			targets = append(targets, a)
		}
	}
	return targets, prefixes, nil
}

// capTargets keeps the first n targets and the prefixes they still
// reach, so the probe's trace never names a subnet the cap cut.
func capTargets(targets []netip.Addr, prefixes []netip.Prefix, n int) ([]netip.Addr, []netip.Prefix) {
	targets = targets[:min(n, len(targets))]
	prefixes = slices.DeleteFunc(slices.Clone(prefixes), func(p netip.Prefix) bool {
		return !slices.ContainsFunc(targets, p.Contains)
	})
	return targets, prefixes
}

// defaultRouteAddr returns this computer's address on its default IPv4
// route. Connecting a UDP socket sends nothing; it only makes the OS
// choose the source address it would route through. 192.0.2.1 is
// TEST-NET-1, reserved for documentation, so it is never contacted.
func defaultRouteAddr() (netip.Addr, bool) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, false // no default route, e.g. offline
	}
	defer func() { _ = conn.Close() }()
	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	addr, ok := netip.AddrFromSlice(ua.IP.To4())
	return addr, ok && !addr.IsUnspecified()
}

func joinPrefixes(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
