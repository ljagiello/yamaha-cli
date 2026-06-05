package ynca

import (
	"slices"
	"strings"
)

// This file gives the small, closed sets of YNCA wire values a named Go
// type each, instead of leaving them as bare strings compared against
// magic literals at call sites. It is the YNCA-side twin of pkg/yxc/enums.go
// and a direct port of the ynca reference library's enum handling
// (src/ynca/enums.py), adapted for Go.
//
// Design (shared with pkg/yxc):
//
//   - The underlying type is `string` and the constant values are the exact
//     wire strings, so a value round-trips losslessly: an unrecognised
//     firmware value is preserved verbatim in the field (never silently
//     rewritten), which matters for a CLI that renders device state.
//   - Each type has a `Known()` method and a `Parse*` helper that maps an
//     unrecognised value to the type's `*Unknown` sentinel (mirroring
//     ynca's `_missing_` → "< UNKNOWN >" fallback). Callers branch on
//     `Known()` for forward-compatibility; renderers use the raw value via
//     `string(...)` so nothing is lost.
//   - Closed sets also expose a list function (e.g. Inputs()) so the CLI can
//     enumerate valid values when a command is run with no argument, the
//     same affordance the YXC `input`/`sound`/`decoder` commands offer.

// UnknownValue is the shared sentinel the Parse* helpers normalise an
// unrecognised wire value to. Identical to yxc.UnknownValue so the two
// backends render unknown state the same way.
const UnknownValue = "< UNKNOWN >"

// Input is a subunit's INP value (the selected source), e.g. "HDMI2",
// "TUNER", "NET RADIO". The set is the union of physical connectors and
// subunit-backed sources from the ynca reference library (enums.py Input).
type Input string

// Input wire values. Physical connectors first, then subunit-backed
// sources. Note several carry spaces ("NET RADIO", "V-AUX") so they must be
// compared as whole strings, not tokens.
const (
	InputAudio        Input = "AUDIO"
	InputAudio1       Input = "AUDIO1"
	InputAudio2       Input = "AUDIO2"
	InputAudio3       Input = "AUDIO3"
	InputAudio4       Input = "AUDIO4"
	InputAudio5       Input = "AUDIO5"
	InputAV1          Input = "AV1"
	InputAV2          Input = "AV2"
	InputAV3          Input = "AV3"
	InputAV4          Input = "AV4"
	InputAV5          Input = "AV5"
	InputAV6          Input = "AV6"
	InputAV7          Input = "AV7"
	InputCD           Input = "CD"
	InputCoaxial1     Input = "COAXIAL1"
	InputCoaxial2     Input = "COAXIAL2"
	InputDock         Input = "DOCK"
	InputHDMI1        Input = "HDMI1"
	InputHDMI2        Input = "HDMI2"
	InputHDMI3        Input = "HDMI3"
	InputHDMI4        Input = "HDMI4"
	InputHDMI5        Input = "HDMI5"
	InputHDMI6        Input = "HDMI6"
	InputHDMI7        Input = "HDMI7"
	InputLine1        Input = "LINE1"
	InputLine2        Input = "LINE2"
	InputLine3        Input = "LINE3"
	InputMainZoneSync Input = "Main Zone Sync"
	InputMultiCh      Input = "MULTI CH"
	InputOptical1     Input = "OPTICAL1"
	InputOptical2     Input = "OPTICAL2"
	InputPhono        Input = "PHONO"
	InputTV           Input = "TV"
	InputVAux         Input = "V-AUX"

	InputAirPlay  Input = "AirPlay"
	InputBluetMix Input = "Bluetooth"
	InputDeezer   Input = "Deezer"
	InputIpod     Input = "iPod"
	InputIpodUSB  Input = "iPod (USB)"
	InputMCLink   Input = "MusicCast Link"
	InputNapster  Input = "Napster"
	InputNetRadio Input = "NET RADIO"
	InputPandora  Input = "Pandora"
	InputPC       Input = "PC"
	InputRhapsody Input = "Rhapsody"
	InputServer   Input = "SERVER"
	InputSirius   Input = "SIRIUS"
	InputSiriusIR Input = "SIRIUS InternetRadio"
	InputSiriusXM Input = "SiriusXM"
	InputSpotify  Input = "Spotify"
	InputTidal    Input = "TIDAL"
	InputTuner    Input = "TUNER"
	InputUAW      Input = "UAW"
	InputUSB      Input = "USB"
	InputUnknown  Input = UnknownValue
)

// allInputs is the closed set of modelled inputs, in a stable order for
// listing. A receiver only supports a subset (its @SYS:INPNAME list);
// callers that want the live set should read INPNAME (see Inputs note).
var allInputs = []Input{
	InputAudio, InputAudio1, InputAudio2, InputAudio3, InputAudio4, InputAudio5,
	InputAV1, InputAV2, InputAV3, InputAV4, InputAV5, InputAV6, InputAV7,
	InputCD, InputCoaxial1, InputCoaxial2, InputDock,
	InputHDMI1, InputHDMI2, InputHDMI3, InputHDMI4, InputHDMI5, InputHDMI6, InputHDMI7,
	InputLine1, InputLine2, InputLine3, InputMainZoneSync, InputMultiCh,
	InputOptical1, InputOptical2, InputPhono, InputTV, InputVAux,
	InputAirPlay, InputBluetMix, InputDeezer, InputIpod, InputIpodUSB, InputMCLink,
	InputNapster, InputNetRadio, InputPandora, InputPC, InputRhapsody, InputServer,
	InputSirius, InputSiriusIR, InputSiriusXM, InputSpotify, InputTidal, InputTuner,
	InputUAW, InputUSB,
}

// Known reports whether i is one of the modelled inputs.
func (i Input) Known() bool { return slices.Contains(allInputs, i) }

// ParseInput normalises a wire INP value to an Input, mapping anything
// unrecognised to InputUnknown. The raw value is matched case-sensitively
// against the wire spellings (YNCA reports them verbatim); callers that
// rendered the value keep the original via string(...).
func ParseInput(s string) Input {
	in := Input(strings.TrimSpace(s))
	if in.Known() {
		return in
	}
	return InputUnknown
}

// Inputs returns the modelled input names as plain strings, for the
// `ynca input` no-arg listing. This is the static superset across Yamaha
// models; the genuinely-supported subset is device-specific (read via
// @SYS:INPNAME=?), so callers that have a live list should prefer it.
func Inputs() []string {
	out := make([]string, 0, len(allInputs))
	for _, in := range allInputs {
		out = append(out, string(in))
	}
	return out
}

// SoundProgram is a subunit's SOUNDPRG value (the DSP program), e.g.
// "Standard", "Surround Decoder", "2ch Stereo". Ported from enums.py
// SoundPrg.
type SoundProgram string

const (
	SoundProgramUnknown SoundProgram = UnknownValue
)

// allSoundPrograms is the closed set of modelled DSP programs, in the order
// the ynca library declares them.
var allSoundPrograms = []SoundProgram{
	"Action Game", "Adventure", "Arena", "Cellar Club", "Chamber",
	"Church in Freiburg", "Church in Royaumont", "Church in Tokyo", "Disco",
	"Drama", "Enhanced", "Hall in Amsterdam", "Hall in Frankfurt",
	"Hall in Munich", "Hall in Munich A", "Hall in Munich B", "Hall in Stuttgart",
	"Hall in USA A", "Hall in USA B", "Hall in Vienna", "Mono Movie",
	"Music Video", "Pavilion", "Recital/Opera", "Roleplaying Game", "Sci-Fi",
	"Spectacle", "Sports", "Standard", "Surround Decoder", "The Bottom Line",
	"The Roxy Theatre", "Village Gate", "Village Vanguard", "Warehouse Loft",
	"2ch Stereo", "5ch Stereo", "7ch Stereo", "9ch Stereo", "11ch Stereo",
	"All-Ch Stereo",
}

// Known reports whether p is one of the modelled sound programs.
func (p SoundProgram) Known() bool { return slices.Contains(allSoundPrograms, p) }

// ParseSoundProgram normalises a wire SOUNDPRG value, mapping anything
// unrecognised to SoundProgramUnknown.
func ParseSoundProgram(s string) SoundProgram {
	sp := SoundProgram(strings.TrimSpace(s))
	if sp.Known() {
		return sp
	}
	return SoundProgramUnknown
}

// SoundPrograms returns the modelled sound-program names as plain strings,
// for the `ynca sound` no-arg listing.
func SoundPrograms() []string {
	out := make([]string, 0, len(allSoundPrograms))
	for _, sp := range allSoundPrograms {
		out = append(out, string(sp))
	}
	return out
}

// Mute is a subunit's MUTE value. Unlike the YXC backend's simple on/off,
// YNCA models attenuation mutes as first-class states (RX-V receivers
// genuinely report "Att -20 dB"/"Att -40 dB"), so collapsing the field to
// a bool — as the original parseMute did — loses real device state. Ported
// from enums.py Mute. Use Muted() when a caller only needs the boolean.
type Mute string

const (
	MuteOff     Mute = "Off"
	MuteOn      Mute = "On"
	MuteAtt20   Mute = "Att -20 dB"
	MuteAtt40   Mute = "Att -40 dB"
	MuteUnknown Mute = UnknownValue
)

// Known reports whether m is one of the modelled mute states.
func (m Mute) Known() bool {
	return m == MuteOff || m == MuteOn || m == MuteAtt20 || m == MuteAtt40
}

// Muted reports whether m represents any non-Off state (On or either
// attenuation level). An unknown value is treated as not-muted, matching
// the original boolean parseMute's "only Off is unmuted, but be
// conservative about garbage" intent — callers that care about the precise
// state branch on the typed value instead.
func (m Mute) Muted() bool {
	return m == MuteOn || m == MuteAtt20 || m == MuteAtt40
}

// ParseMute normalises a wire MUTE value to a Mute, mapping anything
// unrecognised to MuteUnknown (case-insensitive on the bare On/Off, which
// some firmware capitalises differently; the attenuation spellings are
// matched verbatim).
func ParseMute(s string) Mute {
	t := strings.TrimSpace(s)
	switch strings.ToLower(t) {
	case "off":
		return MuteOff
	case "on":
		return MuteOn
	}
	m := Mute(t)
	if m == MuteAtt20 || m == MuteAtt40 {
		return m
	}
	return MuteUnknown
}

// TwoChDecoder is a zone's 2CHDECODER value — the surround decoder applied
// to 2-channel sources (Dolby Surround, DTS Neural:X, the older Pro Logic
// II / NEO:6 family, etc.). Ported from enums.py TwoChDecoder; values vary
// by model generation, so the list is a superset and unknown values are
// preserved. The wire function name is "2CHDECODER" (not a Go identifier),
// hence the explicit Func2ChDecoder constant in types.go.
type TwoChDecoder string

const (
	TwoChDecoderUnknown TwoChDecoder = UnknownValue
)

// allTwoChDecoders is the modelled superset across model generations.
var allTwoChDecoders = []TwoChDecoder{
	"Dolby PL", "Dolby PLII Movie", "Dolby PLII Music", "Dolby PLII Game",
	"Dolby PLIIx Movie", "Dolby PLIIx Music", "Dolby PLIIx Game",
	"DTS NEO:6 Cinema", "DTS NEO:6 Music",
	"Auto", "Dolby Surround", "DTS Neural:X", "AURO-3D",
	"Dolby ProLogicII(Music)", "Dolby ProLogicII(Movie)", "Dolby ProLogicII(Game)",
}

// Known reports whether d is one of the modelled decoders.
func (d TwoChDecoder) Known() bool { return slices.Contains(allTwoChDecoders, d) }

// ParseTwoChDecoder normalises a wire 2CHDECODER value, mapping anything
// unrecognised to TwoChDecoderUnknown.
func ParseTwoChDecoder(s string) TwoChDecoder {
	d := TwoChDecoder(strings.TrimSpace(s))
	if d.Known() {
		return d
	}
	return TwoChDecoderUnknown
}

// TwoChDecoders returns the modelled decoder names as plain strings, for
// the `ynca decoder` no-arg listing. The genuinely-supported set is
// model-specific; unrecognised values still send and surface a device-side
// @RESTRICTED if unsupported.
func TwoChDecoders() []string {
	out := make([]string, 0, len(allTwoChDecoders))
	for _, d := range allTwoChDecoders {
		out = append(out, string(d))
	}
	return out
}

// Band is a TUN subunit's BAND value (the AM/FM tuner band). DAB models use
// a separate @DAB subunit not modelled here. Ported from enums.py BandTun.
type Band string

const (
	BandFM      Band = "FM"
	BandAM      Band = "AM"
	BandUnknown Band = UnknownValue
)

// Known reports whether b is one of the modelled bands.
func (b Band) Known() bool { return b == BandFM || b == BandAM }

// ParseBand normalises a wire BAND value (case-insensitive), mapping
// anything unrecognised to BandUnknown.
func ParseBand(s string) Band {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "FM":
		return BandFM
	case "AM":
		return BandAM
	}
	return BandUnknown
}

// Playback is a source subunit's PLAYBACK *action* (a write-only verb that
// drives transport on a streaming/USB source). Distinct from PlaybackInfo,
// the read-back current state. Ported from enums.py Playback.
type Playback string

const (
	PlaybackStop    Playback = "Stop"
	PlaybackPause   Playback = "Pause"
	PlaybackPlay    Playback = "Play"
	PlaybackSkipRev Playback = "Skip Rev"
	PlaybackSkipFwd Playback = "Skip Fwd"
)

// PlaybackInfo is a source subunit's PLAYBACKINFO value — the *current*
// transport state reported by the device. Ported from enums.py
// PlaybackInfo.
type PlaybackInfo string

const (
	PlaybackInfoStop    PlaybackInfo = "Stop"
	PlaybackInfoPause   PlaybackInfo = "Pause"
	PlaybackInfoPlay    PlaybackInfo = "Play"
	PlaybackInfoUnknown PlaybackInfo = UnknownValue
)

// Known reports whether p is one of the modelled playback states.
func (p PlaybackInfo) Known() bool {
	return p == PlaybackInfoStop || p == PlaybackInfoPause || p == PlaybackInfoPlay
}

// ParsePlaybackInfo normalises a wire PLAYBACKINFO value, mapping anything
// unrecognised to PlaybackInfoUnknown.
func ParsePlaybackInfo(s string) PlaybackInfo {
	switch PlaybackInfo(strings.TrimSpace(s)) {
	case PlaybackInfoStop:
		return PlaybackInfoStop
	case PlaybackInfoPause:
		return PlaybackInfoPause
	case PlaybackInfoPlay:
		return PlaybackInfoPlay
	}
	return PlaybackInfoUnknown
}
