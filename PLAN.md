# Yamaha RX-V583 Go CLI — Build Plan

A command-line tool in Go to control a Yamaha RX-V583 AV receiver over the local network without using the physical remote.

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
- **Push events:** add headers `X-AppName: MusicCast/1` + `X-AppPort: <udp>` to any GET → receiver UDPs JSON deltas to your IP. Re-subscribe every <10 minutes.

## Architecture

```
yamaha-cli/
├── cmd/yamaha/main.go            # cobra entrypoint
├── internal/cli/                 # cobra commands (power, volume, input, zone, tuner, netusb, scene, watch)
├── pkg/yxc/                      # YXC HTTP client — public, importable as a library
│   ├── client.go                 # http.Client + base URL + response_code unwrap
│   ├── system.go                 # getDeviceInfo, getFeatures, getNetworkStatus, network_reboot
│   ├── zone.go                   # main + zone2: setPower, setVolume, setMute, setInput, setSoundProgram, setSleep, getStatus
│   ├── netusb.go                 # setPlayback, getPlayInfo, recallPreset, getListInfo
│   ├── tuner.go                  # setFreq, recallPreset, getPresetInfo
│   ├── dist.go                   # MusicCast Link (Phase 3)
│   ├── events.go                 # UDP listener + header subscription + 10-min renewer (Phase 3)
│   └── types.go                  # enums + structs mirroring getFeatures
├── pkg/discover/                 # SSDP MediaRenderer search → filter Yamaha → return YXC URLs
├── pkg/ynca/                     # (Phase 3) thin TCP/50000 fallback
└── testdata/
    └── getFeatures.json          # captured from real device for offline tests
```

**Why split `pkg/yxc` from `cmd`:** the package is also useful as a standalone Go library — there is no other maintained one. Keep dependency surface tiny: stdlib + cobra (and `koron/go-ssdp` for discovery).

**CLI framework:** [`spf13/cobra`](https://github.com/spf13/cobra). Nested subcommands (`yamaha zone main mute`) are exactly its sweet spot. Integrates with Viper for `~/.config/yamaha-cli/config.yaml` defaults.

## Phase 1 — MVP

Goal: replace the most-used remote buttons.

```
yamaha power on|off|toggle
yamaha volume <int|±N|up|down>     # accepts 60, +5, -5
yamaha mute on|off|toggle
yamaha input <name>                 # tab-completion from getFeatures cache
yamaha status                       # pretty-print getStatus
```

**Tasks:**
1. `go mod init github.com/ljagiello/yamaha-cli`; add cobra.
2. Config loader: `~/.config/yamaha-cli/config.yaml` with `host`, `default_zone`. Overridable via `--host` and `YAMAHA_HOST` env.
3. `pkg/yxc.Client`:
   - Construct from base URL, configurable `http.Client` timeout (default 5 s).
   - `Do(ctx, method string, params url.Values) (json.RawMessage, error)` — GET `/v1/<method>?<params>`, parse JSON, return error if `response_code != 0` (with code-to-string mapping from spec).
4. Implement `getDeviceInfo`, `getFeatures`, `getStatus`, `setPower`, `setVolume`, `setMute`, `setInput`.
5. `getFeatures` cached to `~/.cache/yamaha-cli/features.json`. Refresh on `--refresh-features` or when `system_version` differs from cache.
6. Volume command: support `60` (raw int), `+5`/`-5` (delta — convert to absolute against current `getStatus` volume), `up`/`down` (server-side step), `--db` (convert -22.5 → int via getFeatures range), `--percent` (0..100 → scale).
7. `status` command: pretty-print `power`, `input`, `volume` (int + dB), `mute`, `sound_program`.
8. Capture `getFeatures.json` from `192.168.1.116` into `testdata/`. Unit-test client against a `httptest.Server` replaying canned responses.

**Acceptance:** running `yamaha status` against the live RX-V583 prints power/input/volume/mute. `yamaha volume +5` raises by 5 integer steps. `yamaha input hdmi2` switches to HDMI 2.

## Phase 2 — Full surface

```
yamaha --zone zone2 ...             # global flag, default = main
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
yamaha discover                     # SSDP scan, list MusicCast devices on LAN
```

**Tasks:**
1. `pkg/discover` using `koron/go-ssdp`. Filter for `manufacturer == "Yamaha Corporation"`. Parse description XML → extract YXC base URL from `urn:schemas-yamaha-com:service:X_YamahaExtendedControl:1`.
2. Tuner: handle FM Hz unit gotcha (87500 = 87.5 MHz) — accept user-friendly `102.5` and convert.
3. NetUSB browse + queue (Phase 2 stretch — `getListInfo` paged 8 items at a time).
4. Tab completion: cobra's built-in completion + dynamic completion for input names (read from `getFeatures` cache).

## Phase 3 — Watch, multi-room, YNCA fallback

```
yamaha watch                        # subscribe to UDP events, stream JSON deltas to stdout
yamaha link create <leader> <follower>...
yamaha link dissolve
yamaha reboot                       # system/requestSystemReboot (or requestNetworkReboot)
yamaha bluetooth list|connect|disconnect
yamaha ypao status                  # read YPAO calibration state
yamaha raw <yxc-method> [params...]    # escape hatch for any uncovered endpoint
yamaha ynca <command>                  # YNCA passthrough (e.g., @MAIN:VOL=?)
```

**Tasks:**
1. `pkg/yxc/events.go`: bind UDP socket, attach `X-AppName: MusicCast` + `X-AppPort: <chosen>` to a `getStatus` call, parse incoming JSON deltas, renew every ~8 minutes. **Note exact header value is `MusicCast` (no `/1` suffix) per the official app's source — the public PDF says `MusicCast/<ver>` but the app uses bare `MusicCast`.**
2. `pkg/ynca`: TCP/50000 client. Connect, send line, read line, close. Use `assets/local_yud3ga.xml` from the AV Controller APK as the authoritative command list for RX-V583 — it contains the exact Cmd_List tree (every YNCA function, every parameter range) for "yud3g"-class receivers (RX-V583 is netmodule_generation 1 → yud3g).
3. MusicCast Link distribution endpoints (`/dist/setServerInfo`, `/dist/setClientInfo`, `/dist/startDistribution`).
4. **Bonus undocumented endpoints worth exposing** (extracted from MusicCast app, not in public spec PDF):
   - `system/requestNetworkReboot`, `requestSystemReboot` — clean reboots
   - `system/setPartyMode`, `setPartyVolume` — party mode broadcast
   - `system/setSleep` (already in Phase 2) + `clock/setMultiAlarm`, `clock/stopAlarm` — alarm/sleep timer
   - `system/getYpaoConfig`, `setYpaoVolume`, `setSpeakerEqualizer` — YPAO room calibration tweaks
   - `system/connectBluetoothDevice`, `disconnectBluetoothDevice`, `getBluetoothDeviceList`, `deleteBluetoothDeviceHistory`
   - `netusb/getMcPlaylist`, `manageMcPlaylist`, `clearMcPlaylist`, `moveMcPlaylistItem` — MusicCast playlist editing
   - `dist/setMcSurround`, `confirmMcSurround`, `resetMcSurround`, `setMcSpeakerTestTone` — MusicCast Surround pairing
   - `ccs/startCcs`, `stopCcs`, `executeCommand`, `devices`, `presets` — Cinema-Caster service

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
- `/tmp/yxc-mc.txt` — sorted list of all 184 YXC endpoints discovered in the MusicCast APK.
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
- Running against `192.168.1.116`:
  - `yamaha status` prints current power/input/volume.
  - `yamaha power on` then `yamaha volume 60` then `yamaha input hdmi2` performs all three actions in under 1 second total (excluding the ~2 s power-on settle).
  - `yamaha mute on` mutes; `yamaha mute off` unmutes.
- Unit tests pass against canned `httptest.Server` replaying `testdata/getFeatures.json` and a few `getStatus`/`setVolume` exchanges.
- README documents config file, env vars, and the no-auth-on-LAN security note.
