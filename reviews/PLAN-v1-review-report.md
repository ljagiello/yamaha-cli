# PLAN v1 → v2 Design Review Report

**Date:** 2026-05-07
**Reviewer:** Claude (via /improving-plans)
**Input:** `PLAN.md` (v1)
**Output:** `PLAN.v2.md`

## Context gathered

- Read `PLAN.md` end-to-end (199 lines).
- Confirmed all `/tmp/...` artifacts cited by v1 still exist:
  - `/tmp/yxc_features.json`, `/tmp/yxc_desc_49154.xml`, `/tmp/yxc-mc.txt`
  - `/tmp/yamaha-apk/{avcontroller,musiccast}-{jadx,apktool}` directories all present.
- Worktree contains only `LICENSE` — no implementation yet, so review is purely on the design.
- No prior reviews directory existed; created fresh.

## Ideas surfaced (4 of a possible 5)

I surfaced four design choices that were either ambiguous in v1 or expensive to retrofit later. A fifth candidate (volume safety rails / max-volume cap) was considered but dropped — too speculative for an MVP, easy to add as a `safety.max_volume` config knob later.

### 1. Output formatting — **Decided: TTY auto-detect + `--output` flag**

**Problem in v1:** Only "pretty-print" mentioned for `status`. Any shell pipeline (`yamaha status | jq`) would have to scrape human text.

**Options presented:**
- A. Auto-detect TTY (recommended) — gh / kubectl / docker idiom.
- B. Always require `--output`.
- C. No flag — only human-readable.

**User picked A.** Captured in v2 as a global `--output {json|yaml|table|auto}` flag defaulting to `auto`, resolving via `mattn/go-isatty`. Mutating commands emit `{}` in JSON modes rather than the full state, to avoid making every set look like a get.

### 2. First-run UX — **Decided: auto-discover + prompt to save**

**Problem in v1:** Plan had `yamaha discover` in Phase 2 and a static config file, but no story for the first invocation. User would have to manually find the IP and edit YAML before `yamaha status` worked.

**Options presented:**
- A. Auto-discover + interactive save (recommended).
- B. Friendly error pointing to `yamaha discover`.
- C. Always discover, no config file.

**User picked A.** Captured in v2:
- SSDP discovery promoted from Phase 2 into Phase 1.
- Wizard runs only when stdout is a TTY; non-interactive contexts exit code 64 with a hint, never hang.
- Saves to `~/.config/yamaha-cli/config.yaml` and re-runs the original command transparently.

### 3. Device addressing — **Decided: multi-device with aliases from day one**

**Problem in v1:** Implicit single-device assumption. MusicCast Link is intrinsically multi-device, and once `yxc.Client` instances are threaded through every cobra command, retrofitting addressing means touching every command signature.

**Options presented:**
- A. Multi-device with aliases (recommended).
- B. Single device now, refactor later.
- C. No config, always `--host`.

**User picked A.** Captured in v2:
- Config schema: `default_device: <name>` + `devices: { name: { host, default_zone } }`.
- 6-step resolution order: `--host` → `--device` → `YAMAHA_DEVICE` → `default_device` → single-device shortcut → first-run wizard.
- Per-device feature cache keyed by `device_id` (MAC) instead of IP — survives DHCP renewals.

### 4. Phase 3 scope — **Decided: trim hard + `raw` escape hatch**

**Problem in v1:** Phase 3 listed ~30 endpoints across YPAO, CCS, Sonos, Bluetooth, MusicCast playlists, surround pairing, Disklavier, alarms. Heavy scope, mostly never-used by a single-receiver hobbyist. Risk of perpetual "Phase 3 in progress".

**Options presented:**
- A. Trim hard + `raw` escape hatch (recommended).
- B. Keep all 30 as aspirational scope.
- C. Ship `raw` only, no typed Phase 3 commands.

**User picked A.** Captured in v2:
- Phase 3 = `watch`, `link`, `reboot`, `ynca`, `raw` (5 commands).
- All 184 endpoints in `/tmp/yxc-mc.txt` reachable from day one via `yamaha raw <method> [k=v...]`.
- Bonus catalog moved to a "Future expansion" appendix — promoting any to typed commands later is purely additive.

## Other small additions

Two items I added to v2 without explicit user input because they were obvious gaps, not design choices:

- **Exit-code scheme** — sysexits-lite (0/1/2/64/69/70). Without this, scripts can't distinguish "device unreachable" from "feature unsupported", and shell idioms break.
- **Live-device integration tests** behind `//go:build integration` — exercises read-only paths against `192.168.1.116`. Skipped in normal `go test`, never runs in CI. The user has the device — leverage it.

Plus a small correctness fix:
- **Per-device cache key** — switched from implicit IP-based to MAC-based (`device_id` from `getDeviceInfo`). Avoids stale caches after DHCP renewals or when one binary controls multiple receivers.

## Considered and dropped

- **Volume safety rails (`safety.max_volume` config option)** — speculative; user can add when they accidentally blast their speakers once. Not worth surfacing now.
- **`yamaha exec script.txt` for chained commands** — shell already does this with `&&`. Don't reinvent.
- **`--debug` / `YAMAHA_DEBUG` request/response logging** — useful but uncontroversial; will be added during implementation, not a design decision.
- **Build/distribution (homebrew tap, goreleaser)** — out of scope for this design pass; `go install` works for v1 acceptance.

## Unresolved questions

None blocking. Worth revisiting in a future review:

1. **Power-on settle UX** — should `yamaha power on` poll until `power=on` reflects in `getStatus` (blocking, friendly), or return immediately (fast, scripts must add their own wait)? Current v2 leaves it to the user via shell composition. May want a `--wait` flag later.
2. **`prepareInputChange` automation** — v2 mentions this in "Critical gotchas" but doesn't specify whether `pkg/yxc.SetInput` calls it automatically when `func_list` requires it. Probably should — but adds an implicit extra round-trip.
3. **First-run wizard alias-naming** — v2 defaults to slugified `network_name` (e.g., `RX-V583 FBE863` → `rx-v583-fbe863`). Probably fine; `living-room` is more semantic but requires explicit user input.

## Net result

v2 is materially more useful than v1: zero-config first run, scriptable output, multi-device-ready architecture, focused Phase 3 with universal endpoint reach via `raw`. None of the v1 research / endpoint catalog / artifact references were lost — they all carry forward.
