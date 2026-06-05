# Review: feat/ynca-derived-improvements

**Date:** 2026-06-04
**Branch:** feat/ynca-derived-improvements (PR #11)
**Verdict:** Needs Work → **all findings resolved** (follow-up commits)

> **Resolution (2026-06-04):** All six Important findings below were addressed
> on the branch. `wakeLocked` was hardened to break on no-progress (#1) and to
> drain the full wake reply past any interleaved push (#2) — with a regression
> test that fails against the old first-newline drain. Tests were added for the
> `SendMulti` no-fence/timeout path (#3), `GetStatus` `@UNDEFINED` + cross-subunit
> filter (#4), the `power/mute toggle` + `volume up/down` + `mute on/off` + `sound`
> subcommands and `@UNDEFINED`→70 (#5), and the DSP lenient-features path (#6).
> Full suite passes under `-race`; golangci-lint clean. The Needs-Decision items
> (wake design, non-idempotent-write retry) are left as noted for the author.

## Summary

A large (+2.6k/−264, 42 files) but cohesive PR that adopts patterns from the Python `ynca` library: a typed YNCA control backend, typed YXC received-value enums, feature-gated DSP commands, zone3/4 support, dB-scaling consolidation, and new exit-code mappings. The architecture is sound and the tests are unusually behavior-protecting (real `httptest`/TCP fakes, wire-level assertions, no tautologies). The one area that needs attention before merge is the new `WithWakeOnConnect` raw-read drain, which has a real (if narrow) corruption vector and a missing loop guard; everything else is either solid or a worth-adding test.

## Critical

None. No security issues, no data-loss paths, no crashes found. The closest thing to a correctness bug (wake-ping reply swallowing) is conditional on an unsolicited push interleaving in a ~600 ms window and mostly degrades into a surfaced error rather than silent-wrong-data — tracked under Important.

## Important

### 1. `wakeLocked` drain loop has no guard for `Read() == (0, nil)`
**Location:** `pkg/ynca/client.go` (`wakeLocked`, the `for { c.conn.Read(buf) }` loop)
**Problem:** The loop breaks only on `n>0 && '\n'` or `rerr != nil`. A `Read` that returns `(0, nil)` (legal for `io.Reader`, emitted by some net stacks) falls through both and re-loops, busy-spinning until the read deadline fires. Bounded by `wakeTimeout` (≤600 ms) so it can't hang forever, but it can burn a core for that window.
**Worth doing: Yes** — one-line guard, removes a hot-spin footgun. Cost of not fixing: a rare CPU spike on flaky sockets; cost of fixing: trivial.
**Recommendation:** `break` on `rerr != nil || n == 0` after the newline check, or simplify to a single `Read` (one read already absorbs the dropped-first-command case the feature targets).

### 2. `wakeLocked` can hand the caller a stale reply when an unsolicited report interleaves
**Location:** `pkg/ynca/client.go` (`wakeLocked`)
**Problem:** The raw drain stops at the **first** `\n`. YNCA receivers send unsolicited report lines (panel volume/input changes, standby transitions) — the `ynca` library has an entire async reader for exactly this. If such a line lands in the wake window, the raw read consumes it as "the wake reply" and leaves the real `@SYS:MODELNAME=` echo in the kernel buffer, which the scanner then returns as the reply to the caller's first command (Probe → wrong version; a typed `get` → wrong function line → `ParsePower`/parse error). The code comment asserts "it can never corrupt the next command," which holds only under an unstated assumption that the connection is quiescent during connect.
**Worth doing: Yes (as a decision — see Needs Decision #1)** — the trade is real-but-narrow corruption + an extra round-trip per dial, versus a simpler design that sidesteps the raw-read entirely. Cost of not fixing: occasional wrong/erroring first command on a busy receiver; cost of fixing: a small redesign or an honest doc-comment.
**Recommendation:** Either (a) document the quiescent-connection assumption explicitly and accept it, or (b) replace the unconditional wake with "send the Probe; on EOF/no-reply, redial-and-retry once" — same standby-recovery benefit, no raw-read corruption surface, no extra round-trip on the happy path. Agent recommends (b).

### 3. `SendMulti` no-fence / timeout path is untested
**Location:** test gap for `pkg/ynca/client.go` `sendMultiOnceLocked` error tail; tests in `pkg/ynca/client_test.go`
**Problem:** Both `SendMulti` tests always return the `@SYS:VERSION=…` fence. The error tail (post-loop `ctx.Err()`, `scanner.Err()`→`isTimeout`→`ErrNoReply`, final `io.EOF`) is unexercised. This is load-bearing: if the drain ever failed to return on a missing fence, `ynca status` would hang and nothing would catch it. The code looks correct (mirrors the tested `Send` path), so this is a test gap, not a confirmed bug.
**Worth doing: Yes** — cheap, guards a hang regression on the headline new command.
**Recommendation:** Add a fake that answers the command but returns `""` for `@SYS:VERSION=?`, with a short `WithTimeout`, asserting `errors.Is(err, ErrNoReply)` and prompt return.

### 4. `GetStatus` `@UNDEFINED` and cross-subunit-filter branches untested
**Location:** `pkg/ynca/control.go` `GetStatus`; test `pkg/ynca/control_test.go`
**Problem:** `TestGetStatus` covers only the happy MAIN fan-out. Two real branches are untested: (a) an unsupported subunit (`BASIC=?`→`@UNDEFINED`) — note this currently yields an *empty* Status (the `@UNDEFINED` line fails `parseLine` and is filtered), worth pinning down; (b) the `!EqualFold(su, subunit)` filter that drops a stray `@ZONE2:…` line while querying MAIN — if deleted, another subunit's state would corrupt the decoded Status and nothing would fail.
**Worth doing: Yes** — the cross-subunit filter is silent-corruption prevention; it deserves a test.
**Recommendation:** Add (a) a BASIC→`@UNDEFINED` case asserting the Status/err contract, and (b) a fan-out interleaving a `@ZONE2:PWR=On` line, asserting it doesn't leak into the MAIN Status.

### 5. `power toggle` / `mute toggle` (and `volume up/down`, `mute on/off`, `sound`) untested at the CLI level
**Location:** `internal/cli/ynca.go` typed subcommands; tests `internal/cli/ynca_test.go`
**Problem:** The GET-then-invert-then-SET glue for `toggle` lives in the CLI handlers and is the most interesting logic, yet only `power on` (unconditional set), `volume -- -30.3` (absolute), `status`, `@RESTRICTED`, and `repl` are tested. No `toggle`, no `mute`, no `volume up/down`, no `sound`. `pkg/ynca` covers `GetPower`/`SetPower` individually, but the toggle *composition* is CLI-only.
**Worth doing: Yes** — the invert logic is exactly where a bug would live.
**Recommendation:** Table-style subcommand tests: `power toggle` from `@MAIN:PWR=On` sends `@MAIN:PWR=Standby` (and the reverse); `mute toggle`; `volume up`→`@MAIN:VOL=Up`.

### 6. DSP "lenient" path (features-fetch error → defer to device) untested
**Location:** `internal/cli/dsp.go` (the `ferr == nil && … && !ZoneHasFunc` gate); tests `internal/cli/dsp_test.go`
**Problem:** Supported (gate passes) and unsupported (gate rejects) are both the `ferr == nil` branch. The deliberate policy — when `loadFeatures` errors, do **not** block, proceed to the device — has no test. A future "tighten the gate to fail-closed" change would break sparse-firmware/transient-failure devices silently.
**Worth doing: Yes** — pins an intentional, regression-prone policy decision.
**Recommendation:** A test where features loading fails but the `setExtraBass` endpoint is reachable, asserting the set still reaches the wire.

## Tests

**Overall: Solid.** The new tests drive real client code over real fakes (a TCP listener, `httptest` servers) rather than stubbing the unit under test; they assert wire-level effects (exact URLs/bytes, decoded output) and would fail if the implementation were deleted. Spot-checks found no tautological or structure-only tests. Global mutable state (`wakeTimeout`, `yncaProbeTimeout/SendTimeout`, the `fl` feature-loader, `lookupByUDNFn`) is correctly saved/restored via `t.Cleanup`, and the wake tests correctly avoid `t.Parallel()` while mutating `wakeTimeout`.

Well-covered (verified): `IsTransport`/`DialError` (incl. the dial-timeout-is-transport vs per-command-deadline-is-not distinction), `ErrorExitCode` YNCA-transport→69, `canonicalZone`/`validZone` 4-zone accept+reject, `VolumeDBScale` fallback + full round-trip inverse property, enum forward-compat unmarshal, and the `zone3→zone9` test update (correct — zone3 is now valid, so the rejection assertion moved to a still-invalid token).

Coverage gaps beyond the Important items above (all Minor):
- **`SendMulti` connection-reset retry** — the `isConnReset`→redial path is a literal copy of the tested `Send` path but isn't independently driven.
- **`@UNDEFINED`→exit 70 at the subcommand level** — only `@RESTRICTED`→75 is tested end-to-end through a typed subcommand; the symmetric 70 case is covered only in isolation (`TestFriendlyYNCAError`, `ErrorExitCode` table). Cheap to mirror.
- **Wake split-read / late-reply** — `TestWakeOnConnect` sends the whole reply in one write, so the multi-`Read` continuation never runs; the late-reply case (reply arrives after `wakeTimeout`) — the exact scenario the raw-read design exists to protect — is unexercised. Add a fake that writes the reply in two chunks and one that delays it past the deadline.

Determinism note: `TestSend_CtxCancellation` (pre-existing, not this PR) asserts a wall-clock upper bound with a ~130 ms margin — the single most flake-prone shape on a loaded CI runner. No action needed; flagged for awareness.

## Needs Decision

1. **Keep `WithWakeOnConnect` as-is, document it, or replace it with redial-on-probe-failure?** It's the highest-risk, highest-cost addition: a raw-read drain with the two edge cases above, plus a wake ping on *every* dial (≈4 round trips for one `ynca power on`, since settle-then-mutate each dial fresh). The alternative — Probe, and on EOF/no-reply redial-and-retry once — delivers the same standby-recovery with no raw-read corruption surface and no happy-path overhead. This is the one design call worth making explicitly before merge.

2. **`Send`'s conn-reset retry can double-execute a non-idempotent write (`VOL=Up`).** Pre-existing (the raw `ynca @MAIN:VOL=Up` passthrough already had this), but this PR adds typed `volume up/down` that leans on it. The reset-retry only fires when the first `Scan` fails with a reset, which for a write that already hit the wire is a real double-increment. Demoted because it's not this PR's mess — but worth a decision: document the at-most-twice semantics, or don't auto-retry writes that aren't known-idempotent.

_Not worth acting on (noted for completeness):_ the `Band`/`BandDAB` enum value has no CLI surface yet, and `Known()`/`Parse*` across the six enums are largely unused by current callers — both are cheap, harmless forward-compat and shouldn't be trimmed reactively.
