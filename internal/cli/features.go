package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// featureLoader wraps a per-process memo around the
// yxc.FeaturesCache.LoadOrFetch flow so subcommands can call
// loadFeatures(...) repeatedly within one invocation without re-fetching.
//
// Process-local cache only — the on-disk cache (managed by
// yxc.FeaturesCache) is the cross-invocation source of truth.
type featureLoader struct {
	mu       sync.Mutex
	deviceID string
	feats    *yxc.Features
}

// fl is the singleton loader for the current process. It's replaced when
// the active device changes (e.g. after first-run wizard). For Phase 1
// we run a single command per process so a singleton is fine.
var fl = &featureLoader{}

// loadFeatures returns the *yxc.Features for the active device, fetching
// (and persisting) on miss. A second call with refresh=true forces a
// re-fetch.
//
// Both the device-info round-trip (when needed) and the features fetch
// run through runWithRediscover so a stale-IP receiver recovers
// transparently — matching what the README's "DHCP resilience" section
// promises for any command, not just the ones that mutate state.
func loadFeatures(ctx context.Context, s *state, refresh bool) (*yxc.Features, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("cli: loadFeatures: nil state")
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()

	deviceID, err := resolveDeviceID(ctx, s)
	if err != nil {
		return nil, err
	}
	if !refresh && fl.feats != nil && fl.deviceID == deviceID {
		return fl.feats, nil
	}

	cache := &yxc.FeaturesCache{}
	feats, err := cache.LoadOrFetch(ctx, deviceID, func(ctx context.Context) (*yxc.Features, error) {
		var f *yxc.Features
		ferr := runWithRediscover(ctx, s, func(c *yxc.Client) error {
			got, e := c.GetFeatures(ctx)
			if e != nil {
				return e
			}
			f = got
			return nil
		})
		return f, ferr
	}, refresh)
	if err != nil {
		return nil, err
	}
	fl.deviceID = deviceID
	fl.feats = feats
	return feats, nil
}

// loadFeaturesQuiet is a non-fatal variant: it never fails the calling
// command if features can't be obtained (used by status to compute
// volume_percent — falling back to the universal 0..161 max when the
// fetch fails is harmless).
func loadFeaturesQuiet(ctx context.Context, s *state) (*yxc.Features, error) {
	feats, err := loadFeatures(ctx, s, false)
	if err != nil {
		return nil, err
	}
	return feats, nil
}

// resolveDeviceID returns the YXC device_id (LAN MAC) for the active
// receiver. Source preference, in order:
//
//  1. The process-local memo (within one command invocation).
//  2. The config entry for the active alias (persisted across runs).
//  3. A live getDeviceInfo round-trip (then persisted to config when
//     the device is alias-resolved).
//
// The live fetch goes through runWithRediscover so a stale-IP receiver
// recovers transparently.
func resolveDeviceID(ctx context.Context, s *state) (string, error) {
	if fl.deviceID != "" {
		return fl.deviceID, nil
	}
	if s.alias != "" && s.device.DeviceID != "" {
		fl.deviceID = s.device.DeviceID
		return s.device.DeviceID, nil
	}

	var info *yxc.DeviceInfo
	err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
		got, e := c.GetDeviceInfo(ctx)
		if e != nil {
			return e
		}
		info = got
		return nil
	})
	if err != nil {
		return "", err
	}
	if info == nil || info.DeviceID == "" {
		return "", fmt.Errorf("yxc: getDeviceInfo returned empty device_id")
	}
	fl.deviceID = info.DeviceID

	// Best-effort persist for alias-resolved devices so the next
	// invocation skips the round-trip. Failures are non-fatal — the
	// command still works, we just spend the extra getDeviceInfo
	// round-trip again next time.
	if s.alias != "" && s.cfg != nil {
		persistDeviceID(s, info.DeviceID)
	}
	return info.DeviceID, nil
}

// persistDeviceID atomically writes the device_id back into the config
// entry for the active alias. Best-effort: any error is swallowed so the
// command in flight can complete.
func persistDeviceID(s *state, deviceID string) {
	if s.cfg == nil || s.cfg.Devices == nil {
		return
	}
	d, ok := s.cfg.Devices[s.alias]
	if !ok {
		return
	}
	if d.DeviceID == deviceID {
		return
	}
	d.DeviceID = deviceID
	s.cfg.Devices[s.alias] = d
	s.device = d
	_ = config.Save(s.cfg)
}
