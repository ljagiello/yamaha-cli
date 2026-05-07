//go:build integration

package yxc

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestIntegration_ReadOnly hits getDeviceInfo, getFeatures and
// getStatus(main) on the live receiver named by -yamaha-host. Asserts
// only structural sanity; no state-mutating calls.
//
// Run with:
//
//	go test -tags=integration -yamaha-host=192.168.1.116 ./pkg/yxc/...
func TestIntegration_ReadOnly(t *testing.T) {
	if yamahaHostFlag == nil || *yamahaHostFlag == "" {
		t.Skip("-yamaha-host not set; skipping integration test")
	}
	host := *yamahaHostFlag

	c, err := New(host, WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New(%q): %v", host, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// getDeviceInfo: model_name should be a non-empty string.
	di, err := c.GetDeviceInfo(ctx)
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	if strings.TrimSpace(di.ModelName) == "" {
		t.Errorf("GetDeviceInfo: empty model_name in %+v", di)
	}
	if strings.TrimSpace(di.DeviceID) == "" {
		t.Errorf("GetDeviceInfo: empty device_id in %+v", di)
	}

	// getFeatures: at least one zone, volume_range present for the
	// first zone, system input list non-empty.
	feat, err := c.GetFeatures(ctx)
	if err != nil {
		t.Fatalf("GetFeatures: %v", err)
	}
	if len(feat.Zone) < 1 {
		t.Fatalf("GetFeatures: expected >= 1 zone, got %d", len(feat.Zone))
	}
	zoneID := feat.Zone[0].ID
	if zoneID == "" {
		t.Errorf("GetFeatures: first zone has empty id")
	}
	if min, max, step, ok := feat.VolumeRange(zoneID); !ok {
		t.Errorf("GetFeatures: VolumeRange(%q) not present", zoneID)
	} else if max <= min || step <= 0 {
		t.Errorf("GetFeatures: nonsensical volume range min=%d max=%d step=%d", min, max, step)
	}
	if len(feat.SystemInputIDs()) == 0 {
		t.Errorf("GetFeatures: empty system input_list")
	}

	// getStatus(main): power should be one of the documented values.
	st, err := c.GetStatus(ctx, "main")
	if err != nil {
		t.Fatalf("GetStatus(main): %v", err)
	}
	switch st.Power {
	case "on", "standby":
	default:
		t.Errorf("GetStatus(main): unexpected power %q (want on|standby)", st.Power)
	}
	if st.Volume < 0 {
		t.Errorf("GetStatus(main): negative volume %d", st.Volume)
	}
}
