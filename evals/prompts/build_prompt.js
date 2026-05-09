// Promptfoo prompt function. Loads the named skill's SKILL.md and composes
// a (system, user) message pair so the model sees the same instructions an
// agent would after it activates the skill.
//
// Tests reference this via:
//   prompts:
//     - file://prompts/build_prompt.js:buildPrompt
//
// And then in each test:
//   vars:
//     skillDir: skills/yamaha-receiver   # path under repo root
//     userPrompt: "..."                  # message sent as the user turn

const fs = require('node:fs');
const path = require('node:path');

const REPO_ROOT = path.resolve(__dirname, '..', '..');

function stripFrontmatter(text) {
  const m = text.match(/^---\n[\s\S]*?\n---\n?([\s\S]*)$/);
  return m ? m[1] : text;
}

function loadSkill(skillDir) {
  const skillPath = path.join(REPO_ROOT, skillDir, 'SKILL.md');
  if (!fs.existsSync(skillPath)) {
    throw new Error(`SKILL.md not found at ${skillPath} (vars.skillDir=${skillDir})`);
  }
  return stripFrontmatter(fs.readFileSync(skillPath, 'utf8'));
}

function buildPrompt({ vars }) {
  if (!vars || !vars.skillDir || !vars.userPrompt) {
    throw new Error('build_prompt requires vars.skillDir and vars.userPrompt');
  }
  const skillBody = loadSkill(vars.skillDir);

  const system = [
    `You have activated the "${vars.skillDir}" Agent Skill. Treat the SKILL.md content`,
    `below as authoritative. Apply its gotchas; do not invent commands or parameters that`,
    `are not documented there. When the user asks for a command, return one that`,
    `would actually work according to the skill.`,
    '',
    '--- SKILL.md ---',
    skillBody,
  ].join('\n');

  return JSON.stringify([
    { role: 'system', content: system },
    { role: 'user', content: vars.userPrompt },
  ]);
}

module.exports = buildPrompt;
module.exports.buildPrompt = buildPrompt;
