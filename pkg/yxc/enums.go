package yxc

import "time"

// This file gives the small, closed sets of *received* YXC state values a
// named Go type each, instead of leaving them as bare strings compared
// against magic literals at call sites.
//
// Design (ported from the ynca library's enum handling, adapted for Go):
//
//   - The underlying type is `string` and the constant values are the wire
//     strings, so JSON (de)serialisation stays trivial and lossless — an
//     unrecognised firmware value is preserved verbatim in the field (it
//     is NOT silently rewritten), which matters for a CLI that renders
//     device state.
//   - Each type has a `Known()` method and a `Parse*` helper that maps an
//     unrecognised value to the type's `*Unknown` sentinel (mirroring
//     ynca's `_missing_` → "< UNKNOWN >" fallback). Callers branch on
//     `Known()` for forward-compatibility; renderers use the raw value via
//     `string(...)` so nothing is lost.
//
// UnknownValue is the shared sentinel the Parse* helpers normalise an
// unrecognised wire value to.
const UnknownValue = "< UNKNOWN >"

// PowerState is a zone's power field from <zone>/getStatus.
type PowerState string

const (
	PowerOn      PowerState = "on"
	PowerStandby PowerState = "standby"
	PowerUnknown PowerState = UnknownValue
)

// Known reports whether p is one of the modelled power states.
func (p PowerState) Known() bool { return p == PowerOn || p == PowerStandby }

// ParsePowerState normalises s to a PowerState, mapping anything
// unrecognised to PowerUnknown.
func ParsePowerState(s string) PowerState {
	switch PowerState(s) {
	case PowerOn, PowerStandby:
		return PowerState(s)
	default:
		return PowerUnknown
	}
}

// PlaybackState is the netusb/getPlayInfo `playback` field (the *current*
// transport state). Distinct from Playback, which is the set of transport
// *actions* accepted by netusb/setPlayback.
type PlaybackState string

const (
	PlaybackStatePlay    PlaybackState = "play"
	PlaybackStateStop    PlaybackState = "stop"
	PlaybackStatePause   PlaybackState = "pause"
	PlaybackStateUnknown PlaybackState = UnknownValue
)

// Known reports whether p is one of the modelled playback states.
func (p PlaybackState) Known() bool {
	return p == PlaybackStatePlay || p == PlaybackStateStop || p == PlaybackStatePause
}

// ParsePlaybackState normalises s, mapping anything unrecognised to
// PlaybackStateUnknown.
func ParsePlaybackState(s string) PlaybackState {
	switch PlaybackState(s) {
	case PlaybackStatePlay, PlaybackStateStop, PlaybackStatePause:
		return PlaybackState(s)
	default:
		return PlaybackStateUnknown
	}
}

// RepeatMode is the netusb/getPlayInfo `repeat` field. (The ynca library
// notes some firmware reports "Single" where newer reports "One"; YXC has
// only used off/one/all in the wild, but Known()/Parse keep us safe.)
type RepeatMode string

const (
	RepeatOff     RepeatMode = "off"
	RepeatOne     RepeatMode = "one"
	RepeatAll     RepeatMode = "all"
	RepeatUnknown RepeatMode = UnknownValue
)

// Known reports whether r is one of the modelled repeat modes.
func (r RepeatMode) Known() bool {
	return r == RepeatOff || r == RepeatOne || r == RepeatAll
}

// ParseRepeatMode normalises s, mapping anything unrecognised to
// RepeatUnknown.
func ParseRepeatMode(s string) RepeatMode {
	switch RepeatMode(s) {
	case RepeatOff, RepeatOne, RepeatAll:
		return RepeatMode(s)
	default:
		return RepeatUnknown
	}
}

// ShuffleMode is the netusb/getPlayInfo `shuffle` field.
type ShuffleMode string

const (
	ShuffleOff     ShuffleMode = "off"
	ShuffleOn      ShuffleMode = "on"
	ShuffleSongs   ShuffleMode = "songs"
	ShuffleAlbums  ShuffleMode = "albums"
	ShuffleUnknown ShuffleMode = UnknownValue
)

// Known reports whether s is one of the modelled shuffle modes.
func (s ShuffleMode) Known() bool {
	return s == ShuffleOff || s == ShuffleOn || s == ShuffleSongs || s == ShuffleAlbums
}

// ParseShuffleMode normalises s, mapping anything unrecognised to
// ShuffleUnknown.
func ParseShuffleMode(s string) ShuffleMode {
	switch ShuffleMode(s) {
	case ShuffleOff, ShuffleOn, ShuffleSongs, ShuffleAlbums:
		return ShuffleMode(s)
	default:
		return ShuffleUnknown
	}
}

// DistRole is this device's role in a MusicCast Link group, from
// dist/getDistributionInfo.
type DistRole string

const (
	DistRoleServer  DistRole = "server"
	DistRoleClient  DistRole = "client"
	DistRoleNone    DistRole = "none"
	DistRoleUnknown DistRole = UnknownValue
)

// Known reports whether r is one of the modelled distribution roles.
func (r DistRole) Known() bool {
	return r == DistRoleServer || r == DistRoleClient || r == DistRoleNone
}

// ParseDistRole normalises s, mapping anything unrecognised to
// DistRoleUnknown.
func ParseDistRole(s string) DistRole {
	switch DistRole(s) {
	case DistRoleServer, DistRoleClient, DistRoleNone:
		return DistRole(s)
	default:
		return DistRoleUnknown
	}
}

// Band is a tuner band, used for both the received `band` field and the
// send-side band parameter (see validTunerBand).
type Band string

const (
	BandFM      Band = "fm"
	BandAM      Band = "am"
	BandDAB     Band = "dab"
	BandUnknown Band = UnknownValue
)

// Known reports whether b is one of the modelled bands.
func (b Band) Known() bool { return b == BandFM || b == BandAM || b == BandDAB }

// playTimeToDuration converts a YXC `play_time`/`total_time` value (whole
// seconds) into a time.Duration. Negative sentinels some inputs report
// (e.g. -60 while buffering) are clamped to 0.
func playTimeToDuration(seconds int) time.Duration {
	if seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
