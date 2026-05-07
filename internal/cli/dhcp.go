package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/internal/debuglog"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// rediscoverTimeout bounds the SSDP scan launched on transport failure.
// 3s is the same budget the first-run wizard uses; long enough for a
// healthy LAN, short enough that the user notices the redo if it fails.
const rediscoverTimeout = 3 * time.Second

// lookupByUDNFn is the package-level seam that runWithRediscover calls
// when it needs to re-locate a device by UDN. Tests stub this to avoid
// driving real SSDP traffic.
var lookupByUDNFn = discover.LookupByUDN

// runWithRediscover executes op against a YXC client built from s.device.
// On a transport error AND when s was resolved via a config alias (i.e.
// not via --host/YAMAHA_HOST) AND the alias has a saved UDN, it runs an
// SSDP LookupByUDN scan. If the device is found at a new IP, the config
// is updated atomically and op is retried once with a fresh client.
//
// Pre-v5 configs without a saved UDN skip the rediscovery step entirely
// (per PLAN.v6 "DHCP resilience" / "Skipped when").
func runWithRediscover(ctx context.Context, s *state, op func(*yxc.Client) error) error {
	if op == nil {
		return fmt.Errorf("cli: nil op")
	}

	err := op(s.client)
	if err == nil {
		return nil
	}
	if !shouldRediscover(s, err) {
		return err
	}

	logRediscover(s.debug, s.alias, s.device.UDN)
	newDev, lookupErr := lookupByUDNFn(ctx, s.device.UDN, rediscoverTimeout)
	if lookupErr != nil {
		// Surface the unreachable error rather than the lookup error so
		// the user gets the consistent UDN-aware message.
		return &unreachableError{alias: s.alias, udn: s.device.UDN, cause: err}
	}

	// New IP — atomically update the config map, rebuild the client, retry.
	if err := persistRediscoveredHost(s, newDev.Host); err != nil {
		return err
	}
	opts := []yxc.Option{yxc.WithTimeout(5 * time.Second)}
	if s.debug != nil && s.debug.Enabled() {
		opts = append(opts, yxc.WithHTTPClient(newDebugHTTPClient(5*time.Second, s.debug)))
	}
	newClient, cerr := yxc.New(newDev.Host, opts...)
	if cerr != nil {
		return cerr
	}
	s.client = newClient

	if err := op(newClient); err != nil {
		// Retried once and still failing. Don't loop — surface as
		// unreachable so the exit-code mapper returns 69.
		if yxc.IsTransport(err) {
			return &unreachableError{alias: s.alias, udn: s.device.UDN, cause: err}
		}
		return err
	}
	return nil
}

// shouldRediscover decides whether the given error from op merits one
// SSDP rediscovery attempt. It's a transport error AND the device came
// from config (alias != "") AND we have a UDN to match.
func shouldRediscover(s *state, err error) bool {
	if !yxc.IsTransport(err) {
		return false
	}
	if s == nil || s.alias == "" {
		return false
	}
	if s.device.UDN == "" {
		return false
	}
	return true
}

// persistRediscoveredHost writes the new host back to the config file
// while preserving everything else. Atomic via config.Save's tmp+rename.
func persistRediscoveredHost(s *state, newHost string) error {
	if s.cfg == nil {
		return fmt.Errorf("cli: cannot persist rediscovered host: no config")
	}
	if s.cfg.Devices == nil {
		s.cfg.Devices = map[string]config.Device{}
	}
	d, ok := s.cfg.Devices[s.alias]
	if !ok {
		// Shouldn't happen — the alias came from config in the first place.
		// Be defensive: insert a new entry rather than dropping the update.
		d = s.device
	}
	d.Host = newHost
	s.cfg.Devices[s.alias] = d
	s.device = d
	return config.Save(s.cfg)
}

func logRediscover(dbg *debuglog.Logger, alias, udn string) {
	if dbg == nil {
		return
	}
	dbg.Tracef("→ rediscover alias=%s udn=%s", alias, udn)
}
