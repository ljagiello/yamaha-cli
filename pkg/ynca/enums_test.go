package ynca

import (
	"slices"
	"testing"
)

func TestParseInput(t *testing.T) {
	if got := ParseInput("HDMI2"); got != InputHDMI2 {
		t.Errorf("ParseInput(HDMI2) = %q", got)
	}
	if got := ParseInput("NET RADIO"); got != InputNetRadio || !got.Known() {
		t.Errorf("ParseInput(NET RADIO) = %q known=%v", got, got.Known())
	}
	if got := ParseInput("totally-made-up"); got != InputUnknown || got.Known() {
		t.Errorf("ParseInput(unknown) = %q known=%v, want unknown sentinel", got, got.Known())
	}
}

func TestInputsAndSoundProgramsListed(t *testing.T) {
	if !slices.Contains(Inputs(), "TUNER") {
		t.Error("Inputs() missing TUNER")
	}
	if !slices.Contains(SoundPrograms(), "Surround Decoder") {
		t.Error("SoundPrograms() missing Surround Decoder")
	}
	// Every listed value must round-trip back as Known.
	for _, in := range Inputs() {
		if !ParseInput(in).Known() {
			t.Errorf("listed input %q does not parse as Known", in)
		}
	}
	for _, sp := range SoundPrograms() {
		if !ParseSoundProgram(sp).Known() {
			t.Errorf("listed sound program %q does not parse as Known", sp)
		}
	}
}

func TestParseMuteTriState(t *testing.T) {
	cases := []struct {
		in    string
		want  Mute
		muted bool
	}{
		{"Off", MuteOff, false},
		{"off", MuteOff, false}, // case-insensitive on/off
		{"On", MuteOn, true},
		{"Att -20 dB", MuteAtt20, true},
		{"Att -40 dB", MuteAtt40, true},
		{"weird", MuteUnknown, false}, // unknown is treated as not-muted
	}
	for _, tc := range cases {
		got := ParseMute(tc.in)
		if got != tc.want {
			t.Errorf("ParseMute(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got.Muted() != tc.muted {
			t.Errorf("ParseMute(%q).Muted() = %v, want %v", tc.in, got.Muted(), tc.muted)
		}
	}
}

func TestParseTwoChDecoderAndBand(t *testing.T) {
	if got := ParseTwoChDecoder("Dolby Surround"); got != "Dolby Surround" || !got.Known() {
		t.Errorf("ParseTwoChDecoder(Dolby Surround) = %q known=%v", got, got.Known())
	}
	if got := ParseTwoChDecoder("nonsense"); got != TwoChDecoderUnknown {
		t.Errorf("ParseTwoChDecoder(nonsense) = %q, want unknown", got)
	}
	if got := ParseBand("fm"); got != BandFM {
		t.Errorf("ParseBand(fm) = %q, want FM", got)
	}
	if got := ParseBand("AM"); got != BandAM {
		t.Errorf("ParseBand(AM) = %q, want AM", got)
	}
	if got := ParseBand("dab"); got != BandUnknown {
		t.Errorf("ParseBand(dab) = %q, want unknown (DAB is a separate subunit)", got)
	}
}

func TestParsePlaybackInfo(t *testing.T) {
	if got := ParsePlaybackInfo("Play"); got != PlaybackInfoPlay || !got.Known() {
		t.Errorf("ParsePlaybackInfo(Play) = %q", got)
	}
	if got := ParsePlaybackInfo("Buffering"); got != PlaybackInfoUnknown {
		t.Errorf("ParsePlaybackInfo(Buffering) = %q, want unknown", got)
	}
}

func TestIsSourceSubunit(t *testing.T) {
	// Real source subunit ids (case-insensitive) are accepted...
	for _, id := range []string{"SPOTIFY", "spotify", "NETRADIO", "USB", "BT"} {
		if !IsSourceSubunit(id) {
			t.Errorf("IsSourceSubunit(%q) = false, want true", id)
		}
	}
	// ...but non-source upper-case tokens are rejected (the bug fix: TUNER
	// must not be mistaken for a source subunit and sent as @TUNER:...).
	for _, id := range []string{"TUNER", "TUN", "HDMI1", "MAIN", "SYS", ""} {
		if IsSourceSubunit(id) {
			t.Errorf("IsSourceSubunit(%q) = true, want false", id)
		}
	}
}

func TestSubunitForInput(t *testing.T) {
	cases := map[string]string{
		"NET RADIO": SubunitNetRadio,
		"Spotify":   SubunitSpotify,
		"USB":       SubunitUSB,
		"SiriusXM":  SubunitSirius, // all Sirius variants map to SIRIUS
		"HDMI2":     "",            // physical input has no source subunit
		"TUNER":     "",            // tuner is @TUN, not a streaming source
	}
	for input, want := range cases {
		if got := SubunitForInput(input); got != want {
			t.Errorf("SubunitForInput(%q) = %q, want %q", input, got, want)
		}
	}
}
