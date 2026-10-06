//go:build integration

package discover

import (
	"context"
	"flag"
	"net/netip"
	"testing"
	"time"
)

// yamahaHostFlag names a live receiver on this computer's LAN.
//
// Run with:
//
//	go test -tags=integration -run Integration -v -yamaha-host=192.168.1.116 ./pkg/discover/
var yamahaHostFlag = flag.String("yamaha-host", "", "Yamaha receiver IP for integration tests")

func needHost(t *testing.T) string {
	t.Helper()
	if *yamahaHostFlag == "" {
		t.Skip("-yamaha-host not set; skipping integration test")
	}
	return *yamahaHostFlag
}

// TestIntegration_Sweep runs the real subnet probe, without SSDP, and
// expects it to find the receiver. The trace and timing it logs are what
// sweepDialTimeout and sweepParallel are tuned against.
func TestIntegration_Sweep(t *testing.T) {
	host := needHost(t)
	ctx := WithTrace(context.Background(), t.Logf)

	start := time.Now()
	devs, err := sweep(ctx, 3*time.Second, netip.Addr{})
	t.Logf("probe took %v", time.Since(start).Round(time.Millisecond))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, d := range devs {
		if d.Host == host {
			return
		}
	}
	t.Fatalf("probe did not find %s; found %+v", host, devs)
}

// TestIntegration_Describe probes the receiver the way `config add` does
// and expects its UDN.
func TestIntegration_Describe(t *testing.T) {
	host := needHost(t)
	ctx := WithTrace(context.Background(), t.Logf)

	start := time.Now()
	dev, err := Describe(ctx, host, 8*time.Second)
	t.Logf("describe took %v", time.Since(start).Round(time.Millisecond))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if dev.UDN == "" {
		t.Fatalf("Describe returned no UDN: %+v", dev)
	}
}
