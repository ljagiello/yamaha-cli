# Review: feat/ynca-parity

**Date:** 2026-06-04
**Branch:** feat/ynca-parity (PR [#12](https://github.com/ljagiello/yamaha-cli/pull/12))
**Verdict:** Needs Work → **Resolved** (all 8 findings addressed in a follow-up commit)

## Resolution (follow-up)

All eight findings below were fixed:

1. **watch supervisor untested** → `internal/cli/ynca_watch_test.go`: `TestYncaWatch_ReconnectsThenCancels` (drop → reconnect event → re-dial → clean cancel returns nil). Stress-tested 8× under `-race`.
2. **`--source` uppercase passthrough** → added `ynca.IsSourceSubunit`; `resolveYncaSource` now rejects non-source tokens (`TUNER`, `HDMI1`) with a usage error. Covered by `TestIsSourceSubunit` + `TestYncaSubcmd_TransportRejectsNonSource` (asserts exit 2).
3. **misleading `Muted()` comment** → rewritten to state the deliberate behavior change accurately (unknown → not muted) instead of claiming false parity.
4. **diff/info/now-playing logic untested** → `internal/cli/ynca_offline_test.go`: `TestParseTranscript`, `TestSplitSubunitFunc`, `TestCollectYncaInfo_PresenceMatrix` (present/absent/restricted matrix), `TestCollectYncaInfo_TransportErrorAborts`, `TestBuildYNCANowPlayingPayload`.
5. **FM-only replay fixture** → added `amTranscript` + `fmRdsTranscript`; `TestReplay_AMTuner`, `TestReplay_RDS`, `TestReplay_TransportVerbs` now exercise the AM `Atoi` path, the `BandAM` status arm, the RDS drain, and all five transport verbs.
6. **watch no host re-settle on DHCP shift** → supervisor now counts consecutive `*DialError`s and re-settles via `yncaWatchSettle` (SSDP-by-UDN) after `yncaWatchRediscoverAfter`. Covered by `TestYncaWatch_RediscoversAfterDialFailures`.
7. **dump catalog untested** → `TestDefaultDumpCommands` asserts non-empty, every entry is a `=?` GET, breadth sentinels present, and no put-only function leaks in (read-only invariant).
8. **`formatStepped` rounding deviation** → documented the intentional `math.Round` (half-away-from-zero) vs Python banker's-rounding divergence at exact midpoints in `codec.go`.

Verification after fixes: `gofmt` clean · `go build`/`go vet` · `go test ./... -race` (incl. 8× watch stress) · `golangci-lint` 0 issues.

---

*Original review below, unchanged.*

**Verdict (as reviewed):** Needs Work

## Summary

Brings the YNCA backend to near-parity with YXC — 27 improvements (Session/reader, typed enums, ~17 new commands, dump/diff/replay tooling) across 35 files. The protocol and concurrency *core* is genuinely strong: the `Session` reader/writer/cancel triad, the deadline watchdogs, and the "@UNDEFINED keeps the connection open" invariant were all traced and confirmed correct under `-race`, and the core tests pin real behavior rather than ceremony. The gaps are breadth, not depth: the **headline feature (`ynca watch`) ships with zero tests** and a known DHCP-resilience limitation, plus two small correctness/UX nits and a couple of misleading comments. Nothing here is a blocker or a core bug — these are "finish the edges" items before merge.

Two parallel agents reviewed (architecture/correctness → **Clean**; test quality → **Solid**). Both verdicts are fair on their own axis; the combined verdict is **Needs Work** because the most important new capability is the least tested.

## Critical

None. No data races, no goroutine leaks, no resource leaks, no security issues, no wrong-action bugs found.

## Important

### 1. `ynca watch` supervisor/reconnect is entirely untested
**Location:** `internal/cli/ynca_watch.go:49-114`
**Problem:** The whole point of the new `Session` is `watch`, and its CLI supervisor — reconnect/backoff loop, clean-cancel-on-SIGINT (`ctx.Err()` → return nil), backoff doubling/cap, and the NDJSON-vs-table + `@UNDEFINED/@RESTRICTED` control-line rendering — has no test. Tell: `yncaWatchBackoffMin/Max` are exported as vars *specifically* "so integration tests can shrink them," and those tests were never written. (`Session.Run` itself is well covered in `session_test.go`; the wrapper is not.)
**Recommendation:** Use the existing `pushServer`-style fake to drop the connection once and assert (a) a `reconnect` line is emitted, (b) the loop re-dials, (c) cancel returns nil.
**Worth doing: Yes** — it's the headline feature and the harness already exists; cost of skipping is a silent regression in the one path users will run unattended for hours.

### 2. `--source` uppercase passthrough breaks the command's own "clean usage error" promise
**Location:** `internal/cli/ynca_nowplaying.go:138-142` (`resolveYncaSource`)
**Problem:** Any all-uppercase `--source` token is passed straight through as a subunit id. `--source TUNER` → `@TUNER:METAINFO=?` (the tuner subunit is `TUN`), `--source HDMI1`/`MAIN` likewise → a device-side `@UNDEFINED`, exactly the outcome the doc comment promises to avoid for non-streaming inputs. The `up == sf` heuristic can't tell a real source id (`SPOTIFY`) from a non-source uppercase token.
**Recommendation:** Validate the uppercased token against the known source-subunit set (the codomain of `SubunitForInput`) before passing it through; else return the usage error.
**Worth doing: Yes** — small, self-contained, and it makes the command honor its own contract. Cost of not fixing: a worse error message (never a wrong action), so low urgency.

### 3. `GetMute().Muted()` changed behavior for unrecognized values, and the comment claims false parity
**Location:** `pkg/ynca/enums.go:211-218`
**Problem:** The removed `parseMute` returned `!EqualFold(v,"off")` → an *unknown* MUTE value counted as **muted (true)**. The new `Muted()` returns **false** for `MuteUnknown`. The doc comment asserts this "matches the original boolean parseMute's intent" — it does not. Real-hardware impact is ~nil (firmware only emits the four known values), and the toggle path (`internal/cli/ynca.go`) would send `On` instead of `Off` only on a genuinely unparseable value.
**Recommendation:** Keep the new (arguably better) behavior but fix the comment to stop mischaracterizing the prior one — or, for strict parity, make `Muted()` return true for `MuteUnknown`.
**Worth doing: Yes (comment only)** — a one-line comment fix. Changing the behavior is optional; the new semantics are defensible.

### 4. Offline + tri-state command logic untested (`diff`, `info`, `now-playing` payload)
**Location:** `internal/cli/ynca_diff.go:71` (`parseTranscript`), `internal/cli/ynca_info.go:145` (`collectYncaInfo`/`subunitPresent`), `internal/cli/ynca_nowplaying.go:158` (`buildYNCANowPlayingPayload`)
**Problem:** Branchy logic that regresses silently: `parseTranscript` does fiddly parsing (trailing `",` trim, comment skip, `colon > eq` reject, upper-case) and is the actionable output of `dump` — yet it's pure/offline and trivially testable. `subunitPresent` maps `@UNDEFINED`→absent / `@RESTRICTED`→present / transport→abort, exactly the kind of tri-state a test should pin. `buildYNCANowPlayingPayload` drops empty fields and gates `playback` on `Known()`.
**Recommendation:** Direct table test for `parseTranscript`/`splitSubunitFunc` (no device). Cover the present/absent/restricted matrix of `collectYncaInfo` with the existing `startFakeYNCA` harness.
**Worth doing: Yes (parseTranscript + collectYncaInfo)** — high value, low cost, pure functions / existing harness. The payload-builder test is lower priority.

### 5. Replay fixture is FM-only: AM tuner + RDS branches never execute
**Location:** `pkg/ynca/tuner.go:137-143` (AM branch of `GetTunerStatus`), `:83` (`GetAMFreq`, distinct `Atoi` parse path), `:154` (`GetRDSInfo`); fixture `pkg/ynca/replay_test.go` `rxv583Transcript`
**Problem:** The transcript has `@TUN:BAND=FM` only and no RDS fields, so the `BandAM`/`FreqKHz` path and the entire RDS fan-out drain are uncovered. `GetAMFreq` uses `strconv.Atoi` where FM uses `ParseFloat` — a genuinely different path at zero coverage.
**Recommendation:** Add an AM transcript variant and an RDS group to the replay fixture (or a second fixture). The replay harness already exists, so this is cheap.
**Worth doing: Yes** — low effort given the harness, and it closes a real parse-path gap.

### 6. `ynca watch` never re-settles the host after the initial probe (DHCP shift mid-watch)
**Location:** `internal/cli/ynca_watch.go:62, 92-113`
**Problem:** `yncaSettleHost` runs once before the supervisor loop; the loop then reuses the captured `s.device.Host` forever. If the receiver DHCP-shifts to a new IP during a long watch, `Session.Run` re-dials the stale IP indefinitely (backoff capped at 30s) and never rediscovers — whereas every one-shot command recovers via `runYNCAWithRediscover`.
**Recommendation:** Acceptable for v1 if documented. For resilience, re-run `yncaSettleHost` after N consecutive `DialError`s inside the supervisor.
**Worth doing: Decision (see below)** — it's a new gap but a design limitation, not a regression; the fix has non-trivial surface.

### 7. `defaultDumpCommands` built-in catalog untested
**Location:** `internal/cli/ynca_dump.go:177`; the one dump test (`ynca_commands_test.go:217`) deliberately uses a `--commands` file to avoid the catalog.
**Problem:** The catalog assembly (`FunctionsForScope` × subunits × fan-out groups, `CanGet()` filter, source list) has no assertion. A descriptor-registry change (a function marked non-gettable, a dropped subunit) ships green.
**Recommendation:** Pure unit test on `defaultDumpCommands()`: non-empty, every entry ends in `=?`, includes known sentinels (`@TUN:RDSINFO=?`, `@SPOTIFY:METAINFO=?`), every line parses.
**Worth doing: Yes** — trivial pure-function test guarding a data-driven surface.

### 8. `formatStepped` rounding deviates from the Python reference at exact midpoints
**Location:** `pkg/ynca/codec.go:30`
**Problem:** Go's `math.Round` is half-away-from-zero; ynca's Python helper uses banker's rounding. They differ only at exact half-step midpoints (e.g. AM 1005 kHz → Go `1010` vs Python `1000`; FM 90.5 on the 0.2 grid → `90.60`). Not a bug — the device clamps to its own grid — but undocumented.
**Recommendation:** One-line comment in `codec.go` noting the intentional deviation, to pre-empt a future "why doesn't this match ynca?" investigation.
**Worth doing: Yes (comment only)** — already flagged in the PR description; a code comment makes it discoverable at the call site.

## Tests

**Verdict from the test lens: Solid.** The core is well-protected; the gap is CLI-command *breadth* (findings 1, 4, 5, 7 above), not protocol/session depth.

Genuinely good tests worth preserving as-is:
- `control_test.go` `TestWakeOnConnect_DrainsInterleavedPush` / `_BoundedUnderContinuousStream` — model the real race (push inside the wake window; unbounded push starving the drain) and assert *termination*. Would catch a real hang.
- `session_test.go` `TestSessionStreamsReportsAndSuppressesKeepAliveEcho` / `TestSessionKeepAlivePings` — pin the keep-alive-echo-suppression invariant and prove the heartbeat actually hits the wire. **Not flaky** despite the 20ms ticker (channel + 2s timeout, no `time.Sleep` race); 15×+ `-race` repeats stayed green.
- `control_test.go` `TestGetStatus_FiltersOtherSubunit` — interleaves a foreign-subunit line into a BASIC fan-out and asserts no leak; deleting the `EqualFold(su, subunit)` guard fails it.
- `replay_test.go` tri-state mute — asserts `Att -20 dB` survives as `MuteAtt20` and doesn't flatten.

**Fake fidelity:** the new `newReplayYNCA` is faithful, not papering over bugs — it models fan-out group expansion, the `@SYS:VERSION=?` fence echo, and `@UNDEFINED` for absent subunits, so the tests exercise real `Send`/`SendMulti`/`parseLine`/`classifyReply` + the enum/codec parsers (confirmed no data race in the SET-then-GET shared-store path). Wire assertions are exact-string, not loose substring. One benign modeling shortcut: a group GET on an empty subunit returns a single `@UNDEFINED` where a real device might return EOF — both surface as errors, so test intent holds.

## Needs Decision

1. **`ynca watch` DHCP resilience (finding 6).** Ship v1 as-is with a one-line note in the `watch` help/docs that a long-running watch won't follow a DHCP IP change, or invest in re-settling the host after N consecutive dial failures in the supervisor? The one-shot paths already recover; only the long-lived watch doesn't. Recommendation: document for now, defer the re-settle.
2. **`Muted()` on unknown values (finding 3).** Keep the new behavior (unknown → not muted) and just fix the comment, or restore strict parity (unknown → muted)? Recommendation: keep new behavior, fix comment — a device that emits an unparseable MUTE value is already off-spec.

---

*Review only — no code was modified. Verified locally: `go build`, `go vet`, `go test ./... -race`, `golangci-lint` all green on the reviewed tree.*
