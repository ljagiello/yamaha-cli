# yamaha-cli

A command-line tool for controlling Yamaha receivers that speak the **YamahaExtendedControl** (YXC) protocol over the local network — the same HTTP/JSON API used by the MusicCast app. Built and verified against an RX-V583, but should work with any MusicCast-capable Yamaha.

Covers power, volume, mute, input, sound program, surround decoder, scene, tone, sleep, tuner (FM/AM + presets), NetUSB transport (play/pause/skip/etc.), MusicCast presets, MusicCast Link multi-room, system reboot, push events (`watch`), generic YXC passthrough (`raw`), and legacy YNCA passthrough (`ynca`). Plus SSDP discovery, multi-device config with DHCP-resilience, and machine-readable output for shell pipelines.

## Contents

- [Install](#install)
- [Quickstart](#quickstart)
- [Commands](#commands)
- [Configuration](#configuration)
- [Output formats](#output-formats)
- [Exit codes](#exit-codes)
- [Zone scope](#zone-scope)
- [Watch](#watch)
- [MusicCast Link](#musiccast-link)
- [Raw passthrough](#raw-passthrough)
- [YNCA passthrough](#ynca-passthrough)
- [DHCP resilience](#dhcp-resilience)
- [Debugging](#debugging)
- [Security note](#security-note)
- [Run with Friday](#run-with-friday)
- [Roadmap](#roadmap)
- [Contributing & License](#contributing--license)

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

## Commands

```text
# Zone-scoped (default zone from config, or main):
yamaha status                                # zone power/input/volume/mute/sound_program
yamaha power on|off|toggle [--no-wait]
yamaha volume <int|±N|up|down> [--db|--percent|--step N]
yamaha mute on|off|toggle
yamaha input <name>                          # validated against device features
yamaha sound <program>                       # DSP sound program; validated against features
yamaha decoder <type>                        # surround decoder type; validated
yamaha scene <1..N>                          # recall a scene (N from features)
yamaha tone bass <-12..+12> | tone treble <-12..+12> | tone reset
yamaha sleep 0|30|60|90|120|off              # sleep timer in minutes

# NetUSB / MusicCast playback engine:
yamaha netusb info                           # now-playing payload
yamaha netusb play|pause|stop|toggle
yamaha netusb next|prev|ff|rew
yamaha netusb shuffle | netusb repeat        # toggles
yamaha preset list                           # NetUSB MusicCast presets
yamaha preset recall <num>

# Tuner:
yamaha tuner status
yamaha tuner fm <MHz>                        # e.g. 102.5
yamaha tuner am <kHz>                        # e.g. 1530
yamaha tuner preset <num> [--band fm|am]
yamaha tuner presets [--band fm|am]

# Multi-room (MusicCast Link):
yamaha link create <leader> <follower> [<follower>...]
yamaha link dissolve [<leader>]
yamaha link info [<leader>]

# System / events / passthroughs:
yamaha reboot --yes                          # always requires --yes
yamaha watch [--device a,b,c]                # NDJSON push events
yamaha raw <method> [k=v ...]                # generic YXC GET passthrough
yamaha ynca <line>                           # TCP/50000 YNCA passthrough

# Discovery & config:
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

# Sound program / surround decoder / scene / tone.
yamaha sound straight
yamaha decoder dolby_surround
yamaha scene 2
yamaha tone bass +3
yamaha tone reset
yamaha sleep 60                              # auto-off in 60 min

# Tuner: FM/AM, recall a preset, list presets.
yamaha tuner fm 102.5
yamaha tuner am 1530
yamaha tuner preset 7 --band fm
yamaha tuner presets --band fm | jq

# NetUSB transport + now-playing.
yamaha netusb play
yamaha netusb info
yamaha netusb ff
yamaha preset recall 3                       # MusicCast preset

# Push events — NDJSON, auto-reconnect with backoff, SIGINT clean-shutdown.
yamaha watch | jq 'select(.delta.main.volume)'
yamaha watch --device living-room,bedroom

# MusicCast Link: group two receivers, then dissolve.
yamaha link create living-room bedroom
yamaha link info
yamaha link dissolve

# Generic YXC passthrough — covers the ~184 endpoints in the public spec.
yamaha raw system/setPartyMode enable=true
yamaha raw netusb/setPlaybackMode mode=repeat type=track

# YNCA passthrough — legacy line protocol on TCP/50000.
yamaha ynca @MAIN:VOL=?
yamaha ynca @SYS:MODELNAME=?

# System reboot — always requires --yes.
yamaha reboot --yes

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

Run `yamaha config path` to print the resolved path. The wizard, `discover --add`, and the DHCP-resilience flow all write through a `<file>.tmp` + rename so concurrent invocations cannot corrupt it.

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

This is the gh / kubectl / docker idiom — `yamaha status` is pretty in your terminal and a parseable JSON object the moment you pipe it.

Successful mutating commands (`power on`, `volume 60`, `input hdmi2`) emit `{}` in JSON modes and a single `ok` line in table mode. They never re-print the entire device state — that's `yamaha status`'s job.

Disable colour with `--no-color` or `NO_COLOR=1`.

## Exit codes

Sysexits-lite. Errors go to stderr in `error: <message>` form regardless of `--output`. With `--output json` or `--output yaml`, a structured payload (`{ "error": "...", "code": <int>, "yxc_response_code": <int|null> }`) is also emitted to stdout.

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

`--zone {main|zone2}` only applies to zone-scoped commands. Default is the device's `default_zone` from config (or `main` if unset).

| Command | Zone-scoped |
|---|---|
| `status` | yes |
| `power` | yes |
| `volume` | yes |
| `mute` | yes |
| `input` | yes |
| `sound` | yes |
| `decoder` | yes |
| `scene` | yes |
| `tone` | yes |
| `sleep` | yes |
| `tuner *` | no (tuner is system-wide) |
| `netusb *` | no |
| `preset list` / `preset recall` | no |
| `watch` | no |
| `link create` / `link dissolve` / `link info` | no |
| `reboot` | no (system-wide; `--zone` is ignored) |
| `raw` | no (caller supplies the method path) |
| `ynca` | no |
| `discover` | no |
| `config show` / `config path` | no |
| `completion` | no |
| `version` | no |

## Watch

`yamaha watch` subscribes to the receiver's UDP push channel and emits one event per line. Default output is NDJSON; in table mode each event renders as one or more `HH:MM:SS  alias  zone.field = value` lines.

```json
{"ts":"2026-05-07T18:42:01.123Z","device":"living-room","delta":{"main":{"volume":62}}}
{"ts":"2026-05-07T18:42:01.456Z","device":"living-room","event":"reconnect","reason":"udp: silent for 30s"}
```

`--device a,b,c` watches multiple aliases concurrently; events from each are tagged with the alias. The subscriber auto-reconnects with exponential backoff (1 s → 60 s) when the receiver goes silent for 30 s, emitting a `reconnect` control event each time. SIGINT triggers a clean shutdown — the channel closes, in-flight goroutines drain, and the command exits 0.

## MusicCast Link

`yamaha link` wraps the `dist/*` YXC endpoints to drive multi-room audio. One device is the **leader** (server); one or more **followers** (clients) sync to it. All peers must be aliases in the config.

```bash
yamaha link create living-room bedroom kitchen   # leader, followers...
yamaha link info                                 # show distribution state
yamaha link dissolve                             # tear it down (defaults to active device)
```

`link create` runs three steps in order — `setServerInfo` on the leader, `setClientInfo` on each follower, then `startDistribution` — and rolls back partial groups on failure (stopping the leader and clearing each follower's server pointer). The existing-membership check refuses to enslave a follower that is already a group server *or* a client of another group (dissolve the existing group first), and a peer cannot be both leader and follower in the same call. `link dissolve` mirrors this: it refuses to dissolve a target that isn't currently a server, so dissolving from a client (or unattached) device returns a clear usage error rather than issuing a no-op `setServerInfo type=remove`.

## Raw passthrough

`yamaha raw <method> [k=v ...]` is the escape hatch for endpoints not yet wrapped by a typed command. The method argument is the YXC path (e.g. `system/setPartyMode`, `netusb/setPlaybackMode`); subsequent positional `k=v` pairs are url-encoded into the query string. Repeated keys append, matching how the receiver expects array params (`client_list[0].ip_address=…`). The reply is rendered through the standard `--output` formatter.

```bash
yamaha raw system/getDeviceInfo
yamaha raw main/setVolume volume=42
yamaha raw netusb/setPlaybackMode mode=repeat type=track
```

This covers the ~184 endpoints in the YXC public spec — party mode, YPAO, Bluetooth pairing, MusicCast playlists, surround pairing, alarms, and so on.

## YNCA passthrough

`yamaha ynca <line>` speaks the legacy line-based YNCA protocol on TCP/50000, used by classic AVR firmware (RX-V583 included). It's useful when YXC doesn't expose a particular control. The leading `@` is optional; the receiver's reply line is printed verbatim.

```bash
yamaha ynca @MAIN:VOL=?
yamaha ynca @SYS:MODELNAME=?
```

A capability probe runs once per invocation. Devices that don't speak YNCA fail fast with exit 70 (`device does not support YNCA`) instead of a vague timeout.

## DHCP resilience

Receivers on home networks routinely get a new IP after DHCP renewals or router reboots. The CLI handles this transparently when the active device came from the config file (alias-resolved, with a saved UDN).

On any transport error — YXC HTTP or YNCA TCP — the CLI runs a 3 s SSDP scan filtered by manufacturer = `Yamaha Corporation`, matches the saved UDN, atomically updates the config with the new IP, and retries the original command once. The user sees only the success result — pass `--debug` to see the rediscovery line.

**Skipped when:**

- Active device came from `--host` / `YAMAHA_HOST` (anonymous, no UDN).
- The config entry has no UDN (pre-v5 config). Re-run `yamaha discover --add` to refresh the entry, or use `--host` directly. Otherwise: exit 69.

At most one rediscovery attempt per command. Repeated failures fall through to exit 69.

## Debugging

`--debug` (or `YAMAHA_DEBUG=1`) traces every YXC request and response on stderr:

```text
$ yamaha --debug volume +5
→ GET http://192.168.1.116/YamahaExtendedControl/v1/main/setVolume?volume=up&step=5
← 200 {"response_code":0}
```

Retries are logged as `→ retry`; DHCP rediscovery as `→ rediscover alias=… udn=…`. Stdout stays clean — pipe to `jq` while debugging:

```bash
yamaha --debug status 2> trace.log | jq .volume_db
```

## Security note

YXC is **HTTP-only** (no TLS) and **unauthenticated** — there is no password, token, or pairing step. This is the receiver's design, not a CLI choice. The protocol assumes a trusted LAN.

Practical implications:

- Anyone on the same L2 segment can issue the same commands. Don't expose the receiver to untrusted networks.
- The CLI has no credentials to manage and writes none to disk.
- Don't port-forward port 80 (or TCP/50000 for YNCA) of the receiver to the public internet.

## Run with Friday

This repo ships an [Agent Skill](https://agentskills.io/specification) at `skills/yamaha-receiver-control/` so AI agents can drive `yamaha` without re-deriving its surface from `--help`. Drop it into [Friday Studio](https://hellofriday.ai/) — the shareable AI workspace runtime from [Tempest Labs](https://hellofriday.ai/) — to get scheduling, signals, MCP tools, and memory on top of the CLI. Everything runs locally; your data stays on your machine; every step is logged.

**Layout** (per the [agentskills.io specification](https://agentskills.io/specification)):

```text
skills/yamaha-receiver-control/
├── SKILL.md                  # entry point: setup, commands, exit codes, gotchas
└── references/
    ├── COMMANDS.md           # full subcommand & flag reference
    └── CONFIG.md             # config schema, resolution order, DHCP resilience
```

**Install via the Agent Skills CLI:**

```bash
npx skills add ljagiello/yamaha-cli/skills/yamaha-receiver-control
```

**Or in Friday Studio:**

1. Install Friday from [hellofriday.ai](https://hellofriday.ai/) (macOS).
2. Open **Skills** in the Studio sidebar and click **+ Add**.
3. Add by reference: `ljagiello/yamaha-cli/skills/yamaha-receiver-control`.
4. Reference it from any `workspace.yml`, or let agents load it automatically based on the skill's description.

See the [Friday Skills docs](https://docs.hellofriday.ai/core-concepts/skills) for the full workflow, and the [Friday blog](https://blog.hellofriday.ai/) for the philosophy.

After install, ask the agent in plain English (`"turn the receiver on and switch to HDMI 2"`, `"what's the volume in dB?"`, `"discover the Yamaha on my LAN and save it as 'living-room'"`) — it'll pick up the skill from the description and shell out to `yamaha` with the right flags. Progressive disclosure means the metadata is ~100 tokens at startup; `SKILL.md` loads only when the skill activates; `references/*.md` only when the agent navigates to them.

**Prerequisite:** the agent's host needs the `yamaha` binary on `PATH` (`go install github.com/ljagiello/yamaha-cli/cmd/yamaha@latest`) and LAN access to the receiver — same as a human user.

## Roadmap

The Phase 1 / 2 / 3 surface (every command in this README) is implemented and verified against an RX-V583. The original "184 bonus endpoints" from the YXC public spec — party mode, YPAO, Bluetooth pairing, MusicCast playlists, surround pairing, Cinema-Caster, alarms, Sonos integration — are reachable through `yamaha raw <method>` without further code.

What's still on the table:

- Typed wrappers for the bonus endpoints (`yamaha bluetooth list`, `yamaha ypao status`, etc.) where there's a concrete use case.
- Live integration tests against non-RX-V receivers.

## Contributing & License

Personal-use CLI; PRs welcome but no guarantees on review velocity. MIT — see [LICENSE](./LICENSE).
