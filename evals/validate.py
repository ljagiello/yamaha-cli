#!/usr/bin/env python3
"""
Static validator for every Agent Skill in the repo.

Walks the working tree, finds every directory containing a `SKILL.md`, and
checks each one against the Agent Skills specification:
  https://agentskills.io/specification

Reports per-skill ERRORs and WARNs, then exits non-zero if any ERRORs were
found. Pure stdlib — no PyYAML or other deps required.

Usage:
  python3 evals/validate.py            # validate every skill under cwd
  python3 evals/validate.py path/to    # validate every skill under path/to
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

NAME_RE = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
RESERVED_NAME_TOKENS = ("anthropic", "claude")  # per Anthropic best-practices

MAX_NAME_LEN = 64
MAX_DESCRIPTION_LEN = 1024
MAX_COMPAT_LEN = 500
MAX_BODY_LINES = 500
SOFT_BODY_LINES = int(MAX_BODY_LINES * 0.9)


def parse_frontmatter(text: str) -> tuple[dict, str]:
    """Return (frontmatter dict, body). Raises ValueError on malformed input."""
    m = re.match(r"^---\n(.*?)\n---\n?(.*)$", text, re.S)
    if not m:
        raise ValueError("missing or malformed frontmatter delimiters")
    fm_raw, body = m.group(1), m.group(2)
    fm: dict[str, object] = {}
    metadata: dict[str, str] | None = None
    for line in fm_raw.splitlines():
        if not line.strip():
            continue
        if metadata is not None and line.startswith("  "):
            mm = re.match(r"  ([\w-]+):\s*(.*)$", line)
            if mm:
                metadata[mm.group(1)] = mm.group(2).strip()
            continue
        metadata = None
        m2 = re.match(r"^([a-z][\w-]*):\s*(.*)$", line)
        if not m2:
            continue
        key, value = m2.group(1), m2.group(2).strip()
        if key == "metadata" and not value:
            metadata = {}
            fm[key] = metadata
        else:
            fm[key] = value
    return fm, body


def validate_skill(skill_dir: Path) -> list[tuple[str, str]]:
    issues: list[tuple[str, str]] = []
    skill_md = skill_dir / "SKILL.md"
    text = skill_md.read_text(encoding="utf-8")

    try:
        fm, body = parse_frontmatter(text)
    except ValueError as e:
        return [("ERROR", f"frontmatter: {e}")]

    name = str(fm.get("name", "") or "")
    desc = str(fm.get("description", "") or "")
    compat = str(fm.get("compatibility", "") or "")

    if not name:
        issues.append(("ERROR", "missing required field: name"))
    else:
        if len(name) > MAX_NAME_LEN:
            issues.append(("ERROR", f"name length {len(name)} > {MAX_NAME_LEN}"))
        if not NAME_RE.match(name):
            issues.append((
                "ERROR",
                f"name {name!r} violates charset/format rules "
                "(lowercase a-z/0-9/hyphens; no leading/trailing/consecutive hyphens)",
            ))
        if name != skill_dir.name:
            issues.append(("ERROR", f"name {name!r} != directory name {skill_dir.name!r}"))
        if any(tok in name for tok in RESERVED_NAME_TOKENS):
            issues.append(("ERROR", f"name {name!r} contains reserved token (anthropic/claude)"))
        if "<" in name or ">" in name:
            issues.append(("ERROR", "name contains XML tag characters"))

    if not desc:
        issues.append(("ERROR", "missing required field: description"))
    elif len(desc) > MAX_DESCRIPTION_LEN:
        issues.append(("ERROR", f"description length {len(desc)} > {MAX_DESCRIPTION_LEN}"))
    if "<" in desc or ">" in desc:
        issues.append(("WARN", "description contains < or > (XML tag risk per spec)"))

    if compat and len(compat) > MAX_COMPAT_LEN:
        issues.append(("ERROR", f"compatibility length {len(compat)} > {MAX_COMPAT_LEN}"))

    body_lines = body.count("\n")
    if body_lines > MAX_BODY_LINES:
        issues.append(("ERROR", f"SKILL.md body {body_lines} lines > recommended cap {MAX_BODY_LINES}"))
    elif body_lines > SOFT_BODY_LINES:
        issues.append(("WARN", f"SKILL.md body {body_lines} lines (>90% of {MAX_BODY_LINES} cap)"))

    seen_refs: set[str] = set()
    for m in re.finditer(r"\[[^\]]+\]\(([^)#]+\.md)\)", body):
        ref = m.group(1).strip()
        if ref in seen_refs:
            continue
        seen_refs.add(ref)
        if "\\" in ref:
            issues.append(("ERROR", f"file reference uses backslash: {ref!r}"))
            continue
        if ref.startswith(("http://", "https://", "/")):
            continue
        if ref.count("/") > 1:
            issues.append(("WARN", f"reference {ref!r} is more than one directory deep from SKILL.md"))
        target = (skill_dir / ref).resolve()
        if not target.is_file():
            issues.append(("ERROR", f"broken file reference: {ref!r}"))

    for m in re.finditer(r"(?<![\w/])[\w.-]+\\[\w.-]+", body):
        token = m.group(0)
        if token.startswith("\\\\") or "\\n" in token or "\\t" in token:
            continue
        issues.append(("WARN", f"possible Windows-style path in body: {token!r}"))

    return issues


def discover_skills(root: Path) -> list[Path]:
    """All directories containing a SKILL.md, excluding .git and worktree leftovers."""
    out: list[Path] = []
    for p in root.rglob("SKILL.md"):
        if any(part in {".git", ".claude"} for part in p.parts):
            continue
        out.append(p.parent)
    return sorted(set(out))


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    skills = discover_skills(root)
    if not skills:
        print(f"no SKILL.md found under {root}", file=sys.stderr)
        return 1

    total_errors = 0
    total_warns = 0
    for sd in skills:
        rel = sd.relative_to(root)
        issues = validate_skill(sd)
        errors = [i for i in issues if i[0] == "ERROR"]
        warns = [i for i in issues if i[0] == "WARN"]
        total_errors += len(errors)
        total_warns += len(warns)
        if not issues:
            print(f"OK    {rel}")
            continue
        print(f"\n=== {rel} ({len(errors)} error{'s' if len(errors)!=1 else ''}, "
              f"{len(warns)} warning{'s' if len(warns)!=1 else ''}) ===")
        for sev, msg in issues:
            print(f"  [{sev}] {msg}")

    print()
    print(f"Summary: {len(skills)} skill(s), {total_errors} error(s), {total_warns} warning(s).")
    return 1 if total_errors else 0


if __name__ == "__main__":
    sys.exit(main())
