package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/internal/output"
	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// Native fuzz targets for the parsers in this package that consume input we
// don't control: command-line arguments, the receiver's JSON re-decoded for
// output, UDP push events from the LAN, and local transcript / command
// files. Seeds and crashers under testdata/fuzz run on every `go test`; to
// fuzz one target:
//
//	go test -run='^$' -fuzz='^FuzzParseKVPairs$' -fuzztime=60s ./internal/cli

// requireUsageError fails unless err maps to exit code 2.
func requireUsageError(t *testing.T, err error, call string) {
	t.Helper()
	if code := ErrorExitCode(err); code != 2 {
		t.Fatalf("%s: error %v exits %d, want a usage error (2)", call, err, code)
	}
}

// maxFuzzInput caps the byte inputs the transcript and JSON targets examine.
// These parsers are line- or token-oriented, so longer inputs reach no new
// paths, but they stall the run: Go's minimizer tries O(n²) byte-range
// removals on every interesting input.
const maxFuzzInput = 512

// argSep joins fuzzed argv entries: OS arguments are C strings and can never
// contain NUL, so splitting on it loses nothing and lets the fuzzer vary the
// argument count.
const argSep = "\x00"

func FuzzParseKVPairs(f *testing.F) {
	for _, s := range []string{
		"volume=42",
		"mode=repeat\x00type=track",
		"group_id=abc123\x00type=add\x00zone=main\x00client_list[0].ip_address=192.168.1.50",
		"k=1\x00k=2\x00k=3",
		"k=", "k==v", "a=b=c", "k=a&b=c", "k=%zz", "k=v;w", "k=\xff\xfe", "ключ=значение",
		"=v", "k", "", "=", "k=v\x00=v",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, argSep)
		call := fmt.Sprintf("parseKVPairs(%q)", args)
		got, err := parseKVPairs(args)
		if slices.ContainsFunc(args, func(a string) bool { return strings.IndexByte(a, '=') <= 0 }) {
			requireUsageError(t, err, call)
			return
		}
		if err != nil {
			t.Fatalf("%s = %v, want success", call, err)
		}

		// Each arg is one value under the text before its first '=', in
		// argument order: repeated keys append.
		want := url.Values{}
		for _, a := range args {
			k, v, _ := strings.Cut(a, "=")
			want[k] = append(want[k], v)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %v, want %v", call, got, want)
		}
		// The request carries exactly what was typed.
		back, err := url.ParseQuery(got.Encode())
		if err != nil || !reflect.DeepEqual(back, got) {
			t.Fatalf("%s: query %q decodes to %v (%v), want %v", call, got.Encode(), back, err, got)
		}
	})
}

func FuzzParseSignedInt(f *testing.F) {
	for _, s := range []string{
		"3", "+3", "-3", "0", "+0", "-12", "+12", "-+3", "", "+", "-", " 3", "3 ",
		"1e3", "0x10", "1_000", "٣", "9223372036854775807", "-9223372036854775808", "9223372036854775808",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// strconv.Atoi already takes one optional leading sign, so it is the
		// whole contract: "+3" support must not admit anything more.
		got, err := parseSignedInt(s)
		want, wantErr := strconv.Atoi(s)
		if (err == nil) != (wantErr == nil) || (err == nil && got != want) {
			t.Fatalf("parseSignedInt(%q) = %d, %v; strconv.Atoi = %d, %v", s, got, err, want, wantErr)
		}
	})
}

// TestSignedArgs_RejectDoubledSigns pins the doubled-sign bug the fuzzer
// found: one leading sign is fine, but "+-3" and friends must be a usage
// error (exit 2) before any device I/O. The state has no client or host, so
// a value that wrongly parses fails with a non-usage error, never a dial.
func TestSignedArgs_RejectDoubledSigns(t *testing.T) {
	doubled := []string{"+-3", "++3", "-+3"}
	for _, v := range doubled {
		if n, err := parseSignedInt(v); err == nil {
			t.Errorf("parseSignedInt(%q) = %d, want an error", v, n)
		}
	}
	for _, c := range []struct {
		name  string
		build func() *cobra.Command
		args  func(v string) []string
	}{
		{"tone", newToneCmd, func(v string) []string { return []string{"bass", v} }},
		{"ynca tone", newYncaToneCmd, func(v string) []string { return []string{"bass", v} }},
		{"ynca volume", newYncaVolumeCmd, func(v string) []string { return []string{v} }},
	} {
		for _, v := range doubled {
			t.Run(c.name+" "+v, func(t *testing.T) {
				cmd := c.build()
				cmd.SetContext(context.Background())
				setStateOnCmd(cmd, &state{zone: "main"})
				requireUsageError(t, cmd.RunE(cmd, c.args(v)), c.name+" "+v)
			})
		}
	}
}

func FuzzParseOnOff(f *testing.F) {
	vocab := map[string]bool{
		"on": true, "true": true, "enable": true, "enabled": true, "1": true,
		"off": false, "false": false, "disable": false, "disabled": false, "0": false,
	}
	for _, s := range []string{"on", "OFF", " True\t", "Enabled", "1", "0", "yes", "", "o n", "onn", " on", "on\x00", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := parseOnOff(s)
		want, known := vocab[strings.ToLower(strings.TrimSpace(s))]
		if !known {
			requireUsageError(t, err, fmt.Sprintf("parseOnOff(%q)", s))
			return
		}
		if err != nil || got != want {
			t.Fatalf("parseOnOff(%q) = %v, %v; want %v", s, got, err, want)
		}
	})
}

func FuzzCanonicalZone(f *testing.F) {
	for _, s := range []string{"main", "MAIN", " zone2 ", "Zone3", "zone4", "zone1", "zone9", "zone", "kitchen", "", "main\x00", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := canonicalZone(s)
		norm := strings.ToLower(strings.TrimSpace(s))
		if !slices.Contains([]string{"main", "zone2", "zone3", "zone4"}, norm) {
			requireUsageError(t, err, fmt.Sprintf("canonicalZone(%q)", s))
			return
		}
		if err != nil || got != norm {
			t.Fatalf("canonicalZone(%q) = %q, %v; want %q", s, got, err, norm)
		}
	})
}

func FuzzParseYncaScope(f *testing.F) {
	for _, s := range []string{"system", "ZONE", " tuner ", "Source", "zones", "", "all", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		scope, ok := parseYncaScope(s)
		norm := strings.ToLower(strings.TrimSpace(s))
		known := slices.Contains([]string{"system", "zone", "tuner", "source"}, norm)
		if ok != known || (ok && string(scope) != norm) || (!ok && scope != "") {
			t.Fatalf("parseYncaScope(%q) = %q, %v", s, scope, ok)
		}
		// `ynca list <scope>` must never come back empty for a scope it accepts.
		if ok && len(ynca.FunctionsForScope(scope)) == 0 {
			t.Fatalf("parseYncaScope(%q) accepted scope %q with no catalog functions", s, scope)
		}
	})
}

// wireRecorder stands in for the receiver at the http.RoundTripper layer: it
// records the request and answers success, so a parsed argument is observed
// exactly as the receiver would see it, without opening a socket.
type wireRecorder struct{ req *http.Request }

func (w *wireRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	w.req = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"response_code":0}`)),
		Request:    r,
	}, nil
}

func FuzzParseVolumeArg(f *testing.F) {
	// Pre-load the per-process features memo so loadFeatures never fetches.
	feats := volumeFeatures()
	prev := fl
	fl = &featureLoader{deviceID: "FUZZ", feats: feats}
	f.Cleanup(func() { fl = prev })
	lo, hi, _, _ := feats.VolumeRange("main")
	baseline, stepDB, _ := feats.VolumeDBScale("main")

	for _, c := range []struct {
		raw         string
		db, percent bool
		step        int
	}{
		{"up", false, false, 0}, {"Down", false, false, 5}, {"UP", false, false, -1},
		{"+5", false, false, 0}, {"-5", false, false, 0}, {"+5", false, false, 2}, {"+005", false, false, 0},
		{"+9223372036854775807", false, false, 0}, {"+5", true, false, 0}, {"up", false, true, 0},
		{"-0", false, false, 3}, {"-9223372036854775808", false, false, 0},
		{"42", false, false, 0}, {"0", false, false, 0}, {"161", false, false, 0}, {"999", false, false, 0},
		{"9223372036854775807", false, false, 0}, {"0x10", false, false, 0}, {" 5", false, false, 0},
		{"-22.5", true, false, 0}, {"-80.5", true, false, 0}, {"-200", true, false, 0},
		// Non-finite and huge floats: float->int conversion of these is
		// platform-dependent (amd64 yields MinInt64, arm64 saturates).
		{"nan", true, false, 0}, {"NaN", false, true, 0}, {"inf", true, false, 0}, {"-Inf", true, false, 0},
		{"1e19", true, false, 0}, {"-1e19", true, false, 0},
		{"0", true, false, 0}, {"16.5", true, false, 0}, {"1e3", true, false, 0}, {"1e400", true, false, 0},
		{"50", false, true, 0}, {"0", false, true, 0}, {"100", false, true, 0}, {"100.5", false, true, 0},
		{"abc", false, false, 0}, {"", false, false, 0}, {"+", false, false, 0}, {"-", true, false, 0},
	} {
		f.Add(c.raw, c.db, c.percent, c.step)
	}
	f.Fuzz(func(t *testing.T, raw string, db, percent bool, step int) {
		if db && percent {
			return // runVolume rejects the pair before parsing
		}
		call := fmt.Sprintf("parseVolumeArg(%q, db=%v, percent=%v, step=%d)", raw, db, percent, step)

		rec := &wireRecorder{}
		// A fresh client per iteration also starts with a clear rate limiter.
		c, err := yxc.New("192.0.2.1", yxc.WithHTTPClient(&http.Client{Transport: rec}))
		if err != nil {
			t.Fatalf("yxc.New: %v", err)
		}
		s := &state{zone: "main", client: c}

		arg, err := parseVolumeArg(s, context.Background(), raw, db, percent, step)
		if err != nil {
			// Features are loaded, so every rejection is about the argument.
			requireUsageError(t, err, call)
			// --db takes absolute values such as the README's
			// `volume -22.5 --db`; only a '+' delta is misuse.
			if db && !strings.HasPrefix(raw, "+") {
				if v, perr := strconv.ParseFloat(raw, 64); perr == nil && !math.IsNaN(v) {
					t.Fatalf("%s rejected dB value %v: %v", call, v, err)
				}
			}
			return
		}

		if err := c.SetVolume(context.Background(), "main", arg); err != nil {
			t.Fatalf("%s: SetVolume: %v", call, err)
		}
		if rec.req == nil || !strings.HasSuffix(rec.req.URL.Path, "/main/setVolume") {
			t.Fatalf("%s: last request %v, want main/setVolume", call, rec.req)
		}
		q := rec.req.URL.Query()
		vol, gotStep := q.Get("volume"), q.Get("step")

		if vol == "up" || vol == "down" {
			if db || percent {
				t.Fatalf("%s: relative volume=%s accepted with --db/--percent", call, vol)
			}
			wantStep := ""
			if step > 0 {
				wantStep = strconv.Itoa(step)
			}
			if raw == "up" || raw == "UP" || raw == "Up" || raw == "down" || raw == "DOWN" || raw == "Down" {
				if !strings.EqualFold(vol, raw) || gotStep != wantStep {
					t.Fatalf("%s sent volume=%s step=%q, want volume=%s step=%q", call, vol, gotStep, strings.ToLower(raw), wantStep)
				}
				return
			}
			// Signed delta: the sign picks the direction and the magnitude
			// is the step, unless --step overrides it.
			if len(raw) < 2 || (raw[0] != '+' && raw[0] != '-') {
				t.Fatalf("%s: relative volume=%s from a non-delta argument", call, vol)
			}
			mag, perr := strconv.ParseUint(raw[1:], 10, 64)
			if perr != nil {
				t.Fatalf("%s: relative volume=%s from a non-numeric delta", call, vol)
			}
			wantDir := "up"
			if raw[0] == '-' {
				wantDir = "down"
			}
			if wantStep == "" {
				wantStep = strconv.FormatUint(mag, 10)
			}
			if vol != wantDir || gotStep != wantStep {
				t.Fatalf("%s sent volume=%s step=%q, want volume=%s step=%q", call, vol, gotStep, wantDir, wantStep)
			}
			return
		}

		n, err := strconv.Atoi(vol)
		if err != nil || n < lo || n > hi || gotStep != "" {
			t.Fatalf("%s sent volume=%q step=%q, want an absolute volume in [%d,%d]", call, vol, gotStep, lo, hi)
		}
		switch {
		case db:
			v, _ := strconv.ParseFloat(raw, 64)
			if math.IsNaN(v) {
				t.Fatalf("%s: NaN dB accepted as volume=%d", call, n)
			}
			// The nearest wire step to v, clamped to the device range.
			want := lo
			switch x := (v - baseline) / stepDB; {
			case x >= float64(hi):
				want = hi
			case x > float64(lo):
				want = int(math.Round(x))
			}
			if n != want {
				t.Fatalf("%s sent volume=%d, want %d", call, n, want)
			}
		case percent:
			if v, _ := strconv.ParseFloat(raw, 64); !(v >= 0 && v <= 100) {
				t.Fatalf("%s: percent %v outside [0,100] accepted as volume=%d", call, v, n)
			}
		default:
			v, _ := strconv.Atoi(raw)
			if want := min(max(v, lo), hi); n != want {
				t.Fatalf("%s sent volume=%d, want %d", call, n, want)
			}
		}
	})
}

func FuzzDecodeRawReply(f *testing.F) {
	for _, s := range []string{
		`{"response_code":0}`,
		`{"response_code":0,"power":"on","volume":42,"mute":false,"input":"hdmi1","tone_control":{"mode":"manual","bass":-2}}`,
		`{"response_code":0,"client_list":[{"ip_address":"192.168.1.50"}]}`,
		``, `null`, `[]`, `[1,"a",null]`, `"str"`, `42`, `1e400`, `{"a":1e400}`, `{"a":1}{"b":2}`, `{"a":`,
		`{"a":"\ud800"}`, "\xff\xfe", `{"":{"":{}}}`, `{"a":-0}`, `{"big":12345678901234567890}`, `{"k":"l1\nl2"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxFuzzInput {
			return
		}
		v := decodeRawReply(raw)
		if len(raw) > 0 && !json.Valid(raw) && v != string(raw) {
			t.Fatalf("decodeRawReply(%q) = %#v, want the bytes surfaced as a string", raw, v)
		}
		// Whatever the receiver sent must print in every output format.
		for _, format := range []output.Format{output.FormatJSON, output.FormatYAML, output.FormatTable} {
			var buf bytes.Buffer
			if err := output.Render(&buf, v, format, false); err != nil {
				t.Fatalf("render %v of decodeRawReply(%q): %v", format, raw, err)
			}
			if format != output.FormatJSON {
				continue
			}
			var back any
			if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
				t.Fatalf("JSON output %q for %q is not JSON: %v", buf.Bytes(), raw, err)
			}
			// `raw -o json` prints the reply itself (an invalid-UTF-8
			// fallback string is the one value JSON can't carry verbatim).
			if s, isStr := v.(string); v != nil && (!isStr || utf8.ValidString(s)) && !reflect.DeepEqual(back, v) {
				t.Fatalf("JSON output %q for %q decodes to %#v, want %#v", buf.Bytes(), raw, back, v)
			}
		}
	})
}

func FuzzFormatWatchEvent(f *testing.F) {
	for _, s := range []string{
		`{"main":{"volume":60,"signal_info_updated":true},"device_id":"00A0DEADBEEF"}`,
		`{"netusb":{"play_info_updated":true,"play_time":12}}`,
		`{"main":{"input":"hdmi1"},"zone2":{"power":"standby"}}`,
		`{}`, `null`, `[]`, `42`, `"str"`, `{"a.b":1,"a":{"b":2}}`, `{"":1}`, `{"k":"l1\nl2"}`,
		`{"a":`, "\xff\xfe", "not json\n", `{"a":1e400}`,
	} {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	f.Add([]byte(nil), false)
	f.Add([]byte(nil), true)
	const alias = "living-room"
	f.Fuzz(func(t *testing.T, raw []byte, useTable bool) {
		if len(raw) > maxFuzzInput {
			return
		}
		line := formatWatchEvent(alias, &yxc.Event{Raw: raw}, useTable)
		if len(raw) == 0 {
			if line != "" {
				t.Fatalf("empty event rendered as %q, want it dropped", line)
			}
			return
		}
		if !strings.HasSuffix(line, "\n") {
			t.Fatalf("event %q rendered as %q, want a newline-terminated line", raw, line)
		}
		if useTable {
			if !strings.Contains(line, "  "+alias+"  ") {
				t.Fatalf("table line %q for %q lacks the device alias", line, raw)
			}
			return
		}

		// NDJSON: exactly one line holding one JSON object.
		if n := strings.Count(line, "\n"); n != 1 {
			t.Fatalf("event %q rendered as %d lines, want one NDJSON line: %q", raw, n, line)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("NDJSON line %q for %q is not a JSON object: %v", line, raw, err)
		}
		if obj["device"] != alias || obj["ts"] == nil || obj["event"] != nil {
			t.Fatalf("NDJSON line %q for data event %q: want ts, device=%q and no event", line, raw, alias)
		}
		delta, ok := obj["delta"]
		if !ok {
			t.Fatalf("NDJSON line %q for %q has no delta", line, raw)
		}
		var want any
		if json.Unmarshal(raw, &want) == nil {
			if !reflect.DeepEqual(delta, want) {
				t.Fatalf("delta for %q = %#v, want %#v", raw, delta, want)
			}
		} else if s, isStr := delta.(string); !isStr || (utf8.Valid(raw) && s != string(raw)) {
			t.Fatalf("undecodable event %q: delta = %#v, want the raw bytes as a string", raw, delta)
		}
	})
}

func FuzzScanTranscript(f *testing.F) {
	f.Add([]byte("# a dump header comment\n@MAIN:PWR=On\n@MAIN:VOL=-30.0\n# @ZONE3:PWR=? -> @UNDEFINED\n" +
		"@tun:band=FM\ngarbage line without at\n@BAD\n@MAIN:INP=HDMI2\",\n\n"))
	f.Add([]byte("# yamaha-cli YNCA dump\n# device: 192.168.1.116\n# commands: 2\n@MAIN:PWR=On\n# @ZONE9:PWR=? -> @UNDEFINED\n"))
	f.Add([]byte("@MAIN:VOL=-30.0\r\n@SYS:VERSION=2.87/1.81\r\n"))
	f.Add([]byte("@:X=1\n@A:=1\n@A=B:C\n@a:b:c=d\n@ä:ß=1\n@\xff:\xfe=1\n\x00@A:B=1\n  @A:B=1  \n@A:B\r=1"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			return
		}
		set, err := scanTranscript(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("scanTranscript(%q): %v", data, err)
		}
		for k := range set {
			su, fn := splitSubunitFunc(k)
			if su == "" || fn == "" || "@"+su+":"+fn != k || strings.ContainsAny(su, ":=") || strings.Contains(fn, "=") {
				t.Fatalf("scanTranscript(%q) yielded malformed key %q", data, k)
			}
		}
		// Comments never count: the same transcript with every line
		// commented out reports nothing.
		commented := []byte("#" + strings.ReplaceAll(string(data), "\n", "\n#"))
		if set, err := scanTranscript(bytes.NewReader(commented)); err == nil && len(set) != 0 {
			t.Fatalf("commented-out transcript %q reported %v", commented, keysOf(set))
		}
	})
}

func FuzzDumpTranscript(f *testing.F) {
	for _, c := range [][2]string{
		{"@MAIN:PWR=?", "@MAIN:PWR=On"},
		{"@ZONE9:PWR=?", "@UNDEFINED"},
		{"@MAIN:SPBASS=?", "@RESTRICTED"},
		{"@MAIN:BASIC=?", "@MAIN:VOL=-30.0"},
		{"@TUN:RDSINFO=?", "@TUN:RDSPRGSERVICE=  JAZZ  "},
		{"@MAIN:PWR=?", ""},
		{"@MAIN:PWR=?", "garbage"},
		{"@MAIN:PWR=?", "@UNDEFINED\r@MAIN:PWR=On"},
		// A bare LF inside one CRLF-framed reply must not escape into
		// extra transcript lines.
		{"@ZONE9:PWR=?", "@UNDEFINED\n@ZONE9:PWR=On"},
		{"@MAIN:BASIC=?", "@MAIN:VOL=-30.0\n@MAIN:MUTE=Off"},
	} {
		f.Add(c[0], c[1])
	}
	f.Fuzz(func(t *testing.T, request, reply string) {
		// Requests come from a line-scanned file or the built-in catalog,
		// and splitCRLF never yields a reply containing "\r\n".
		if strings.Contains(request, "\n") || strings.Contains(reply, "\r\n") || len(request)+len(reply) > maxFuzzInput {
			return
		}
		var buf bytes.Buffer
		writeDumpReplies(&buf, request, []string{reply})
		set, err := scanTranscript(&buf)
		if err != nil {
			t.Fatalf("scanTranscript of dump %q: %v", buf.String(), err)
		}
		// One reply reports at most one function, and a rejected one none.
		rejected := strings.HasPrefix(reply, "@UNDEFINED") || strings.HasPrefix(reply, "@RESTRICTED")
		if len(set) > 1 || (rejected && len(set) != 0) {
			t.Fatalf("reply %q to %q dumped as %q reports %d functions", reply, request, buf.Bytes(), len(set))
		}
	})
}

func FuzzScanDumpCommands(f *testing.F) {
	f.Add([]byte("# scoped dump\n@MAIN:PWR=?\n\n  @MAIN:VOL=?  \r\n@TUN:BAND=?"))
	f.Add([]byte("# only comments\n\n   \n#@MAIN:PWR=?\n"))
	f.Add([]byte(""))
	f.Add([]byte("@A\x00B=?\n\xff\xfe\n\t#x\n@A:B=?\r@C:D=?"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			return
		}
		cmds, err := scanDumpCommands(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("scanDumpCommands(%q): %v", data, err)
		}
		for _, c := range cmds {
			if c == "" || c != strings.TrimSpace(c) || strings.HasPrefix(c, "#") || strings.Contains(c, "\n") || !bytes.Contains(data, []byte(c)) {
				t.Fatalf("scanDumpCommands(%q) yielded command %q", data, c)
			}
		}
	})
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func FuzzSlugify(f *testing.F) {
	for _, s := range []string{
		"RX-V583 FBE863", "  My Living Room!!  ", "", "Café", "!!!", "living   room", "--a--",
		"K", "İstanbul", "\xff\xfe", "a\x00b", strings.Repeat("ab-", 100),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		slug := slugify(name)
		if !slugPattern.MatchString(slug) {
			t.Fatalf("slugify(%q) = %q, want lowercase alnum runs joined by single dashes", name, slug)
		}
		if again := slugify(slug); again != slug {
			t.Fatalf("slugify not idempotent: %q -> %q -> %q", name, slug, again)
		}
		cfg := &config.Config{Devices: map[string]config.Device{slug: {}, slug + "-2": {}}}
		alias := uniqueAlias(slug, cfg)
		if _, taken := cfg.Devices[alias]; taken {
			t.Fatalf("uniqueAlias(%q) = %q collides with a configured alias", slug, alias)
		}
	})
}
