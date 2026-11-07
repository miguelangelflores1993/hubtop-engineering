# AGENTS.md

Rules for every agent (and person) working in this repository. `CLAUDE.md`
imports this file; the agent definitions in `.claude/agents/` refer to it.

## What this repo is

The public companion to HubTop: a case study (`README.md`), the agent delivery
pipeline (`.claude/`, `bin/`) and, later, **HubTop Lite**, one end-to-end flow
rebuilt from scratch. No code, data or configuration is copied from the
product repo without review.

## Hard rules

- **No real data.** No real URLs, project IDs, emails, phone numbers or
  customer data. Seeds and fixtures use invented names and the
  `example.com` / `example.test` domains.
- **No secrets.** Nothing that looks like a key, token or password is written
  to a file, a command or a commit. Local values live in an ignored `.env`
  that agents never read. Run `gitleaks` before every push.
- **Folder names** are kebab-case and carry no version (`client-microapp`,
  not `client_microapp_v2`).
- **Identity comes from the verified JWT**, never from a request body or a
  query parameter. Authorization is decided in the API or in RLS, never by a
  UI condition.

## Layout (target)

| Path | Owner |
|---|---|
| `apps/api` | Go API: HTTP handlers, auth middleware, SQL |
| `apps/client-microapp`, `apps/professional-microapp` | Flutter microapps, one per mode |
| `apps/host` | Flutter host: router and composition |
| `packages/app-core` | Contracts, session, networking |
| `packages/design-system` | Shared UI |
| `supabase/` | Schema, migrations, RLS, seed |
| `specs/` | One folder per feature spec |
| `bin/`, `tools/`, `.claude/` | Delivery tooling |

"Code" in the rules below means anything under `apps/`, `packages/` or
`supabase/`. Docs, specs and tooling are not code for review purposes.

## Delivery pipeline

Every change to code goes through the same path. Tooling and docs skip the
review receipts but not CI.

1. **Spec.** Non-trivial work starts from `specs/NNN-name/spec.md`:
   context, requirements (`FR-…`), success criteria (`SC-…`). The spec says
   *what*, never *how*.
2. **Lead.** The main session traces the flow, plans and writes the brief:
   objective, files, out of scope, what is already ruled out, the acceptance
   command and the expected report. Git writes and codegen stay with the lead.
3. **Implement.** A whole slice goes to the `sidekick` agent with that brief.
   Short or serial work (debugging, a one-line fix) stays with the lead.
4. **Two independent reviews**, on the committed diff, in parallel:
   - Stage 1: the `verifier` agent, in a clean context. Its `APPROVED`
     saves the stage 1 receipt (`bin/agent-review approve`).
   - Stage 2: the lead runs `/code-review <level>`, with the level that
     `bin/change-size --level` prints. `NO BLOCKERS` is saved with
     `bin/agent-review approve --stage review --level <level>`.
5. **Merge.** `bin/agent-merge <pr>` is the only way an agent merges. It
   refuses a diff over the size limits, a diff without both receipts, a head
   that doesn't contain the current base, and any CI check that isn't green.
   A new commit invalidates both receipts.

Act only on review findings that come with evidence (`path:line`, why, how
to reproduce). A second `CHANGES REQUIRED` on the same slice goes to the user.

### Size

`bin/change-size` caps each pull request: 1000 added production lines, 400
added or deleted lines in sensitive paths (`tools/sensitive-paths.ere`), 3000
added lines in total. Over the cap, split into stacked pull requests.

### Hooks

- `close-gate.sh` (Stop): runs the cheap checks on changed code before a
  session can end, blocks on failure, escalates to the user after two blocks.
  When green and the diff touches a sensitive path, asks once for the
  `verifier`.
- `secret-output-scan.py` (PostToolUse): redacts secrets from tool output
  before the model sees them.
- `review-receipt-guard.py` (PreToolUse on Bash): only the `verifier` saves
  the stage 1 receipt, only the main session saves stage 2, and nothing writes
  to the receipts folder except `bin/agent-review`.

The hooks are guardrails against shortcuts, not a security boundary.

## Commits and pull requests

- Conventional commit subjects (`feat:`, `fix:`, `docs:`, `chore:`…).
- One slice per pull request. The description links its spec and lists what
  was run to verify it.
