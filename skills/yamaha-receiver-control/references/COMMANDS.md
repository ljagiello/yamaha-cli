# Command reference

All commands accept the global flags below. Run `yamaha <cmd> --help` for cobra's auto-generated per-command help.

## Global flags

| Flag | Env | Effect |
|---|---|---|
| `--host <ip>` | `YAMAHA_HOST` | Bypass config; talk to this IP directly. Anonymous (no DHCP-resilience). |
| `--device <alias>` | `YAMAHA_DEVICE` | Use the named device from config. |
| `--zone <main\|zone2>` | `YAMAHA_ZONE` | Override the zone for zone-scoped commands. Default: device's `default_zone`, else `main`. |
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
| `discover [--add]` | no | SSDP scan. Without `--add`: print found Yamaha devices. With `--add`: interactive prompt to save one to config (wizards through pick + alias). |
| `config show` | no | Print resolved config (JSON/YAML/table per `--output`). |
| `config path` | no | Print absolute config file path. |
| `completion {bash\|zsh\|fish\|powershell}` | no | Emit shell completion script to stdout. |
| `version` (or `--version`) | no | Print `yamaha-cli <version>`. |

Phase 2 will add `sound`, `decoder`, `scene`, `tone`, `sleep`, `tuner`, `netusb`, `preset`. Phase 3 will add `watch`, `link`, `reboot`, and `raw <method> [k=v ...]` (a generic YXC GET passthrough).

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

### Mutating commands

On success: `{}` (JSON/YAML modes) or a single `ok` line (table mode). The full state goes through `status`, not the mutating command.

### Error payload

JSON/YAML modes also emit:

```json
{"error": "yxc: transport: dial tcp 192.168.1.250:80: connect: host is down", "code": 69, "yxc_response_code": null}
```

Table mode prints `error: <message>` to stderr.
