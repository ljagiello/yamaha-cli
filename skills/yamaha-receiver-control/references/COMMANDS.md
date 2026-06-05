# Command reference

All commands accept the global flags below. Run `yamaha <cmd> --help` for cobra's auto-generated per-command help.

## Global flags

| Flag | Env | Effect |
|---|---|---|
| `--host <ip>` | `YAMAHA_HOST` | Bypass config; talk to this IP directly. Anonymous (no DHCP-resilience). |
| `--device <alias>` | `YAMAHA_DEVICE` | Use the named device from config. |
| `--zone <main\|zone2\|zone3\|zone4>` | `YAMAHA_ZONE` | Override the zone for zone-scoped commands. Default: device's `default_zone`, else `main`. Any of the four canonical zones is accepted; the receiver rejects a zone it lacks (exit 70). |
| `-o, --output <fmt>` | — | `auto` (default), `json`, `yaml`, `table`. `auto` = table on TTY, JSON when piped. |
| `--no-color` | `NO_COLOR` (any non-empty value) | Disable ANSI styling in table mode. |
| `--debug` | `YAMAHA_DEBUG` (truthy: `1`, `true`, `yes`, `on`) | Log every YXC request/response to stderr. |
| `--no-wait` | — | For `power on`/`toggle`: skip the post-action getStatus poll (default 200 ms tick, 10 s timeout). |
| `--refresh-features` | — | Force-refresh the cached `getFeatures` (per-device, MAC-keyed, 7 d TTL). |

## Subcommands

| Command | Zone-scoped | Effect |
|---|---|---|
| `status` | yes | Print power/input/volume/volume_db/volume_percent/mute/sound_program/zone for the active zone. |
| `power on\|off\|toggle [--no-wait]` | yes | Set power. `off` is fire-and-forget; `on` (and `toggle` off→on) polls getStatus until `power=on` or 10 s elapse. |
| `volume <int\|±N\|up\|down> [--db\|--percent\|--step N]` | yes | Set volume. See "Volume modes" below. |
| `mute on\|off\|toggle` | yes | Set/toggle mute. |
| `input <name>` | yes | Switch input. Validated client-side against cached features; auto-refresh on miss. Auto-fires `prepareInputChange` when `func_list` requires it. |
| `sound <program>` | yes | Set DSP sound program. Validated against `zone.sound_program_list`; "did you mean" on miss. |
| `decoder <type>` | yes | Set surround decoder type. Validated against `zone.surr_decoder_type_list`. |
| `scene <1..N>` | yes | Recall scene N. Bounds-checked against `zone.scene_num`. |
| `tone bass <-12..+12>` / `tone treble <-12..+12>` / `tone reset` | yes | Adjust bass/treble (range from `zone.range_step.tone_control`). `reset` switches to mode=auto with bass=0, treble=0. |
| `pure-direct on\|off` / `enhancer on\|off` / `extra-bass on\|off` / `adaptive-drc on\|off` | yes | Boolean DSP switches. Each is feature-gated on the zone's `func_list` (`direct` / `enhancer` / `extra_bass` / `adaptive_drc`); an unsupported control exits 2 with a clear message instead of hitting the device. |
| `sleep 0\|30\|60\|90\|120\|off` | yes | Set zone sleep timer (minutes). `off` ≡ `0`. |
| `tuner status` | no | Print band/freq/preset for the active band; the inactive band is included as a nested map. |
| `tuner fm <MHz>` | no | Tune FM (e.g. `102.5`). Validated against `tuner.range_step.fm_freq` when known. |
| `tuner am <kHz>` | no | Tune AM (e.g. `1530`). Validated against `tuner.range_step.am_freq`. |
| `tuner preset <num> [--band fm\|am]` | no | Recall a tuner preset. Band defaults to receiver's current band, else `fm`. Capped against `tuner.preset.num`. |
| `tuner presets [--band fm\|am]` | no | List saved tuner presets for the band. |
| `netusb info` | no | Print now-playing payload (input, playback, repeat, shuffle, play_time, total_time, plus optional artist/album/track/albumart_url). |
| `netusb play\|pause\|stop\|toggle` | no | One `setPlayback` call (`toggle` ≡ `play_pause`). |
| `netusb next\|prev` | no | Skip / previous track. |
| `netusb ff\|rew` | no | Fast-seek. Issues start, holds ~200 ms, then issues end (auto-end even on SIGINT). |
| `netusb shuffle` / `netusb repeat` | no | Toggle shuffle / repeat mode. |
| `preset list` | no | List NetUSB MusicCast presets (slot/input/text). |
| `preset recall <num>` | no | Recall a NetUSB preset by 1-indexed slot. Capped against `netusb.preset.num`. |
| `link create <leader> <follower> [<follower>...]` | no | Build a MusicCast Link group. Aliases only (config required). Existing-membership check refuses any follower already in a group (server or client); on partial failure rolls back leader + already-set followers. |
| `link dissolve [<leader>]` | no | Tear down a MusicCast Link group. Defaults to the active device. |
| `link info [<leader>]` | no | Print `dist/getDistributionInfo` for the device. |
| `reboot --yes` | no | Request a system reboot. `--yes` is mandatory. Post-ack transport errors are treated as success (the receiver drops TCP mid-reboot). |
| `watch [--device a,b,c]` | no | Subscribe to UDP push events; emit NDJSON (one event per line). Auto-reconnect with exponential backoff (1 s → 60 s) on a 30 s silent window. SIGINT exits cleanly. |
| `raw <method> [k=v ...]` | no | Send a raw YXC request. Method is the YXC path (`system/setPartyMode`, `netusb/setPlaybackMode`, …). Repeated keys append (multi-value). |
| `ynca <line>` | no | Raw YNCA passthrough on TCP/50000. Leading `@` optional; reply printed verbatim. Probes once per invocation; non-YNCA devices exit 70. `@UNDEFINED`→exit 70, `@RESTRICTED`→exit 75. Sends a `@SYS:MODELNAME=?` wake ping on connect. |
| `ynca status\|power\|volume\|mute\|input\|sound` | yes (zone→subunit) | Typed YNCA control for YNCA-only receivers, acting on the `--zone`-mapped subunit (main→MAIN, …). `status` decodes one `@MAIN:BASIC=?` GET; `volume` takes absolute dB (pass negatives after `--`) or `up`/`down`. |
| `ynca repl` | no | Interactive YNCA prompt over one persistent connection (one line per command; `exit`/`quit`/Ctrl-D to leave). |
| `discover [--add]` | no | SSDP scan. Without `--add`: print found Yamaha devices. With `--add`: interactive prompt to save one to config (wizards through pick + alias). |
| `config show` | no | Print resolved config (JSON/YAML/table per `--output`). |
| `config path` | no | Print absolute config file path. |
| `completion {bash\|zsh\|fish\|powershell}` | no | Emit shell completion script to stdout. |
| `version` (or `--version`) | no | Print `yamaha-cli <version>`. |

## Volume modes

The `volume` command takes one positional argument. Modifiers are mutually exclusive with relative forms.

| Form | Wire format | Example |
|---|---|---|
| Absolute integer | `setVolume?volume=N` (clamped 0..max) | `volume 60` |
| Signed delta | `setVolume?volume=up\|down&step=N` (single roundtrip, no GET-then-SET) | `volume +5`, `volume -- -3` |
| Token | `setVolume?volume=up\|down` (default step) | `volume up`, `volume down --step 2` |
| Decibels (absolute) | converts to integer via `getFeatures` range, sends absolute | `volume -22.5 --db` |
| Percent (absolute) | scales 0..100 to 0..max, sends absolute | `volume 50 --percent` |

Errors:
- `+5 --db` or `+5 --percent` → exit 2 with `--db/--percent only apply to absolute values`.
- Unknown input / sound program / scene → exit 1 with `did you mean: <suggestions>` (Levenshtein top-3 against the cached features list).

## Output payload shapes

### `status`

```json
{
  "input": "hdmi2",
  "mute": false,
  "power": "on",
  "sound_program": "standard",
  "volume": 60,
  "volume_db": -50.5,
  "volume_percent": 37,
  "zone": "main"
}
```

`volume_db` and `volume_percent` are computed client-side from the device's volume range. If the device returns `actual_volume.value`, `volume_db` matches it; otherwise it's `-80.5 + 0.5 * volume` (RX-V/A integer step convention).

### `tuner status`

```json
{
  "band": "fm",
  "freq": 102500,
  "freq_human": "102.50 MHz",
  "preset": 7,
  "audio_mode": "stereo"
}
```

`freq` is the wire integer (FM in kHz, AM in kHz); `freq_human` is the formatted display string. `preset` is 0 when no preset matches the current tuning. `audio_mode` (FM only) is omitted when empty.

### `netusb info`

```json
{
  "input": "server",
  "playback": "play",
  "repeat": "off",
  "shuffle": "off",
  "play_time": 73,
  "total_time": 245,
  "play_time_human": "1:13",
  "total_time_human": "4:05",
  "artist": "Brian Eno",
  "album": "Ambient 1: Music for Airports",
  "track": "1/1",
  "albumart_url": "/YamahaExtendedControl/v1/netusb/albumart?id=12&w=300"
}
```

`play_time_human`/`total_time_human` are the raw seconds rendered as `m:ss` (or `h:mm:ss`); a zero/absent length renders as `""`. Empty metadata fields (artist/album/track/albumart_url) are dropped from the payload.

### `tuner presets` / `preset list`

A JSON array of per-row objects. `tuner presets`:

```json
[
  {"band": "FM", "num": 1, "freq": 89300,  "freq_human": "89.30 MHz"},
  {"band": "FM", "num": 2, "freq": 102100, "freq_human": "102.10 MHz"}
]
```

`preset list` (NetUSB):

```json
[
  {"num": 1, "input": "server",     "text": "Living Room NAS"},
  {"num": 2, "input": "net_radio",  "text": "BBC Radio 6 Music"}
]
```

### `link info`

A JSON object mirroring the device's `dist/getDistributionInfo` response — `group_id`, `role` (`server`/`client`/`none`), `server_zone`, `client_list`, etc. Use it to confirm a `link create` took before issuing further commands.

### `watch` event line

Each NDJSON line is one of two shapes. Data event:

```json
{"ts":"2026-05-07T18:42:01.123Z","device":"living-room","delta":{"main":{"volume":62}}}
```

Control event (subscribe / renew / reconnect / shutdown):

```json
{"ts":"2026-05-07T18:42:01.456Z","device":"living-room","event":"reconnect","reason":"udp: silent for 30s"}
```

`ts` is UTC ISO-8601 with millisecond precision; `device` is the alias (or `--host` value when anonymous); `delta` is the verbatim push payload from the receiver. Lines are emitted in arrival order; with `--device a,b,c` events from different devices may interleave but each individual line is atomic.

### Mutating commands

On success: `{}` (JSON/YAML modes) or a single `ok` line (table mode). The full state goes through `status`, not the mutating command. `link create` / `link dissolve` are exceptions — they emit `{group_id, leader, followers?}` so callers can correlate the resulting group.

### Error payload

JSON/YAML modes also emit:

```json
{"error": "yxc: transport: dial tcp 192.168.1.250:80: connect: host is down", "code": 69, "yxc_response_code": null}
```

Table mode prints `error: <message>` to stderr.
