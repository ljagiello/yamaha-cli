# Improvements for yamaha-cli, Inspired by the `ynca` Library

This document reconciles yamaha-cli's YNCA backend against the mature `ynca` Python reference library, surfacing concrete gaps where the legacy-receiver code path lags behind both `ynca` and yamaha-cli's own MusicCast (YXC) command surface. Every item below targets a real, verified gap — nothing here is already implemented. Items are grounded in `ynca` source (file:line refs) and mapped to specific yamaha-cli target files, with effort/impact tags and a prioritization table at the end.

---

## Top 5 quick wins (high value, S effort)

1. **Typed `Input`/`SoundProgram` enums with `UNKNOWN` sentinel** (2.1) — closes the YNCA/YXC consistency gap; YXC already proved the pattern.
2. **Attenuation-aware `Mute` (Off / On / Att-20 / Att-40)** (2.2) — stop a lossy bool from erasing real device state on the user's RX-V583.
3. **`ynca scene <n>` recall** (3.4) — single most-used one-touch control; trivial PUT.
4. **`ynca decoder` + DSP boolean toggles** (3.7, 3.8) — the closest YNCA twins to existing top-level YXC commands; one-method PUTs the YNCA backend simply lacks.
5. **`ynca sleep` + `ynca system power` (SYS:PWR)** (3.6) — two common features the Power codec already supports.

## Top 5 high-impact bets (worth the L effort)

1. **`ynca watch` + Session/reader event model** (1.1, 4.1) — gives legacy receivers the live-state observation YXC already has; the headline capability gap.
2. **Function-descriptor catalog (name + Cmd capability flag + converter)** (2.3) — the single structural change every other data-model item hangs off.
3. **`ynca tuner` (band/freq/preset/RDS)** (3.1) — the tuner is the killer source on pre-MusicCast receivers and is completely absent.
4. **`ynca now-playing` + transport verbs** (3.2) — metadata/playback is a glaring daily-use gap for streaming inputs.
5. **`ynca dump` → transcript-seeded replay fake → fixtures** (4.2, 4.4) — closes the record/replay/test loop that is `ynca`'s core ergonomic win.

---

## 1. Connection & Protocol Robustness

### 1.1 Persistent Session + background reader with message-callback fan-out
The YNCA client is strictly one-shot request/response (`pkg/ynca/doc.go`, `client.go` Send/SendMulti) and structurally cannot observe the unsolicited `@SUBUNIT:FUNC=val` reports the receiver pushes on front-panel/remote changes — `ynca`'s entire design is a long-lived reader thread that parses every pushed line and fans it out to callbacks (`connection.py:75-105`, `protocol.py:109-142`).
- **Target:** `pkg/ynca/session.go` (new).
- **Today:** one conn, one Send/SendMulti cycle; push reports only incidentally drained during the connect-time wake.
- **Change:** add a `Session` owning the conn + scanner, with `Run(ctx)` dispatching `parseLine` results (plus `@UNDEFINED`/`@RESTRICTED` bare-line status) to registered `func(status, subunit, function, value)` callbacks. Make it a distinct mode that *owns* the conn (Send/SendMulti unavailable while running) to avoid reply/push interleaving against the shared mutex.
- **Tags:** [effort L] [impact high] — *foundation for 1.2 and the `ynca watch` command (4.1).*

### 1.2 Background keep-alive to defeat the ~40s standby drop
Only a connect-time wake exists (`WithWakeOnConnect`, `client.go:63-77,138-140`); there's no periodic heartbeat. The REPL (`ynca.go runYncaRepl`) holds one connection across user think-time and *will* hit the ~40s standby timeout, silently losing the next command. `ynca` reuses its idle send-queue timeout to send `@SYS:MODELNAME=?` every 30s (under the 40s threshold) and suppresses the echo (`protocol.py:46-47,144-172`).
- **Target:** `pkg/ynca/session.go`, `internal/cli/ynca.go` (REPL).
- **Change:** on a long-lived connection (the Session, and the REPL via a `time.Ticker`), send `@SYS:MODELNAME=?` after ~30s idle and suppress the matching `SYS:MODELNAME` echo so it isn't surfaced as state. Serialize the keep-alive write against user writes on the shared send path.
- **Tags:** [effort M] [impact med]

### 1.3 Scale SendMulti's drain timeout by expected reply count
`sendMultiOnceLocked` (`client.go:370-379`) drains an unbounded fan-out GET bounded by the flat `c.timeout` (default 3s) for the whole drain. A legitimately slow BASIC fan-out on a busy receiver aborts with `ErrNoReply` and closes the conn. `ynca` uses `2 + num_commands_sent*(COMMAND_SPACING*5)` precisely to avoid false timeouts (`api.py:101-108`).
- **Target:** `pkg/ynca/client.go`.
- **Change:** give SendMulti an adaptive deadline (base + per-line budget, or a configurable multiplier), reset the read deadline per line so the budget covers the whole drain, and leave single-line Send on its tight timeout. *Prerequisite for tuner/now-playing fan-outs in §3.*
- **Tags:** [effort S] [impact med]

### 1.4 Correlate `@UNDEFINED`/`@RESTRICTED` with the in-flight request
`classifyReply` (`client.go:451-459`) builds `ErrUndefinedCommand{Line: reply}` from the contextless reply (literally `@UNDEFINED`), so the CLI must separately thread the request line into `friendlyYNCAError`. Because Send is strictly one-line-in/one-line-out under a mutex, the client *knows* the in-flight line — `ynca` can't do this correlation, but yamaha-cli can.
- **Target:** `pkg/ynca/errors.go` (`ErrUndefinedCommand`/`ErrRestricted`, lines 37-53), `pkg/ynca/client.go`.
- **Change:** add a `Request` field set from the line Send just wrote, so the typed error self-describes (`@MAIN:ZONENAME=? -> @UNDEFINED`). Simplify `friendlyYNCAError` call sites. Concrete types unchanged, so exit-code mapping (70/75) is unaffected.
- **Tags:** [effort S] [impact med]

### 1.5 Replace string-matched errno with `errors.Is(syscall.Errno)` on the reconnect-retry path
`isConnReset` (`client.go:511-532`) string-matches `net.OpError` text (`strings.Contains(ne.Err.Error(), "broken pipe"/"connection reset")`), which is locale/Go-version brittle. This is the *one-shot reconnect-and-retry* gate only — and it is already fairly complete: it catches `io.EOF`, `io.ErrClosedPipe`, and `os.ErrClosed` (`client.go:515-518`), and the *separate* `IsTransport` classifier (`errors.go:114`) already treats `io.ErrUnexpectedEOF` and generic `net.OpError` as transport failures for DHCP rediscovery. The only genuine miss is a `syscall.Errno` reset that does *not* surface as broken-pipe/connection-reset text — a narrow slice. `ynca` subclasses pyserial's ReaderThread to plug an analogous disconnect-detection hole (`connection.py:19-38`).
- **Target:** `pkg/ynca/client.go` (`isConnReset`, scoped strictly to the retry path).
- **Change:** add `errors.Is(err, syscall.ECONNRESET/EPIPE/ECONNABORTED)` alongside the existing string fallback. Do *not* add `io.ErrUnexpectedEOF` here — `IsTransport` already handles it end-to-end, and `isConnReset`'s `io.EOF` branch already covers the common reset case.
- **Tags:** [effort S] [impact low]

### 1.6 Opt-in YNCA wire-traffic ring buffer under `--debug`
`--debug` tracing is HTTP/YXC-only (`internal/debuglog` + `httptrace.go`); a user debugging a YNCA-only receiver gets zero visibility into the `@SUB:FUNC=val` lines. `ynca` records every sent/received line with a timestamp in a thread-safe ring buffer, and that same format feeds its fixtures (`protocol.py:22-33,116-119`).
- **Target:** `pkg/ynca/client.go`, `internal/debuglog`.
- **Change:** add a `WithLogger`/`WithCommLog` option that emits each sent/received line (with timestamp) via `debuglog.Logger.Tracef` so `--debug` shows YNCA `->`/`<-` traffic, plus an optional bounded in-memory ring. Nil-guard before formatting to stay zero-cost.
- **Tags:** [effort S] [impact low]

---

## 2. Data Model & Type Safety

### 2.1 String-backed `Input`/`SoundProgram` enums with `Known()`/`Parse*` + `UNKNOWN`
Verified internal inconsistency: YXC ported `ynca`'s `_missing_`→UNKNOWN pattern (`pkg/yxc/enums.go:9-24`), but the YNCA backend types only `Power`/`Volume`/`Mute` and leaves `Input`/`SoundProgram` as bare strings (`control.go:144-168`).
- **Target:** `pkg/ynca/control.go` or new `pkg/ynca/enums.go`.
- **Change:** add `type Input string` and `type SoundProgram string` (mirroring the YXC layout) with `Known()` + `Parse*` + an `UNKNOWN` sentinel, seeded from `ynca`'s enum lists. `Parse*` must be lossless (preserve unrecognised wire values verbatim). Underlying type stays string, so JSON/`buildYNCAStatusPayload` is unaffected.
- **Tags:** [effort M] [impact high]

### 2.2 Attenuation-aware `Mute` tri-state instead of a lossy bool
Verified: `parseMute` (`control.go:58`) returns bool — anything `!= "Off"` is muted, collapsing `Att -20 dB`/`Att -40 dB` into `muted=true`. `ynca` models all four as first-class enum members (`enums.py:269-281`); RX-V receivers genuinely report attenuation mutes.
- **Target:** `pkg/ynca/control.go`, `internal/cli/ynca.go` (toggle path, lines 310-322).
- **Change:** add `type Mute string` (`MuteOff`/`MuteOn`/`MuteAtt20`/`MuteAtt40`/`MuteUnknown`) + `ParseMute`. Keep a `Muted() bool` convenience so the toggle path is preserved, but surface the precise state in status output. Keep a boolean status key for script consumers; `SetMute` keeps its bool input (device only accepts On/Off on write).
- **Tags:** [effort S] [impact med]

### 2.3 Function-descriptor catalog (name + Cmd capability flag + converter)
Function names are duplicated magic strings in `control.go` plus a parallel documentation-only constant block in `types.go:22-29` that doesn't constrain `Send`. There's no machine-readable notion of read-only vs writable. `ynca`'s per-function descriptor (`function.py:34-113`) binds name → typed property → converter → `Cmd.GET|PUT` flag; the corpus names this and init-group batching as the two highest-value patterns to replicate.
- **Target:** `pkg/ynca/functions.go` (new), absorbing `types.go:22-29` doc-only constants.
- **Change:** add a minimal `Function` struct `{Name; Cmd (Get|Put bits); parse; format}` and a registry map. `get`/`put` consult it (assert capability client-side before hitting the wire). Keep public typed getters/setters as thin wrappers so no CLI caller changes. Keep it a Go struct+map — *not* a reflection/`__get__` port. Note: functions like `2CHDECODER` (3.7) are not valid Go identifiers, so the descriptor's `Name` field (not the Go const name) carries the wire name — design for this `name_override` case from the start.
- **Tags:** [effort L] [impact high] — *enabling refactor; land before/with the feature PRs in §3.*

### 2.4 Generalize `formatVolume` into a stepsize-aligned numeric codec
Verified: `formatVolume` (`control.go:47`) hardcodes step 0.5 / 1 decimal for volume only. `ynca` reuses `number_to_string_with_stepsize` across volume (0.5), FM freq (0.2), AM freq (10), and tone (0.5) (`helpers.py:8-23`).
- **Target:** `pkg/ynca/control.go`.
- **Change:** extract `formatStepped(value float64, decimals int, step float64) string` (preserving the `-0.0`→`0.0` normalisation), keep `formatVolume` as a thin caller, and wire it into the 2.3 descriptor converters. Foundation for tuner-frequency (3.1) and tone (3.5). Guard with a test that `formatVolume == formatStepped(v,1,0.5)`.
- **Tags:** [effort S] [impact med]

### 2.5 Distinguish "unknown/absent" from zero-value in `Status`
Verified: `GetStatus` (`control.go`) leaves omitted fields at Go zero values (Power `""`, Volume `0.0`, Input `""`, Mute `false`), indistinguishable from real readings; the only presence hint is `VolumeRaw == ""`. `ynca`'s read-through cache returns `None` for un-reported functions, never confusing absent with a real value.
- **Target:** `pkg/ynca/control.go`, `internal/cli/ynca.go` (`buildYNCAStatusPayload`, lines 459-473).
- **Change:** initialise typed fields to their `Unknown` sentinel (from 2.1/2.2) and overwrite only on successful parse; treat `VolumeRaw == ""` as "not reported" and omit `volume_db` from the payload in that case, exactly as input/sound_program are already conditionally omitted. Builds on 2.1/2.2.
- **Tags:** [effort S] [impact med]

---

## 3. Feature & Command Coverage

> All §3 items depend on a small **enabling refactor**: extend `types.go` with the new subunit/function constants (`SubunitTuner='TUN'`, source-subunit ids, `FuncScene`/`FuncSleep`/`FuncSpBass`/`FuncBand`/`FuncFMFreq`/`FuncPreset`/`FuncMetaInfo`/`Func2ChDecoder`/the DSP-toggle funcs, and init-group names `GroupMETAINFO`/`GroupRDSINFO`/`GroupSCENENAME`). Today these stop at zones + BASIC fields and are documentation-only. Land this (or 2.3) first so feature PRs don't each invent string literals. **[effort S] [impact low — enabling]**

### 3.1 `ynca tuner` — band / frequency / preset / RDS
On pre-MusicCast receivers the AM/FM tuner is one of the most-used sources, fully exposed over YNCA, yet a YNCA user can only `input TUNER` then hand-craft raw lines. YXC already ships a full `tuner` command. `ynca` models TUN as band, fmfreq (0.2 MHz step), amfreq (10 kHz step), preset/up/down, mem, searchmode, and RDS fields via the RDSINFO group (`subunits/tun.py:17-41`).
- **Target:** `pkg/ynca/control.go`, new `internal/cli/ynca_tuner.go` (mirroring `tuner.go`).
- **Change:** add tuner control methods (GET/PUT `@TUN:BAND`/`FMFREQ`/`AMFREQ`/`PRESET`/`MEM`, RDS fan-out via SendMulti+VERSION fence) and a `ynca tuner status|fm|am|preset` subtree. Frequencies aligned to device step (FM 0.2 MHz ×100, AM 10 kHz) using the 2.4 codec. Route through `runYNCASet` so preset up/down isn't retried. Unit-test the formatter.
- **Tags:** [effort L] [impact high]

### 3.2 `ynca now-playing` + transport verbs
On a streaming/network input, the user wants to see what's playing and drive transport; YXC exposes this via `netusb info`, but the YNCA backend has none of it. `ynca` surfaces artist/album/song/track/station + playbackinfo + elapsed/total across ~17 source subunits via the METAINFO group (`subunits/__init__.py:21-103`).
- **Target:** `pkg/ynca/control.go`, new `internal/cli/ynca_nowplaying.go`.
- **Change:** add `GetNowPlaying(subunit)` (drains `@<SRC>:METAINFO=?` via SendMulti) returning a struct shaped like the YXC `PlayInfo`, plus `ynca now-playing` and `ynca play|pause|stop|next|prev` → `@<SRC>:PLAYBACK=...`. The one new piece of model knowledge is the input→subunit map (`'NET RADIO'`→NETRADIO, etc.) — keep it small/data-driven and fall back to "no now-playing for input X" rather than erroring.
- **Tags:** [effort L] [impact high]

### 3.3 `ynca input`/`ynca sound` value discovery + validation
Verified: `newYncaInputCmd`/`newYncaSoundCmd` use `cobra.ExactArgs(1)` (`ynca.go:337,364`) and send blind; an invalid value only fails at the device with no suggestion. YXC already lists valid values on no-arg (commit `8ed1d54`) and validates with did-you-mean.
- **Target:** `pkg/ynca` (exported `Inputs()`/`SoundPrograms()`), `internal/cli/ynca.go`.
- **Change:** expose `Inputs()`/`SoundPrograms() []string` (sound from the static `SoundPrg` enum; **inputs preferably from a live `@SYS:INPNAME=?` fan-out** so the list reflects the actual receiver, falling back to the static set). Relax both commands to `MaximumNArgs(1)`: no-arg lists values; with-arg runs **`yxc.DidYouMean`** (reuse the existing exported helper) and returns a `*ValidationError`. Validation is advisory (suggest on miss, still allow send) to honor the never-crash-on-unseen-value stance.
- **Tags:** [effort M] [impact high]

### 3.4 `ynca scene <n>` recall (+ scene-name readback)
Scenes are the most convenient one-touch control (input + DSP + power in one recall), per-zone in YNCA, and already on YXC — but a YNCA user must hand-craft `@MAIN:SCENE=Scene 3`. `ynca`: `ZoneBase.scene()` + scene1name..scene12name via SCENENAME group (`zone.py:140-182`).
- **Target:** `pkg/ynca/control.go`, `internal/cli/ynca.go`.
- **Change:** add `RecallScene(subunit, n)` (PUT `@<subunit>:SCENE=Scene <n>`) and optional `GetSceneNames(subunit)` (SCENENAME fan-out), plus `ynca scene <n>` (and no-arg name listing). Bound `n` to 1..12 client-side for a clean usage error. Route through `runYNCASet`.
- **Tags:** [effort S] [impact high]

### 3.5 `ynca tone` — bass/treble
Bass/treble is a standard everyday adjustment, per-zone in YNCA, already on YXC. `ynca`: spbass/sptreble/hpbass/hptreble on a 0.5 dB step (`zone.py:154-163`).
- **Target:** `pkg/ynca/control.go`, new `internal/cli/ynca_tone.go` (shaped like `tone.go`).
- **Change:** add `GetTone`/`SetTone(subunit, channel, db)` for SPBASS/SPTREBLE (using the 2.4 formatter) and a `ynca tone <bass|treble> <±N>` command with a `reset` form. Don't hardcode a tight numeric range client-side (model variation degrades to a device-side `@RESTRICTED`).
- **Tags:** [effort S] [impact med]

### 3.6 `ynca sleep` timer + SYS-level power
Two cheap wins: a per-zone sleep timer (common AV feature, already a YXC command) and a system-wide power read/set. `ynca` models both: `ZoneBase.sleep` (Sleep enum Off/30/60/90/120) and `System.pwr` (`zone.py:152`, `system.py:46`). The YNCA backend has neither, and `ynca power` always maps `--zone` to MAIN/ZONE2-4, never `@SYS:PWR`.
- **Target:** `pkg/ynca/control.go`, `internal/cli/ynca.go`.
- **Change:** add `SetSleep(subunit, minutes)`/`GetSleep` (PUT the Sleep enum value, e.g. `@<zone>:SLEEP=30 min`, with the same 0/30/60/90/120 allowlist as YXC `sleep.go`) and reuse the existing Power codec against subunit `"SYS"`. Wire `ynca sleep <0|30|60|90|120|off>` and `ynca system power` (or a `--system` flag). Verify the SLEEP wire spelling against `ynca`'s Sleep enum.
- **Tags:** [effort S] [impact med]

### 3.7 `ynca decoder` — surround decoder selection (`2CHDECODER`)
Verified parity hole: YXC ships a first-class `decoder` command (`internal/cli/decoder.go:13`, backed by `pkg/yxc/zone.go:222` `SetSurroundDecoder` + `surr_decoder_type_list`), but the YNCA backend has zero decoder support (`control.go` covers only PWR/VOL/MUTE/INP/SOUNDPRG). `ynca` models it as `twochdecoder = EnumFunctionMixin[TwoChDecoder](TwoChDecoder, name_override="2CHDECODER")` over `@<zone>:2CHDECODER` with a rich `TwoChDecoder` enum (`zone.py:169-170`, `enums.py:643-674`).
- **Target:** `pkg/ynca/control.go`, new `internal/cli/ynca_decoder.go` (shaped like `decoder.go`).
- **Change:** add `GetDecoder`/`SetDecoder(subunit, type)` over `@<zone>:2CHDECODER` (one-method PUT) plus a `ynca decoder [type]` command: no-arg lists the known `TwoChDecoder` values, with-arg sets. Exercises the 2.3 `name_override` case (`2CHDECODER` is not a Go identifier), so land it after the descriptor catalog. Validation advisory; unrecognised values fall through to a device-side `@RESTRICTED`.
- **Tags:** [effort S] [impact high]

### 3.8 `ynca` DSP boolean toggles — pure-direct / enhancer / extra-bass / adaptive-drc / straight / surround-ai / 3d-cinema
Verified parity hole: yamaha-cli already established this exact command family on the YXC side (`internal/cli/dsp.go:19-116` `zoneSwitches`: pure-direct→`direct`, enhancer→`enhancer`, extra-bass→`extra_bass`, adaptive-drc→`adaptive_drc`; registered `root.go:179-181`) — and the `dsp.go` comment itself notes these "map to per-model YXC endpoints `ynca` models on the YNCA side." `ynca` exposes `puredirmode`, `enhancer`, `adaptivedrc`, `exbass`, `straight`, `surroundai`, `threedcinema` on the zone subunit. The YNCA backend models none of them.
- **Target:** `pkg/ynca/control.go`, new `internal/cli/ynca_dsp.go` (mirroring the `zoneSwitch` table).
- **Change:** add a YNCA `zoneSwitch`-style table mapping CLI names → `@<zone>:<FUNC>=On|Off` (PUREDIRMODE/ENHANCER/EXBASS/ADAPTIVEDRC/STRAIGHT/SURROUNDAI/3DCINEMA), reuse `parseOnOff`, and register one cobra command per toggle under `ynca`. Route writes through `runYNCASet`. Gate leniently (a missing function degrades to a device-side `@RESTRICTED`, not a hard client-side fail) since support is per-model.
- **Tags:** [effort S] [impact high]

### 3.9 `ynca volume up/down` step-size + `--db` parity with the YXC volume command
Verified within-tool inconsistency: YNCA `VolumeUp`/`VolumeDown` send a bare `@<sub>:VOL=Up`/`Down` with no step (`pkg/ynca/control.go:135-141`), while yamaha-cli's *own* YXC volume command exposes `--step` and `--db`/`--percent` (`internal/cli/volume.go:22-24`, `pkg/yxc/zone.go:32-37` `VolumeUp(step int)`/`VolumeDown(step int)`). `ynca` emits `Up {step} dB` for steps 1/2/5, with bare `Up` only for the 0.5 default (`zone.py:51-66` `do_vol_up`/`do_vol_down`).
- **Target:** `pkg/ynca/control.go` (`VolumeUp`/`VolumeDown`), `internal/cli/ynca.go` (`newYncaVolumeCmd`, line 257).
- **Change:** give `VolumeUp`/`VolumeDown` a step argument emitting `Up {n} dB`/`Down {n} dB` for the supported 1/2/5 steps (bare `Up`/`Down` for the 0.5 default), and add a `--step` flag (plus a `--db` note) to `ynca volume up|down`. Keep these relative writes off the auto-retry path. Closes the gap with the YXC `volume` command.
- **Tags:** [effort S] [impact med]

---

## 4. Tooling & Ergonomics

### 4.1 `ynca watch` — stream live YNCA push reports
The corpus repeatedly flags the asymmetry: YXC has a rich self-healing `watch` over UDP push; YNCA push reports are silently discarded. This is the user-facing surface of the 1.1 Session/reader work.
- **Target:** new `internal/cli/ynca_watch.go`.
- **Change:** register a `watch` subcommand backed by the 1.1 `Session` (its *own* connection, not the shared client). Stream each inbound `@SUB:FUNC=VALUE` as NDJSON (or compact `ts SUB.FUNC = value` table lines on a TTY), reusing `watch.go`'s rendering and clean-SIGINT semantics. Suppress keep-alive echoes (1.2) to avoid phantom MODELNAME events.
- **Tags:** [effort L] [impact high] — *depends on 1.1 + 1.2.*

### 4.2 `ynca dump` — capture full device state as a replayable transcript
`ynca`'s highest-leverage tooling move: one canonical `@SUBUNIT:FUNCTION=VALUE` format flows from a live dump into checked-in logs, replay-server seed, and test assertions (`dumper.py:69-93`). yamaha-cli has every primitive (SendMulti VERSION-fence, mutex client, `parseLine`) but no capture command — reverse-engineering a receiver means typing lines into the REPL one at a time.
- **Target:** new `internal/cli/ynca_dump.go`, `go:embed`'d default catalog.
- **Change:** add `ynca dump [--commands FILE] [--out FILE]`. With no `--commands`, iterate an embedded default catalog of `@SUB:FUNC=?` GETs; send each with ~100ms spacing; write every request and reply line in exact wire format; use a trailing `@SYS:VERSION=?` as the drain barrier (already `versionSentinel`). Support `#` comments; restrict to GETs (or document that PUTs are sent as-is) so no mutating line is retried.
- **Tags:** [effort M] [impact high] — *produces the seed format for 4.4.*

### 4.3 `ynca diff <reference> <other>` — compare two device transcripts
Once dumps exist, the natural follow-on is spotting what an unknown model supports that a known one doesn't — `ynca`'s `differ.py:8-54`. Cheap (set difference over parsed lines) and makes dump artifacts actionable. No model table needed; capability is discovered by comparing real captures.
- **Target:** new `internal/cli/ynca_diff.go`.
- **Change:** parse both files into subunit→function sets (reusing `parseLine`, tolerating leading junk / trailing `",`), print `@SUB:FUNC` present in `other` but missing from `reference`. Honor `-o json` for a `{added, by_subunit}` payload via the existing `printResult` path. Pure offline file parsing.
- **Tags:** [effort S] [impact med]

### 4.4 Transcript-seeded YNCA replay fake as test fixtures
Verified: every YNCA test (`pkg/ynca/client_test.go`, `internal/cli/ynca_test.go`) hand-scripts raw byte responses on an ad-hoc `net.Listen` server — verbose and duplicated, with no checked-in fixtures. `ynca`'s `debug_server.py` instead loads a transcript into an in-memory store and replays the device's own answers, including BASIC/METAINFO fan-out.
- **Target:** new fake in `pkg/ynca` test code or `internal/testutil`; `pkg/ynca/testdata/`.
- **Change:** add a reusable transcript-seeded `net.Listener` fake: read a `.txt` into a subunit→function→value store, answer GETs, echo the `@SYS:VERSION=?` fence, synthesize the BASIC fan-out from stored fields. Add real-device transcripts under `pkg/ynca/testdata/` (e.g. captured from the RX-V583) and migrate the scripted-byte tests incrementally. The 4.2 dump command produces the seed format. *(Note: a root-level `testdata/getFeatures.json` already exists for YXC; this adds a YNCA `testdata/` for transcripts.)*
- **Tags:** [effort L] [impact high] — *closes the record→replay→test loop.*

### 4.5 `yamaha info` / `ynca info` — fast model/capability snapshot (both backends, with `--zone` validation)
`features.go` has `getFeatures`/`getDeviceInfo` plumbed but no user command — the only way to see capabilities is `raw system/getFeatures` (raw ~30KiB JSON). On the YNCA side, `yncaSubunitForZone` maps zone3/4 blindly (`types.go:14-15`) with no device validation. `ynca`'s `connection_check` reads model + zones in ~1.5s (`api.py:135-182`).
- **Target:** new `yamaha info` (YXC) and `ynca info` commands; `pkg/ynca/control.go`.
- **Change:** add `yamaha info` (YXC): compact model/firmware/device_id/api_version header plus the active zone's input/sound-program/decoder lists and volume range, sourced from the cached `getFeatures` (`--all-zones`, `--raw` flags). Mirror as `ynca info` for legacy receivers using a *single batched AVAIL + MODELNAME drain* — note the connect-time wake (`client.go:63-75,169`) already provides MODELNAME, so the marginal cost is only the AVAIL fan-out. Use that same drain to validate `--zone` against the device (warn/guide, don't hard-fail — absent ≠ unsupported), folding in what would otherwise be a redundant standalone `ConnectionCheck` primitive (the existing `Probe` at `ynca.go:86-100` already confirms YNCA support every invocation). Reads only; warm-cache YXC path adds no round-trips.
- **Tags:** [effort M] [impact high]

### 4.6 REPL/`raw` discovery affordance — list known subunits/functions
The YNCA REPL is a blank prompt with no in-session help, and typed subcommands require an exact arg with no discovery — unlike their YXC twins. `ynca`'s terminal docstring *is* on-screen help (`terminal.py:18-40`), and `all_commands_ever_seen.txt` enumerates the known vocabulary.
- **Target:** `internal/cli/ynca.go` (`runYncaRepl`, lines 405-454).
- **Change:** handle `help`/`?` (format reference) and `?SUB` (list that subunit's known functions) from the same `go:embed`'d catalog used by `dump` (4.2). Add `yamaha ynca list [subunit]` for non-interactive listing. Label it "known commands" since the real supported set is device-specific (confirm via dump/diff).
- **Tags:** [effort S] [impact med]

### 4.7 Embed the YXC method catalog; fix the broken `/tmp/yxc-mc.txt` reference
Verified: `raw.go:17` and `:40` document `/tmp/yxc-mc.txt` as the YXC method catalog — a developer-machine path that doesn't exist for any end user, with no in-CLI alternative. (YXC-only doc fix; not a YNCA capability gain — kept here, not in the headline quick-wins.)
- **Target:** `internal/cli/raw.go`.
- **Change:** `go:embed` the ~180-method catalog and expose `yamaha raw --list` (or `raw list`) to print it; rewrite `raw.go`'s Long help to point at that flag. Optionally fold this correction into the 4.6 discovery work. Keep the list as plain, easily-updatable reference data (advisory — the device's `response_code` remains the source of truth).
- **Tags:** [effort S] [impact low]

---

### Suggested sequencing
1. **Enabling layer:** §3 constants refactor + 2.3 descriptor catalog + 2.4 codec → unblocks data-model and feature work cleanly.
2. **Quick data-model wins:** 2.1, 2.2, 2.5 (typed enums + honest status).
3. **Quick feature wins:** 3.4, 3.7, 3.8, 3.5, 3.6, 3.9, 3.3 (scene/decoder/DSP-toggles/tone/sleep/volume-step/discovery).
4. **Protocol robustness:** 1.3, 1.4, 1.5 (S items), then 1.1 + 1.2 (Session + keep-alive).
5. **Tooling loop:** 4.2 → 4.4 → 4.3, then 4.1 (watch, on the Session), plus 4.5/4.6/4.7.
6. **Larger features:** 3.1 (tuner), 3.2 (now-playing) once the descriptor/codec/constants foundation is in place.

---

### Prioritization at a glance

| #   | Improvement                                          | Effort | Impact |
|-----|------------------------------------------------------|--------|--------|
| 1.1 | Persistent Session + background reader               | L      | High   |
| 1.2 | Background keep-alive (~30s heartbeat)               | M      | Med    |
| 1.3 | Adaptive SendMulti drain timeout                     | S      | Med    |
| 1.4 | Correlate `@UNDEFINED`/`@RESTRICTED` with request    | S      | Med    |
| 1.5 | `syscall.Errno` checks on reconnect-retry path       | S      | Low    |
| 1.6 | YNCA wire-traffic trace under `--debug`              | S      | Low    |
| 2.1 | Typed `Input`/`SoundProgram` enums + `UNKNOWN`       | M      | High   |
| 2.2 | Attenuation-aware `Mute` tri-state                   | S      | Med    |
| 2.3 | Function-descriptor catalog                          | L      | High   |
| 2.4 | Stepsize-aligned numeric codec                       | S      | Med    |
| 2.5 | Distinguish unknown/absent from zero-value           | S      | Med    |
| 3.0 | §3 enabling constants refactor                       | S      | Low    |
| 3.1 | `ynca tuner` (band/freq/preset/RDS)                  | L      | High   |
| 3.2 | `ynca now-playing` + transport verbs                | L      | High   |
| 3.3 | `input`/`sound` discovery + validation               | M      | High   |
| 3.4 | `ynca scene <n>` recall                              | S      | High   |
| 3.5 | `ynca tone` (bass/treble)                            | S      | Med    |
| 3.6 | `ynca sleep` + SYS-level power                       | S      | Med    |
| 3.7 | `ynca decoder` (`2CHDECODER`)                        | S      | High   |
| 3.8 | `ynca` DSP boolean toggles                           | S      | High   |
| 3.9 | `ynca volume up/down` step-size + `--db`             | S      | Med    |
| 4.1 | `ynca watch` (live push reports)                     | L      | High   |
| 4.2 | `ynca dump` (replayable transcript)                 | M      | High   |
| 4.3 | `ynca diff` (compare transcripts)                   | S      | Med    |
| 4.4 | Transcript-seeded replay fake + fixtures             | L      | High   |
| 4.5 | `yamaha info`/`ynca info` + `--zone` validation     | M      | High   |
| 4.6 | REPL/`raw` discovery affordance                     | S      | Med    |
| 4.7 | Embed YXC method catalog; fix `/tmp/yxc-mc.txt`      | S      | Low    |
