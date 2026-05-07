# PLAN v3 → v4 Design Review Report

**Date:** 2026-05-07
**Reviewer:** Claude (via /improving-plans)
**Input:** `PLAN.v3.md`
**Output:** `PLAN.v4.md`
**Prior reviews:** `reviews/PLAN-v1-review-report.md`, `reviews/PLAN-v2-review-report.md`

## Context gathered

- Re-read `PLAN.v3.md` end-to-end with attention to caching and reliability claims that were copied forward unchanged from earlier versions.
- Re-read both prior review reports to avoid retreading already-resolved items.
- No additional codebase exploration needed — v1 and v2 reviews already exhausted the local artifacts (`/tmp/yxc-mc.txt`, decompiled APK, live `getFeatures` capture).

## Ideas surfaced (4 of a possible 5)

Four genuine design gaps surfaced. A handful of related-but-uncontroversial fixes were folded in without polling.

### 1. getFeatures cache invalidation — **Decided: TTL + on-miss + manual refresh**

**Problem in v3:** Phase 1 task 8 listed "refresh when cached `system_version` ≠ live `getDeviceInfo.system_version`" as a refresh trigger. That requires a `getDeviceInfo` HTTP call on every command — the cache exists to *avoid* HTTP on hot paths, so this contradicts itself. Carried forward unchanged from v1; no prior review caught it.

**Options presented:**
- A. TTL (7 days) + on-validation-miss + manual refresh (recommended).
- B. Per-invocation version check (status quo in v3).
- C. Manual refresh only.

**User picked A.** Captured in v4:
- New "getFeatures cache invalidation" section spelling out triggers and non-triggers.
- Phase 1 task 8 rewritten — version check removed.
- Definition of done now includes a stale-cache test: edit cache to omit `hdmi3`, run `yamaha input hdmi3`, verify exactly one auto-refresh + one `setInput`.
- TTL operationalized via file mtime (no separate metadata file needed).

### 2. HTTP retry policy — **Decided: one retry on transient errors only**

**Problem in v3:** Plan said `*http.Client` default 5 s timeout. No mention of retries. The probe data confirmed the device is on Wi-Fi (SSID `Domek_5G`, ~106 ms RTT) — transient packet loss and ARP-cache cold starts are realistic. Without retries, every other `yamaha volume +5` could appear broken on a flaky link.

**Options presented:**
- A. One retry on transient errors only (recommended).
- B. Exponential backoff (3 retries).
- C. No retries.

**User picked A.** Captured in v4:
- New "HTTP retry policy" section listing exactly which error types retry vs don't.
- 250 ms backoff before the single retry. Total wall-clock cap with 5 s timeout: ~10.25 s.
- No retry on YXC `response_code != 0` or HTTP non-200 — those are deterministic, not transient.
- Test suite gains a transient-error retry matrix.
- Gotcha #12 added: "HTTP retries are silent and capped at one. Visible in `--debug`."

### 3. `yamaha raw` parameter encoding — **Decided: positional `k=v` pairs**

**Problem in v3:** Phase 3 showed `yamaha raw netusb/getMcPlaylist bank=1` but didn't specify parsing rules. Special characters, spaces, multi-value parameters all unspecified.

**Options presented:**
- A. Positional `k=v`, auto URL-encode (recommended).
- B. `--param` flags.
- C. Single JSON-object positional.

**User picked A.** Captured in v4:
- "Raw — passthrough to any YXC endpoint" section in Phase 3.
- Multi-value via repeating key (`k=v1 k=v2`).
- Shell quoting handles spaces. CLI URL-encodes values.
- Concrete examples provided for `getMcPlaylist`, `setPartyMode`, `setName` (with quoted value), `manageList`.

### 4. `yamaha watch` resilience — **Decided: auto-reconnect with backoff**

**Problem in v3:** Plan said "renew every ~8 minutes" but didn't address reconnection when the device drops mid-stream. For a tool meant to feed scripts/dashboards, silent multi-hour gaps are a sneaky failure mode.

**Options presented:**
- A. Auto-reconnect with exponential backoff (recommended).
- B. Exit on first failure.
- C. Silent retry forever.

**User picked A.** Captured in v4:
- "Watch — output and resilience" section in Phase 3.
- Backoff schedule: 1s → 2s → 5s → 15s → 60s, capped at 60s.
- Three trigger types: UDP-renewal failure, 30s of silence, initial-subscription error.
- Control event NDJSON line on each reconnect attempt: `{"event":"reconnect","attempt":N,"reason":"…"}`.
- TTY mode renders the control event with a distinct prefix.
- Watch never exits voluntarily on connection failure; SIGINT exits clean (code 0).
- Gotcha #13 added: "If you see attempt count in the dozens, the device is genuinely down."

## Other additions (no question needed)

Six small fixes folded into v4 without polling:

- **`--zone` applicability table** — v3 had the `--zone` flag but never said which commands accept it. New "Zone scope" section provides an explicit table covering all 16 commands. Passing `--zone` to a zone-irrelevant command is a soft warning, not an error.
- **Atomic config writes** — temp file + rename pattern for the wizard and `discover --add`. Concurrent invocations no longer race the YAML.
- **`--version` flag** in addition to the `version` subcommand. Cobra-idiomatic.
- **`YAMAHA_DEBUG` truthy parsing** — pinned to `1`/`true`/`yes`/`on` (case-insensitive). Empty / `0`/`false`/`no`/`off` → off. Prevents the "I set `YAMAHA_DEBUG=true` and it didn't work" footgun.
- **First-run alias collisions** — auto-suggest `<alias>-2`, `<alias>-3`, etc.
- **`--db` / `--percent` are absolute-only** — explicit error for `+N --db` combinations. Prevents re-introducing the GET-then-SET race that v3 just eliminated. Also pins "one integer step ≈ 0.5 dB" so users have a mental conversion.

## Considered and dropped

- **MusicCast Link orchestration sequence** — Initially considered as a question. Resolved by writing it down without polling: the sequence (`setServerInfo` on leader → `setClientInfo` on each follower → `startDistribution` on leader) is documented in YXC and `aiomusiccast`; rollback on partial failure and cycle detection are obvious requirements.
- **YNCA capability probe** — Phase 3 question, resolved without polling: probe `@SYS:VERSION=?` once per device, cache the result, surface a clear error on unsupported.
- **Volume `+2.5 --db`** — Resolved by simply forbidding it. dB-deltas would force GET-then-SET and undo a v3 win.
- **Watch event filters (`--filter main.volume`)** — Premature. Add when usage shows it's needed.
- **`yamaha config edit`** opening `$EDITOR` — Convenience, not a design decision.

## Unresolved questions (carry to v5 if useful)

None blocking. Possible future iterations:

1. **Concurrent YXC requests within a single Client** — v4 mentions the 100 ms intra-process rate-limit but doesn't specify the mechanism (mutex vs. token bucket vs. channel). Implementation detail; will be obvious during coding.
2. **`watch --filter <jq-expr>`** — server-side filtering would reduce noise for dashboard use cases.
3. **Structured logs (slog) instead of plain stderr** — `--debug` currently writes line-oriented text. If users start piping it through structured-log tooling, slog might be worth it. Premature today.
4. **MQTT bridge mode** — `yamaha bridge --mqtt <broker>`. Whole different scope; probably a sibling tool, not a flag.
5. **Goreleaser / homebrew tap** — distribution. Phase 1 ships via `go install`; package distribution is a v1.0 polish task.

## Net result

v4 closes the four most consequential remaining gaps in v3 (cache invalidation, retry policy, raw encoding, watch resilience) and folds in six small uncontroversial fixes. The plan is now ready to implement: every command's behavior is specified, every error path has an exit code, every cache decision has triggers and non-triggers documented, and Phase 3 features that were "obvious to design later" (link orchestration, YNCA capability probe) are written down.

Three review passes in, the plan covers:
- 25+ resolved design decisions
- Explicit acceptance criteria for Phase 1
- Test strategy (unit + integration + transient-error matrix + stale-cache test)
- Authoritative protocol references (YXC GET-only, exact headers, APK-derived endpoint catalog)
- A clean escape hatch (`raw`) so 90% of "future expansion" is reachable from day one without code changes.
