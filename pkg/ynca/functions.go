package ynca

import "strings"

// This file is a small, Go-idiomatic port of the ynca reference library's
// per-function descriptor model (src/ynca/function.py). The Python library
// binds each YNCA function to a typed property, a converter, and a
// Cmd.GET|PUT capability flag via descriptor objects on each subunit class.
//
// A full reflection/descriptor port would be un-Go-like and risky, so this
// is deliberately a metadata CATALOG rather than a behavioural layer: it
// records, in one machine-readable place, every function the typed control
// layer knows about — its exact wire name, whether it is readable/writable,
// a one-line description, and (for closed-set enums) a value lister. The
// discovery surfaces — `ynca list`, the REPL `?`/`help`, `ynca info`, and
// the `ynca dump` default catalog — are all driven from it, so the known
// vocabulary lives in exactly one place instead of being scattered as magic
// strings across the command files.
//
// It is advisory: get/put do NOT consult it to hard-fail before the wire
// (support is genuinely model-specific, and the device's @UNDEFINED /
// @RESTRICTED reply is the real authority). The catalog answers "what could
// this command be?", not "what will this device accept?".

// Cmd is a bitmask of the directions a function supports, mirroring ynca's
// Cmd.GET / Cmd.PUT flags.
type Cmd uint8

const (
	// CmdGet marks a readable function (@SUB:FUNC=? returns a value).
	CmdGet Cmd = 1 << iota
	// CmdPut marks a writable function (@SUB:FUNC=value is accepted).
	CmdPut
)

// CanGet reports whether the function is readable.
func (c Cmd) CanGet() bool { return c&CmdGet != 0 }

// CanPut reports whether the function is writable.
func (c Cmd) CanPut() bool { return c&CmdPut != 0 }

// String renders the capability as "get", "put", or "get/put".
func (c Cmd) String() string {
	switch {
	case c.CanGet() && c.CanPut():
		return "get/put"
	case c.CanGet():
		return "get"
	case c.CanPut():
		return "put"
	default:
		return ""
	}
}

// Scope groups functions by the kind of subunit they apply to, so the
// discovery surfaces can present them by category.
type Scope string

const (
	ScopeSystem Scope = "system"
	ScopeZone   Scope = "zone"
	ScopeTuner  Scope = "tuner"
	ScopeSource Scope = "source"
)

// Function describes one YNCA function: its wire FUNCTION token, the scope
// it belongs to, its read/write capability, a one-line description, and —
// for closed-set enum functions — a Values lister the CLI uses to enumerate
// valid arguments when a command runs with no argument.
type Function struct {
	Name   string // wire FUNCTION token, e.g. "2CHDECODER"
	Scope  Scope  // system | zone | tuner | source
	Cmd    Cmd    // CmdGet, CmdPut, or both
	Desc   string // human-readable one-liner
	Values func() []string
}

// catalog is the registry of every function the typed control layer
// models. It is intentionally a superset across Yamaha models; a given
// receiver supports a subset (discoverable via `ynca dump`).
var catalog = []Function{
	// System.
	{FuncModelName, ScopeSystem, CmdGet, "Receiver model name (e.g. RX-V583)", nil},
	{FuncVersion, ScopeSystem, CmdGet, "Firmware/protocol version", nil},
	{FuncPower, ScopeSystem, CmdGet | CmdPut, "System power (On/Standby)", nil},
	{FuncInpName, ScopeSystem, CmdGet, "Per-connector input display names (INPNAME group)", nil},

	// Zone.
	{FuncPower, ScopeZone, CmdGet | CmdPut, "Zone power (On/Standby)", nil},
	{FuncVolume, ScopeZone, CmdGet | CmdPut, "Zone volume in dB (0.5 dB grid; Up/Down for relative)", nil},
	{FuncMute, ScopeZone, CmdGet | CmdPut, "Zone mute (Off/On/Att -20 dB/Att -40 dB)", nil},
	{FuncInput, ScopeZone, CmdGet | CmdPut, "Zone input/source", Inputs},
	{FuncSoundProgram, ScopeZone, CmdGet | CmdPut, "DSP sound program", SoundPrograms},
	{Func2ChDecoder, ScopeZone, CmdGet | CmdPut, "2-channel surround decoder", TwoChDecoders},
	{FuncScene, ScopeZone, CmdPut, "Recall a scene (Scene <n>)", nil},
	{FuncSleep, ScopeZone, CmdGet | CmdPut, "Sleep timer (Off/30/60/90/120 min)", nil},
	{FuncSpBass, ScopeZone, CmdGet | CmdPut, "Speaker bass tone in dB (0.5 dB grid)", nil},
	{FuncSpTreble, ScopeZone, CmdGet | CmdPut, "Speaker treble tone in dB (0.5 dB grid)", nil},
	{FuncBasic, ScopeZone, CmdGet, "Fan-out GET of the zone's common fields", nil},
	{FuncPureDirMode, ScopeZone, CmdGet | CmdPut, "Pure Direct mode (On/Off)", nil},
	{FuncEnhancer, ScopeZone, CmdGet | CmdPut, "Compressed Music Enhancer (On/Off)", nil},
	{FuncExBass, ScopeZone, CmdGet | CmdPut, "Extra Bass (Auto/Off)", nil},
	{FuncAdaptiveDRC, ScopeZone, CmdGet | CmdPut, "Adaptive DRC (Auto/Off)", nil},
	{FuncStraight, ScopeZone, CmdGet | CmdPut, "Straight decode (On/Off)", nil},
	{FuncSurroundAI, ScopeZone, CmdGet | CmdPut, "Surround:AI (On/Off)", nil},
	{Func3DCinema, ScopeZone, CmdGet | CmdPut, "CINEMA DSP 3D mode (Auto/Off)", nil},

	// Tuner (@TUN).
	{FuncBand, ScopeTuner, CmdGet | CmdPut, "Tuner band (AM/FM)", nil},
	{FuncFMFreq, ScopeTuner, CmdGet | CmdPut, "FM frequency (0.2 MHz grid)", nil},
	{FuncAMFreq, ScopeTuner, CmdGet | CmdPut, "AM frequency (10 kHz grid)", nil},
	{FuncPreset, ScopeTuner, CmdGet | CmdPut, "Tuner preset number (Up/Down for relative)", nil},
	{FuncMem, ScopeTuner, CmdPut, "Store current station to a preset slot", nil},

	// Source (streaming/USB/network) playback & metadata.
	{FuncPlayback, ScopeSource, CmdPut, "Transport control (Play/Pause/Stop/Skip Fwd/Skip Rev)", nil},
	{FuncPlaybackInfo, ScopeSource, CmdGet, "Current transport state (Play/Pause/Stop)", nil},
	{FuncMetaInfo, ScopeSource, CmdGet, "Now-playing metadata fan-out (artist/album/song/…)", nil},
}

// Functions returns the full descriptor catalog. The slice is freshly
// copied so callers can sort/filter it without mutating the registry.
func Functions() []Function {
	out := make([]Function, len(catalog))
	copy(out, catalog)
	return out
}

// FunctionsForScope returns the catalog entries for one scope.
func FunctionsForScope(scope Scope) []Function {
	var out []Function
	for _, f := range catalog {
		if f.Scope == scope {
			out = append(out, f)
		}
	}
	return out
}

// LookupFunction returns the first catalog entry whose wire name matches
// name (case-insensitive), and whether one was found. When a function is
// modelled under more than one scope (e.g. PWR on both system and zone),
// the first registration wins — callers that care about scope use
// FunctionsForScope.
func LookupFunction(name string) (Function, bool) {
	for _, f := range catalog {
		if strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return Function{}, false
}
