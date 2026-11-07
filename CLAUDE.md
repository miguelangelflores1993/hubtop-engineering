# HubTop Engineering

@AGENTS.md

## Agents

The helper agents live in `.claude/agents/` so every machine and cloud session
has the same ones. The main session is the **lead**: it traces the flow,
plans, writes the brief, reviews the diff and runs the closing tests.

- A whole slice goes early to `sidekick` (Sonnet) with a complete brief:
  objective, files, out of scope, what is already ruled out, acceptance
  command and return format. Reuse the same sidekick with SendMessage for
  follow-ups so its context persists.
- The lead always reviews the diff, runs the relevant tests itself and sends
  concrete feedback. Round 1: feedback to the same sidekick. Round 2: a
  rewritten brief to a fresh sidekick. Round 3: the user decides.
- `verifier` (Opus) runs one pass on every change to code before it reaches
  `main`. Launch it in the background on the committed diff and run
  `/code-review <level>` meanwhile; the two stages are independent.
- Merge only with `bin/agent-merge <pr>`. `gh pr merge` is denied in
  `.claude/settings.json`.

## Brief template

```text
Objective: <one sentence, the user-visible outcome>
Spec: specs/NNN-name/spec.md (FR-…, SC-…)
Files: <paths you may touch>
Out of scope: <what not to touch, even if it looks wrong>
Ruled out: <what the lead already checked>
Acceptance: <the exact command that must pass>
Report: files touched, commands run with exact results, deviations
```
