package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FeaturesCacheTTL is the maximum age before a cached features file is
// considered stale and refetched.
const FeaturesCacheTTL = 7 * 24 * time.Hour

// FeaturesCache persists getFeatures responses per device on disk.
//
// Files live under <userCacheDir>/yamaha-cli/<device-id>-features.json.
// device-id should be the receiver's MAC-style device_id so the cache
// survives DHCP renewals.
type FeaturesCache struct {
	// Dir overrides the default cache directory. If empty, the cache uses
	// os.UserCacheDir() + "/yamaha-cli". Tests inject a temporary dir here.
	Dir string
	// TTL overrides FeaturesCacheTTL. If zero, the default is used.
	TTL time.Duration
}

// NewFeaturesCache returns a cache rooted at the platform cache dir.
func NewFeaturesCache() *FeaturesCache {
	return &FeaturesCache{}
}

// dir resolves the on-disk root for cache files, creating it if needed.
func (fc *FeaturesCache) dir() (string, error) {
	if fc.Dir != "" {
		if err := os.MkdirAll(fc.Dir, 0o755); err != nil {
			return "", err
		}
		return fc.Dir, nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(root, "yamaha-cli")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

func (fc *FeaturesCache) ttl() time.Duration {
	if fc.TTL > 0 {
		return fc.TTL
	}
	return FeaturesCacheTTL
}

// pathFor returns the absolute path of the per-device cache file.
func (fc *FeaturesCache) pathFor(deviceID string) (string, error) {
	if deviceID == "" {
		return "", errors.New("yxc: cache: empty device ID")
	}
	d, err := fc.dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, deviceID+"-features.json"), nil
}

// FetchFunc is the signature of the callback supplied to LoadOrFetch.
type FetchFunc func(context.Context) (*Features, error)

// LoadOrFetch returns a cached Features for deviceID if one exists and is
// fresh; otherwise it calls fetch, persists the result atomically, and
// returns it.
//
// If refresh is true the cache is bypassed and fetch is always called
// (and the result re-saved).
func (fc *FeaturesCache) LoadOrFetch(ctx context.Context, deviceID string, fetch FetchFunc, refresh bool) (*Features, error) {
	if fetch == nil {
		return nil, errors.New("yxc: cache: nil fetch func")
	}
	path, err := fc.pathFor(deviceID)
	if err != nil {
		return nil, err
	}

	if !refresh {
		if f, ok, err := fc.tryLoad(path); err != nil {
			return nil, err
		} else if ok {
			return f, nil
		}
	}

	f, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	if err := fc.save(path, f); err != nil {
		return nil, fmt.Errorf("yxc: cache: save %s: %w", path, err)
	}
	return f, nil
}

// tryLoad returns (features, true, nil) if a fresh cache file exists,
// (nil, false, nil) on cache miss / expired, and (nil, false, err) on
// genuine I/O / parse errors.
func (fc *FeaturesCache) tryLoad(path string) (*Features, bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if time.Since(st.ModTime()) > fc.ttl() {
		return nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	var f Features
	if err := json.Unmarshal(raw, &f); err != nil {
		// Corrupt cache — treat as miss rather than fatal.
		return nil, false, nil
	}
	return &f, true, nil
}

// save writes the features to path atomically (tmp + rename).
func (fc *FeaturesCache) save(path string, f *Features) error {
	if f == nil {
		return errors.New("yxc: cache: nil Features")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "features-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails before the rename.
	cleanup := func() { _ = os.Remove(tmpName) }

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Path returns the on-disk path the cache will use for deviceID.
// Useful for `yamaha config path`-style commands.
func (fc *FeaturesCache) Path(deviceID string) (string, error) {
	return fc.pathFor(deviceID)
}

// Invalidate removes the cache file for deviceID, if any.
func (fc *FeaturesCache) Invalidate(deviceID string) error {
	path, err := fc.pathFor(deviceID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
