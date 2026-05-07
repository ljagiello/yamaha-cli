package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
		return s.client.GetFeatures(ctx)
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

// resolveDeviceID returns the YXC device_id (MAC) for the active receiver,
// caching the value on the loader.
func resolveDeviceID(ctx context.Context, s *state) (string, error) {
	if fl.deviceID != "" {
		return fl.deviceID, nil
	}
	info, err := s.client.GetDeviceInfo(ctx)
	if err != nil {
		return "", err
	}
	if info.DeviceID == "" {
		return "", fmt.Errorf("yxc: getDeviceInfo returned empty device_id")
	}
	fl.deviceID = info.DeviceID
	return info.DeviceID, nil
}
