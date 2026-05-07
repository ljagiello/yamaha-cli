# PLAN v4 → v5 Design Review Report

**Date:** 2026-05-07
**Reviewer:** Claude (via /improving-plans)
**Input:** `PLAN.v4.md`
**Output:** `PLAN.v5.md`
**Prior reviews:** `reviews/PLAN-v1-review-report.md`, `reviews/PLAN-v2-review-report.md`, `reviews/PLAN-v3-review-report.md`

## Context gathered

- Re-read `PLAN.v4.md`. By this point the plan is comprehensive (~290 lines, 25+ resolved decisions, full Phase 1 acceptance suite).
- Re-read all three prior review reports to avoid retreading resolved items.
- No additional codebase or artifact exploration needed — the local artifacts (`/tmp/yxc-mc.txt`, decompiled APK, live `getFeatures` capture) were exhausted in prior reviews.

## Stance going in

After three reviews, the plan was already implementable. The skill explicitly says "do not gold plate". I deliberately limited myself to two genuinely-new gaps that prior reviews had missed, plus three uncontroversial micro-fixes. v5 is a small delta, not a rewrite.

## Ideas surfaced (2 of a possible 5)

### 1. DHCP resilience — **Decided: store UDN, auto-rediscover on unreachable**

**Problem in v4:** Yamaha receivers on home networks routinely change IP via DHCP renewal or router reboot. v4 stored only the literal IP in config. After a DHCP shuffle, every command would fail with exit 69 until the user manually re-ran `yamaha discover --add`. Real reliability gap; not caught in any prior review.

**Options presented:**
- A. Store UDN, auto-rediscover on unreachable (recommended).
- B. Manual `yamaha refresh` subcommand.
- C. Status quo (user re-runs discover manually).

**User picked A.** Captured in v5:
- New "DHCP resilience" section.
- Config schema gains `udn` per device (free from SSDP, no extra protocol cost).
- `pkg/discover.LookupByUDN(ctx, udn, timeout)` added as a public method.
- Trigger: any HTTP transport error after the in-Client retry, on a config-resolved (not `--host`) call.
- Flow: 3 s SSDP scan → match by UDN → atomic config update → retry once.
- Skipped for `--host` calls (anonymous) and pre-v5 configs without UDN (graceful fallback to exit 69 with hint).
- Acceptance test: edit config IP to a wrong-but-routable address; `yamaha status` succeeds after one transparent rediscover. `--debug` shows `→ rediscover` then the original call.
- Gotcha #14 added: "DHCP resilience requires a saved UDN."

### 2. SIGINT cancellation — **Decided: cancel cleanly via root context**

**Problem in v4:** Plan used `ctx, ...` in method signatures but never specified how SIGINT propagated. The 10 s power-on poll, the long-running `yamaha watch`, and in-flight HTTP requests all needed an explicit cancellation policy. v3's review flagged "graceful shutdown" for watch (exit 0) but didn't connect it to a single mechanism for the rest of the CLI.

**Options presented:**
- A. Cancel cleanly via root context (recommended).
- B. Hard exit on first SIGINT.
- C. Two-step: graceful then hard.

**User picked A.** Captured in v5:
- New "Signal handling" section.
- `cmd/yamaha/main.go` builds the root context via `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`.
- All HTTP requests use `http.NewRequestWithContext(ctx, …)`.
- Power-on poll selects on `ctx.Done()` between 200 ms ticks.
- `watch` UDP receive + renewal goroutine consume `ctx.Done()`; SIGINT sends a final `{"event":"shutdown"}` NDJSON line and exits 0.
- SSDP search uses context-bounded deadline.
- Exit code 130 added to the table for SIGINT during non-watch commands.
- Acceptance test: Ctrl-C during `yamaha power on` exits 130 within 200 ms; receiver still acts on the original power-on request.
- HTTP retry policy updated: do **not** retry on context-cancelled errors (don't retry past the user's "stop").
- Gotcha #15 added: "SIGINT does not roll back receiver-side actions."

## Other additions (no question needed)

Three small fixes folded into v5:

- **`pkg/yxc.Client` documented as safe for concurrent use** — matches `net/http.Client` convention. Said explicitly in the package docstring, not just implied. Removes ambiguity for library consumers (the CLI itself only makes one call per process so doesn't care, but library users do).
- **`NO_COLOR` honored in TTY table mode** — universal convention per [no-color.org](https://no-color.org). `--no-color` flag also accepted.
- **Exit code 130 added to the table** for SIGINT cancellation (consequence of the SIGINT decision).

## Considered and dropped

- **`yamaha get <field>`** as a selective alternative to `yamaha status | jq` — dropped. `jq` is universally available; adding a parallel command surface for marginal ergonomic gain is gold-plating. Scripts pipe through `jq`.
- **Color/styling palette in TTY table mode** — implementation detail. Will pick a sensible default during coding (success green, error red, dim metadata). Not worth a design decision.
- **Concurrent YXC requests / rate-limit mechanism** — covered the contract (mutex-guarded last-call timestamp); the implementation choice is incidental.
- **`yamaha discover --all-networks`** for multi-VLAN setups — out of scope for a personal-use CLI.
- **`yamaha config edit`** opening `$EDITOR` — convenience, not a design decision.
- **Structured logs (slog)** — `--debug` writes line-oriented text; structured logging is overkill for a single-process CLI.

## Unresolved questions (carry to v6 if useful)

None blocking. Plausible future iterations:

1. **`watch --filter <jq-expr>`** — server-side filtering for dashboard use cases. Premature today.
2. **MQTT bridge mode** — `yamaha bridge --mqtt <broker>` republishes events. Whole different scope; probably a sibling tool.
3. **`yamaha quick-fix`** — a heuristic recovery command that re-runs SSDP for all configured devices and prunes/updates entries. Nice-to-have if the user accumulates many receivers over time.
4. **Goreleaser / homebrew tap** — distribution. v1.0 polish task, not a Phase 1 concern.

## Net result

Five review passes in. v5 closes the last two reliability gaps (DHCP drift and signal handling) with two small, targeted additions. The plan is now end-to-end implementable with explicit behavior for every command, every cache decision, every error path, every signal, and every common LAN failure mode (offline, IP drift, slow retry).

This is likely the natural stopping point for design-space iteration. Further passes would either invent features or polish words; the next valuable activity is implementation, where reality will surface anything the design missed.

**Decisions resolved across all five passes:**
- v1: output formatting, first-run UX, multi-device addressing, Phase 3 scope/raw + (silent) exit codes, integration tests
- v2: volume deltas (server-side relative), power-on wait default, watch output format (NDJSON), input validation strategy + (silent) `--debug`, `prepareInputChange` automation, zero-found wizard, header conventions
- v3: cache invalidation (TTL), HTTP retry policy, raw param encoding, watch resilience + (silent) `--zone` table, atomic config writes, `--version`, `YAMAHA_DEBUG` parsing, alias collisions, dB-deltas forbidden
- v4: DHCP resilience (UDN), SIGINT handling + (silent) thread-safety doc, `NO_COLOR`, exit code 130

That's roughly 30 design decisions, all consistent with each other, with traceable rationale per pass.
