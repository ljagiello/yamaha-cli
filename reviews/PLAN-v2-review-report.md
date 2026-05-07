# PLAN v2 → v3 Design Review Report

**Date:** 2026-05-07
**Reviewer:** Claude (via /improving-plans)
**Input:** `PLAN.v2.md`
**Output:** `PLAN.v3.md`
**Prior review:** `reviews/PLAN-v1-review-report.md` (v1 → v2)

## Context gathered

- Re-read `PLAN.v2.md` end-to-end.
- Read `reviews/PLAN-v1-review-report.md` to avoid retreading the four already-decided items (output format, first-run UX, multi-device addressing, Phase 3 trim).
- Sampled `/tmp/yxc-mc.txt` (184 endpoints) and `/tmp/yxc_features.json` to ground reasoning in real data.
- Ran `grep` over `/tmp/yamaha-apk/musiccast-jadx/sources/` for HTTP method usage:
  - `b3.java:1162` uses `setRequestMethod("GET")` — that's the YXC client.
  - `b3.java:1293` uses POST but for `/yamahapim/...` Yamaha account endpoints (Content-Type: text/xml) — not YXC.
  - Other POST sites (`fe/d.java`, `nc/l.java`, `pc/j.java`, etc.) are Gracenote / legacy XML / Firebase. None are YXC.
  - **Conclusion:** YXC is GET-only. v2's plan is correct on this; one initial concern (whether to model POST + JSON-body endpoints) was invalidated by the evidence.

## Ideas surfaced (4 of a possible 5)

I picked four ideas that represented genuine design tradeoffs not covered in the v1 review. Skipped a few near-misses (concurrency model — too speculative; HTTP method modeling — invalidated by evidence above).

### 1. Volume deltas: TOCTOU GET-then-SET vs server-side relative — **Decided: server-side relative**

**Problem in v2:** The volume command's `+5`/`-5` flow read `getStatus.volume` first, computed the new absolute, then issued `setVolume`. Two roundtrips and a TOCTOU window where another remote could change volume between reads.

**Discovery:** YXC supports `setVolume?volume=up|down&step=N` server-side. v1's research summary already noted this, but v2's volume task spec didn't use it.

**Options presented:**
- A. Server-side relative (recommended).
- B. Keep GET-then-SET for client-side clamping & "would exceed max" message.

**User picked A.** Captured in v3:
- `+5` → `setVolume?volume=up&step=5`. `-5` → `setVolume?volume=down&step=5`.
- Single roundtrip. Device clamps internally.
- Acceptance tests now assert exactly one HTTP call for `volume +5` (visible in `--debug` log).

### 2. Power-on `--wait` default — **Decided: block by default, `--no-wait` opts out**

**Problem in v2:** v1's review report flagged this as an "unresolved question" carried into v2. Plan said to "poll until power=on" but didn't pin the default behavior. Chained commands like `yamaha power on && yamaha volume 60` would silently fail if `power on` returned before the device was ready.

**Options presented:**
- A. Block by default, `--no-wait` to opt out (recommended).
- B. Return immediately, `--wait` to opt in (Unix-classic).
- C. Auto-detect TTY.

**User picked A.** Captured in v3:
- `power on` and `power toggle` (when transitioning off→on) poll `getStatus` every 200 ms until `power == "on"` or 10 s elapses.
- Timeout → exit 1 with "device did not report power=on within 10s; check the receiver".
- `--no-wait` skips the poll loop entirely.
- `power off` is fire-and-forget regardless.
- Resolves the v1 unresolved question.

### 3. `yamaha watch` output format — **Decided: NDJSON with wrapper**

**Problem in v2:** Phase 3 said `yamaha watch` "streams JSON deltas" but didn't specify framing, timestamps, or multi-device disambiguation. Without a pinned format, the implementation would pick something arbitrary that scripts couldn't rely on.

**Options presented:**
- A. NDJSON wrapped: `{ts, device, delta}` per line (recommended).
- B. Raw deltas, one per line, no wrapper.
- C. Pretty TTY, NDJSON when piped.

**User picked A.** Captured in v3:
- One event per line: `{"ts":"...","device":"living-room","delta":{...}}`.
- TTY mode renders compact human-readable lines: `12:34:56  living-room  main.volume = 60`.
- `--device a,b,c` watches multiple devices simultaneously; the `device` field disambiguates.

### 4. Client-side input validation — **Decided: strict + auto-refresh on miss**

**Problem in v2:** Plan said "tab-completion from getFeatures cache" but didn't say what happens when the user types `yamaha input typo` directly. Receiver would return generic `response_code: 6`, useless for diagnostics.

**Options presented:**
- A. Strict + auto-refresh on miss (recommended).
- B. Strict, no auto-refresh.
- C. Lenient — always send to device.

**User picked A.** Captured in v3:
- New "Validation strategy" section.
- Validation flow: cache → match? → refresh on miss → match? → "did you mean: …" with Levenshtein top-3.
- Applied to `input` in Phase 1, extended to `sound`/`decoder`/`scene` in Phase 2.
- Acceptance test asserts zero HTTP calls (other than cache refresh) when input is unknown.

## Other additions (no question needed)

Five small fixes added to v3 without polling — all uncontroversial:

- **`--debug` / `YAMAHA_DEBUG=1`** — request/response trace to stderr. Was flagged as "deferred but missing" in v1's report. Now an explicit task and a new `internal/debuglog` package.
- **`pkg/yxc.SetInput` auto-calls `prepareInputChange`** when `func_list` requires it. Resolves the v1 unresolved question. Library users who want fine control can still call `Raw` directly.
- **Zero-found-devices** in first-run wizard exits 69 with a "pass --host manually" hint. Resolves an edge case neither v1 nor v2 spelled out.
- **User-Agent / X-AppName resolved.** v2 had a contradiction (the architecture said `User-Agent: yamaha-cli/<ver>` while the APK section noted apps use `User-Agent: MusicCast`). Resolved: self-identify with `User-Agent: yamaha-cli/<ver>`; only attach `X-AppName: MusicCast` on event-subscribing requests (where the receiver actually parses it for UDP target).
- **Confirmation that YXC is GET-only.** Added as gotcha #11 with citation to `b3.java:1162`. Removes an architectural ambiguity.

## Considered and dropped

- **Concurrency / rate-limiting policy across multiple `yamaha` invocations** — Realized this is a non-issue. Each invocation is a separate process with its own `Client`; the receiver itself serializes HTTP/1.1. The 100 ms intra-process rate-limit is enough.
- **POST + JSON-body endpoint support** — Initial worry, invalidated by APK evidence. YXC is GET-only.
- **Versioning strategy / `--version` output format** — Trivial, can be decided during implementation.
- **Cobra completion installation walkthrough** — Already covered by adding `yamaha completion {bash|zsh|fish}` to the command list. Setup instructions belong in README, not the plan.
- **`yamaha config edit` opening `$EDITOR`** — Nice-to-have, not worth designing now.
- **Volume safety rails** — Still deferred from v1. User can add a `safety.max_volume` config knob later if/when needed.

## Unresolved questions (carry to v4 if useful)

None blocking. Possible future iterations:

1. **`yamaha exec script.txt`** — chained-command runner with one connection reuse and bounded retry per line. Shell-with-`&&` covers 95% of cases; only worth doing if scripted automation grows.
2. **`yamaha watch --filter`** — filter event stream to specific paths (`main.volume`, `netusb.*`). Premature optimization until usage shows it's needed.
3. **MQTT bridge mode** — `yamaha bridge --mqtt <broker>` republishes events to MQTT and accepts commands back. Whole different scope.
4. **First-class log file path** — currently `2> trace.log` is the escape hatch. If debug usage grows, a `--debug-file <path>` flag would be tidier.

## Net result

v3 closes the four most consequential gaps in v2 (volume race, power-on UX, watch format, validation strategy) and folds in five small uncontroversial fixes. The plan is now implementable end-to-end without an implementer needing to make significant judgment calls. All v1 unresolved questions are now resolved.
