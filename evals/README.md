# Skill evals

Two layers of automated checks for the `yamaha-receiver` skill.

## Layer 1 — Static validator (`validate.py`)

Stdlib-only Python script. Walks the repo, finds every `SKILL.md`, and checks
each one against the [Agent Skills specification](https://agentskills.io/specification):

- Frontmatter is well-formed; required `name` and `description` are present
- `name` ≤ 64 chars, lowercase a–z / 0–9 / hyphens, no leading/trailing/double
  hyphens, no reserved tokens (`anthropic`, `claude`), no XML
- `name` matches the parent directory
- `description` ≤ 1024 chars, non-empty
- `compatibility` (if present) ≤ 500 chars
- `SKILL.md` body ≤ 500 lines (warns at 90 % of cap)
- Every Markdown link to a `.md` file resolves to a real file
- Reference files are at most one level deep from `SKILL.md`
- No backslash paths or Windows-style paths in the body

```bash
python3 evals/validate.py                    # validate every skill under cwd
python3 evals/validate.py skills/yamaha-receiver  # validate a single skill
```

Exits non-zero if any skill has ERRORs (warnings are reported but not fatal).
Wired into CI via `.github/workflows/validate-skills.yml`.

## Layer 2 — Behavioral evals (promptfoo)

Drive Claude with the `yamaha-receiver` skill loaded as the system prompt and
assert that its output respects the skill — emits valid `yamaha` commands,
applies the documented gotchas (device-specific enums, `--yes` for `reboot`,
hardcoded sleep values, link aliases), and refuses to invent missing
subcommands.

### Setup

```bash
export ANTHROPIC_API_KEY=sk-ant-...
npm install -g promptfoo            # or use npx (no install)
```

### Run

```bash
# Full suite (all skills, all scenarios)
npx promptfoo eval --config evals/promptfooconfig.yaml

# Filter to one skill while iterating
npx promptfoo eval --config evals/promptfooconfig.yaml \
  --filter-pattern yamaha-receiver

# Browse the last run as a UI
npx promptfoo view
```

### How it's wired

- `promptfooconfig.yaml` — top-level config: declares the prompt function, the
  Anthropic provider, and includes per-skill test files.
- `prompts/build_prompt.js` — prompt function. Reads `<skillDir>/SKILL.md`,
  strips the frontmatter, and emits a `[system, user]` chat-message pair.
- `tests/<skill>.yaml` — per-skill test cases. Each case sets `vars.skillDir`
  (path to the skill from repo root) and `vars.userPrompt` (the user turn),
  then declares promptfoo `assert` blocks.

This layer simulates *content correctness*: given that the agent has loaded
the skill, does it use the skill correctly? It does **not** test *activation*
(whether the agent decides to load the skill from just the description) and
it does **not** test *execution* (whether running the suggested command
actually changes the receiver). Both are deliberately out of scope — keep
this layer cheap, deterministic, and free of live-device flakiness.

### Adding a new test case

```yaml
# evals/tests/yamaha-receiver.yaml
- description: short summary of what this case proves
  vars:
    skillDir: skills/yamaha-receiver
    userPrompt: |
      The user message to send.
  assert:
    - type: contains            # or regex, not-contains, llm-rubric, ...
      value: 'something_expected'
    - type: llm-rubric
      value: |
        Plain-English rubric for what the response must demonstrate.
```

See [promptfoo's assertion reference](https://www.promptfoo.dev/docs/configuration/expected-outputs/)
for the full assertion catalog (`equals`, `contains`, `contains-all`,
`not-contains`, `regex`, `javascript`, `similar`, `llm-rubric`, etc.).

## What's not gated in CI

The behavioral layer is intentionally manual — it costs Anthropic API credits
and can be flaky on prompt-sensitive cases. Run it before publishing skill
changes; the static validator is the only thing CI gates on.
