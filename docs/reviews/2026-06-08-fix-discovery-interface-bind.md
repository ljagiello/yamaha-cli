# Review: fix/discovery-interface-bind

**Date:** 2026-06-08
**Branch:** fix/discovery-interface-bind (PR #13)
**Verdict:** Needs Work

## Summary

The PR fixes macOS SSDP discovery by binding the search socket to concrete
non-loopback IPv4 interface IPs instead of the wildcard `0.0.0.0:0` that
go-ssdp 0.9.0 defaults to, then fans out one goroutine per interface and dedups
Locations across replies. **The production code is correct** — the concurrency
was verified race-free and the bind-address format matches go-ssdp's
`ResolveUDPAddr` expectation. The verdict is *Needs Work* solely on test
quality: the two new tests stub out the actual fix and would ship green even if
the headline behavior regressed.

## Critical

None. The fix is functionally sound — the manual test plan confirmed the
receiver is found, and the goroutine fan-out is leak-free (buffered channel
sized to the goroutine count means abandoned senders never block).

## Important

### 1. Dedup test can't tell "scanned all interfaces" from "scanned one"
**Location:** `pkg/discover/discover_test.go:231-256`
**Problem:** The stubbed `ssdpSearchFn` ignores its `localAddr` arg and returns
two *identical* Locations unconditionally; the test only asserts `len(locs) ==
1`. Because every response is identical, the map in `defaultSearchLocations`
collapses to 1 regardless of how many goroutines run or whether cross-interface
dedup works. The test passes if the scan only hits `addrs[0]` (the exact bug the
PR exists to fix), only `addrs[1]`, or if cross-interface dedup is deleted
entirely. The name promises "DedupsAcrossBoundInterfaces" but nothing crosses
interfaces.
**Recommendation:** Return a *distinct* Location per `localAddr`, record which
addrs were scanned (mutex-guarded slice — the fan-out calls the stub from
multiple goroutines, so a naive append races under `-race`), then assert both
addresses were scanned AND both distinct Locations appear. Add one interface
that also echoes the other's Location to genuinely exercise dedup.
**Worth doing: Yes** — this is the PR's headline behavior and the test currently
gives false confidence. The fix is cheap and traces directly to this PR's
change.

### 2. The real macOS fix (`searchAddrs` / `ipv4FromAddr`) has 0% coverage
**Location:** `pkg/discover/discover.go:162-205`; stubbed in both tests at
`discover_test.go:204-206` and `239-241`
**Problem:** `searchAddrs` is the function that enumerates interfaces, applies
the `FlagUp && FlagMulticast && !FlagLoopback` filter, extracts IPv4, skips
loopback/unspecified, and falls back to `[]string{""}` when nothing qualifies.
This filtering *is* the substance of "fix discovery on macOS." Both new tests
replace it with a hard-coded slice, so `go tool cover` reports `searchAddrs
0.0%`, `ipv4FromAddr 0.0%`. A bug in the flag mask or a `nil`-vs-`[]string{""}`
fallback would ship with every test green.
**Recommendation:** `ipv4FromAddr` is pure and trivially table-testable (feed it
`*net.IPNet` v4/v6-only, `*net.IPAddr`, loopback, string-fallback addr). For
`searchAddrs`, inject the `net.Interfaces()` call behind a seam and table-test
the flag filter, loopback/unspecified skip, and the empty→`[]string{""}`
fallback — that fallback is the load-bearing macOS path.
**Worth doing: Yes for `ipv4FromAddr` and the `[]string{""}` fallback** — both
are pure, cheap, and central to the fix. The `searchAddrs` interface-injection
seam is more ceremony (it touches real OS state); **defensible to skip** if you
at least cover the fallback branch some other way.

### 3. Error aggregation (`firstErr`) and context cancellation are untested
**Location:** `pkg/discover/discover.go:104-106, 125-127, 135-136, 156-158`
**Problem:** Three new branches have no coverage: the `firstErr` aggregation
(return wrapped error only when *all* goroutines error; swallow errors when some
succeed), the up-front `ctx.Err()` short-circuit, and the two in-loop
cancellation checks. Both new tests use error-free stubs and background
contexts. A regression like "return on first error instead of aggregating" or
"ignore `ctx.Done()` in the collect loop" would not be caught.
**Recommendation:** Add a test where all addrs error (assert wrapped `ssdp
search: %w` surfaces), one where one errors and one succeeds (assert the good
Location returns and the error is swallowed), and a cancelled-context test
(assert `ctx.Err()` without invoking `ssdpSearchFn`).
**Worth doing: Partially** — the all-error and mixed-error cases are cheap and
worth it (they pin the partial-success policy). The ctx-cancellation tests are
lower value since the timeout is a fixed 3s; **fine to skip** unless adding them
is trivial.

### 4. Context cancellation abandons goroutines but doesn't shorten the scan
**Location:** `pkg/discover/discover.go:128-131`
**Problem:** `ssdp.Search` takes no context and blocks for the full `waitSec`.
When the parent ctx is cancelled, `defaultSearchLocations` returns promptly but
the abandoned goroutines keep their UDP sockets open for up to `waitSec` before
exiting. "ctx cancelled" ≠ "scan stopped." Bounded and harmless at the fixed 3s
timeout, but non-obvious to the next reader.
**Recommendation:** No code change — go-ssdp can't be cancelled. A one-line
comment at line 128 ("goroutines run to waitSec even after ctx cancel; channel
slack lets them exit cleanly") saves the next reader the trace.
**Worth doing: Yes (comment only)** — zero-cost, prevents a future reader from
"fixing" a non-bug or worrying about a leak that isn't there.

### 5. Latent data race: `gotAddr` written from a goroutine without sync
**Location:** `pkg/discover/discover_test.go:207-217`
**Problem:** The bind test's stub writes the closure var `gotAddr` from inside
the goroutine fan-out. Safe *today* only because the stub returns one addr (one
goroutine). The moment someone changes that test's `searchAddrsFn` to return ≥2
addrs, it's an unsynchronized concurrent write and a `-race` failure.
**Recommendation:** Capture observed addrs into a mutex-guarded slice instead of
a bare string — the same change finding #1 needs, so do them together.
**Worth doing: Yes, bundled with #1** — near-zero marginal cost once #1's
synchronization is in place; removes a tripwire for the next editor.

## Tests

Verdict from the test lens: **Weak.** The two new tests pass under `-race` and
restore the `ssdpSearchFn` / `searchAddrsFn` global seams cleanly via
`t.Cleanup` (no cross-test leakage — good), and the bind test does pin `waitSec`
and the search-target wiring. But they protect surprisingly little of the actual
change: the cross-interface scan/dedup behavior would ship green if regressed
(#1), and the interface-enumeration logic that *is* the fix has 0% coverage
(#2). Pre-existing tests (`TestSearch_*`, `TestLookupByUDN_*`,
`TestParseDescriptionXML*`) remain solid and exercise real HTTP/parse/dedup
paths — concerns are scoped to the two PR-added tests and the now-uncovered
`searchAddrs` family.

## Needs Decision

1. **Output ordering is now non-deterministic.** Locations are appended in
   channel-arrival order across concurrent goroutines, so multi-device scans can
   return devices in a different order each run. This reaches the interactive
   `--add` picker (`internal/cli/discover.go:73-83`), which prints a numbered
   `[1]..[N]` list — a given receiver can land at a different index on
   successive scans. Irrelevant for the common single-device case and for JSON
   output. **Decide:** sort `devs` (by `Host` or `Name`) in `fetchAndFilter`
   before display, or accept the wrinkle since users pick by name not index.
   *Low priority.*

2. **`ipv4FromAddr` default branch is dead in practice.** `net.Interface.Addrs()`
   only returns `*net.IPNet`, so the `*net.IPAddr` and `ParseCIDR` default cases
   never fire from the actual call site (`discover.go:179`). It's cheap and
   correct defensive code. **Decide:** collapse to the `*net.IPNet` case for
   simplicity, or keep it as future-proofing. Not worth churning on its own —
   resolve it naturally if you add the `ipv4FromAddr` table test from #2.
