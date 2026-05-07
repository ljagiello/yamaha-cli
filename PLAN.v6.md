<!-- v6 - 2026-05-07 - Generated via /improving-plans from PLAN.v5.md -->

# Yamaha RX-V583 Go CLI — Build Plan v6

A command-line tool in Go to control a Yamaha RX-V583 AV receiver over the local network without using the physical remote.

## What changed in v6

A single, targeted consistency fix:

- **`YAMAHA_HOST` env var** is now in the resolution order. v5 listed it in the README docs requirement but never wired it into the lookup chain. Slotted between `--host` (rule 1) and `--device` (rule 3) so env vars consistently mirror their flag counterparts.

**This is a convergence pass.** Five prior reviews resolved every genuine design choice. v6 is essentially v5 with this one inconsistency repaired. The next valuable activity is implementation.

## Target device

- **Model:** Yamaha RX-V583 (2017, 7.2-channel, MusicCast)
- **IP:** `192.168.1.116`
- **Firmware:** 2.87 (YXC api_version 2.08)
- **Device ID:** `00A0DEFBE863`
- **UDN:** `uuid:9ab0c000-f668-11de-9976-00a0defbe863`

## Research summary

| Track | Outcome |
|---|---|
| **Firmware analysis** | Dead end. The 35.7 MB blob (`R0424-0287.bin`) is fully encrypted (uniform 8.0 bits/byte entropy, no ECB patterns). Bootloader holds the key. No protocol artifacts recoverable via static analysis. |
| **Android APK analysis** | Also dead end for firmware decryption. Both **AV Controller** (`com.yamaha.av.avcontroller` v5.60) and **MusicCast Controller** (`com.yamaha.av.musiccastcontroller` v6.21) decompiled. Neither contains firmware keys — the receiver self-updates from Yamaha's CDN after the app sends `system/updateFirmware?type=network`. The one AES find (whitebox-key-in-JPEG decryptor) is for Gracenote credentials, not firmware. **However**: the APKs yielded the authoritative YXC endpoint catalog (184 endpoints) and YNCA command grammar XML for RX-V583 (`assets/local_yud3ga.xml`, `local_yud3gb.xml`). |
| **Protocol research** | RX-V583 speaks **YamahaExtendedControl (YXC)** — HTTP/JSON on port 80, **GET-only** (verified via APK source). Public PDF spec mirrors exist. YNCA also documented but officially unconfirmed for V583. |
| **Live device probe** | **Both protocols actually work.** YXC `getDeviceInfo` returned full device JSON. YNCA on TCP/50000 echoed `@SYS:VERSION=2.87/1.81`. UPnP MediaRenderer also exposed on port 49154 (ignored — over-engineered). |
| **Existing impls** | No actively maintained MIT/Apache Go library. `atamanroman/ymc` exists but GPL + abandoned. Best non-Go references: `aiomusiccast` (Python, Home Assistant), `pyamaha` (clean taxonomy), `foxthefox/yamaha-yxc-nodejs`. **Genuine gap worth filling.** |

**Decision: target YXC. YNCA is a stretch goal — and `assets/local_yud3ga.xml` from the AV Controller APK is the authoritative RX-V583-specific command list.**

**Firmware decrypt:** abandoned. Would require bootloader extraction (UART/JTAG/chip-off) — out of scope.

## Verified device capabilities (live `/system/getFeatures`)

- **Zones:** `main` (full feature set) + `zone2` (zone_b: power/volume/mute/input only)
- **Inputs (22):** hdmi1-4, av1-3, audio1-3, aux, tuner, usb, bluetooth, server, net_radio, airplay, spotify, tidal, deezer, pandora, mc_link
- **Volume:** integer 0–161 (= -80.5..+16.5 dB step 0.5; **one integer step ≈ 0.5 dB**). Always read range from `getFeatures`, never hardcode.
- **Sound programs (20):** munich, vienna, standard, action_game, 2ch_stereo, 7ch_stereo, surr_decoder, straight, …
- **Surround decoders:** auto, dolby_surround, dts_neural_x, dts_neo6_cinema, dts_neo6_music
- **Tuner:** FM 87.5–107.9 MHz step 200, AM 530–1710 step 10, 40 presets
- **NetUSB:** play queue 200, 40 MusicCast presets, MusicCast Link distribution v2.0, CCS supported
- **Tone control:** -12..+12. Dialogue level 0..3. Sleep timer. 4 scenes.
- **Push events:** add headers `X-AppName: MusicCast` + `X-AppPort: <udp>` to any GET → receiver UDPs JSON deltas to your IP. Re-subscribe every <10 minutes.

## Architecture

```
yamaha-cli/
├── cmd/yamaha/main.go            # cobra entrypoint; signal.NotifyContext for SIGINT/SIGTERM
├── internal/cli/                 # cobra commands (power, volume, input, zone, status, discover, raw, watch, …)
├── internal/config/              # YAML loader, multi-device schema, first-run wizard, atomic writes, UDN persistence
├── internal/output/              # render: json | yaml | table | auto (TTY-detect, NO_COLOR-aware)
├── internal/debuglog/            # request/response trace when --debug or YAMAHA_DEBUG=<truthy>
├── pkg/yxc/                      # YXC HTTP client — safe for concurrent use
│   ├── client.go                 # http.Client + base URL + retry + response_code unwrap + typed errors
│   ├── system.go                 # getDeviceInfo, getFeatures, getNetworkStatus, requestSystemReboot
│   ├── zone.go                   # main + zone2: setPower, setVolume, setMute, setInput, setSoundProgram, setSleep, getStatus
│   ├── netusb.go                 # setPlayback, getPlayInfo, recallPreset, getListInfo
│   ├── tuner.go                  # setFreq, recallPreset, getPresetInfo
│   ├── dist.go                   # MusicCast Link (Phase 3)
│   ├── events.go                 # UDP listener + header subscription + 8-min renewer + reconnect (Phase 3)
│   ├── validate.go               # validate input/sound_program/scene against cached getFeatures
│   └── types.go                  # enums + structs mirroring getFeatures
├── pkg/discover/                 # SSDP MediaRenderer search → filter Yamaha → return YXC base URLs + UDN; also: lookup-by-UDN
├── pkg/ynca/                     # (Phase 3) thin TCP/50000 fallback + RX-V583 command catalog
└── testdata/
    ├── getFeatures.json          # captured from real device for offline tests
    └── getStatus.json            # canned snapshots for table-renderer tests
```

**Why split `pkg/yxc` and `pkg/discover` from `internal/`:** they're useful as standalone Go libraries. Keep dependency surface tiny: stdlib + cobra + viper + `koron/go-ssdp` + `mattn/go-isatty`.

**`pkg/yxc.Client` is safe for concurrent use.** Multiple goroutines can call methods on the same `Client` instance simultaneously. Internal state (the embedded `http.Client`, base URL, headers) is read-only after construction; the 100 ms intra-process rate-limit uses a mutex-guarded last-call timestamp. Matches the convention of `net/http.Client`.

**CLI framework:** [`spf13/cobra`](https://github.com/spf13/cobra) + [`spf13/viper`](https://github.com/spf13/viper) for config + env-var binding.

## Configuration

`~/.config/yamaha-cli/config.yaml` (XDG-respecting; falls back to `$HOME/.config/...`).

```yaml
default_device: living-room

devices:
  living-room:
    host: 192.168.1.116
    udn: uuid:9ab0c000-f668-11de-9976-00a0defbe863    # auto-saved on first discovery
    default_zone: main                                 # main | zone2
  bedroom:
    host: 192.168.1.118
    udn: uuid:9ab0c000-f668-11de-9976-00a0defaa111
    default_zone: main
```

**Resolution order for the active device:** flag > env > config, with each flag/env pair adjacent in priority.

1. `--host <ip>` flag — bypasses the config entirely (anonymous device, no alias, no UDN).
2. `YAMAHA_HOST` env var — same semantics as `--host` (anonymous, no UDN, no DHCP rediscovery).
3. `--device <name>` flag → look up in `devices`.
4. `YAMAHA_DEVICE` env var → look up in `devices`.
5. `default_device` from config.
6. Single-device shortcut: if exactly one device exists, use it regardless of `default_device`.
7. None of the above → trigger first-run flow.

**Atomic writes.** Both the wizard, `discover --add`, and the DHCP-resilience flow write to `~/.config/yamaha-cli/config.yaml.tmp` then rename. Concurrent invocations cannot corrupt the file.

**UDN persistence.** First-run wizard and `discover --add` always save the UDN. Pre-v5 configs without UDN keep working but lose DHCP resilience until refreshed.

## DHCP resilience

Yamaha receivers on home networks routinely get a new IP after DHCP renewals or router reboots. The CLI handles this transparently when the active device was resolved from config (i.e., not from `--host` or `YAMAHA_HOST`).

**Trigger:** any HTTP transport error (one retry exhausted: connection refused, no route to host, timeout, ECONNRESET) on a config-resolved host.

**Flow:**
1. Run a 3 s SSDP scan with `ST: urn:schemas-upnp-org:device:MediaRenderer:1`.
2. Filter responses for `manufacturer == "Yamaha Corporation"`, match by UDN.
3. **Found at a new IP:** update the config (atomic write), trace the IP change in `--debug`, retry the original command transparently. The user sees only success.
4. **Not found:** exit 69 with stderr `device "<alias>" (UDN <udn>) not reachable; check power and network`.

**Skipped when:**
- Active device was resolved via `--host <ip>` or `YAMAHA_HOST` (anonymous, no UDN to match).
- Cached UDN is missing from the config (pre-v5 config). Exit 69 with hint to run `yamaha discover --add` to re-save the entry with UDN.

**Not retried more than once.** Each command does at most one rediscover attempt. Repeated failures fall through to exit 69.

## First-run flow

When no host can be resolved by the rules above:

- **Interactive (stdout is a TTY):** print "No device configured. Searching the LAN…", run a 3 s SSDP scan, list found Yamaha devices, prompt the user to pick one, prompt for an alias (default: slugified `network_name`, e.g. `RX-V583 FBE863` → `rx-v583-fbe863`), and save the config including UDN. Re-run the original command transparently.
- **Alias collision:** if the proposed alias is already in `devices`, suggest `<alias>-2`, `<alias>-3`, … and prompt the user to confirm or override.
- **Zero devices found:** exit code 69 with stderr hint: `no Yamaha devices found on LAN; pass --host <ip> manually`.
- **Non-interactive (piped, scripted, CI):** exit with code `64` (EX_USAGE) and stderr hint: `no device configured; run 'yamaha discover' or pass --host`.

The wizard never auto-runs in non-interactive contexts — pipelines should fail fast and loud, not hang waiting for input.

## Output formatting

Global flag: `--output {json|yaml|table|auto}` (alias `-o`). Default: `auto`.

`auto` resolves to:
- `table` (human-readable) when `stdout` is a TTY.
- `json` when stdout is piped or redirected.

This is the gh / kubectl / docker idiom.

**Color in table mode:** ANSI styling on by default in TTY mode; honors the `NO_COLOR` environment variable (any value → no color) per [no-color.org](https://no-color.org). `--no-color` flag also supported.

**Successful mutating commands** (`power on`, `volume 60`, etc.) emit `{}` in JSON modes and a single confirmation line in table mode. They never print the entire device state — that's `yamaha status`'s job.

## Validation strategy

`yamaha input`, `yamaha sound`, `yamaha decoder`, `yamaha scene` all validate the argument against the cached `getFeatures.<device-id>.json` *before* making any HTTP call.

Flow:
1. Load cached `getFeatures` for the active device. If cache is missing or TTL expired, fetch and save first.
2. If the argument is in the cache's allowed list → proceed with the YXC call.
3. If not → fetch `getFeatures` once (auto-refresh) and re-validate against the fresh data.
4. If still not a match → exit code 1 with a `did you mean: …` suggestion built from the closest 3 candidates by Levenshtein distance.

Why: the device returns a generic `response_code: 6` ("not found") for invalid arguments — useless for diagnostics. Strict client-side validation gives the user a real error message with suggestions.

## getFeatures cache invalidation

Per-device cache file: `~/.cache/yamaha-cli/<device-id>-features.json` (key by MAC, not IP).

**TTL:** 7 days (file mtime).

**Refresh triggers:**
- File missing.
- File mtime > 7 days old.
- `--refresh-features` flag passed explicitly.
- Validation miss (see Validation strategy) — covers the "user upgraded firmware mid-week and got a new input" edge case.

**Not** a refresh trigger: per-invocation `system_version` comparison. That would require a `getDeviceInfo` HTTP call on every command including hot paths like `volume +5`, defeating the cache's purpose.

## HTTP retry policy

`pkg/yxc.Client` retries every YXC GET **once** on transient errors:

**Retried:**
- `net.OpError` (connection refused, no route to host, DNS failure)
- `context.DeadlineExceeded` on the request
- `io.ErrUnexpectedEOF` / connection reset by peer

**Not retried:**
- YXC `response_code != 0` (device-side decisions, not transient)
- HTTP non-200 status (4xx/5xx)
- Validation errors raised by our code
- Any error from a context cancelled via SIGINT (don't retry past the user's "stop")

**Backoff:** 250 ms before the single retry. Total wall-clock cap with default 5 s timeout: ~10.25 s. The retry is invisible to the caller unless `--debug` is on.

If the retry also fails on a config-resolved host with a saved UDN, the CLI then attempts DHCP-resilience rediscovery (one shot) before giving up.

## Signal handling

The cobra root command builds its `context.Context` from `signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)`. Every long-running operation respects it:

- **HTTP requests:** issued via `http.NewRequestWithContext(ctx, …)`. SIGINT cancels in-flight calls cleanly.
- **Power-on poll:** the 200 ms-tick loop selects on `ctx.Done()` between ticks. SIGINT exits the loop immediately.
- **`yamaha watch`:** the UDP receive loop and the ~8-min renewal goroutine both consume `ctx.Done()`. SIGINT cancels both, sends a final `{"event":"shutdown"}` NDJSON line in graceful mode, and exits 0.
- **SSDP discovery:** the `koron/go-ssdp` search runs on a deadline; SIGINT cancels via context.

**Exit codes:**
- SIGINT during `yamaha watch` → **0** (graceful shutdown is the expected path).
- SIGINT during any other command → **130** (128 + SIGINT). Stderr prints `cancelled by user`.

The receiver is **not** rolled back on cancel — `power on` has already issued the request; cancellation only stops *our* wait.

## Debug / observability

- `--debug` flag, or `YAMAHA_DEBUG=<truthy>` env var → log every YXC request URL + response body to stderr in a one-line-per-call format prefixed with `→` and `←`. SSDP probes also logged. Retry attempts logged with `→ retry`. DHCP-rediscover attempts logged with `→ rediscover`.
- Truthy parsing: any case-insensitive value of `1`, `true`, `yes`, `on`. Empty / `0`/`false`/`no`/`off` → off.
- Debug output is independent of `--output`: stdout stays clean (still parseable JSON when piped), stderr gets the trace.
- No log file by default; `2> trace.log` is the user's escape hatch.

## Exit codes

Sysexits-lite — small, predictable, plays well with shell idioms:

| Code | Meaning |
|---|---|
| 0 | Success (including `yamaha watch` graceful shutdown via SIGINT) |
| 1 | Generic error (validation failure, "did you mean", power-on timeout) |
| 2 | Misuse / invalid CLI argument (cobra default) |
| 64 | `EX_USAGE` — no device configured & non-interactive |
| 69 | `EX_UNAVAILABLE` — device unreachable (network error, retry exhausted, DHCP-rediscover failed, zero-found in wizard) |
| 70 | `EX_SOFTWARE` — device returned non-zero `response_code` (e.g., feature unsupported, device not ready) |
| 130 | SIGINT during a non-watch command |

Error message goes to stderr in human form regardless of `--output`. With `--output json`, also emit `{"error": "...", "code": <int>, "yxc_response_code": <int|null>}` to stdout.

## Zone scope

The `--zone {main|zone2}` flag applies only to zone-scoped commands. Default: device's `default_zone`.

| Command | `--zone` | Notes |
|---|---|---|
| `status` | yes | |
| `power` | yes | |
| `volume` | yes | |
| `mute` | yes | |
| `input` | yes | |
| `sound` | yes | |
| `decoder` | yes | |
| `scene` | yes | |
| `tone` | yes | |
| `sleep` | yes | |
| `netusb` | no | NetUSB is system-wide |
| `tuner` | no | Tuner is system-wide |
| `preset` | no | |
| `discover` | no | |
| `config` | no | |
| `version` / `--version` | no | |
| `watch` | no | Subscribes to all zones simultaneously |
| `link` | no | |
| `reboot` | no | |
| `raw` | no | User encodes zone in the path: `raw zone2/setVolume volume=60` |
| `ynca` | no | YNCA encodes the zone in the subunit (`@ZONE2:VOL=...`) |

Passing `--zone` to a zone-irrelevant command is a soft warning (logged to stderr), not an error.

## Phase 1 — MVP

Goal: replace the most-used remote buttons + scriptable output + zero-config first run.

```
yamaha power on|off|toggle [--no-wait]
yamaha volume <int|±N|up|down> [--db|--percent|--step N]
yamaha mute on|off|toggle
yamaha input <name>                 # tab-completion + strict validation
yamaha status                       # pretty (TTY) or JSON (piped)
yamaha discover [--add]
yamaha config show
yamaha config path
yamaha completion {bash|zsh|fish}
yamaha version                      # also: yamaha --version
```

**Tasks:**
1. `go mod init github.com/ljagiello/yamaha-cli`; pull cobra + viper + koron/go-ssdp + mattn/go-isatty.
2. `cmd/yamaha/main.go`: build root context via `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`. Pass to all cobra `RunE` handlers.
3. `internal/config`: load multi-device YAML with the resolution order above; **atomic write** (`<file>.tmp` + `rename`); persist `udn` per device. Bind to viper. Global flags: `--host`, `--device`, `--zone`, `--output`, `--no-color`, `--debug`, `--no-wait`, `--refresh-features`. Env var bindings: `YAMAHA_HOST` ↔ `--host`, `YAMAHA_DEVICE` ↔ `--device`, `YAMAHA_DEBUG` ↔ `--debug`, `NO_COLOR` ↔ `--no-color`.
4. `internal/output.Render(v any, format string)`: auto-detect TTY, emit JSON/YAML/table. Honor `NO_COLOR`/`--no-color` for ANSI styling. Format `error` payloads consistently.
5. `internal/debuglog`: stderr request/response tracer activated by `--debug` or `YAMAHA_DEBUG=<truthy>`. Truthy-parse helper covers `1`/`true`/`yes`/`on` (case-insensitive); empty / `0`/`false`/`no`/`off` → off.
6. `pkg/discover` using `koron/go-ssdp`:
   - `Search(ctx, timeout)` → `[]Device{Name, Host, Model, BaseURL, UDN}`. Search target `urn:schemas-upnp-org:device:MediaRenderer:1`. Filter for `manufacturer == "Yamaha Corporation"`. Dedup by UDN.
   - `LookupByUDN(ctx, udn, timeout)` → `(Device, error)`. Used by DHCP-resilience flow.
7. First-run wizard in `internal/cli`: triggered when device resolution fails AND stdout is a TTY. Handles zero-found (exit 69), one-found, multi-found. Alias collision → `<alias>-N` suggestion. Saves UDN. Writes via the atomic-write helper.
8. **DHCP resilience** in `internal/cli`: when a config-resolved host fails after the in-Client retry, call `discover.LookupByUDN`. On success, atomic-update the config and retry the command once. On failure, exit 69. Skipped for `--host`/`YAMAHA_HOST` calls. Skipped for pre-v5 configs without UDN (exit 69 with hint).
9. `pkg/yxc.Client`:
   - **Safe for concurrent use** (documented in Go doc).
   - Construct from base URL + `*http.Client` (default 5 s timeout).
   - Headers: `User-Agent: yamaha-cli/<ver>` on every request; `X-AppName: MusicCast` + `X-AppPort: <port>` only on event-subscription requests.
   - `Do(ctx, method string, params url.Values) (json.RawMessage, error)` — GET `/v1/<method>?<params>` via `http.NewRequestWithContext`. Parse JSON, return typed error if `response_code != 0`. Map known codes (5 → `ErrDeviceNotReady`, 6 → `ErrNotFound`, etc.).
   - **Retry once** on transient errors after 250 ms backoff. Do **not** retry on YXC `response_code != 0`, HTTP non-200, or context-cancelled errors.
   - 100 ms intra-Client rate-limit via mutex-guarded last-call timestamp.
   - Higher-level methods: `GetDeviceInfo`, `GetFeatures`, `GetStatus`, `SetPower`, `SetVolume`, `SetMute`, `SetInput`.
   - **`SetInput` auto-calls `PrepareInputChange` first** when the active zone's `func_list` (from cached features) requires it.
10. **`getFeatures` caching:** `~/.cache/yamaha-cli/<device-id>-features.json`. **TTL 7 days** (file mtime). Refresh on: missing, expired, `--refresh-features`, or validation miss. **No** per-invocation `system_version` check.
11. **Volume command:**
    - `60` → `setVolume?volume=60` (clamped 0..max from features).
    - `+5` → `setVolume?volume=up&step=5`. `-5` → `setVolume?volume=down&step=5`.
    - `up` / `down` → `setVolume?volume=up|down` (default step).
    - `--db -22.5` → convert to int via getFeatures range, send absolute. **Absolute only.**
    - `--percent 50` → 0..100 scaled to 0..max, send absolute. **Absolute only.**
    - `--step <n>` overrides the step in `up`/`down` and `+N`/`-N`.
    - Mixing `--db`/`--percent` with `+N`/`-N` is an error (exits 2).
12. **Power-on wait:** `yamaha power on` (and `power toggle` when transitioning off→on) polls `getStatus` every 200 ms until `power == "on"` or 10 s elapses. The poll selects on `ctx.Done()` between ticks. On timeout → exit 1 with "device did not report power=on within 10s; check the receiver". On SIGINT → exit 130 with "cancelled by user". `--no-wait` skips. `power off` is fire-and-forget.
13. **Validation:** `yamaha input <name>` validates against cached `input_list` (strict, with one auto-refresh on miss). Same pattern for `sound`, `decoder`, `scene` in Phase 2.
14. Status command: render power/input/volume(int+dB+pct)/mute/sound_program. Both table and JSON shapes have stable field names.
15. Capture `getFeatures.json` from `192.168.1.116` into `testdata/`. Unit tests against `httptest.Server` replaying canned responses + a deliberate error matrix (`response_code` ∈ {0, 1, 2, 3, 5, 6, 99}) + a transient-error retry matrix + a DHCP-shuffle simulation (host A returns ECONNREFUSED, SSDP returns same UDN at host B, retry succeeds at host B, config updated).
16. **Live integration tests** behind `//go:build integration`: `go test -tags=integration -yamaha-host=192.168.1.116 ./...` exercises read-only structural assertions (`getFeatures` returns valid JSON; `main` zone exists; volume range is sane). Never asserts specific values. Skipped in normal `go test`.

**Acceptance:**
- Fresh install with no config: `yamaha status` triggers the discovery wizard, saves `living-room` (with UDN), then prints status. Zero-found exits 69. Non-TTY exits 64.
- `yamaha status` (TTY) prints a 4-line table; `yamaha status | jq .power` returns `"on"`.
- `yamaha volume +5` is observable in `--debug` as exactly one HTTP request: `→ setVolume?volume=up&step=5`.
- `yamaha volume +5 --db` exits 2 with a clear message.
- `yamaha input typo` → no HTTP call; exit 1 with `unknown input "typo"; did you mean: hdmi1, hdmi2, hdmi3?`.
- `yamaha input hdmi2` switches; `--debug` shows a `prepareInputChange` call only when `func_list` says it's required.
- `yamaha power on && yamaha volume 60 && yamaha input hdmi2` works without manual sleeps.
- **Ctrl-C during `yamaha power on`:** exits 130 with "cancelled by user". The receiver was already told to power on; cancellation only stops our wait.
- `yamaha --device bedroom volume 60` works after a second device is added.
- `YAMAHA_HOST=192.168.1.116 yamaha status` works without any config file present (anonymous mode).
- **DHCP shuffle:** manually edit config IP to a wrong-but-routable address (e.g., `192.168.1.250`); `yamaha status` succeeds after one transparent rediscover, and the config is updated to the real IP.
- Network unreachable + UDN missing → exit 69 with hint to refresh config.
- YXC `response_code != 0` → exit 70 with code in error payload.
- Power-on timeout → exit 1.
- Stale-cache scenario: cache deliberately omits `hdmi3`; running `yamaha input hdmi3` triggers exactly one cache refresh and one `setInput`. Refreshed cache persisted atomically.

## Phase 2 — Full surface

```
yamaha --zone zone2 ...             # global flag (zone-scoped commands only)
yamaha sound <program>              # validated
yamaha decoder <type>               # validated
yamaha scene <1-4>                  # validated
yamaha tone bass <-12..+12>
yamaha tone treble <-12..+12>
yamaha sleep <minutes|off>
yamaha tuner fm 102.5 | tuner am 1530 | tuner preset 7
yamaha netusb play|pause|stop|next|prev|ff|rew
yamaha netusb info
yamaha netusb shuffle|repeat
yamaha preset list
yamaha preset recall <1-40>
```

**Tasks:**
1. Tuner: handle FM Hz unit gotcha (87500 = 87.5 MHz) — accept user-friendly `102.5` and convert.
2. NetUSB browse + queue (stretch — `getListInfo` paged 8 items at a time).
3. Tab completion: cobra's built-in completion + dynamic completion for input names / sound programs / scenes (read from per-device cached `getFeatures`).
4. Extend strict validation to `sound`, `decoder`, `scene`.

## Phase 3 — Watch, multi-room, raw escape hatch

```
yamaha watch [--device a,b,c]              # subscribe to UDP events, stream NDJSON
yamaha link create <leader> <follower>...  # MusicCast Link
yamaha link dissolve
yamaha reboot                              # system/requestSystemReboot
yamaha raw <method> [key=value ...]        # generic YXC GET passthrough
yamaha ynca <command>                      # YNCA passthrough (e.g., @MAIN:VOL=?)
```

### Watch — output and resilience

NDJSON, one event per line, with wrapper:

```json
{"ts":"2026-05-07T12:34:56.123Z","device":"living-room","delta":{"main":{"volume":60}}}
{"ts":"2026-05-07T12:35:01.482Z","device":"living-room","delta":{"main":{"input":"hdmi2"}}}
```

In TTY mode (`--output table`), render compact human-readable lines:
```
12:34:56  living-room  main.volume = 60
12:35:01  living-room  main.input  = hdmi2
```

`--device a,b,c` watches multiple devices simultaneously; the `device` field disambiguates.

**Auto-reconnect with exponential backoff.** Reconnect triggers:
- UDP renewal failure (the periodic re-subscribe HTTP GET fails).
- 30 s of silence (heartbeat).
- Connection error during initial subscription.

Backoff schedule: 1 s → 2 s → 5 s → 15 s → 60 s, capped at 60 s. Reset on successful reconnect.

Control event NDJSON line on each reconnect attempt:
```json
{"ts":"...","device":"living-room","event":"reconnect","attempt":3,"reason":"30s silence"}
```

`watch` never exits voluntarily on connection failure. **SIGINT cancels via the root context, sends a final `{"event":"shutdown"}` NDJSON line, and exits 0.**

### Raw — passthrough to any YXC endpoint

`yamaha raw <method> [key=value ...]` accepts any of the 184 endpoints from `/tmp/yxc-mc.txt`. Method is the path under `/YamahaExtendedControl/v1/`. Positional `key=value` arguments are URL-encoded and joined with `&`. Multi-value via repeating key:

```bash
yamaha raw netusb/getMcPlaylist bank=1 lang=en
yamaha raw system/setPartyMode enable=true
yamaha raw system/setName name="Living Room"      # shell quotes the value
yamaha raw netusb/manageList list_id=1 type=play index=3
```

Output is the raw `response_code`-validated JSON, rendered per `--output`. Anything in the bonus catalog (YPAO, CCS, Sonos, MusicCast playlists, surround pairing, Bluetooth device list, alarms) is reachable from day one. Promoting any one of them to a typed command later is purely additive.

### Link — MusicCast multi-room

`yamaha link create <leader> <follower>...` orchestrates the canonical sequence:

1. `setServerInfo` on the **leader** (declares it as a distribution source).
2. `setClientInfo` on **each follower** (points it at the leader).
3. `startDistribution` on the **leader** (kicks off audio).

If any step fails, attempt rollback (`stopDistribution` on leader, drop client info on followers) and surface the error. Cycle detection: refuse to make a device a follower when it's already a leader of a different group.

### YNCA fallback

`pkg/ynca`: TCP/50000 client. Connect, send line, read line, close. Use `assets/local_yud3ga.xml` from the AV Controller APK as the authoritative command list for RX-V583 — it contains the exact Cmd_List tree (every YNCA function, every parameter range) for "yud3g"-class receivers.

**Capability probe.** First time the user runs `yamaha ynca <command>` against a device, send `@SYS:VERSION=?` with a 3 s read timeout. Cache the result (`<device-id>-ynca.txt` with `supported|unsupported`). Subsequent invocations skip the probe. If unsupported, exit 70 with a clear error.

## Critical gotchas

1. **Always check `response_code`.** HTTP 200 + `response_code != 0` is the failure mode.
2. **Don't hardcode enums.** Inputs, sound programs, max volume — all come from `getFeatures`. Strict validation reads the cached file.
3. **`prepareInputChange` is automated** in `pkg/yxc.SetInput` when `func_list` requires it. Manual callers (`yamaha raw setInput …`) are on their own.
4. **Rate-limit ~100 ms** between commands within a single `Client`. Embedded box, easy to overrun.
5. **Power-on settles in 2–5 s.** Default `yamaha power on` polls `getStatus` until `power=on` (max 10 s). `--no-wait` skips. Don't `sleep` — poll. SIGINT cancels the poll cleanly (exit 130); the receiver still acts on the original power-on request.
6. **Tuner FM is in Hz, not kHz:** `87500` means 87.5 MHz.
7. **Volume is an integer 0..161, not 0..100, not dB.** `--db` and `--percent` are absolute-only. `+N`/`-N` use server-side `volume=up|down&step=N`. **One step ≈ 0.5 dB.**
8. **No HTTPS, no auth.** Document in README — Yamaha design choice. CLI has no credentials to manage.
9. **Event subscription is unicast UDP from the same source IP** — breaks across NAT. Renew at ~8 min, not 10.
10. **Per-device feature cache is keyed by `device_id` (MAC),** not IP. Survives DHCP renewals; safe across multi-device setups. **TTL 7 days; no per-invocation version check.**
11. **YXC is GET-only.** Verified via APK source. `pkg/yxc.Client.Do` does not need a method parameter.
12. **HTTP retries are silent and capped at one.** Visible in `--debug`. Anything more masks real failures.
13. **`watch` reconnects forever, but logs every attempt.** Attempt count in the dozens means the device is genuinely down — kill with SIGINT.
14. **DHCP resilience requires a saved UDN.** Pre-v5 configs without UDN fall back to plain exit 69 with a hint. The first-run wizard and `yamaha discover --add` always save UDN.
15. **SIGINT does not roll back receiver-side actions.** `yamaha power on` followed by Ctrl-C still leaves the receiver powering on; only our wait loop cancels.

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
- **NO_COLOR convention:** https://no-color.org

## Captured live artifacts

- `/tmp/yamaha-fw/R0424-0287.bin` — encrypted firmware (no further use; bootloader-decrypted).
- `/tmp/yxc_features.json` — full `getFeatures` response from the live device. **Copy into `testdata/getFeatures.json` when scaffolding the repo.**
- `/tmp/yxc_desc_49154.xml` — UPnP MediaRenderer description XML (contains UDN `uuid:9ab0c000-f668-11de-9976-00a0defbe863`).

## Decompiled APK artifacts (authoritative protocol references)

- `/tmp/yamaha-apk/avcontroller-jadx/sources/y1/b.java` — full YXC command switch (every endpoint with case-id, params, headers).
- `/tmp/yamaha-apk/musiccast-jadx/sources/a0/b3.java` — YXC client + `X-AppName`/`X-AppPort` event subscription. Confirms YXC is GET-only (line 1162: `setRequestMethod("GET")`); the POST at line 1293 is for non-YXC `/yamahapim/...` account endpoints.
- `/tmp/yamaha-apk/musiccast-jadx/sources/fe/d.java` — YXC response JSON parsing for ~50 commands.
- `/tmp/yxc-mc.txt` — sorted list of all 184 YXC endpoints. **The `raw` subcommand's de-facto reference.**
- `/tmp/yamaha-apk/avc-apktool/assets/local_yud3ga.xml` + `local_yud3gb.xml` — **YNCA Cmd_List spec for RX-V583 class** (yud3g, netmodule_generation 1).
- `/tmp/yamaha-apk/avcontroller-jadx/sources/v1/q.java`, `e1/b.java` — legacy YNCA/`/YamahaRemoteControl/ctrl` XML envelope construction.
- `/tmp/yamaha-apk/musiccast-jadx/sources/r2/b0.java`, `zc/d.java`, `nc/n.java` — SSDP discovery + UDP event listener implementation.
- `/tmp/yamaha-apk/mc-apktool/assets/mc_devices.json` — model → image/feature mapping.

**Header conventions:**
- `User-Agent: yamaha-cli/<ver>` (self-identification — receiver doesn't enforce UA).
- `X-AppName: MusicCast` only on event-subscribing GETs.

## Definition of done (Phase 1)

- `go install` produces a single `yamaha` binary.
- **First-run flow:** TTY runs the wizard, saves config (with UDN), prints status. Non-TTY exits 64. Zero-found exits 69. Alias collision suggests a suffix.
- **Live RX-V583 against `192.168.1.116`:**
  - `yamaha status` (TTY) prints a 4-line table.
  - `yamaha status | jq -r .power` returns `on`.
  - `yamaha volume +5` is exactly one HTTP request in `--debug`.
  - `yamaha volume +5 --db` exits 2.
  - `yamaha power on && yamaha volume 60 && yamaha input hdmi2` works without manual sleeps.
  - `yamaha mute on` mutes; `yamaha mute off` unmutes.
  - `yamaha input typo` exits 1 with "did you mean" and zero `setInput` HTTP calls.
  - `yamaha --device bedroom status` works after `yamaha discover --add`.
  - `YAMAHA_HOST=192.168.1.116 yamaha status` works with no config file (anonymous mode).
  - **Ctrl-C during `yamaha power on`** exits 130 within 200 ms; receiver still powers on.
  - **DHCP shuffle:** edit config IP to wrong-but-routable address; `yamaha status` succeeds via transparent rediscover; config updated.
- **Failure modes:**
  - Network unreachable + UDN saved → silent rediscover; if rediscover also fails → exit 69.
  - Network unreachable + no UDN (pre-v5 config) → exit 69 with refresh hint.
  - YXC `response_code != 0` → exit 70.
  - Power-on timeout → exit 1.
- **Cache behavior:**
  - Stale-cache scenario succeeds with one auto-refresh + one `setInput`.
  - 7-day TTL: bumping mtime to >7 days ago triggers refresh.
- **Tests:**
  - `go test ./...` passes against canned `httptest.Server` (response_code matrix + transient-retry matrix + DHCP-shuffle simulation).
  - `go test -tags=integration -yamaha-host=192.168.1.116 ./...` passes read-only structural smoke tests.
- **Docs:** README covers config schema (multi-device example with UDN), env vars (`YAMAHA_HOST`, `YAMAHA_DEVICE`, `YAMAHA_DEBUG`, `NO_COLOR`), `--output` modes, exit-code table, zone-scope table, DHCP-resilience behavior, no-auth-on-LAN security note, and at least 5 idiomatic command examples.

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
