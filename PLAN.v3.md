<!-- v3 - 2026-05-07 - Generated via /improving-plans from PLAN.v2.md -->

# Yamaha RX-V583 Go CLI — Build Plan v3

A command-line tool in Go to control a Yamaha RX-V583 AV receiver over the local network without using the physical remote.

## What changed in v3

Four design decisions resolved during the v2 design review, plus a handful of uncontroversial fixes:

1. **Volume deltas use server-side relative ops.** `+N`/`-N` map to `setVolume?volume=up|down&step=N` instead of GET-then-SET. One roundtrip, no TOCTOU race.
2. **`yamaha power on` blocks by default** until `getStatus.power == "on"` (max 10 s timeout). `--no-wait` opts out. Makes `yamaha power on && yamaha volume 60` Just Work without manual sleeps.
3. **`yamaha watch` emits NDJSON with a wrapper** — `{"ts": <iso>, "device": <alias>, "delta": <yxc-event>}` — one line per event. Tailable, dedup-friendly, multi-device-safe.
4. **Input validation is strict with auto-refresh on miss.** Unknown input → refresh getFeatures cache once → re-validate. If still unknown, show "did you mean: …" before any HTTP call.

Plus uncontroversial additions:
- **`--debug` / `YAMAHA_DEBUG=1`** logs full request/response to stderr.
- **`pkg/yxc.SetInput` auto-calls `prepareInputChange`** when `func_list` requires it.
- **First-run wizard handles zero-found-devices** by exiting 64 with a "pass --host manually" hint.
- **Confirmed YXC is GET-only** (verified via APK analysis: POST sites in the apps are legacy XML / Gracenote / account APIs, not YXC). `pkg/yxc.Client.Do` stays GET-only.
- **`User-Agent: yamaha-cli/<ver>`** for self-identification; **`X-AppName: MusicCast`** only on event-subscription requests (per APK header observation).

## Target device

- **Model:** Yamaha RX-V583 (2017, 7.2-channel, MusicCast)
- **IP:** `192.168.1.116`
- **Firmware:** 2.87 (YXC api_version 2.08)
- **Device ID:** `00A0DEFBE863`

## Research summary

| Track | Outcome |
|---|---|
| **Firmware analysis** | Dead end. The 35.7 MB blob (`R0424-0287.bin`) is fully encrypted (uniform 8.0 bits/byte entropy, no ECB patterns). Bootloader holds the key. No protocol artifacts recoverable via static analysis. |
| **Android APK analysis** | Also dead end for firmware decryption. Both **AV Controller** (`com.yamaha.av.avcontroller` v5.60) and **MusicCast Controller** (`com.yamaha.av.musiccastcontroller` v6.21) decompiled. Neither contains firmware keys — the receiver self-updates from Yamaha's CDN after the app sends `system/updateFirmware?type=network`. The one AES find (whitebox-key-in-JPEG decryptor) is for Gracenote credentials, not firmware. **However**: the APKs yielded the authoritative YXC endpoint catalog (184 endpoints) and YNCA command grammar XML for RX-V583 (`assets/local_yud3ga.xml`, `local_yud3gb.xml`). Massive win for the CLI even if firmware stays opaque. |
| **Protocol research** | RX-V583 speaks **YamahaExtendedControl (YXC)** — HTTP/JSON on port 80, **GET-only** (confirmed via APK source: every YXC call uses `setRequestMethod("GET")`; POST sites are in unrelated codepaths). Public PDF spec mirrors exist. YNCA also documented but officially unconfirmed for V583. |
| **Live device probe** | **Both protocols actually work.** YXC `getDeviceInfo` returned full device JSON. YNCA on TCP/50000 echoed `@SYS:VERSION=2.87/1.81`. UPnP MediaRenderer also exposed on port 49154 (ignored — over-engineered). |
| **Existing impls** | No actively maintained MIT/Apache Go library. `atamanroman/ymc` exists but GPL + abandoned. Best non-Go references: `aiomusiccast` (Python, Home Assistant), `pyamaha` (clean taxonomy), `foxthefox/yamaha-yxc-nodejs`. **Genuine gap worth filling.** |

**Decision: target YXC. YNCA is a stretch goal — and `assets/local_yud3ga.xml` from the AV Controller APK is the authoritative RX-V583-specific command list, save the user from rebuilding it from scratch.**

**Firmware decrypt:** abandoned. Would require bootloader extraction (UART/JTAG/chip-off) — out of scope for a control CLI.

## Verified device capabilities (live `/system/getFeatures`)

- **Zones:** `main` (full feature set) + `zone2` (zone_b: power/volume/mute/input only)
- **Inputs (22):** hdmi1-4, av1-3, audio1-3, aux, tuner, usb, bluetooth, server, net_radio, airplay, spotify, tidal, deezer, pandora, mc_link
- **Volume:** integer 0–161 (= -80.5..+16.5 dB step 0.5). **Always read range from `getFeatures`, never hardcode.**
- **Sound programs (20):** munich, vienna, standard, action_game, 2ch_stereo, 7ch_stereo, surr_decoder, straight, …
- **Surround decoders:** auto, dolby_surround, dts_neural_x, dts_neo6_cinema, dts_neo6_music
- **Tuner:** FM 87.5–107.9 MHz step 200, AM 530–1710 step 10, 40 presets
- **NetUSB:** play queue 200, 40 MusicCast presets, MusicCast Link distribution v2.0, CCS supported
- **Tone control:** -12..+12. Dialogue level 0..3. Sleep timer. 4 scenes.
- **Push events:** add headers `X-AppName: MusicCast` + `X-AppPort: <udp>` to any GET → receiver UDPs JSON deltas to your IP. Re-subscribe every <10 minutes.

## Architecture

```
yamaha-cli/
├── cmd/yamaha/main.go            # cobra entrypoint
├── internal/cli/                 # cobra commands (power, volume, input, zone, status, discover, raw, watch, …)
├── internal/config/              # YAML loader, multi-device schema, first-run wizard
├── internal/output/              # render: json | yaml | table | auto (TTY-detect)
├── internal/debuglog/            # request/response trace when --debug or YAMAHA_DEBUG=1
├── pkg/yxc/                      # YXC HTTP client — public, importable as a library
│   ├── client.go                 # http.Client + base URL + response_code unwrap + typed errors
│   ├── system.go                 # getDeviceInfo, getFeatures, getNetworkStatus, requestSystemReboot
│   ├── zone.go                   # main + zone2: setPower, setVolume, setMute, setInput, setSoundProgram, setSleep, getStatus
│   ├── netusb.go                 # setPlayback, getPlayInfo, recallPreset, getListInfo
│   ├── tuner.go                  # setFreq, recallPreset, getPresetInfo
│   ├── dist.go                   # MusicCast Link (Phase 3)
│   ├── events.go                 # UDP listener + header subscription + 8-min renewer (Phase 3)
│   ├── validate.go               # validate input/sound_program/scene against cached getFeatures
│   └── types.go                  # enums + structs mirroring getFeatures
├── pkg/discover/                 # SSDP MediaRenderer search → filter Yamaha → return YXC base URLs + names
├── pkg/ynca/                     # (Phase 3) thin TCP/50000 fallback + RX-V583 command catalog
└── testdata/
    ├── getFeatures.json          # captured from real device for offline tests
    └── getStatus.json            # canned snapshots for table-renderer tests
```

**Why split `pkg/yxc` and `pkg/discover` from `internal/`:** they're useful as standalone Go libraries (no other maintained one). Keep dependency surface tiny: stdlib + cobra + viper + `koron/go-ssdp` + `mattn/go-isatty`.

**CLI framework:** [`spf13/cobra`](https://github.com/spf13/cobra). Nested subcommands fit naturally. [`spf13/viper`](https://github.com/spf13/viper) for config + env-var binding.

## Configuration

`~/.config/yamaha-cli/config.yaml` (XDG-respecting; falls back to `$HOME/.config/...`).

```yaml
default_device: living-room

devices:
  living-room:
    host: 192.168.1.116
    default_zone: main          # main | zone2
  bedroom:
    host: 192.168.1.118
    default_zone: main
```

**Resolution order for the active device:**
1. `--host <ip>` flag — bypasses the config entirely (anonymous device, no alias).
2. `--device <name>` flag → look up in `devices`.
3. `YAMAHA_DEVICE` env var → look up in `devices`.
4. `default_device` from config.
5. Single-device shortcut: if exactly one device exists, use it regardless of `default_device`.
6. None of the above → trigger first-run flow (see below).

**Adding more devices:** running `yamaha discover` prints found devices and offers to append them to the config. Manual editing is also supported.

## First-run flow

When no host can be resolved by the rules above:

- **Interactive (stdout is a TTY):** print "No device configured. Searching the LAN…", run a 3 s SSDP scan, list found Yamaha devices, prompt the user to pick one, prompt for an alias (default: slugified `network_name`, e.g. `RX-V583 FBE863` → `rx-v583-fbe863`), and save the config. Re-run the original command transparently.
- **Zero devices found:** exit code 69 with stderr hint: `no Yamaha devices found on LAN; pass --host <ip> manually`.
- **Non-interactive (piped, scripted, CI):** exit with code `64` (EX_USAGE) and a stderr hint: `no device configured; run 'yamaha discover' or pass --host`.

The wizard never auto-runs in non-interactive contexts — pipelines should fail fast and loud, not hang waiting for input.

## Output formatting

Global flag: `--output {json|yaml|table|auto}` (alias `-o`). Default: `auto`.

`auto` resolves to:
- `table` (human-readable) when `stdout` is a TTY.
- `json` when stdout is piped or redirected.

This is the gh / kubectl / docker idiom.

**Successful mutating commands** (`power on`, `volume 60`, etc.) emit `{}` in JSON modes and a single confirmation line in table mode. They never print the entire device state — that's `yamaha status`'s job.

**Implementation:** `internal/output.Render(v any, format string)` uses `mattn/go-isatty` to resolve `auto`. Table rendering is hand-rolled (no `tablewriter` dependency for ~5 fields).

## Validation strategy

`yamaha input`, `yamaha sound`, `yamaha decoder`, `yamaha scene` all validate the argument against the cached `getFeatures.<device-id>.json` *before* making any HTTP call.

Flow:
1. Load cached `getFeatures` for the active device. If cache is missing, fetch and save first.
2. If the argument is in the cache's allowed list → proceed with the YXC call.
3. If not → fetch `getFeatures` once (auto-refresh) and re-validate against the fresh data.
4. If still not a match → exit code 1 with a `did you mean: …` suggestion built from the closest 3 candidates by Levenshtein distance.

Why: the device returns a generic `response_code: 6` ("not found") for invalid arguments — useless for diagnostics. Strict client-side validation gives the user a real error message with suggestions, and the auto-refresh handles the rare "user upgraded firmware and unlocked a new input" case without making them remember `--refresh-features`.

## Debug / observability

- `--debug` flag or `YAMAHA_DEBUG=1` env var → log every YXC request URL + response body to stderr in a one-line-per-call format prefixed with `→` and `←`. SSDP probes also logged.
- Debug output is independent of `--output`: stdout stays clean (still parseable JSON when piped), stderr gets the trace.
- No log file by default; `2> trace.log` is the user's escape hatch.

## Exit codes

Sysexits-lite — small, predictable, plays well with shell idioms:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic error (validation failure, "did you mean", power-on timeout) |
| 2 | Misuse / invalid CLI argument (cobra default) |
| 64 | `EX_USAGE` — no device configured & non-interactive |
| 69 | `EX_UNAVAILABLE` — device unreachable (network error, timeout, zero-found in wizard) |
| 70 | `EX_SOFTWARE` — device returned non-zero `response_code` (e.g., feature unsupported, device not ready) |

Error message goes to stderr in human form regardless of `--output`. With `--output json`, also emit `{"error": "...", "code": <int>, "yxc_response_code": <int|null>}` to stdout.

## Phase 1 — MVP

Goal: replace the most-used remote buttons + scriptable output + zero-config first run.

```
yamaha power on|off|toggle [--no-wait]
yamaha volume <int|±N|up|down>     # 60, +5, -5, up, down
yamaha mute on|off|toggle
yamaha input <name>                 # tab-completion + strict validation from getFeatures cache
yamaha status                       # pretty (TTY) or JSON (piped)
yamaha discover [--add]             # SSDP scan, list/save devices  (promoted from Phase 2)
yamaha config show                  # print resolved config
yamaha config path                  # print path to the config file
yamaha completion {bash|zsh|fish}   # shell completion script
yamaha version                      # version + commit
```

**Tasks:**
1. `go mod init github.com/ljagiello/yamaha-cli`; pull cobra + viper + koron/go-ssdp + mattn/go-isatty.
2. `internal/config`: load multi-device YAML with the resolution order above. Bind to viper. `--host`, `--device`, `--zone`, `--output`, `--debug`, `--no-wait` global flags.
3. `internal/output.Render(v any, format string)`: auto-detect TTY, emit JSON/YAML/table. Format `error` payloads consistently.
4. `internal/debuglog`: stderr request/response tracer activated by `--debug` or `YAMAHA_DEBUG=1`.
5. `pkg/discover` using `koron/go-ssdp`: search `urn:schemas-upnp-org:device:MediaRenderer:1` (3 s timeout), fetch description XML, filter for `manufacturer == "Yamaha Corporation"`. Return `[]Device{Name, Host, Model, BaseURL, UDN}`. Dedup by UDN.
6. First-run wizard in `internal/cli`: triggered when device resolution fails AND stdout is a TTY. Handles zero-found, multi-found, and one-found cases. Writes to `~/.config/yamaha-cli/config.yaml`.
7. `pkg/yxc.Client`:
   - Construct from base URL + `*http.Client` (default 5 s timeout).
   - Headers: `User-Agent: yamaha-cli/<ver>` on every request; `X-AppName: MusicCast` + `X-AppPort: <port>` only on event-subscription requests.
   - `Do(ctx, method string, params url.Values) (json.RawMessage, error)` — GET `/v1/<method>?<params>`. Parse JSON, return typed error if `response_code != 0`. Map known codes (5 → `ErrDeviceNotReady`, 6 → `ErrNotFound`, etc.).
   - Higher-level methods: `GetDeviceInfo`, `GetFeatures`, `GetStatus`, `SetPower`, `SetVolume`, `SetMute`, `SetInput`.
   - **`SetInput` auto-calls `PrepareInputChange` first** when the active zone's `func_list` (from cached features) requires it. No user-visible flag — just works.
8. `getFeatures` caching: `~/.cache/yamaha-cli/<device-id>-features.json`. Refresh on `--refresh-features`, when missing, when cached `system_version` ≠ live `getDeviceInfo.system_version`, or on validation miss (see Validation strategy above). Use device_id (MAC) in the filename so multi-device caches don't collide.
9. **Volume command:**
   - `60` → `setVolume?volume=60` (clamped 0..max from features).
   - `+5` → `setVolume?volume=up&step=5` (server-side relative, no GET-then-SET).
   - `-5` → `setVolume?volume=down&step=5`.
   - `up` / `down` → `setVolume?volume=up|down` (default step).
   - `--db -22.5` → convert to int via getFeatures range, send absolute.
   - `--percent 50` → 0..100 scaled to 0..max, send absolute.
   - `--step <n>` overrides default step for `+N`/`-N` if user wants something non-numeric (e.g., `--step 2 up`).
10. **Power-on wait:** `yamaha power on` (and `power toggle` when transitioning off→on) polls `getStatus` every 200 ms until `power == "on"` or 10 s elapses. On timeout → exit 1 with "device did not report power=on within 10s; check the receiver". `--no-wait` skips the poll loop. `power off` is fire-and-forget.
11. **Validation:** `yamaha input <name>` validates against cached `input_list` (strict, with one auto-refresh on miss). Same pattern for `sound`, `decoder`, `scene` in Phase 2.
12. Status command: render power/input/volume(int+dB+pct)/mute/sound_program. Both table and JSON shapes have stable field names.
13. Capture `getFeatures.json` from `192.168.1.116` into `testdata/`. Unit tests against `httptest.Server` replaying canned responses + a deliberate error matrix (`response_code` ∈ {0, 1, 2, 3, 5, 6, 99}).
14. **Live integration tests** behind `//go:build integration`: `go test -tags=integration -yamaha-host=192.168.1.116 ./...` exercises read-only structural assertions (`getFeatures` returns valid JSON; `main` zone exists; volume range is sane). Never asserts specific values (volume drifts during the day). Skipped in normal `go test`. CI never runs them.

**Acceptance:**
- Fresh install with no config: `yamaha status` triggers the discovery wizard, saves `living-room`, then prints status.
- `yamaha status` (TTY) prints a 4-line table; `yamaha status | jq .power` returns `"on"`.
- `yamaha volume +5` increments by 5 in one HTTP call (verifiable in `--debug` log: single `→ setVolume?volume=up&step=5`).
- `yamaha input typo` → no HTTP call; exit 1 with `unknown input "typo"; did you mean: hdmi1, hdmi2, hdmi3?`.
- `yamaha input hdmi2` switches; `--debug` shows a `prepareInputChange` call only when `func_list` says it's required.
- `yamaha power on && yamaha volume 60 && yamaha input hdmi2` works without manual sleeps. Total time: ~2–5 s (dominated by power-on settle).
- `yamaha --device bedroom volume 60` works once a second device is added.
- A device-unreachable error exits with code 69; a YXC-error exits with code 70.

## Phase 2 — Full surface

```
yamaha --zone zone2 ...             # global flag, default = main (or device's default_zone)
yamaha sound <program>              # 2ch_stereo, straight, surr_decoder, ...   (validated)
yamaha decoder <type>               # auto, dolby_surround, dts_neural_x, ...   (validated)
yamaha scene <1-4>                  # validated
yamaha tone bass <-12..+12>
yamaha tone treble <-12..+12>
yamaha sleep <minutes|off>
yamaha tuner fm 102.5 | tuner am 1530 | tuner preset 7
yamaha netusb play|pause|stop|next|prev|ff|rew
yamaha netusb info                  # now-playing + album art URL
yamaha netusb shuffle|repeat
yamaha preset list                  # MusicCast preset enumeration
yamaha preset recall <1-40>
```

**Tasks:**
1. Tuner: handle FM Hz unit gotcha (87500 = 87.5 MHz) — accept user-friendly `102.5` and convert.
2. NetUSB browse + queue (stretch — `getListInfo` paged 8 items at a time).
3. Tab completion: cobra's built-in completion + dynamic completion for input names / sound programs / scenes (read from per-device cached `getFeatures`).
4. Extend strict validation (Phase 1) to `sound`, `decoder`, `scene`.

## Phase 3 — Watch, multi-room, raw escape hatch

Five commands cover everything the v1 plan listed under Phase 3 and "bonus endpoints":

```
yamaha watch [--device a,b,c]              # subscribe to UDP events, stream NDJSON
yamaha link create <leader> <follower>...  # MusicCast Link
yamaha link dissolve
yamaha reboot                              # system/requestSystemReboot
yamaha raw <method> [key=value ...]        # generic YXC GET passthrough
yamaha ynca <command>                      # YNCA passthrough (e.g., @MAIN:VOL=?)
```

**Watch output format:** NDJSON, one event per line:

```json
{"ts":"2026-05-07T12:34:56.123Z","device":"living-room","delta":{"main":{"volume":60}}}
{"ts":"2026-05-07T12:35:01.482Z","device":"living-room","delta":{"main":{"input":"hdmi2"}}}
```

In TTY mode (`--output table`), render compact human-readable lines:
```
12:34:56  living-room  main.volume = 60
12:35:01  living-room  main.input  = hdmi2
```

`--device a,b,c` watches multiple devices simultaneously; the `device` field disambiguates the stream.

`yamaha raw` is the keystone for endpoint reach. It accepts any of the 184 endpoints from `/tmp/yxc-mc.txt`, e.g.:

```bash
yamaha raw netusb/getMcPlaylist bank=1
yamaha raw system/setPartyMode enable=true
yamaha raw system/getYpaoConfig
```

Output is the raw `response_code`-validated JSON (rendered per `--output`). Anything in the bonus catalog (YPAO, CCS, Sonos, MusicCast playlists, surround pairing, Bluetooth device list, alarms) is reachable from day one through `raw` — promoting any one of them to a typed command later is purely additive.

**Tasks:**
1. `pkg/yxc/events.go`: bind UDP socket, attach `X-AppName: MusicCast` + `X-AppPort: <chosen>` to a `getStatus` call, parse incoming JSON deltas, renew every ~8 minutes.
2. `internal/cli/watch.go`: wrap `events.go` output as NDJSON `{ts, device, delta}` per line. Support multi-device via concurrent subscriptions.
3. `pkg/yxc.Raw(ctx, method, params)`: thin wrapper around `Do` with no schema validation — returns `json.RawMessage`.
4. `pkg/ynca`: TCP/50000 client. Connect, send line, read line, close. Use `assets/local_yud3ga.xml` from the AV Controller APK as the authoritative command list for RX-V583 — it contains the exact Cmd_List tree (every YNCA function, every parameter range) for "yud3g"-class receivers (RX-V583 is netmodule_generation 1 → yud3g).
5. MusicCast Link distribution endpoints (`/dist/setServerInfo`, `/dist/setClientInfo`, `/dist/startDistribution`).

## Critical gotchas to bake in

1. **Always check `response_code`.** HTTP 200 + `response_code != 0` is the failure mode. Wrap every call.
2. **Don't hardcode enums.** Inputs, sound programs, max volume — all come from `getFeatures`. CLI tab-completion + strict validation must read the cached file.
3. **`prepareInputChange` is automated** in `pkg/yxc.SetInput` when `func_list` requires it. Manual callers (`yamaha raw setInput …`) are on their own.
4. **Rate-limit ~100 ms** between commands within a single `Client`. Embedded box, easy to overrun.
5. **Power-on settles in 2–5 s.** Default `yamaha power on` polls `getStatus` until `power=on` (max 10 s). `--no-wait` skips. Don't `sleep` — poll.
6. **Tuner FM is in Hz, not kHz:** `87500` means 87.5 MHz.
7. **Volume is an integer 0..161, not 0..100, not dB.** Surface `--db` and `--percent` flags that convert against the device's real range from `getFeatures`. `+N`/`-N` use server-side `volume=up|down&step=N`.
8. **No HTTPS, no auth.** Document in README — Yamaha design choice. CLI has no credentials to manage.
9. **Event subscription is unicast UDP from the same source IP** — breaks across NAT. Renew at ~8 min, not 10.
10. **Per-device feature cache is keyed by `device_id` (MAC),** not IP. Survives DHCP renewals; safe across multi-device setups.
11. **YXC is GET-only.** Verified via APK source. POST sites in the apps are legacy XML / Gracenote / account APIs. `pkg/yxc.Client.Do` does not need a method parameter.

## Reference material

- **YXC API Spec — Basic** (PDF): https://community.symcon.de/uploads/short-url/7r8QTdkYFNfJVJmKbtqvdleuzKt.pdf
- **YXC API Spec — Advanced** (PDF, MusicCast Link): https://community.symcon.de/uploads/short-url/vRXaJXAn6vI2DSQYMHF0aqLbdir.pdf
- **MusicCast HTTP simplified API for Control Systems v1.1** (June 2017): https://forum.smartapfel.de/attachment/4358-yamaha-musiccast-http-simplified-api-for-controlsystems-pdf/
- **Reference impl — Python `aiomusiccast`** (Home Assistant): https://github.com/vigonotion/aiomusiccast
- **Reference impl — Python `pyamaha`** (clean module taxonomy): https://github.com/rsc-dev/pyamaha
- **Reference impl — Node.js `yamaha-yxc-nodejs`**: https://github.com/foxthefox/yamaha-yxc-nodejs
- **Reference impl — Go `atamanroman/ymc`** (GPL — read-only reference): https://github.com/atamanroman/ymc
- **YNCA models matrix + grammar**: https://github.com/mvdwetering/yamaha_ynca
- **AV Controller APK** (Yamaha official, decompiled): https://apkpure.com/av-controller/com.yamaha.av.avcontroller
- **MusicCast Controller APK** (Yamaha official, decompiled): https://apkpure.com/musiccast-controller/com.yamaha.av.musiccastcontroller

## Captured live artifacts

- `/tmp/yamaha-fw/R0424-0287.bin` — encrypted firmware (no further use; bootloader-decrypted).
- `/tmp/yxc_features.json` — full `getFeatures` response from the live device. **Copy into `testdata/getFeatures.json` when scaffolding the repo.**
- `/tmp/yxc_desc_49154.xml` — UPnP MediaRenderer description XML.

## Decompiled APK artifacts (authoritative protocol references)

These are the highest-value sources for endpoint discovery — more current than the public PDF spec.

- `/tmp/yamaha-apk/avcontroller-jadx/sources/y1/b.java` — full YXC command switch (every endpoint with case-id, params, headers) for the AV Controller app.
- `/tmp/yamaha-apk/musiccast-jadx/sources/a0/b3.java` — YXC client + `X-AppName`/`X-AppPort` event subscription for the MusicCast app. Confirms YXC is GET-only (line 1162: `setRequestMethod("GET")`); the POST at line 1293 is for non-YXC `/yamahapim/...` account endpoints.
- `/tmp/yamaha-apk/musiccast-jadx/sources/fe/d.java` — YXC response JSON parsing for ~50 commands. Excellent for deriving Go struct shapes.
- `/tmp/yxc-mc.txt` — sorted list of all 184 YXC endpoints discovered in the MusicCast APK. **The `raw` subcommand's de-facto reference.**
- `/tmp/yamaha-apk/avc-apktool/assets/local_yud3ga.xml` + `local_yud3gb.xml` — **YNCA Cmd_List spec for RX-V583 class** (yud3g, netmodule_generation 1). Lift verbatim into a Go YNCA driver.
- `/tmp/yamaha-apk/avcontroller-jadx/sources/v1/q.java`, `e1/b.java` — legacy YNCA/`/YamahaRemoteControl/ctrl` XML envelope construction.
- `/tmp/yamaha-apk/musiccast-jadx/sources/r2/b0.java`, `zc/d.java`, `nc/n.java` — SSDP discovery + UDP event listener implementation (use as reference for `pkg/discover` and `pkg/yxc/events`).
- `/tmp/yamaha-apk/mc-apktool/assets/mc_devices.json` — model → image/feature mapping (RX-V583 listed: `netmodule_generation: 1, image_power: img_model_power_avr2a`).

**Exact User-Agent / header strings observed in the apps:**
- AV Controller: `User-Agent: AV_CONTROLLER/4.00 (Android)`
- MusicCast Controller: `User-Agent: MusicCast`
- Event subscription: `X-AppName: MusicCast` + `X-AppPort: <local-udp-port>` (no `/<ver>` suffix despite what the PDF says).
- **CLI choice:** `User-Agent: yamaha-cli/<ver>` (self-identification — the receiver doesn't enforce UA), `X-AppName: MusicCast` only on event-subscribing GETs.

## Definition of done (Phase 1)

- `go install` produces a single `yamaha` binary.
- **First-run flow:** with no config and `yamaha status` invoked from a TTY, the wizard discovers `192.168.1.116`, prompts for an alias, saves config, and prints status. From a non-TTY, the same call exits 64 with a hint. Zero-found case exits 69.
- **Live RX-V583 against `192.168.1.116`:**
  - `yamaha status` (TTY) prints power/input/volume/mute as a table.
  - `yamaha status | jq -r .power` returns `on`.
  - `yamaha volume +5` is observable in `--debug` as exactly one HTTP request: `→ setVolume?volume=up&step=5`.
  - `yamaha power on && yamaha volume 60 && yamaha input hdmi2` works without manual sleeps; `power on` blocks until power=on or 10 s timeout. With `--no-wait`, returns immediately.
  - `yamaha mute on` mutes; `yamaha mute off` unmutes.
  - `yamaha input typo` exits 1 with a "did you mean" suggestion and zero HTTP calls (visible in `--debug` log: only the cache-refresh attempt, no setInput).
  - `yamaha --device bedroom status` works after `yamaha discover --add` saves a second device (or after manual config edit).
- **Failure modes:**
  - Network unreachable → exit 69, JSON-or-text error per `--output`.
  - YXC `response_code != 0` → exit 70, error includes the code.
  - Power-on timeout → exit 1, message names the timeout duration.
- **Tests:**
  - `go test ./...` passes against canned `httptest.Server` replaying `testdata/getFeatures.json` and `testdata/getStatus.json`, plus an error matrix exercising `response_code` ∈ {0, 1, 2, 3, 5, 6, 99}.
  - `go test -tags=integration -yamaha-host=192.168.1.116 ./...` passes read-only structural smoke tests against the live device. Skipped without the tag.
- **Docs:** README covers config schema (with multi-device example), env vars (`YAMAHA_HOST`, `YAMAHA_DEVICE`, `YAMAHA_DEBUG`), `--output` modes, exit-code table, and the no-auth-on-LAN security note.

## Future expansion (deferred — accessible via `raw` from day one)

These were "Phase 3 bonus endpoints" in v1. They remain documented for context but are not on the build list. Promoting any of them to a typed command later is additive.

| Endpoint | Use case |
|---|---|
| `system/setPartyMode`, `setPartyVolume` | Party-mode broadcast |
| `system/getYpaoConfig`, `setYpaoVolume`, `setSpeakerEqualizer` | YPAO room calibration |
| `system/connectBluetoothDevice`, `disconnectBluetoothDevice`, `getBluetoothDeviceList`, `deleteBluetoothDeviceHistory` | Bluetooth pairing |
| `netusb/getMcPlaylist`, `manageMcPlaylist`, `clearMcPlaylist`, `moveMcPlaylistItem` | MusicCast playlist editing |
| `dist/setMcSurround`, `confirmMcSurround`, `resetMcSurround`, `setMcSpeakerTestTone` | MusicCast Surround pairing |
| `ccs/startCcs`, `stopCcs`, `executeCommand`, `devices`, `presets` | Cinema-Caster service |
| `clock/setMultiAlarm`, `stopAlarm` | Alarm clock features |
| `system/setAnalyticsStatus`, `setAnalyticsConfirmed` | Telemetry consent |
| `system/getConnectSonosDeviceList`, `setSonosToken`, `setSonosSetting` | Sonos integration |
| `system/getDisklavierSettings`, `setDisklavierSettings` | Disklavier piano (irrelevant for V583) |

Full catalog in `/tmp/yxc-mc.txt`.
