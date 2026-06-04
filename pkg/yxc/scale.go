package yxc

import "math"

// Default dB volume scale, used when a receiver's getFeatures omits an
// `actual_volume_db` range_step.
//
// This is the RX-V/A integer-step convention — one integer wire step ≈
// 0.5 dB with a -80.5 dB baseline — verified against the live RX-V583.
// Most receivers DO report `actual_volume_db` (e.g. some A-series go to
// -99.5), so this fallback is a last resort, not the common path. Keeping
// it here as named constants means the convention lives in exactly one
// place instead of being re-hardcoded at each conversion site.
const (
	DefaultVolumeDBBaseline = -80.5
	DefaultVolumeDBStep     = 0.5
)

// VolumeDBScale resolves the (baseline, step) pair used to convert between
// the integer wire volume and dB for the named zone. It prefers the
// device's `actual_volume_db` range_step (min as baseline, step as
// increment) and falls back to the RX-V convention when the device omits
// it. exact reports whether the device-supplied scale was used (false
// means the fallback baseline/step were returned).
//
// Nil-safe: a nil *Features (or unknown zone) yields the default scale.
func (f *Features) VolumeDBScale(zone string) (baseline, step float64, exact bool) {
	if mn, _, st, ok := f.VolumeRangeDB(zone); ok && st > 0 {
		return mn, st, true
	}
	return DefaultVolumeDBBaseline, DefaultVolumeDBStep, false
}

// VolumeIntToDB converts an integer wire volume to its dB value for the
// named zone, using VolumeDBScale. Nil-safe.
func (f *Features) VolumeIntToDB(zone string, n int) float64 {
	baseline, step, _ := f.VolumeDBScale(zone)
	return baseline + step*float64(n)
}

// VolumeDBToInt converts a dB value to the nearest integer wire volume for
// the named zone, using VolumeDBScale. The result is rounded to the
// nearest step but NOT clamped to the device's volume range — callers
// clamp against VolumeRange. Nil-safe.
func (f *Features) VolumeDBToInt(zone string, db float64) int {
	baseline, step, _ := f.VolumeDBScale(zone)
	if step == 0 {
		return 0
	}
	return int(math.Round((db - baseline) / step))
}
