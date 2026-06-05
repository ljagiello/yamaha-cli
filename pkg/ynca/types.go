package ynca

// Subunit identifiers we know about.
//
// These constants exist so command-routing code does not repeat magic
// strings; the typed control layer (control.go and the per-feature files)
// uses them, while callers may still build raw lines such as `@MAIN:PWR=?`.
const (
	SubunitSystem = "SYS"
	SubunitMain   = "MAIN"
	SubunitZone2  = "ZONE2"
	// Zone3, Zone4 are not present on RX-V583 but are included for
	// forward compatibility with larger receivers.
	SubunitZone3 = "ZONE3"
	SubunitZone4 = "ZONE4"

	// SubunitTuner is the AM/FM tuner (@TUN). DAB models expose a separate
	// @DAB subunit not modelled here.
	SubunitTuner = "TUN"
)

// Source subunits that back a streaming/network/USB Input. Used by the
// now-playing path to map a selected input to the subunit that answers
// @<SRC>:METAINFO=? / @<SRC>:PLAYBACK=. Only the subset confirmed in the
// ynca reference library is listed.
const (
	SubunitAirPlay  = "AIRPLAY"
	SubunitBluetMix = "BT"
	SubunitDeezer   = "DEEZER"
	SubunitIpod     = "IPOD"
	SubunitIpodUSB  = "IPODUSB"
	SubunitMCLink   = "MCLINK"
	SubunitNapster  = "NAPSTER"
	SubunitNetRadio = "NETRADIO"
	SubunitPandora  = "PANDORA"
	SubunitPC       = "PC"
	SubunitRhapsody = "RHAP"
	SubunitServer   = "SERVER"
	SubunitSirius   = "SIRIUS"
	SubunitSpotify  = "SPOTIFY"
	SubunitTidal    = "TIDAL"
	SubunitUAW      = "UAW"
	SubunitUSB      = "USB"
)

// Common functions across subunits. As with the subunit constants these
// name the wire FUNCTION token; the descriptor catalog in functions.go
// records each one's read/write capability.
const (
	FuncPower     = "PWR"       // On / Standby
	FuncVolume    = "VOL"       // float in dB
	FuncMute      = "MUTE"      // On / Off / Att -20 dB / Att -40 dB
	FuncInput     = "INP"       // HDMI1 / HDMI2 / ...
	FuncModelName = "MODELNAME" // e.g. "RX-V583"
	FuncVersion   = "VERSION"   // firmware version, e.g. "2.87/1.81"
	FuncBasic     = "BASIC"     // fan-out GET of a zone's common fields

	// Zone controls.
	FuncSoundProgram = "SOUNDPRG"
	FuncScene        = "SCENE"
	FuncSleep        = "SLEEP"
	FuncSpBass       = "SPBASS"
	FuncSpTreble     = "SPTREBLE"
	Func2ChDecoder   = "2CHDECODER"

	// Zone boolean DSP toggles.
	FuncPureDirMode = "PUREDIRMODE"
	FuncEnhancer    = "ENHANCER"
	FuncExBass      = "EXBASS"
	FuncAdaptiveDRC = "ADAPTIVEDRC"
	FuncStraight    = "STRAIGHT"
	FuncSurroundAI  = "SURROUNDAI"
	Func3DCinema    = "3DCINEMA"

	// Tuner (@TUN).
	FuncBand   = "BAND"
	FuncFMFreq = "FMFREQ"
	FuncAMFreq = "AMFREQ"
	FuncPreset = "PRESET"
	FuncMem    = "MEM"

	// Source playback / metadata (across the source subunits).
	FuncPlayback     = "PLAYBACK"
	FuncPlaybackInfo = "PLAYBACKINFO"
	FuncMetaInfo     = "METAINFO"

	// System.
	FuncInpName = "INPNAME"
)

// Init-group names: the fan-out GETs a single SendMulti can drain. A
// @<subunit>:<GROUP>=? returns several @<subunit>:FUNC=value report lines
// at once (the technique ynca uses to minimise round-trips).
const (
	GroupBasic     = "BASIC"
	GroupMetaInfo  = "METAINFO"
	GroupRdsInfo   = "RDSINFO"
	GroupSceneName = "SCENENAME"
	GroupInpName   = "INPNAME"
)
