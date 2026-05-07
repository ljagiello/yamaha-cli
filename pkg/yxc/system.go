package yxc

import (
	"context"
	"encoding/json"
	"fmt"
)

// GetDeviceInfo returns identification metadata for the receiver
// (model, device_id/MAC, firmware version, YXC API version).
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	raw, err := c.Do(ctx, "system/getDeviceInfo", nil)
	if err != nil {
		return nil, err
	}
	var di DeviceInfo
	if err := json.Unmarshal(raw, &di); err != nil {
		return nil, fmt.Errorf("yxc: getDeviceInfo: %w", err)
	}
	return &di, nil
}

// GetFeatures returns the static feature catalog: zones, inputs, sound
// programs, ranges, etc. The CLI caches this per-device on disk.
func (c *Client) GetFeatures(ctx context.Context) (*Features, error) {
	raw, err := c.Do(ctx, "system/getFeatures", nil)
	if err != nil {
		return nil, err
	}
	var f Features
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("yxc: getFeatures: %w", err)
	}
	return &f, nil
}

// GetNetworkStatus returns `/system/getNetworkStatus`. The shape is
// dynamic (network_name, IP, MAC variants) so we expose the raw bytes;
// callers can json.Unmarshal into whatever subset they need.
func (c *Client) GetNetworkStatus(ctx context.Context) (json.RawMessage, error) {
	return c.Do(ctx, "system/getNetworkStatus", nil)
}

// RequestSystemReboot asks the receiver to reboot. The response is a
// plain `{"response_code":0}`; a successful return means the device
// accepted the request, not that it has rebooted yet.
func (c *Client) RequestSystemReboot(ctx context.Context) error {
	_, err := c.Do(ctx, "system/requestSystemReboot", nil)
	return err
}
