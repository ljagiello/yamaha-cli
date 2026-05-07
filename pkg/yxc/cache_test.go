package yxc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fixedFeatures returns a small but valid Features payload for cache
// round-trips. We avoid the giant testdata fixture so each test stays
// self-contained.
func fixedFeatures() *Features {
	return &Features{
		ResponseCode: 0,
		System: SystemFeatures{
			FuncList: []string{"power"},
			ZoneNum:  1,
			InputList: []InputItem{
				{ID: "hdmi1", PlayInfoType: "none"},
			},
		},
		Zone: []ZoneFeatures{
			{ID: "main", FuncList: []string{"power", "volume"}},
		},
	}
}

// TestFeaturesCache_LoadOrFetch_Fresh verifies that a fresh cache file is
// returned without invoking the fetch callback.
func TestFeaturesCache_LoadOrFetch_Fresh(t *testing.T) {
	dir := t.TempDir()
	fc := &FeaturesCache{Dir: dir}

	// Pre-populate by calling LoadOrFetch once.
	var fetches int32
	fetch := FetchFunc(func(_ context.Context) (*Features, error) {
		atomic.AddInt32(&fetches, 1)
		return fixedFeatures(), nil
	})

	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("first LoadOrFetch: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("first call: fetches = %d, want 1", got)
	}

	// Second call should hit cache — no extra fetch.
	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("second LoadOrFetch: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Errorf("second call: fetches = %d, want 1 (cache miss)", got)
	}
}

// TestFeaturesCache_TTLExpiry verifies that a cache file older than the
// TTL is treated as a miss and the fetch callback runs again.
func TestFeaturesCache_TTLExpiry(t *testing.T) {
	dir := t.TempDir()
	fc := &FeaturesCache{Dir: dir}

	var fetches int32
	fetch := FetchFunc(func(_ context.Context) (*Features, error) {
		atomic.AddInt32(&fetches, 1)
		return fixedFeatures(), nil
	})

	// Seed the cache.
	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Backdate the file to >7 days ago so it's stale per the default TTL.
	path, err := fc.pathFor("device-x")
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	old := time.Now().Add(-(FeaturesCacheTTL + 24*time.Hour))
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Errorf("expected refetch after TTL expiry, fetches = %d (want 2)", got)
	}
}

// TestFeaturesCache_RefreshForcesFetch verifies refresh=true bypasses a
// fresh cache and re-saves the new payload.
func TestFeaturesCache_RefreshForcesFetch(t *testing.T) {
	dir := t.TempDir()
	fc := &FeaturesCache{Dir: dir}

	var fetches int32
	fetch := FetchFunc(func(_ context.Context) (*Features, error) {
		atomic.AddInt32(&fetches, 1)
		return fixedFeatures(), nil
	})

	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("seed fetches = %d, want 1", got)
	}
	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, true); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Errorf("refresh did not force fetch: fetches = %d, want 2", got)
	}
}

// TestFeaturesCache_AtomicWrite verifies that when fetch fails (e.g.
// because the context is cancelled mid-flight), the on-disk cache file
// is unchanged. tmp + rename guarantees the destination only ever sees a
// fully-written payload.
func TestFeaturesCache_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	fc := &FeaturesCache{Dir: dir}

	// Seed with a known-good payload.
	good := fixedFeatures()
	good.System.ZoneNum = 1
	if _, err := fc.LoadOrFetch(context.Background(), "device-x",
		FetchFunc(func(_ context.Context) (*Features, error) { return good, nil }),
		false,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	path, err := fc.pathFor("device-x")
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	originalStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	originalContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readfile: %v", err)
	}

	// Now force a refresh whose fetch returns ctx.Canceled. Cache must
	// be left intact: no rename should have happened, and no leftover
	// .tmp files should be in the directory.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fc.LoadOrFetch(ctx, "device-x",
		FetchFunc(func(c context.Context) (*Features, error) {
			return nil, c.Err()
		}),
		true,
	)
	if err == nil {
		t.Fatal("expected error from cancelled fetch")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// On-disk file must be byte-identical.
	gotContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("post-cancel read: %v", err)
	}
	if !bytes.Equal(gotContent, originalContent) {
		t.Errorf("cache content changed after failed fetch")
	}
	postStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("post-cancel stat: %v", err)
	}
	if !postStat.ModTime().Equal(originalStat.ModTime()) {
		t.Errorf("cache mtime changed: %v -> %v", originalStat.ModTime(), postStat.ModTime())
	}

	// And no leftover tmp files in the cache dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, ent := range entries {
		if filepath.Ext(ent.Name()) == ".tmp" {
			t.Errorf("leftover tmp file: %s", ent.Name())
		}
	}
}

// TestFeaturesCache_Invalidate removes the on-disk file so the next
// LoadOrFetch goes through fetch again.
func TestFeaturesCache_Invalidate(t *testing.T) {
	dir := t.TempDir()
	fc := &FeaturesCache{Dir: dir}

	var fetches int32
	fetch := FetchFunc(func(_ context.Context) (*Features, error) {
		atomic.AddInt32(&fetches, 1)
		return fixedFeatures(), nil
	})
	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := fc.Invalidate("device-x"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, err := fc.LoadOrFetch(context.Background(), "device-x", fetch, false); err != nil {
		t.Fatalf("post-invalidate fetch: %v", err)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Errorf("fetches = %d, want 2 (invalidated cache)", got)
	}

	// Invalidate on a non-existent ID is a no-op.
	if err := fc.Invalidate("does-not-exist"); err != nil {
		t.Errorf("Invalidate(missing): unexpected error %v", err)
	}
}
