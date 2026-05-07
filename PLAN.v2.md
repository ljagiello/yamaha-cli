<!-- v2 - 2026-05-07 - Generated via /improving-plans from PLAN.md -->

# Yamaha RX-V583 Go CLI — Build Plan v2

A command-line tool in Go to control a Yamaha RX-V583 AV receiver over the local network without using the physical remote.

## What changed in v2

Four design decisions resolved during the v1 design review:

1. **Output formatting** — auto-detect TTY (human-readable in terminal, JSON when piped). Global `--output json|yaml|table|auto` flag for explicit control.
2. **First-run UX** — auto-discover via SSDP and prompt the user to save. No manual config-file editing required to get started.
3. **Multi-device addressing** — config models a map of named devices; `--device <name>` (or `YAMAHA_DEVICE` env) selects which one. Single-device setups are the degenerate case.
4. **Phase 3 scope** — trimmed hard to `watch`, `link`, `reboot`, `ynca`, plus a generic `raw <method> [params...]` escape hatch. ~25 bonus endpoints (YPAO, CCS, Sonos, MusicCast playlists, surround pairing, etc.) move to "Future expansion" — accessible day-one through `raw`, but not first-class commands.

Plus two smaller additions surfaced in review:
- **Exit-code scheme** documented (sysexits-lite).
- **Live-device integration test build tag** added to Phase 1.

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
| **Protocol research** | RX-V583 speaks **YamahaExtendedControl (YXC)** — HTTP/JSON on port 80. Public PDF spec mirrors exist. YNCA also documented but officially unconfirmed for V583. |
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
├── pkg/yxc/                      # YXC HTTP client — public, importable as a library
│   ├── client.go                 # http.Client + base URL + response_code unwrap + typed errors
│   ├── system.go                 # getDeviceInfo, getFeatures, getNetworkStatus, requestSystemReboot
│   ├── zone.go                   # main + zone2: setPower, setVolume, setMute, setInput, setSoundProgram, setSleep, getStatus
│   ├── netusb.go                 # setPlayback, getPlayInfo, recallPreset, getListInfo
│   ├── tuner.go                  # setFreq, recallPreset, getPresetInfo
│   ├── dist.go                   # MusicCast Link (Phase 3)
│   ├── events.go                 # UDP listener + header subscription + 8-min renewer (Phase 3)
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
# Selected when --device is not passed and YAMAHA_DEVICE is unset.
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

**Adding more devices:** running `yamaha discover` (Phase 2) prints found devices and offers to append them to the config. Manual editing is also supported.

## First-run flow

When no host can be resolved by the rules above:

- **Interactive (stdout is a TTY):** print "No device configured. Searching the LAN…", run a 3 s SSDP scan, list found Yamaha devices, prompt the user to pick one, prompt for an alias (default: slugified network_name), and save the config. Re-run the original command transparently.
- **Non-interactive (piped, scripted, CI):** exit with code `64` (EX_USAGE) and a stderr hint: `no device configured; run 'yamaha discover' or pass --host`.

The wizard never auto-runs in non-interactive contexts — pipelines should fail fast and loud, not hang waiting for input.

## Output formatting

Global flag: `--output {json|yaml|table|auto}` (alias `-o`). Default: `auto`.

`auto` resolves to:
- `table` (human-readable) when `stdout` is a TTY.
- `json` when stdout is piped or redirected.

This is the gh / kubectl / docker idiom. It means `yamaha status` looks pretty in a terminal but `yamaha status | jq .power` Just Works.

**Successful mutating commands** (`power on`, `volume 60`, etc.) emit `{}` in JSON modes and a single confirmation line in table mode. They never print the entire device state — that's `yamaha status`'s job.

**Implementation:** `internal/output.Render(v any, format string)` uses `mattn/go-isatty` to resolve `auto`. Table rendering is hand-rolled (no `tablewriter` dependency for ~5 fields).

## Exit codes

Sysexits-lite — small, predictable, plays well with shell idioms:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic error (most failures fall here) |
| 2 | Misuse / invalid CLI argument (cobra default) |
| 64 | `EX_USAGE` — no device configured & non-interactive |
| 69 | `EX_UNAVAILABLE` — device unreachable (network error, timeout) |
| 70 | `EX_SOFTWARE` — device returned non-zero `response_code` (e.g., feature unsupported, device not ready) |

Error message goes to stderr in human form regardless of `--output`. With `--output json`, also emit `{"error": "...", "code": <int>, "yxc_response_code": <int|null>}` to stdout.

## Phase 1 — MVP

Goal: replace the most-used remote buttons + scriptable output + zero-config first run.

```
yamaha power on|off|toggle
yamaha volume <int|±N|up|down>     # 60, +5, -5, up, down
yamaha mute on|off|toggle
yamaha input <name>                 # tab-completion from getFeatures cache
yamaha status                       # pretty (TTY) or JSON (piped)
yamaha discover                     # SSDP scan, list/save devices  (promoted from Phase 2)
yamaha config show                  # print resolved config
```

**Tasks:**
1. `go mod init github.com/ljagiello/yamaha-cli`; pull cobra + viper + koron/go-ssdp + mattn/go-isatty.
2. `internal/config`: load multi-device YAML with the resolution order above. Bind to viper. `--host`, `--device`, `--zone`, `--output` global flags.
3. `internal/output.Render(v any, format string)`: auto-detect TTY, emit JSON/YAML/table. Format `error` payloads consistently.
4. `pkg/discover` using `koron/go-ssdp`: search `urn:schemas-upnp-org:device:MediaRenderer:1` (3 s timeout), fetch description XML, filter for `manufacturer == "Yamaha Corporation"`. Return `[]Device{Name, Host, Model, BaseURL}`.
5. First-run wizard in `internal/cli`: triggered when device resolution fails AND stdout is a TTY. Prompts and writes to `~/.config/yamaha-cli/config.yaml`.
6. `pkg/yxc.Client`:
   - Construct from base URL + `*http.Client` (default 5 s timeout).
   - `Do(ctx, method string, params url.Values) (json.RawMessage, error)` — GET `/v1/<method>?<params>` with `User-Agent: yamaha-cli/<ver>`. Parse JSON, return typed error if `response_code != 0`. Map known codes (5 → `ErrDeviceNotReady`, 6 → `ErrNotFound`, etc.).
   - Higher-level methods: `GetDeviceInfo`, `GetFeatures`, `GetStatus`, `SetPower`, `SetVolume`, `SetMute`, `SetInput`.
7. `getFeatures` caching: `~/.cache/yamaha-cli/<device-id>-features.json`. Refresh on `--refresh-features` flag, when the file is missing, or when cached `system_version` ≠ live `getDeviceInfo.system_version`. Use device_id (MAC) in the filename so multi-device caches don't collide.
8. Volume command: support `60` (raw int), `+5`/`-5` (delta — read current via `getStatus`, clamp), `up`/`down` (server-side step), `--db -22.5` (convert via getFeatures range), `--percent 50` (0..100 scaled).
9. Status command: render power/input/volume(int+dB+pct)/mute/sound_program. Both table and JSON shapes have stable field names.
10. Capture `getFeatures.json` from `192.168.1.116` into `testdata/`. Unit tests against `httptest.Server` replaying canned responses.
11. **Live integration tests** behind `//go:build integration`: `go test -tags=integration -yamaha-host=192.168.1.116 ./...` exercises read-only paths against a real device. Skipped in normal `go test`. CI never runs them.

**Acceptance:**
- Fresh install with no config: `yamaha status` triggers the discovery wizard, saves `living-room`, then prints status.
- `yamaha status` (TTY) prints a 4-line table; `yamaha status | jq .power` returns `"on"`.
- `yamaha volume +5` increments by 5 against current.
- `yamaha input hdmi2` switches.
- `yamaha --device bedroom volume 60` works once a second device is added.
- A device-unreachable error exits with code 69; a YXC-error exits with code 70.

## Phase 2 — Full surface

```
yamaha --zone zone2 ...             # global flag, default = main (or device's default_zone)
yamaha sound <program>              # 2ch_stereo, straight, surr_decoder, ...
yamaha decoder <type>               # auto, dolby_surround, dts_neural_x, ...
yamaha scene <1-4>
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
4. `yamaha discover` (already in Phase 1) gains an `--add` flag to append a found device to the config without prompting.

## Phase 3 — Watch, multi-room, raw escape hatch

Trimmed hard. Five commands cover everything the v1 plan listed under Phase 3 and "bonus endpoints":

```
yamaha watch                               # subscribe to UDP events, stream JSON deltas
yamaha link create <leader> <follower>...  # MusicCast Link
yamaha link dissolve
yamaha reboot                              # system/requestSystemReboot
yamaha raw <method> [key=value ...]        # generic YXC GET passthrough
yamaha ynca <command>                      # YNCA passthrough (e.g., @MAIN:VOL=?)
```

`yamaha raw` is the keystone. It accepts any of the 184 endpoints from `/tmp/yxc-mc.txt`, e.g.:

```bash
yamaha raw netusb/getMcPlaylist bank=1
yamaha raw system/setPartyMode enable=true
yamaha raw clock/setMultiAlarm '{"alarm_on":true,...}'   # JSON body for endpoints that need POST
yamaha raw system/getYpaoConfig
```

Output is the raw `response_code`-validated JSON. Anything in the bonus catalog (YPAO, CCS, Sonos, MusicCast playlists, surround pairing, Bluetooth device list, Disklavier, alarms) is reachable from day one through `raw` — promoting any one of them to a typed command later is purely additive.

**Tasks:**
1. `pkg/yxc/events.go`: bind UDP socket, attach `X-AppName: MusicCast` + `X-AppPort: <chosen>` to a `getStatus` call, parse incoming JSON deltas, renew every ~8 minutes. (Note exact header value is `MusicCast` per the official app's source — public PDF says `MusicCast/<ver>` but the app uses bare `MusicCast`.)
2. `pkg/yxc.Raw(ctx, method, params)`: thin wrapper around `Do` with no schema validation — returns `json.RawMessage`. The `raw` subcommand wires this to the output renderer.
3. `pkg/ynca`: TCP/50000 client. Connect, send line, read line, close. Use `assets/local_yud3ga.xml` from the AV Controller APK as the authoritative command list for RX-V583 — it contains the exact Cmd_List tree (every YNCA function, every parameter range) for "yud3g"-class receivers (RX-V583 is netmodule_generation 1 → yud3g).
4. MusicCast Link distribution endpoints (`/dist/setServerInfo`, `/dist/setClientInfo`, `/dist/startDistribution`).

## Critical gotchas to bake in

1. **Always check `response_code`.** HTTP 200 + `response_code != 0` is the failure mode. Wrap every call.
2. **Don't hardcode enums.** Inputs, sound programs, max volume — all come from `getFeatures`. CLI tab-completion + validation must read the cached file.
3. **`prepareInputChange` before `setInput`** for sources whose `func_list` requires it (server, net_radio, etc.). Skip and the receiver hangs in an awkward state.
4. **Rate-limit ~100 ms** between commands. Embedded box, easy to overrun.
5. **Power-on returns immediately but state takes 2–5 s.** When chaining `power on && volume 60`, poll `getStatus` until `power=on` (with a max-wait) instead of blind sleep.
6. **Tuner FM is in Hz, not kHz:** `87500` means 87.5 MHz.
7. **Volume is an integer 0..161, not 0..100, not dB.** Surface `--db` and `--percent` flags that convert against the device's real range from `getFeatures`.
8. **No HTTPS, no auth.** Document in README — Yamaha design choice. CLI has no credentials to manage.
9. **Event subscription is unicast UDP from the same source IP** — breaks across NAT. Renew at ~8 min, not 10.
10. **Per-device feature cache.** Keying by IP is wrong (DHCP changes); key by `device_id` (MAC) returned in `getDeviceInfo`.

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
- `/tmp/yamaha-apk/musiccast-jadx/sources/a0/b3.java` — YXC client + `X-AppName`/`X-AppPort` event subscription for the MusicCast app.
- `/tmp/yamaha-apk/musiccast-jadx/sources/fe/d.java` — YXC response JSON parsing for ~50 commands. Excellent for deriving Go struct shapes.
- `/tmp/yxc-mc.txt` — sorted list of all 184 YXC endpoints discovered in the MusicCast APK. **The `raw` subcommand's de-facto reference.**
- `/tmp/yamaha-apk/avc-apktool/assets/local_yud3ga.xml` + `local_yud3gb.xml` — **YNCA Cmd_List spec for RX-V583 class** (yud3g, netmodule_generation 1). Lift verbatim into a Go YNCA driver.
- `/tmp/yamaha-apk/avcontroller-jadx/sources/v1/q.java`, `e1/b.java` — legacy YNCA/`/YamahaRemoteControl/ctrl` XML envelope construction.
- `/tmp/yamaha-apk/musiccast-jadx/sources/r2/b0.java`, `zc/d.java`, `nc/n.java` — SSDP discovery + UDP event listener implementation (use as reference for `pkg/discover` and `pkg/yxc/events`).
- `/tmp/yamaha-apk/mc-apktool/assets/mc_devices.json` — model → image/feature mapping (RX-V583 listed: `netmodule_generation: 1, image_power: img_model_power_avr2a`).

**Exact User-Agent / header strings observed in the apps (use these for max compatibility):**
- AV Controller: `User-Agent: AV_CONTROLLER/4.00 (Android)`
- MusicCast Controller: `User-Agent: MusicCast`
- Event subscription: `X-AppName: MusicCast` + `X-AppPort: <local-udp-port>` (no `/<ver>` suffix despite what the PDF says).

## Definition of done (Phase 1)

- `go install` produces a single `yamaha` binary.
- **First-run flow:** with no config and `yamaha status` invoked from a TTY, the wizard discovers `192.168.1.116`, prompts for an alias, saves config, and prints status. From a non-TTY, the same call exits 64 with a hint.
- **Live RX-V583 against `192.168.1.116`:**
  - `yamaha status` (TTY) prints power/input/volume/mute as a table.
  - `yamaha status | jq -r .power` returns `on`.
  - `yamaha power on && yamaha volume 60 && yamaha input hdmi2` performs all three actions in under 1 second total (excluding the ~2 s power-on settle).
  - `yamaha mute on` mutes; `yamaha mute off` unmutes.
  - `yamaha --device bedroom status` works after `yamaha discover --add` saves a second device (or after manual config edit).
- **Failure modes:**
  - Network unreachable → exit 69, JSON-or-text error per `--output`.
  - YXC `response_code != 0` → exit 70, error includes the code.
- **Tests:**
  - `go test ./...` passes against canned `httptest.Server` replaying `testdata/getFeatures.json` and `testdata/getStatus.json`.
  - `go test -tags=integration -yamaha-host=192.168.1.116 ./...` passes read-only against the live device. Skipped without the tag.
- **Docs:** README covers config schema (with multi-device example), env vars, `--output` modes, exit-code table, and the no-auth-on-LAN security note.

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
