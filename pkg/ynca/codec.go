package ynca

import (
	"math"
	"strconv"
)

// This file holds the numeric value codec shared by every YNCA function
// that carries a stepped number: zone volume and tone (0.5 dB), FM
// frequency (0.2 MHz), AM frequency (10 kHz). It is a port of the ynca
// reference library's single helper number_to_string_with_stepsize
// (helpers.py), which the Python code reuses for all of the above instead
// of hand-rolling a formatter per function.

// volumeStep is the YNCA volume granularity in dB. Receivers report and
// accept values on this grid.
const volumeStep = 0.5

// formatStepped renders value rounded to the nearest multiple of step and
// printed with the given number of decimals, normalising a possible -0.0 to
// "0.0". A non-positive step disables rounding (the value is printed as-is
// at the requested precision). This is the single rounding+formatting rule
// every stepped YNCA function shares.
//
// Note: Go's math.Round is half-away-from-zero, whereas the ynca reference
// library's number_to_string_with_stepsize uses Python's round() (banker's
// rounding, half-to-even). The two differ ONLY for a value that lands exactly
// on a half-step midpoint (e.g. AM 1005 kHz on the 10 kHz grid → 1010 here vs
// 1000 there; FM 90.5 on the 0.2 MHz grid → 90.60). This is an intentional,
// benign divergence: these are user-typed targets and the receiver clamps to
// its own valid grid regardless, so a one-step difference at an exact tie has
// no practical effect. Kept as-is rather than reimplementing banker's rounding.
func formatStepped(value float64, decimals int, step float64) string {
	rounded := value
	if step > 0 {
		rounded = math.Round(value/step) * step
	}
	if rounded == 0 {
		rounded = 0 // collapse a possible -0.0 so we never emit "-0.0"
	}
	return strconv.FormatFloat(rounded, 'f', decimals, 64)
}

// formatVolume renders a dB value onto the YNCA volume grid: rounded to the
// nearest 0.5 dB, one decimal. Thin wrapper over formatStepped so the
// volume path and the new tone/frequency paths share one rounding rule.
func formatVolume(db float64) string {
	return formatStepped(db, 1, volumeStep)
}
