# yamaha-cli

A command-line tool for controlling Yamaha receivers that speak the
**YamahaExtendedControl** (YXC) protocol over the local network — the same
HTTP/JSON API used by the MusicCast app. Built and verified against an
RX-V583, but should work with any MusicCast-capable Yamaha.

Phase 1 covers the most-used remote buttons (power / volume / mute / input /
status) plus SSDP discovery, multi-device config with DHCP-resilience, and
machine-readable output for shell pipelines.

## Install

```bash
go install github.com/ljagiello/yamaha-cli/cmd/yamaha@latest
```

This drops a single `yamaha` binary in `$(go env GOBIN)` (or `$GOPATH/bin`).

## Quickstart

First run with no config triggers an interactive wizard:

```text
$ yamaha status
No device configured. Searching the LAN…
Found 1 Yamaha device: RX-V583 FBE863 (RX-V583, 192.168.1.116)
Use this device? [Y/n]:
Alias for this device [rx-v583-fbe863]: living-room
Saved living-room → 192.168.1.116 (~/.config/yamaha-cli/config.yaml)
zone           main
power          on
input          hdmi2
volume         60 (-50.5 dB, 37%)
mute           false
sound_program  straight
```

After that, the alias is the default; subsequent commands hit it directly.

Non-interactive (CI, scripts) — pass `--host` or set `YAMAHA_HOST`:

```bash
YAMAHA_HOST=192.168.1.116 yamaha status
```

## Commands (Phase 1)

```text
yamaha status                                # zone power/input/volume/mute
yamaha power on|off|toggle [--no-wait]
yamaha volume <int|±N|up|down> [--db|--percent|--step N]
yamaha mute on|off|toggle
yamaha input <name>                          # validated against device features
yamaha discover [--add]                      # SSDP scan; --add saves to config
yamaha config show                           # print loaded config
yamaha config path                           # print config file path
yamaha completion {bash|zsh|fish|powershell}
yamaha version                               # also: yamaha --version
```

### Examples

```bash
# Read state to a shell pipeline.
yamaha status | jq -r .power                 # → on
yamaha status -o json | jq .volume_db        # → -22.5

# Volume by integer step (clamped to device range), signed delta, or up/down.
yamaha volume 60                             # absolute
yamaha volume +5                             # one HTTP call: setVolume?volume=up&step=5
yamaha volume down --step 3
yamaha volume -22.5 --db                     # absolute, dB-converted
yamaha volume 50 --percent                   # absolute, 0..100 scaled to device max

# Power on then switch input — no manual sleep needed; power on polls until ready.
yamaha power on && yamaha input hdmi2 && yamaha volume 50

# Talk to a second receiver without changing the default.
yamaha --device bedroom mute on
yamaha --zone zone2 volume +3

# One-shot against an arbitrary host (no config file required).
yamaha --host 192.168.1.116 status

# Trace every YXC call to stderr, keep stdout clean for jq.
yamaha --debug volume +5 2> trace.log

# Refresh the cached getFeatures (e.g. after a firmware update).
yamaha --refresh-features input hdmi4
```

## Configuration

Config lives where Go's `os.UserConfigDir()` points, plus `yamaha-cli/config.yaml`:

- **Linux/BSD:** `$XDG_CONFIG_HOME/yamaha-cli/config.yaml` (defaults to `~/.config/yamaha-cli/config.yaml`)
- **macOS:** `~/Library/Application Support/yamaha-cli/config.yaml`
- **Windows:** `%AppData%\yamaha-cli\config.yaml`

Run `yamaha config path` to print the resolved path. The wizard, `discover --add`,
and the DHCP-resilience flow all write through a `<file>.tmp` + rename so concurrent
invocations cannot corrupt it.

```yaml
default_device: living-room

devices:
  living-room:
    host: 192.168.1.116
    udn: uuid:9ab0c000-f668-11de-9976-00a0defbe863    # auto-saved on discovery
    default_zone: main                                 # main | zone2
  bedroom:
    host: 192.168.1.118
    udn: uuid:9ab0c000-f668-11de-9976-00a0defaa111
    default_zone: main
```

### Resolution order for the active device

Flag wins over env wins over config:

1. `--host <ip>` — anonymous (no alias, no UDN, no DHCP-resilience).
2. `YAMAHA_HOST` — same semantics as `--host`.
3. `--device <alias>` → look up in `devices`.
4. `YAMAHA_DEVICE` → look up in `devices`.
5. `default_device` from config.
6. Single-device shortcut: if exactly one device exists, use it.
7. Otherwise: trigger first-run wizard (TTY) or exit 64 (non-TTY).

### Environment variables

| Variable | Maps to | Notes |
|---|---|---|
| `YAMAHA_HOST` | `--host` | Anonymous; skips DHCP-resilience. |
| `YAMAHA_DEVICE` | `--device` | Alias must exist in config. |
| `YAMAHA_ZONE` | `--zone` | `main` or `zone2`. |
| `YAMAHA_DEBUG` | `--debug` | Truthy: `1`, `true`, `yes`, `on`. |
| `NO_COLOR` | `--no-color` | Any non-empty value disables ANSI color. |

## Output formats

Global flag: `--output {auto|json|yaml|table}` (alias `-o`). Default `auto`:

- **Table** when stdout is a TTY (human-readable, ANSI colour by default).
- **JSON** when stdout is piped or redirected.

This is the gh / kubectl / docker idiom — `yamaha status` is pretty in your
terminal and a parseable JSON object the moment you pipe it.

Successful mutating commands (`power on`, `volume 60`, `input hdmi2`) emit
`{}` in JSON modes and a single `ok` line in table mode. They never re-print
the entire device state — that's `yamaha status`'s job.

Disable colour with `--no-color` or `NO_COLOR=1`.

## Exit codes

Sysexits-lite. Errors go to stderr in `error: <message>` form regardless of
`--output`. With `--output json` or `--output yaml`, a structured payload
(`{ "error": "...", "code": <int>, "yxc_response_code": <int|null> }`) is
also emitted to stdout.

| Code | Meaning |
|---|---|
| 0   | Success |
| 1   | Generic error (validation failure, "did you mean", power-on timeout) |
| 2   | Misuse / invalid CLI argument (cobra default, `--db` with `+N`, etc.) |
| 64  | `EX_USAGE` — no device configured, non-interactive context |
| 69  | `EX_UNAVAILABLE` — device unreachable (transport error, DHCP-rediscover failed, zero-found) |
| 70  | `EX_SOFTWARE` — device returned non-zero `response_code` (feature unsupported, device not ready) |
| 130 | SIGINT (`cancelled by user`) |

## Zone scope

`--zone {main|zone2}` only applies to zone-scoped commands. Default is the
device's `default_zone` from config (or `main` if unset).

| Command | Zone-scoped |
|---|---|
| `status` | yes |
| `power` | yes |
| `volume` | yes |
| `mute` | yes |
| `input` | yes |
| `discover` | no |
| `config show` / `config path` | no |
| `completion` | no |
| `version` | no |

Phase 2 will add more zone-scoped commands (`sound`, `decoder`, `scene`,
`tone`, `sleep`); see Roadmap.

## DHCP resilience

Receivers on home networks routinely get a new IP after DHCP renewals or
router reboots. The CLI handles this transparently when the active device
came from the config file (alias-resolved, with a saved UDN).

On any HTTP transport error (after one in-client retry), the CLI runs a 3 s
SSDP scan filtered by manufacturer = `Yamaha Corporation`, matches the saved
UDN, atomically updates the config with the new IP, and retries the original
command once. The user sees only the success result — pass `--debug` to see
the rediscovery line.

**Skipped when:**
- Active device came from `--host` / `YAMAHA_HOST` (anonymous, no UDN).
- The config entry has no UDN (pre-v5 config). Re-run `yamaha discover --add`
  to refresh the entry, or use `--host` directly. Otherwise: exit 69.

At most one rediscovery attempt per command. Repeated failures fall through
to exit 69.

## Debugging

`--debug` (or `YAMAHA_DEBUG=1`) traces every YXC request and response on
stderr:

```text
$ yamaha --debug volume +5
→ GET http://192.168.1.116/YamahaExtendedControl/v1/main/setVolume?volume=up&step=5
← 200 {"response_code":0}
```

Retries are logged as `→ retry`; DHCP rediscovery as
`→ rediscover alias=… udn=…`. Stdout stays clean — pipe to `jq` while
debugging:

```bash
yamaha --debug status 2> trace.log | jq .volume_db
```

## Security note

YXC is **HTTP-only** (no TLS) and **unauthenticated** — there is no password,
token, or pairing step. This is the receiver's design, not a CLI choice. The
protocol assumes a trusted LAN.

Practical implications:

- Anyone on the same L2 segment can issue the same commands. Don't expose the
  receiver to untrusted networks.
- The CLI has no credentials to manage and writes none to disk.
- Don't port-forward port 80 of the receiver to the public internet.

## Roadmap

Phase 1 (this README) is the MVP. Phases 2 and 3 are deferred:

- **Phase 2** — full surface: `sound`, `decoder`, `scene`, `tone`, `sleep`,
  `tuner`, `netusb`, `preset`, `link`, `reboot`. Same conventions as Phase 1.
- **Phase 3** — `watch` (UDP event subscription), MusicCast Link (multi-room
  grouping), and `raw <method> [key=value …]` — a generic YXC GET
  passthrough that exposes all 184 endpoints from the public spec (party
  mode, YPAO, Bluetooth pairing, MusicCast playlists, surround pairing, CCS,
  alarms, Sonos integration). Anything not yet typed is reachable from day
  one once `raw` lands.

## Contributing & License

Personal-use CLI; PRs welcome but no guarantees on review velocity. MIT —
see [LICENSE](./LICENSE).
