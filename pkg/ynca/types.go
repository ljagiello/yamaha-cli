package ynca

// Subunit identifiers we know about (RX-V583).
//
// Documentation-only: callers still build raw lines such as
// `@MAIN:PWR=?`. These constants exist so that command-routing code in
// other packages does not have to repeat magic strings.
const (
	SubunitSystem = "SYS"
	SubunitMain   = "MAIN"
	SubunitZone2  = "ZONE2"
	// Zone3, Zone4 are not present on RX-V583 but are included for
	// forward compatibility with larger receivers.
	SubunitZone3 = "ZONE3"
	SubunitZone4 = "ZONE4"
)

// Common functions across subunits.
//
// As with the subunit constants, these are documentation-only and do
// not constrain what callers may pass through Send.
const (
	FuncPower     = "PWR"       // On / Standby
	FuncVolume    = "VOL"       // float in dB
	FuncMute      = "MUTE"      // On / Off
	FuncInput     = "INP"       // HDMI1 / HDMI2 / ...
	FuncModelName = "MODELNAME" // e.g. "RX-V583"
	FuncVersion   = "VERSION"   // firmware version, e.g. "2.87/1.81"
)
