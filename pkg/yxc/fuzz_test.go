package yxc

// Fuzz targets for every place this package turns untrusted input into Go
// values: receiver HTTP replies, the on-disk features cache, device-reported
// volume scales, and device-supplied strings that reach file paths or
// suggestion ranking. The seeds below run on every plain `go test`; fuzz one
// target with e.g.
//
//	go test -run='^$' -fuzz='^FuzzVolumeScale$' -fuzztime=60s ./pkg/yxc
//
// Go leaves out-of-range float→int conversions implementation-defined
// (arm64 saturates, amd64 yields MinInt64), so fuzz FuzzVolumeScale with
// GOARCH=amd64 too.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// replyTransport answers every request with HTTP 200 and a fixed body,
// without touching the network, and counts how often it was asked.
type replyTransport struct {
	body  []byte
	calls int
}

func (rt *replyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(rt.body)),
	}, nil
}

// replyClient returns a Client whose every request is answered with body.
// Use a fresh one per call: the per-client rate limiter would otherwise
// sleep between calls.
func replyClient(t *testing.T, body []byte) (*Client, *replyTransport) {
	t.Helper()
	rt := &replyTransport{body: body}
	c, err := New("192.0.2.1", WithHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, rt
}

// rxv583Features is the real RX-V583 getFeatures capture.
func rxv583Features(f *testing.F) []byte {
	f.Helper()
	b, err := os.ReadFile("../../testdata/getFeatures.json")
	if err != nil {
		f.Fatalf("read fixture: %v", err)
	}
	return b
}

// addReplySeeds seeds f with realistic receiver replies (the package's test
// fixtures and the real getFeatures capture) plus malformed and hostile ones.
func addReplySeeds(f *testing.F) {
	feats := rxv583Features(f)
	// Large seeds stall the fuzzer's minimiser; ~2 KiB is deep enough.
	nested := strings.Repeat("[", 1000) + strings.Repeat("]", 1000)
	for _, s := range []string{
		string(feats),
		string(feats[:len(feats)/2]), // truncated mid-document
		`{"response_code":0,"model_name":"RX-V583","device_id":"AC44F2000000","system_version":2.87,"api_version":2.11}`,
		`{"response_code":0,"power":"on","volume":60,"mute":false,"input":"hdmi2","sound_program":"standard"}`,
		`{"response_code":0,"power":"standby","volume":99,"actual_volume":{"mode":"db","value":-31.0,"unit":"dB"}}`,
		`{"response_code":0,"input":"server","playback":"play","repeat":"off","shuffle":"on","play_time":42,"total_time":300,"artist":"A","album":"B","track":"T","albumart_url":"/u","albumart_id":7}`,
		`{"response_code":0,"playback":"pause","play_time":-60,"total_time":0}`,
		`{"response_code":0,"menu_name":"USB","max_line":8,"index":0,"total":2,"menu_layer":1,"playing_index":0,"list_info":[{"text":"a","attribute":1},{"text":"b","thumbnail":"http://x"}]}`,
		`{"response_code":0,"preset_info":[{"input":"server","text":"BBC"},{"input":"net_radio","text":"Radio4"}]}`,
		`{"response_code":0,"band":"fm","fm":{"freq":8750,"preset":1,"audio_mode":"stereo"},"am":{"freq":0,"preset":0}}`,
		`{"response_code":0,"preset_info":[{"band":"fm","number":1,"freq":8850},{"band":"fm","number":2,"freq":9020}]}`,
		`{"response_code":0,"group_id":"abc","role":"server","server_zone":"main","client_list":[{"ip_address":"10.0.0.2","zone":"main"},{"ip_address":"10.0.0.3"}],"build_device":"foo","audio_dropout_count":3}`,
		`{"response_code":0,"zone":[{"id":"main","range_step":[{"id":"volume","min":10,"max":0,"step":0},{"id":"actual_volume_db","min":0,"max":-1,"step":-0.5}]}]}`,
		`{"response_code":0,"zone":[{"id":"MAIN","range_step":[{"id":"volume","min":-1e300,"max":1e18,"step":1e-300}]}]}`,
		`{"response_code":5}`,
		`{"response_code":6}`,
		`{"response_code":-1}`,
		`{"response_code":4294967296}`,
		`{"response_code":5,"volume":"loud"}`, // device error must win over a mistyped payload
		`{"response_code":"0"}`,
		`{"response_code":1.5}`,
		`{"response_code":0,"volume":1e400}`,
		`{"response_code":0,"input":"` + "\xff\xfe" + `"}`,
		`{"response_code":0,"x":` + nested + `}`,
		nested,
		``,
		`null`,
		`{}`,
		`[]`,
		`"x"`,
	} {
		f.Add([]byte(s))
	}
}

// FuzzDoResponse fuzzes the response_code envelope every YXC reply passes
// through (Client.Do / doOnce).
func FuzzDoResponse(f *testing.F) {
	addReplySeeds(f)
	f.Fuzz(func(t *testing.T, body []byte) {
		const method = "main/getStatus"
		c, rt := replyClient(t, body)
		raw, err := c.Do(context.Background(), method, nil)

		if rt.calls != 1 {
			t.Fatalf("RoundTrip calls = %d, want 1: a device reply is never retried", rt.calls)
		}
		if IsTransport(err) {
			t.Fatalf("device reply surfaced as a transport error: %v", err)
		}

		// The envelope contract: decode only response_code from the
		// (size-capped) body; non-zero is a typed *Error.
		seen := body[:min(len(body), maxResponseBody)]
		var head struct {
			ResponseCode int `json:"response_code"`
		}
		headErr := json.Unmarshal(seen, &head)
		yerr, isYXC := AsYXC(err)
		switch {
		case headErr != nil:
			if err == nil || isYXC {
				t.Fatalf("undecodable reply: err = %v, want a plain decode error", err)
			}
		case head.ResponseCode != codeOK:
			if !isYXC || yerr.Code != head.ResponseCode || yerr.Method != method {
				t.Fatalf("response_code=%d: err = %#v, want *Error{Code: %d, Method: %q}", head.ResponseCode, err, head.ResponseCode, method)
			}
			if errors.Is(err, ErrNotFound) != (yerr.Code == codeNotFound) ||
				errors.Is(err, ErrDeviceNotReady) != (yerr.Code == codeDeviceNotReady) {
				t.Fatalf("response_code=%d matches the wrong sentinel", yerr.Code)
			}
		default:
			if err != nil {
				t.Fatalf("response_code=0: unexpected error %v", err)
			}
			if !bytes.Equal(raw, seen) {
				t.Fatalf("Do returned %q, want the reply verbatim", raw)
			}
		}
	})
}

// fetchTyped runs one typed getter against body and checks the contract
// every getter shares with Do: a device error (response_code != 0) surfaces
// as *Error whatever the rest of the body holds, and a reply Do rejects is
// never accepted. It returns the decoded value, or nil on error.
func fetchTyped[T any](t *testing.T, name string, body []byte, doErr error, get func(*Client) (*T, error)) *T {
	t.Helper()
	c, _ := replyClient(t, body)
	v, err := get(c)
	if want, ok := AsYXC(doErr); ok {
		if got, ok := AsYXC(err); !ok || got.Code != want.Code {
			t.Fatalf("%s: response_code=%d surfaced as %v, want *Error", name, want.Code, err)
		}
		return nil
	}
	if doErr != nil && err == nil {
		t.Fatalf("%s: accepted a reply Do rejects (%v)", name, doErr)
	}
	if err != nil {
		return nil
	}
	if v == nil {
		t.Fatalf("%s: nil value with nil error", name)
	}
	return v
}

// FuzzTypedDecoders feeds one reply body to every typed getter.
func FuzzTypedDecoders(f *testing.F) {
	addReplySeeds(f)
	f.Fuzz(func(t *testing.T, body []byte) {
		ctx := context.Background()
		c, _ := replyClient(t, body)
		_, doErr := c.Do(ctx, "x", nil)

		fetchTyped(t, "GetDeviceInfo", body, doErr, func(c *Client) (*DeviceInfo, error) { return c.GetDeviceInfo(ctx) })
		if feats := fetchTyped(t, "GetFeatures", body, doErr, func(c *Client) (*Features, error) { return c.GetFeatures(ctx) }); feats != nil {
			checkFeatures(t, feats)
		}
		fetchTyped(t, "GetStatus", body, doErr, func(c *Client) (*Status, error) { return c.GetStatus(ctx, "main") })
		if pi := fetchTyped(t, "GetPlayInfo", body, doErr, func(c *Client) (*PlayInfo, error) { return c.GetPlayInfo(ctx) }); pi != nil {
			if pi.Elapsed() < 0 || pi.Total() < 0 {
				t.Fatalf("PlayInfo{play_time: %d, total_time: %d}: Elapsed=%v Total=%v, want >= 0", pi.PlayTime, pi.TotalTime, pi.Elapsed(), pi.Total())
			}
		}
		fetchTyped(t, "GetListInfo", body, doErr, func(c *Client) (*ListInfo, error) { return c.GetListInfo(ctx, "server", 0, 8, "en") })
		fetchTyped(t, "GetPresetInfo", body, doErr, func(c *Client) (*PresetInfo, error) { return c.GetPresetInfo(ctx) })
		fetchTyped(t, "GetTunerStatus", body, doErr, func(c *Client) (*TunerStatus, error) { return c.GetTunerStatus(ctx) })
		fetchTyped(t, "GetTunerPresetInfo", body, doErr, func(c *Client) (*TunerPresetInfo, error) { return c.GetTunerPresetInfo(ctx, "fm") })
		fetchTyped(t, "GetDistributionInfo", body, doErr, func(c *Client) (*DistributionInfo, error) { return c.GetDistributionInfo(ctx) })
	})
}

// checkFeatures runs the Features accessors the CLI relies on over a
// decoded, untrusted Features value.
func checkFeatures(t *testing.T, f *Features) {
	t.Helper()
	if got, want := len(f.SystemInputIDs()), len(f.System.InputList); got != want {
		t.Fatalf("SystemInputIDs: %d ids for %d inputs", got, want)
	}
	_ = f.String()
	for _, zone := range []string{"main", "zone2", "zone3", "zone4"} {
		if z := f.ZoneByID(zone); z != nil && !strings.EqualFold(z.ID, zone) {
			t.Fatalf("ZoneByID(%q) returned zone %q", zone, z.ID)
		}
		_ = f.ZoneInputs(zone)
		_ = f.ZoneSoundPrograms(zone)
		_ = f.ZoneHasFunc(zone, "prepare_input_change")
		checkVolumeScale(t, f, zone, 0, 0)
	}
}

// checkVolumeScale asserts what the volume conversions guarantee for ANY
// device-reported scale: VolumeRange keeps min <= max in order, the dB step
// is positive, VolumeIntToDB is never NaN, and VolumeDBToInt lands on the
// same side of the baseline as db (no wrap-around).
func checkVolumeScale(t *testing.T, f *Features, zone string, n int, db float64) {
	t.Helper()
	if z := f.ZoneByID(zone); z != nil {
		if i := slices.IndexFunc(z.RangeStep, func(r RangeStep) bool { return r.ID == "volume" }); i >= 0 {
			r := z.RangeStep[i]
			mn, mx, _, _ := f.VolumeRange(zone)
			if r.Min <= r.Max && mn > mx {
				t.Fatalf("VolumeRange(%s) of [%v, %v] = [%d, %d]: order lost", zone, r.Min, r.Max, mn, mx)
			}
		}
	}

	baseline, step, _ := f.VolumeDBScale(zone)
	if !(step > 0) {
		t.Fatalf("VolumeDBScale(%s): step %v, want > 0", zone, step)
	}
	if got := f.VolumeIntToDB(zone, n); math.IsNaN(got) {
		t.Fatalf("VolumeIntToDB(%s, %d) = NaN (baseline %v, step %v)", zone, n, baseline, step)
	}
	if got := f.VolumeDBToInt(zone, db); (db > baseline && got < 0) || (db < baseline && got > 0) {
		t.Fatalf("VolumeDBToInt(%s, %v) = %d: wrong side of baseline %v (step %v)", zone, db, got, baseline, step)
	}
	if got := f.VolumeDBToInt(zone, baseline); got != 0 {
		t.Fatalf("VolumeDBToInt(%s, baseline %v) = %d, want 0", zone, baseline, got)
	}
}

// FuzzVolumeScale fuzzes the volume math against a device-reported range.
// The same (min, max, step) feeds both the integer "volume" and the
// "actual_volume_db" range_step; each conversion reads only its own entry.
func FuzzVolumeScale(f *testing.F) {
	f.Add(-80.5, 16.5, 0.5, 99, -31.0)  // RX-V583
	f.Add(-99.5, 16.5, 0.5, 0, -99.5)   // A-series floor
	f.Add(0.0, 161.0, 1.0, 161, 200.0)  // integer scale, db above max
	f.Add(-80.5, 16.5, 0.0, 10, -40.0)  // step 0: fallback scale
	f.Add(-80.5, 16.5, -0.5, 10, -40.0) // negative step
	f.Add(16.5, -80.5, 0.5, 10, -40.0)  // min > max
	f.Add(0.0, 0.0, 0.5, -1, 0.0)       // empty range
	f.Add(-12.0, 12.0, 0.1, 7, 3.33)    // non-binary step
	f.Fuzz(func(t *testing.T, mn, mx, step float64, n int, db float64) {
		// JSON (the only way a device range reaches us) can't carry
		// NaN or ±Inf.
		for _, v := range []float64{mn, mx, step} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Skip("non-finite range values are not representable in JSON")
			}
		}
		const zone = "main"
		feats := &Features{Zone: []ZoneFeatures{{
			ID: zone,
			RangeStep: []RangeStep{
				{ID: "volume", Min: mn, Max: mx, Step: step},
				{ID: "actual_volume_db", Min: mn, Max: mx, Step: step},
			},
		}}}

		baseline, s, exact := feats.VolumeDBScale(zone)
		if exact != (step > 0) {
			t.Fatalf("VolumeDBScale exact = %v for device step %v", exact, step)
		}
		if exact && (baseline != mn || s != step) {
			t.Fatalf("VolumeDBScale = (%v, %v), want the device's (%v, %v)", baseline, s, mn, step)
		}
		if !exact && (baseline != DefaultVolumeDBBaseline || s != DefaultVolumeDBStep) {
			t.Fatalf("VolumeDBScale = (%v, %v), want the default scale", baseline, s)
		}
		checkVolumeScale(t, feats, zone, n, db)

		// Exact round-trips hold only for receiver-like scales; for
		// absurd ones floating-point precision legitimately loses steps.
		if !exact || mn > mx || step < 0.01 || math.Abs(mn) > 1000 || math.Abs(mx) > 1000 {
			return
		}
		steps := int((mx - mn) / step)
		k := n % (steps + 1)
		if k < 0 {
			k = -k
		}
		eps := 1e-9 * max(1, math.Abs(mn), math.Abs(mx))
		v := feats.VolumeIntToDB(zone, k)
		if v < mn-eps || v > mx+eps {
			t.Fatalf("VolumeIntToDB(%d) = %v, outside [%v, %v]", k, v, mn, mx)
		}
		if back := feats.VolumeDBToInt(zone, v); back != k {
			t.Fatalf("raw %d -> %v dB -> raw %d", k, v, back)
		}
		if math.IsNaN(db) {
			return
		}
		in := min(max(db, mn), mx)
		raw := feats.VolumeDBToInt(zone, in)
		if out := feats.VolumeIntToDB(zone, raw); math.Abs(out-in) > step/2+eps {
			t.Fatalf("%v dB -> raw %d -> %v dB: off by more than half a step (%v)", in, raw, out, step)
		}
	})
}

// FuzzPlayTimeToDuration fuzzes the play_time/total_time conversion
// behind PlayInfo.Elapsed and PlayInfo.Total.
func FuzzPlayTimeToDuration(f *testing.F) {
	for _, s := range []int{0, 1, 42, 300, 86400, -1, -60} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seconds int) {
		d := playTimeToDuration(seconds)
		switch {
		case d < 0:
			t.Fatalf("playTimeToDuration(%d) = %v, want >= 0", seconds, d)
		case seconds <= 0 && d != 0:
			t.Fatalf("playTimeToDuration(%d) = %v, want 0", seconds, d)
		case seconds > 0 && int64(seconds) <= math.MaxInt64/int64(time.Second) && d != time.Duration(seconds)*time.Second:
			t.Fatalf("playTimeToDuration(%d) = %v, want %ds", seconds, d, seconds)
		case seconds < math.MaxInt && d > playTimeToDuration(seconds+1):
			t.Fatalf("playTimeToDuration(%d) = %v exceeds playTimeToDuration(%d)", seconds, d, seconds+1)
		}
	})
}

// FuzzFeaturesCacheLoad fuzzes reading a (corrupt or hostile) features
// cache file from disk.
func FuzzFeaturesCacheLoad(f *testing.F) {
	addReplySeeds(f)
	path := filepath.Join(f.TempDir(), "dev-features.json")
	fc := &FeaturesCache{}
	f.Fuzz(func(t *testing.T, content []byte) {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write cache file: %v", err)
		}
		feats, ok, err := fc.tryLoad(path)
		if err != nil {
			t.Fatalf("tryLoad: %v (a corrupt cache must read as a miss)", err)
		}
		if ok != (feats != nil) {
			t.Fatalf("tryLoad: ok = %v with features %v", ok, feats)
		}
		if ok {
			checkFeatures(t, feats)
		}
	})
}

// FuzzFeaturesCachePath fuzzes the cache file name built from the
// receiver-reported device_id (getDeviceInfo).
func FuzzFeaturesCachePath(f *testing.F) {
	for _, s := range []string{"AC44F2000000", "00A0DED12345", "", ".", "..", "a b", "ü", "\x00"} {
		f.Add(s)
	}
	dir := f.TempDir()
	fc := &FeaturesCache{Dir: dir}
	f.Fuzz(func(t *testing.T, deviceID string) {
		path, err := fc.pathFor(deviceID)
		if err != nil {
			return
		}
		if filepath.Dir(path) != filepath.Clean(dir) || filepath.Base(path) != deviceID+"-features.json" {
			t.Fatalf("pathFor(%q) = %q: not a file directly inside the cache dir %q", deviceID, path, dir)
		}
	})
}

// FuzzLevenshtein fuzzes the edit distance used to rank device-reported
// names (inputs, sound programs) as suggestions.
func FuzzLevenshtein(f *testing.F) {
	f.Add("kitten", "sitting", "mitten")
	f.Add("hdmi2", "hdm2", "tuner")
	f.Add("", "abc", "\xff")
	f.Add("ü", "u", "\xc3")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		ab := Levenshtein(a, b)
		if ba := Levenshtein(b, a); ab != ba {
			t.Fatalf("not symmetric: d(%q,%q)=%d, d(%q,%q)=%d", a, b, ab, b, a, ba)
		}
		// Distance is over runes; invalid UTF-8 decodes to U+FFFD.
		ra, rb := []rune(a), []rune(b)
		if lo, hi := max(len(ra)-len(rb), len(rb)-len(ra)), max(len(ra), len(rb)); ab < lo || ab > hi {
			t.Fatalf("d(%q,%q)=%d outside [%d, %d]", a, b, ab, lo, hi)
		}
		if (ab == 0) != slices.Equal(ra, rb) {
			t.Fatalf("d(%q,%q)=%d, but rune-equal = %v", a, b, ab, slices.Equal(ra, rb))
		}
		if ac, bc := Levenshtein(a, c), Levenshtein(b, c); ac > ab+bc {
			t.Fatalf("triangle inequality: d(a,c)=%d > d(a,b)=%d + d(b,c)=%d for %q %q %q", ac, ab, bc, a, b, c)
		}
	})
}

// FuzzDidYouMean fuzzes suggestion ranking over device-reported candidate
// lists (newline-separated in the fuzz input).
func FuzzDidYouMean(f *testing.F) {
	f.Add("hdmi", "hdmi1\nhdmi2\ntuner\nnet_radio", 3)
	f.Add("", "", 1)
	f.Add("x", "b\na\na", 2)
	f.Add("x", "b\na", 99)
	f.Add("x", "a", 0)
	f.Fuzz(func(t *testing.T, unknown, list string, n int) {
		var cands []string
		if list != "" {
			cands = strings.Split(list, "\n")
		}
		got := DidYouMean(unknown, cands, n)
		if n <= 0 || len(cands) == 0 {
			if got != nil {
				t.Fatalf("DidYouMean(n=%d, %d candidates) = %q, want nil", n, len(cands), got)
			}
			return
		}
		if len(got) != min(n, len(cands)) {
			t.Fatalf("got %d suggestions, want %d", len(got), min(n, len(cands)))
		}
		left := map[string]int{}
		best := math.MaxInt
		for _, c := range cands {
			left[c]++
			best = min(best, Levenshtein(unknown, c))
		}
		for i, s := range got {
			if left[s]--; left[s] < 0 {
				t.Fatalf("suggestion %q is not a (remaining) candidate", s)
			}
			d := Levenshtein(unknown, s)
			if i == 0 && d != best {
				t.Fatalf("first suggestion %q has distance %d, best is %d", s, d, best)
			}
			if i > 0 {
				prev := got[i-1]
				if pd := Levenshtein(unknown, prev); pd > d || (pd == d && prev > s) {
					t.Fatalf("suggestions out of order: %q (d=%d) before %q (d=%d)", prev, pd, s, d)
				}
			}
		}
	})
}

// checkParsed asserts one Parse* helper either returns the input verbatim
// (a modelled value) or the shared unknown sentinel.
func checkParsed(t *testing.T, kind, in, out string, known, rawKnown bool) {
	t.Helper()
	switch {
	case known != rawKnown:
		t.Fatalf("%s: Parse(%q).Known() = %v, but the raw value's Known() = %v", kind, in, known, rawKnown)
	case known && out != in:
		t.Fatalf("%s: Parse(%q) = %q, want the input back", kind, in, out)
	case !known && out != UnknownValue:
		t.Fatalf("%s: Parse(%q) = %q, want %q", kind, in, out, UnknownValue)
	}
}

// FuzzParseEnums fuzzes the Parse* helpers that normalise received state
// strings (power, playback, repeat, shuffle, distribution role).
func FuzzParseEnums(f *testing.F) {
	for _, s := range []string{"on", "standby", "play", "stop", "pause", "off", "one", "all", "songs", "albums", "server", "client", "none", "", "ON", " on", UnknownValue, "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := ParsePowerState(s)
		checkParsed(t, "PowerState", s, string(p), p.Known(), PowerState(s).Known())
		pb := ParsePlaybackState(s)
		checkParsed(t, "PlaybackState", s, string(pb), pb.Known(), PlaybackState(s).Known())
		r := ParseRepeatMode(s)
		checkParsed(t, "RepeatMode", s, string(r), r.Known(), RepeatMode(s).Known())
		sh := ParseShuffleMode(s)
		checkParsed(t, "ShuffleMode", s, string(sh), sh.Known(), ShuffleMode(s).Known())
		d := ParseDistRole(s)
		checkParsed(t, "DistRole", s, string(d), d.Known(), DistRole(s).Known())
	})
}
