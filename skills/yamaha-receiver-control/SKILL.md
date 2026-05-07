---
name: yamaha-receiver-control
description: Controls Yamaha AV receivers (RX-V/RX-A series, MusicCast-capable) over the local network via the yamaha-cli command-line tool. Use when the user asks to power on/off the receiver, change volume, switch HDMI inputs, mute, view receiver state, discover Yamaha devices on the LAN, or otherwise interact with their Yamaha amplifier without the physical remote. Speaks the YamahaExtendedControl (YXC) HTTP/JSON protocol; works against any MusicCast-capable Yamaha but verified on RX-V583.
license: MIT
metadata:
  source-repo: github.com/ljagiello/yamaha-cli
  protocol: YamahaExtendedControl (YXC)
compatibility: Requires Go 1.22+ to install from source, or a prebuilt yamaha binary on PATH. Receiver must be on the same LAN as the host running the skill (SSDP discovery + UDP event push are link-local). No HTTPS, no auth.
---

# yamaha-receiver-control

Drive a Yamaha MusicCast/YXC receiver from the shell via the `yamaha` CLI.

## Setup (run once)

```bash
go install github.com/ljagiello/yamaha-cli/cmd/yamaha@latest
yamaha --version    # verify install: prints "yamaha-cli <version>"
```

Then point the CLI at a receiver (pick **one**):

| Path | When | Persistence |
|---|---|---|
| `yamaha discover --add` | First time, interactive | Saved to config with UDN; survives DHCP IP changes |
| `YAMAHA_HOST=<ip>` env var | One-shot, scripted | None |
| `--host <ip>` flag on each invocation | Quick experiments | None |

If no host is configured and stdout is a TTY, any command triggers an interactive wizard. If non-TTY, it exits 64 with a hint.

## Doing things

All commands work against the active device (resolved per [references/CONFIG.md](references/CONFIG.md)). The `--output` flag controls formatting; default `auto` picks **table** for TTY and **JSON** when piped.

```bash
yamaha status                                # current zone state
yamaha status | jq -r .power                 # → "on"
yamaha power on|off|toggle [--no-wait]       # default: blocks until power reflects
yamaha volume 60                             # absolute integer (0..max from device)
yamaha volume +5                             # one HTTP call: setVolume?volume=up&step=5
yamaha volume -- -5                          # NOTE: negative deltas need "--" because of cobra
yamaha volume -22.5 --db                     # absolute, dB-scaled
yamaha volume 50 --percent                   # absolute, 0..100 → device range
yamaha mute on|off|toggle
yamaha input hdmi1                           # validated against device's input list
yamaha discover                              # SSDP scan, list found Yamaha devices (no state changes)
yamaha config show                           # dump resolved config as JSON/YAML/table
yamaha config path                           # print config file path
```

For multi-zone receivers (RX-V583 has `main` + `zone2`):

```bash
yamaha --zone zone2 volume 40
yamaha --zone zone2 input av1
```

For a second receiver saved in config:

```bash
yamaha --device bedroom power on
```

Full command reference + flag matrix: [references/COMMANDS.md](references/COMMANDS.md).

## Scripting and error handling

**Always pass `--output json` when parsing.** `auto` mode emits a table when stdout is a TTY which is unparseable.

```bash
volume=$(yamaha status --output json | jq -r .volume)
power=$(yamaha status --output json | jq -r .power)
```

**Exit codes** (sysexits-lite):

| Code | Meaning | Typical handling |
|---|---|---|
| 0 | Success | Continue |
| 1 | Validation error / power-on timeout | Bad input; surface to user |
| 2 | CLI usage error (invalid flag combo) | Fix the command |
| 64 | No device configured, non-interactive | Set `YAMAHA_HOST` or `--host` |
| 69 | Device unreachable (transport failure, retry exhausted) | Network problem; receiver may be off |
| 70 | YXC `response_code != 0` (feature unsupported, device not ready) | Check `yxc_response_code` in error JSON |
| 130 | SIGINT during a non-watch command | User cancelled |

With `--output json`, failed commands also emit a structured error to stdout:

```json
{"error": "...", "code": 69, "yxc_response_code": null}
```

## Gotchas

These are non-obvious; check before assuming.

- **Negative volume deltas need `--`** to prevent cobra from parsing `-5` as a flag: `yamaha volume -- -5` (or use `yamaha volume down --step 5`).
- **`--db` and `--percent` are absolute-only.** Combining with `+N`/`-N` exits 2. To shift by ~2.5 dB, use `volume +5` (one integer step ≈ 0.5 dB).
- **Volume range is per-device.** Don't hardcode 0..100. RX-V583 is 0..161 → -80.5..+16.5 dB. Read from `getFeatures` (the CLI does this for you; `volume 50 --percent` is the safe scripted form).
- **Power on takes 2–5 s to reflect.** Default `power on` blocks via polling (max 10 s). Use `--no-wait` only if you don't need the receiver ready immediately afterwards.
- **Input switching may auto-fire `prepareInputChange` first** for inputs whose `func_list` requires it (e.g., `server`, `net_radio`). This is internal and free; just don't be surprised by two HTTP requests in `--debug`.
- **DHCP IP changes are handled transparently** when the device was added via `discover --add` (the UDN is stored). Anonymous `--host` / `YAMAHA_HOST` calls do **not** auto-recover.
- **No HTTPS, no auth.** Anyone on the LAN can issue commands. Don't expose the receiver to untrusted networks.
- **YXC is GET-only.** All operations are `GET /YamahaExtendedControl/v1/<method>?<params>` on port 80. No POSTs, no JSON request bodies.

## Debugging

Pass `--debug` (or set `YAMAHA_DEBUG=1`) to log every YXC request + response to stderr in `→`/`←` form. Stdout stays clean — pipe through `jq` while debugging:

```bash
yamaha --debug status 2> trace.log | jq .volume_db
```

Useful debug-trace prefixes:
- `→ GET <url>` / `← <status> <body>` — every request/response.
- `→ retry` — the silent single retry on transient transport errors fired.
- `→ rediscover alias=… udn=…` — DHCP-resilience kicked in (config will be updated atomically).

## Workflows

### Power on, set inputs and volume, check state
```bash
yamaha power on \
  && yamaha input hdmi2 \
  && yamaha volume 50 --percent \
  && yamaha status
```
No manual `sleep` needed — `power on` blocks until ready.

### Discover and save a receiver in CI/scripts
```bash
yamaha discover --output json | jq '.[0]'   # see what's there
yamaha discover --add                        # interactive; wizards through pick + alias
```

### Scripted volume nudge with sanity check
```bash
current=$(yamaha status --output json | jq -r .volume)
if [ "$current" -lt 100 ]; then
  yamaha volume +5
fi
```

## References

- [references/COMMANDS.md](references/COMMANDS.md) — full subcommand reference, flag matrix, zone-scope table.
- [references/CONFIG.md](references/CONFIG.md) — config schema, resolution order, env vars, DHCP-resilience details.
- Upstream repo and source-of-truth design doc: `https://github.com/ljagiello/yamaha-cli` (see `PLAN.v6.md`).
