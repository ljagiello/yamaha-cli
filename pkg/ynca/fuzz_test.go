package ynca

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
)

// Fuzz targets for every function that decodes bytes the receiver sends
// over TCP/50000: line framing, the @SUBUNIT:FUNCTION=value grammar, reply
// classification, the typed value parsers, and the fan-out decoders. Each
// target asserts properties beyond "does not panic". Run one with e.g.
//
//	go test -run='^$' -fuzz='^FuzzParseLine$' -fuzztime=60s ./pkg/ynca
//
// Plain `go test` replays only the seeds below plus testdata/fuzz/.

// maxFuzzFrame bounds stream inputs to about a full BASIC/METAINFO fan-out.
// Longer streams add no new decoding paths, but Go minimises every new
// interesting input in O(n²) execs and pauses fuzzing meanwhile, so large
// inputs stall the run. It also keeps lines far below bufio's 64 KiB token
// cap (dialLocked and Session.Run), so ErrTooLong never fires.
const maxFuzzFrame = 1 << 10

// maxOneByteFrame bounds the byte-at-a-time framing check: each partial
// read rescans the unterminated line, so its cost grows quadratically.
const maxOneByteFrame = 256

// wire turns a newline-separated transcript into CRLF-framed receiver bytes.
func wire(transcript string) []byte {
	return []byte(strings.ReplaceAll(transcript, "\n", "\r\n"))
}

// streamSeeds are raw receiver byte streams: the package's transcripts plus
// fan-outs and framing edge cases.
var streamSeeds = [][]byte{
	wire(rxv583Transcript),
	wire(amTranscript),
	wire(fmRdsTranscript),
	[]byte("@MAIN:PWR=On\r\n@ZONE2:PWR=Standby\r\n@MAIN:INP=HDMI2"),
	[]byte("@MAIN:SCENE3NAME=Music\r\n@MAIN:SCENE1NAME=Movie\r\n@main:scene2name=TV\r\n@MAIN:SCENENAME=x\r\n"),
	[]byte("@UNDEFINED\r\n@RESTRICTED\r\n@SYS:VERSION=2.87/1.81\r\n"),
	[]byte(""),
	[]byte("\r"),
	[]byte("\r\n"),
	[]byte("a\r"),
	[]byte("\r\r\n\n"),
	[]byte("@MAIN:INP=HDMI2\x00\r\n@MAIN:ARTIST=\xff\xfe\r\n"),
}

// scanCRLF frames r the way the client and session do.
func scanCRLF(r io.Reader) ([]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	sc.Split(splitCRLF)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func FuzzSplitCRLF(f *testing.F) {
	for _, s := range streamSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzFrame {
			t.Skip()
		}
		lines, err := scanCRLF(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("scan(%q): %v", data, err)
		}
		if (len(lines) == 0) != (len(data) == 0) {
			t.Fatalf("scan(%q) = %q: lines must be empty exactly when the input is", data, lines)
		}
		for _, ln := range lines {
			if strings.Contains(ln, "\r\n") {
				t.Fatalf("scan(%q): line %q still contains CRLF", data, ln)
			}
		}
		// Lossless framing: the lines rejoined with CRLF are the stream,
		// minus only the final terminator (CRLF, or a lone CR at EOF).
		var term string
		switch {
		case bytes.HasSuffix(data, []byte("\r\n")):
			term = "\r\n"
		case bytes.HasSuffix(data, []byte("\r")):
			term = "\r"
		}
		if got := strings.Join(lines, "\r\n") + term; got != string(data) {
			t.Fatalf("scan(%q) = %q: rejoined %q", data, lines, got)
		}
		// TCP may deliver a line in any number of reads; byte-at-a-time
		// delivery must frame identically.
		if len(data) <= maxOneByteFrame {
			slow, err := scanCRLF(iotest.OneByteReader(bytes.NewReader(data)))
			if err != nil || !slices.Equal(slow, lines) {
				t.Fatalf("one-byte scan(%q) = %q, %v; want %q", data, slow, err, lines)
			}
		}
	})
}

// lineSeeds are single receiver lines: TestParseLine's cases plus grammar
// edge cases.
var lineSeeds = []string{
	"@MAIN:PWR=On",
	"@SYS:VERSION=2.87/1.81",
	"@MAIN:INP=",
	"@MAIN:MUTE=Att -20 dB",
	"@NETRADIO:STATION=Radio: Paradise = Rock",
	"MAIN:PWR=On",
	"@MAIN=On",
	"@:FUNC=v",
	"@MAIN:=v",
	"@MAIN:PWR",
	"@MAIN=PWR:On",
	"@A:B:C=v",
	"@UNDEFINED",
	"@RESTRICTED",
	"@UNDEFINED:MAIN=x",
	"",
	"@",
	"@MAIN:ARTIST=\xff\xfe\x00\r",
	"@MAIN:SONG=" + strings.Repeat("x", 1024),
}

func FuzzParseLine(f *testing.F) {
	for _, s := range lineSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		su, fn, val, err := parseLine(line)
		if err != nil {
			var pe *ProtocolError
			if !errors.As(err, &pe) || pe.Line != line {
				t.Fatalf("parseLine(%q) err = %v, want *ProtocolError for the line", line, err)
			}
			if su != "" || fn != "" || val != "" {
				t.Fatalf("parseLine(%q) = (%q,%q,%q) alongside error", line, su, fn, val)
			}
			if IsTransport(err) {
				t.Fatalf("parseLine(%q): malformed reply classified as transport", line)
			}
			return
		}
		if su == "" || fn == "" {
			t.Fatalf("parseLine(%q) = (%q,%q,%q): empty subunit or function", line, su, fn, val)
		}
		if strings.ContainsAny(su, ":=") || strings.Contains(fn, "=") {
			t.Fatalf("parseLine(%q) = (%q,%q,%q): delimiter leaked into a token", line, su, fn, val)
		}
		if got := "@" + su + ":" + fn + "=" + val; got != line {
			t.Fatalf("parseLine(%q) re-formats to %q", line, got)
		}
	})
}

func FuzzParseReport(f *testing.F) {
	for _, s := range lineSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		rep, ok := parseReport(line)
		su, fn, val, perr := parseLine(line)
		control := strings.HasPrefix(line, "@UNDEFINED") || strings.HasPrefix(line, "@RESTRICTED")
		if ok != (perr == nil || control) {
			t.Fatalf("parseReport(%q) ok=%v; parseLine err=%v, control=%v", line, ok, perr, control)
		}
		switch {
		case !ok:
			if rep != (Report{}) {
				t.Fatalf("parseReport(%q) dropped but returned %+v", line, rep)
			}
		case perr == nil:
			if want := (Report{Subunit: su, Function: fn, Value: val, Raw: line}); rep != want {
				t.Fatalf("parseReport(%q) = %+v, want %+v", line, rep, want)
			}
		default:
			if rep.Status != "UNDEFINED" && rep.Status != "RESTRICTED" ||
				!strings.HasPrefix(line, "@"+rep.Status) ||
				rep.Subunit != "" || rep.Function != "" || rep.Value != "" || rep.Raw != line {
				t.Fatalf("parseReport(%q) = %+v, want a bare status report", line, rep)
			}
		}
	})
}

func FuzzClassifyReply(f *testing.F) {
	for _, s := range lineSeeds {
		f.Add("@MAIN:PWR=?", s)
	}
	f.Fuzz(func(t *testing.T, request, reply string) {
		got, err := classifyReply(request, reply)
		var und *ErrUndefinedCommand
		var res *ErrRestricted
		switch {
		case strings.HasPrefix(reply, "@UNDEFINED"):
			if got != "" || !errors.As(err, &und) || und.Request != request || und.Line != reply {
				t.Fatalf("classifyReply(%q, %q) = (%q, %v), want *ErrUndefinedCommand", request, reply, got, err)
			}
		case strings.HasPrefix(reply, "@RESTRICTED"):
			if got != "" || !errors.As(err, &res) || res.Request != request || res.Line != reply {
				t.Fatalf("classifyReply(%q, %q) = (%q, %v), want *ErrRestricted", request, reply, got, err)
			}
		default:
			if err != nil || got != reply {
				t.Fatalf("classifyReply(%q, %q) = (%q, %v), want the reply unchanged", request, reply, got, err)
			}
			return
		}
		if IsTransport(err) || err.Error() == "" {
			t.Fatalf("classifyReply(%q, %q): control reply error %v must be a described, non-transport error", request, reply, err)
		}
	})
}

// enum is the shape every typed YNCA value shares: a wire string with a
// Known() check.
type enum interface {
	~string
	Known() bool
}

// checkEnum asserts the shared Parse* contract on one input: the result is
// a modelled value or the type's unknown sentinel, re-parsing it is a no-op,
// and a modelled result is the trimmed wire value itself (up to case where
// the parser is case-insensitive).
func checkEnum[T enum](t *testing.T, parse func(string) T, unknown T, foldCase bool, s string) {
	t.Helper()
	got := parse(s)
	if !got.Known() && got != unknown {
		t.Fatalf("%T: parse(%q) = %q, neither modelled nor the unknown sentinel", got, s, got)
	}
	if again := parse(string(got)); again != got {
		t.Fatalf("%T: parse(%q) = %q but parse(%q) = %q", got, s, got, got, again)
	}
	if !got.Known() {
		return
	}
	want := strings.TrimSpace(s)
	if foldCase && !strings.EqualFold(string(got), want) || !foldCase && string(got) != want {
		t.Fatalf("%T: parse(%q) = %q, not the wire value", got, s, got)
	}
}

func FuzzParseEnums(f *testing.F) {
	seeds := []string{
		"", " ", "HDMI2", " NET RADIO ", "Main Zone Sync", "Standard", "2ch Stereo",
		"On", "off", "OFF", "Att -20 dB", "Att -40 dB", "att -20 db", "Dolby Surround",
		"DTS Neural:X", "FM", "am", "dab", "Play", "Stop", "Pause", "play", "Standby",
		"STANDBY", "Spotify", "SiriusXM", "AirPlay", UnknownValue, "\xff", "On\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkEnum(t, ParseInput, InputUnknown, false, s)
		checkEnum(t, ParseSoundProgram, SoundProgramUnknown, false, s)
		checkEnum(t, ParseTwoChDecoder, TwoChDecoderUnknown, false, s)
		checkEnum(t, ParsePlaybackInfo, PlaybackInfoUnknown, false, s)
		checkEnum(t, ParseMute, MuteUnknown, true, s)
		checkEnum(t, ParseBand, BandUnknown, true, s)
		checkEnum(t, ParsePowerState, PowerUnknown, true, s)

		if m := ParseMute(s); m.Muted() != (m.Known() && m != MuteOff) {
			t.Fatalf("ParseMute(%q) = %q: Muted()=%v", s, m, m.Muted())
		}
		p, err := ParsePower(s)
		if (err == nil) != p.Known() || err == nil && p != ParsePowerState(s) || err != nil && p != "" {
			t.Fatalf("ParsePower(%q) = (%q, %v) disagrees with ParsePowerState = %q", s, p, err, ParsePowerState(s))
		}
		if sub := SubunitForInput(s); sub != "" && (!IsSourceSubunit(sub) || !ParseInput(s).Known()) {
			t.Fatalf("SubunitForInput(%q) = %q: not a source subunit of a modelled input", s, sub)
		}
	})
}

func FuzzSleepMinutes(f *testing.F) {
	for _, s := range []string{"Off", "30 min", " 120 min ", "45 min", "off", "", "30min", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, wireValue string) {
		m, ok := sleepMinutes(wireValue)
		if !ok {
			return
		}
		// Round-trip: a recognised reading is one SetSleep accepts and
		// encodes back to the same wire value.
		if back, ok := sleepWire(m); !ok || back != strings.TrimSpace(wireValue) {
			t.Fatalf("sleepMinutes(%q) = %d, but sleepWire(%d) = (%q, %v)", wireValue, m, m, back, ok)
		}
	})
}

func FuzzSceneNameIndex(f *testing.F) {
	for _, s := range []string{"SCENE1NAME", "scene12name", "SCENENAME", "SCENE0NAME", "SCENE-1NAME", "SCENE+3NAME", "SCENE03NAME", "SCENE1", "NAME", "SCENE99999999999999999999NAME"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, fn string) {
		n, ok := sceneNameIndex(fn)
		if !ok {
			return
		}
		if n < 1 {
			t.Fatalf("sceneNameIndex(%q) = %d, want a scene number >= 1", fn, n)
		}
		canon := "SCENE" + strconv.Itoa(n) + "NAME"
		if back, ok := sceneNameIndex(canon); !ok || back != n {
			t.Fatalf("sceneNameIndex(%q) = %d, but canonical %q gives (%d, %v)", fn, n, canon, back, ok)
		}
	})
}

func FuzzParseNumber(f *testing.F) {
	for _, s := range []string{"-30.5", " 98.50 ", "1530", "-0.0", "16.5", "", "Up", "-", "1e999", "0x1p-2", "1_0", "--1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		n, err := parseNumber(v)
		if err == nil && (math.IsNaN(n) || math.IsInf(n, 0)) {
			t.Fatalf("parseNumber(%q) = %v, want a finite dB/MHz value or an error", v, n)
		}
	})
}

// checkRaw asserts a fan-out decoder's Raw map holds exactly the function
// tokens of the lines addressed to subunit, each with a value one of those
// lines carried.
func checkRaw(t *testing.T, subunit string, raw map[string]string, lines []string) {
	t.Helper()
	reported := map[string][]string{}
	for _, ln := range lines {
		if su, fn, val, err := parseLine(ln); err == nil && strings.EqualFold(su, subunit) {
			reported[fn] = append(reported[fn], val)
		}
	}
	if len(raw) != len(reported) {
		t.Fatalf("%s: Raw has %d functions, lines report %d: %q", subunit, len(raw), len(reported), raw)
	}
	for fn, val := range raw {
		if !slices.Contains(reported[fn], val) {
			t.Fatalf("%s: Raw[%q] = %q, not a value any %s line reported", subunit, fn, val, subunit)
		}
	}
}

func FuzzDecodeFanOut(f *testing.F) {
	for _, s := range streamSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzFrame {
			t.Skip()
		}
		lines, err := scanCRLF(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("scan(%q): %v", data, err)
		}

		st := decodeStatus(SubunitMain, lines)
		checkRaw(t, SubunitMain, st.Raw, lines)
		if st.Mute != st.MuteState.Muted() {
			t.Fatalf("Status Mute=%v but MuteState %q", st.Mute, st.MuteState)
		}
		// VolumeRaw is the presence flag for Volume: when set, Volume must
		// be its decoded value.
		if st.VolumeRaw != "" {
			if db, err := parseNumber(st.VolumeRaw); err != nil || db != st.Volume {
				t.Fatalf("Status VolumeRaw=%q (parses to %v, %v) but Volume=%v", st.VolumeRaw, db, err, st.Volume)
			}
		}

		np := decodeMetaInfo(SubunitSpotify, lines)
		checkRaw(t, SubunitSpotify, np.Raw, lines)

		rds := decodeRDSInfo(lines)
		checkRaw(t, SubunitTuner, rds.Raw, lines)

		names := decodeSceneNames(SubunitMain, lines)
		want := 0
		for _, ln := range lines {
			if su, fn, _, err := parseLine(ln); err == nil && strings.EqualFold(su, SubunitMain) {
				if _, ok := sceneNameIndex(fn); ok {
					want++
				}
			}
		}
		if len(names) != want {
			t.Fatalf("decodeSceneNames returned %d names, lines carry %d", len(names), want)
		}
		if !slices.IsSortedFunc(names, func(a, b SceneName) int { return cmp.Compare(a.Num, b.Num) }) {
			t.Fatalf("decodeSceneNames not ordered by scene number: %+v", names)
		}
	})
}
