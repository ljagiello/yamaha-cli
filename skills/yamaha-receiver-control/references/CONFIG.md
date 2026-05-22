# Configuration

## File location

The CLI uses Go's `os.UserConfigDir()`:

| OS | Path |
|---|---|
| Linux/BSD | `$XDG_CONFIG_HOME/yamaha-cli/config.yaml` (defaults to `~/.config/yamaha-cli/config.yaml`) |
| macOS | `~/Library/Application Support/yamaha-cli/config.yaml` |
| Windows | `%AppData%\yamaha-cli\config.yaml` |

Run `yamaha config path` to print the resolved path. All writes (wizard, `discover --add`, DHCP-resilience updates) go through `<file>.tmp` + `os.Rename` for atomicity.

## Schema

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

- `default_device` (string, required when ≥2 devices): alias used when no flag/env overrides.
- `devices.<alias>.host` (string, required): IP or hostname of the receiver (no scheme, no port).
- `devices.<alias>.udn` (string, optional but recommended): UPnP UDN. Required for DHCP-resilience.
- `devices.<alias>.default_zone` (string, optional): `main` or `zone2`. Falls back to `main`.

## Resolution order (active device)

Highest priority wins. Flag/env pairs are adjacent.

1. `--host <ip>` flag → **anonymous** (no alias, no UDN, no DHCP-resilience).
2. `YAMAHA_HOST` env → same as above.
3. `--device <alias>` flag → look up in `devices`.
4. `YAMAHA_DEVICE` env → look up in `devices`.
5. `default_device` from config.
6. **Single-device shortcut**: if exactly one entry exists in `devices`, use it (regardless of `default_device`).
7. None of the above → trigger first-run flow:
   - **Interactive (TTY)**: SSDP scan → prompt to pick → prompt for alias → save → re-run command transparently. Zero-found exits **69**.
   - **Non-interactive**: exit **64** with `no device configured; run 'yamaha discover' or pass --host`.

## DHCP resilience

When the active device was config-resolved (alias != "") AND has a saved UDN:

1. The first command run in a session starts at the saved IP.
2. If a transport error occurs — YXC HTTP (after the in-Client retry: connection refused, no route, timeout, ECONNRESET) or YNCA TCP (dial failure, EOF on stale conn) — the CLI:
   - Runs a 3 s SSDP scan filtered by `manufacturer == "Yamaha Corporation"`.
   - Matches the response set by UDN.
   - On hit: atomically updates `devices.<alias>.host`, logs `→ rediscover alias=… udn=…` (when `--debug`), retries the original command once.
   - On miss: exits **69** with `device "<alias>" (UDN <udn>) not reachable`.
3. At most one rediscovery attempt per command.

**Skipped when:**
- Active device is `--host` / `YAMAHA_HOST` (anonymous).
- Config entry has no UDN (pre-v5 manual entry). Re-run `yamaha discover --add` to refresh.

## Environment variables

| Variable | Maps to | Notes |
|---|---|---|
| `YAMAHA_HOST` | `--host` | Anonymous mode; skips DHCP-resilience. |
| `YAMAHA_DEVICE` | `--device` | Alias must exist in config. |
| `YAMAHA_ZONE` | `--zone` | `main` or `zone2`. |
| `YAMAHA_DEBUG` | `--debug` | Truthy: `1`, `true`, `yes`, `on` (case-insensitive). |
| `NO_COLOR` | `--no-color` | Any non-empty value disables ANSI color. Per [no-color.org](https://no-color.org). |

## Feature cache

Per-device (MAC-keyed) cache of `getFeatures` lives at `<UserCacheDir>/yamaha-cli/<device-id>-features.json`. Run `yamaha config path` to find `UserCacheDir`'s sibling.

**Refresh triggers** (any of):
- File missing.
- File mtime > 7 days old (TTL).
- `--refresh-features` flag passed explicitly.
- Validation miss in `input`/`sound`/etc. — auto-refreshes once before erroring with `did you mean`.

The cache is *not* invalidated on every invocation by a version check (this would defeat its purpose). After a firmware update, expect a one-time stale-cache "did you mean" until the auto-refresh kicks in, or pass `--refresh-features` once.
