# Re-review: feat/ynca-derived-improvements

**Date:** 2026-06-04
**Branch:** feat/ynca-derived-improvements (PR #11, commits through `be24345`)
**Verdict:** Needs Work → **resolved** (the one finding was fixed)

> **Resolution (2026-06-04):** The unbounded-drain finding below was fixed — the
> wake drain now clamps each per-read deadline to the original `wakeTimeout`
> ceiling (`min(deadline, now+wakeDrainIdle)`), so the loop is bounded by
> `wakeTimeout` even under a non-stop sub-`wakeDrainIdle` stream. A regression
> test (`TestWakeOnConnect_BoundedUnderContinuousStream`) drives a peer that
> streams every 10 ms forever and asserts `Send` returns; it hangs (30 s
> timeout) against the unclamped version and passes in ~0.2 s with the cap.
> `-race` and golangci-lint clean. (The "setExtraBass" comment-drift note was a
> reviewer miss — no such drift exists.)

## Summary

This is a re-review after the six findings from the first pass were fixed. Both
reviewers confirm the fixes are correct and the new tests are discriminating
(verified by reverting each implementation and watching the matching test fail).
The `wakeLocked` rewrite is correct *and simpler* than the redial-on-probe
alternative. The only substantive item is a latent unbounded-loop the new drain
introduced: it has no total ceiling, which a test agent empirically turned into a
hang. One-line fix. Everything else is clean and merge-ready.

## Critical

None.

## Important

### 1. `wakeLocked` drain has no total ceiling — a continuous push stream loops forever (holding the conn mutex)
**Location:** `pkg/ynca/client.go` — `wakeLocked` drain loop (the `for { c.conn.Read(buf) … SetReadDeadline(now + wakeDrainIdle) }`)
**Problem:** Each successful `Read` resets the deadline to `now + wakeDrainIdle` (40 ms), which *overwrites* the original `wakeTimeout` (600 ms) ceiling set before the loop. A peer that emits lines with sub-40 ms gaps keeps pushing the deadline forward, so the drain never concludes "idle." Because this runs inside `dialLocked` under `c.mu`, it blocks the whole connection for as long as the stream continues.
**Evidence:** A reviewer wrote a probe where the peer streams a line every 10 ms after the wake ping — `Send` did **not** return within 2.5 s; the drain ran unbounded until the stream was stopped. The original 600 ms `wakeTimeout` does not cap it because it's discarded on the first read.
**Mitigating context:** Real YNCA receivers are *quiescent* on a fresh connection (verified against the `ynca` reference library: a device emits report lines only in response to a command; unsolicited pushes are sparse single lines from physical knob/remote changes, not a sub-40 ms stream). So this won't trigger on real hardware — it's defense-in-depth against a pathological/chatty/buggy device.
**Worth doing: Yes** — it's a one-line cap (`if time.Now().After(deadline) { break }`, or clamp the per-read deadline to `min(deadline, now+wakeDrainIdle)`), and an unbounded loop holding a lock is exactly the kind of footgun worth closing cheaply. Cost of fixing: ~1 line + an optional sub-`wakeDrainIdle`-stream test. Cost of not fixing: a misbehaving device hangs the connection for the process lifetime.

## Tests

**Solid.** Each of the six new tests was shown to be discriminating (fails against a reverted implementation), drives real client code over real TCP/HTTP fakes, and asserts the client's behavior (wire bytes / decoded output), not the fake's:

- `TestWakeOnConnect_DrainsInterleavedPush` — reverting to the old first-newline drain fails it with the exact stranded-line symptom (`reply = "@SYS:MODELNAME=RX-V583", want @MAIN:PWR=On`). Genuine regression test for prior #2.
- `TestGetStatus_FiltersOtherSubunit` / `_UnsupportedSubunit` — removing the `!EqualFold(su, subunit)` filter flips the result; the `@UNDEFINED` case pins the empty-Status contract (asserts `len(st.Raw)==0`).
- `TestYncaSubcmd_PowerToggle` / `_Mute` / `_VolumeUpDownAndSound` / `_UndefinedExit70` — inverting the toggle logic fails both directions; all assert exact PUT bytes over a real fake.
- `TestZoneSwitch_LenientWhenFeaturesUnavailable` — confirmed it truly forces `loadFeatures` to error (DeviceID short-circuits getDeviceInfo; empty cache forces a fetch; `getFeatures` `response_code:2` → `yxc.Error`, not retried) so the lenient `ferr != nil` branch fires.
- `TestSendMulti_NoFenceReturnsNoReply` — `ErrNoReply` + bounded return; its job is to catch an *infinite hang*, which it does.

Determinism: all `wakeTimeout` mutations restore via defer; wake tests are correctly non-parallel; globals (`fl`, `lookupByUDNFn`, timeouts) all restored. Survives 30× `-race` under `GOMAXPROCS=1`.

Minor test notes (not blocking):
- **Continuous-stream drain untested** — the Important finding above has no test; add one alongside the fix.
- **`n == 0` busy-spin guard untested** — correct but hard to trigger without a custom `net.Conn`; acceptable gap.
- **`TestWakeOnConnect_DrainsInterleavedPush` 15 ms/40 ms coupling** — the server's 15 ms inter-write sleep must stay under the 40 ms `wakeDrainIdle`; a 2.6× margin that held over 30× race runs. Benign failure direction (strands → loud failure, not a false pass). If the slow Windows runner recurs, widen the ratio rather than couple to the production constant.
- **Comment drift** — `TestZoneSwitch_LenientWhenFeaturesUnavailable`'s doc-comment says "setExtraBass" but it uses `zoneSwitches[0]` (pure-direct). Harmless; fix the comment.

## Needs Decision

1. **`WithWakeOnConnect` design (carried over).** Still the most expensive feature (a wake ping per dial; `runYNCASet` settles-then-mutates on fresh connections). The two correctness edges that made it risky are now fixed (and the third, above, is a one-liner), so it's a design preference, not a defect. The reviewers' alternative remains redial-on-probe-failure. Not a blocker.
2. **`Send` conn-reset retry can double-execute a non-idempotent write (`VOL=Up`)** (carried over, pre-existing). Document the at-most-twice semantics, or don't auto-retry non-idempotent writes.
