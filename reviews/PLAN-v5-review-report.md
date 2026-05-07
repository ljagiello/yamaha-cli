# PLAN v5 → v6 Design Review Report

**Date:** 2026-05-07
**Reviewer:** Claude (via /improving-plans)
**Input:** `PLAN.v5.md`
**Output:** `PLAN.v6.md`
**Prior reviews:** `reviews/PLAN-v1-review-report.md` through `reviews/PLAN-v4-review-report.md`

## TL;DR — design has converged

Six review passes in. The v4 review explicitly flagged v5 as "the natural stopping point for design-space iteration; further passes will be diminishing returns." That assessment held up: I went looking for genuine new gaps in v5 and found exactly one minor inconsistency, plus a number of items that on closer inspection are implementation details rather than design choices.

**v6 is essentially v5 with one bug fix.** No AskUserQuestion call was warranted because the fix has no real tradeoff.

## Context gathered

- Re-read `PLAN.v5.md` end-to-end with the explicit goal of finding gaps.
- Re-read all four prior review reports.
- Searched v5 for inconsistencies between sections — particularly between the "Configuration" / "Resolution order" section and the README-docs requirement in "Definition of done".
- No additional codebase or artifact exploration needed.

## Single fix surfaced

### `YAMAHA_HOST` env var was missing from the resolution order

**Problem in v5:**
- v5 "Definition of done" line 513 requires the README to document the env var `YAMAHA_HOST`.
- v5 "Resolution order" (lines 105-110) lists `--host`, `--device`, `YAMAHA_DEVICE`, `default_device`, single-device shortcut, wizard.
- `YAMAHA_HOST` appears in docs requirements but is never wired into the lookup chain.

This was a minor documentation/implementation drift not caught by any prior review. It would have surfaced during README writing or implementation as "wait, what does `YAMAHA_HOST` actually do?"

**Fix in v6 (no question, single obvious answer):**
Resolution order is now seven steps, with env vars adjacent to their flag counterparts (the standard viper / 12-factor convention):

1. `--host <ip>` flag
2. `YAMAHA_HOST` env var (same semantics — anonymous, no UDN, no DHCP rediscovery)
3. `--device <name>` flag
4. `YAMAHA_DEVICE` env var
5. `default_device` from config
6. Single-device shortcut
7. First-run wizard

DHCP-resilience section also updated: the "skipped when" list now includes `YAMAHA_HOST` alongside `--host` (both are anonymous, no UDN to match against).

Phase 1 task 3 explicitly enumerates the env var bindings: `YAMAHA_HOST` ↔ `--host`, `YAMAHA_DEVICE` ↔ `--device`, `YAMAHA_DEBUG` ↔ `--debug`, `NO_COLOR` ↔ `--no-color`.

Acceptance criteria added: `YAMAHA_HOST=192.168.1.116 yamaha status` works with no config file present.

## Items considered and explicitly NOT raised

To document my reasoning so future reviewers don't retread:

- **Logging schema for `--debug`** — `→`/`←` prefix is enough direction; exact format is implementation detail.
- **Configuration migration** — pre-v5 configs without UDN are already handled (graceful fallback, exit 69 with hint). No actual data migration needed; `yamaha discover --add` overwrites cleanly.
- **Goroutine cleanup in `watch`** — context-cancellation already specified; goroutine count and channel patterns are implementation detail.
- **Time zone in `watch ts` field** — UTC ISO 8601 is the only sensible default; not worth a question.
- **`yamaha config show` output format** — implementation detail, not a design choice.
- **Error wrapping convention** — Go's `fmt.Errorf("...: %w", err)` is the universal idiom; mentioning it in the plan is gold-plating.
- **Minimum Go version** — Implementation detail; will be set from go.mod when scaffolding (likely 1.22+).
- **CI configuration** — v1.0 polish task, not Phase 1 design.
- **Config file permissions** — No secrets stored, so 0644 is fine; default umask handles it.
- **`yamaha get <field>`** — would be a parallel command surface for marginal ergonomic gain (`jq` already covers it). Gold-plating.
- **`watch --filter <jq-expr>`** — premature server-side filtering. Already in v3's deferred list.
- **MQTT bridge mode** — different scope, probably a sibling tool. Already in v3/v4 deferred lists.
- **Goreleaser / homebrew tap** — v1.0 polish.
- **Tab completion for arguments in Phase 1** — Phase 1 has `yamaha completion {bash|zsh|fish}` (static script). Phase 2 has dynamic completion logic. Cobra's protocol handles the dispatch; the static script in Phase 1 still works for command names + global flags.

## Unresolved questions

None. The v3, v4, and v5 reports all have small "future" lists; those remain accurate. None block implementation.

## Recommendation

**Stop iterating on the design.** The plan now has:
- ~30 traceable, internally consistent design decisions
- Explicit Phase 1 acceptance criteria covering happy path, every failure mode, every signal, every cache scenario, and DHCP drift
- Authoritative protocol references with file paths
- A clean escape hatch (`raw`) covering the 90% of "future" endpoints without code

The next valuable activity is implementation. Reality during coding will surface anything the plan genuinely missed; further design passes from a clean conversation will only paint over the same surface.

If a future review *is* run, the reviewer should weight "do not gold plate" heavily and explicitly state when nothing material is found rather than manufacturing concerns.
