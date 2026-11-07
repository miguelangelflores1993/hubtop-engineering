#!/usr/bin/env bash
# Tests for the three hooks. Run from anywhere: .claude/hooks/tests/hooks-test.sh
set -uo pipefail

hooks=$(cd "$(dirname "$0")/.." && pwd)
pass=0 failed=0
ok() { pass=$((pass + 1)); }
ko() { failed=$((failed + 1)); echo "FAIL: $1" >&2; }

# --- review-receipt-guard.py -------------------------------------------------
guard() { # agent_type command -> prints deny|allow
  local event
  event=$(jq -nc --arg a "$1" --arg c "$2" '{tool_name:"Bash",tool_input:{command:$c}} + (if $a == "" then {} else {agent_type:$a} end)')
  if printf '%s' "$event" | "$hooks/review-receipt-guard.py" | grep -q '"deny"'; then echo deny; else echo allow; fi
}
expect_guard() { # want agent_type command
  local got
  got=$(guard "$2" "$3")
  if [ "$got" = "$1" ]; then ok; else ko "guard [$2] '$3': want $1, got $got"; fi
}

AR=bin/agent-review
expect_guard allow verifier "printf '%s\n' 'APPROVED: go test ./...' | $AR approve --base origin/main"
expect_guard deny  ''       "printf '%s\n' 'APPROVED: trust me' | $AR approve --base origin/main"
expect_guard deny  sidekick "printf '%s\n' 'APPROVED' | $AR approve"
expect_guard allow ''       "printf '%s\n' 'NO BLOCKERS' | $AR approve --stage review --level low"
expect_guard deny  verifier "printf '%s\n' 'NO BLOCKERS' | $AR approve --stage review --level low"
expect_guard allow sidekick "$AR check"
expect_guard allow ''       "$AR id --base origin/main"
expect_guard allow verifier "$AR approve --base \$(git merge-base HEAD origin/main) <<<'APPROVED'"  # heredoc-string
expect_guard deny  ''       "echo APPROVED > .git/agent-reviews/abc"
expect_guard deny  ''       "cp /tmp/x .git/agent-reviews/abc"
expect_guard deny  ''       "cd .git/agent-reviews && touch abc"
expect_guard deny  ''       "sed -i '' s/x/y/ .git/agent-reviews/abc"
expect_guard deny  ''       "python3 -c 'open(\".git/agent-reviews/a\",\"w\")'"
expect_guard deny  ''       "find .git/agent-reviews -delete"
expect_guard deny  ''       "eval \"$AR approve\""
expect_guard deny  ''       "bash -c 'echo APPROVED > .git/agent-reviews/x'"
expect_guard deny  verifier "\$($AR approve)"
expect_guard allow ''       "cat .git/agent-reviews/abc"
expect_guard allow ''       "ls .git/agent-reviews 2>/dev/null"
expect_guard allow ''       "git commit -m 'docs: explain agent-reviews folder'"
expect_guard allow ''       "go test ./..."
expect_guard deny  ''       "$AR approve 'unterminated"

# --- secret-output-scan.py ---------------------------------------------------
scan() { jq -nc --arg o "$1" '{tool_name:"Bash",tool_response:{stdout:$o,stderr:""}}' | "$hooks/secret-output-scan.py"; }
fake_gh="ghp_$(printf 'a%.0s' {1..36})"
out=$(scan "token=$fake_gh done")
if grep -q 'secret hidden: ' <<<"$out" && ! grep -q "$fake_gh" <<<"$out"; then ok; else ko "scan did not hide a GitHub token: $out"; fi
if jq -e '.hookSpecificOutput.updatedToolOutput.stdout | startswith("token=<secret hidden")' <<<"$out" >/dev/null; then ok; else ko "scan broke the output shape: $out"; fi
out=$(scan "nothing to see here")
if [ -z "$out" ]; then ok; else ko "scan changed a clean output: $out"; fi

# --- close-gate.sh -----------------------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export XDG_CACHE_HOME="$tmp/cache"
repo="$tmp/repo"
mkdir -p "$repo/tools" "$repo/apps/api" && cd "$repo" || exit 1
git init -q -b main && git config user.email t@example.test && git config user.name test
cp "$hooks/../../tools/sensitive-paths.ere" tools/
printf 'package main\n' >apps/api/main_test.go
git add -A && git commit -qm init && git checkout -qb feature
gate() { jq -nc --arg s "$1" --arg c "$repo" '{session_id:$s,cwd:$c,stop_hook_active:false}' | "$hooks/close-gate.sh"; }

out=$(gate s1)
if [ -z "$out" ]; then ok; else ko "gate blocked a branch with no code changes: $out"; fi
git rm -q apps/api/main_test.go
out=$(gate s2)
if jq -e '.decision == "block"' <<<"$out" >/dev/null && grep -q 'deleted test' <<<"$out"; then ok; else ko "gate let a deleted test through: $out"; fi
out=$(gate s2)
if jq -e '.systemMessage | test("escalate")' <<<"$out" >/dev/null; then ok; else ko "gate did not escalate a repeated failure: $out"; fi
git checkout -q HEAD -- apps/api/main_test.go
mkdir -p supabase/migrations && printf 'create table t();\n' >supabase/migrations/001_t.sql
out=$(gate s3)
if jq -e '.decision == "block"' <<<"$out" >/dev/null && grep -q verifier <<<"$out"; then ok; else ko "gate did not ask for the verifier on a migration: $out"; fi
out=$(gate s3)
if [ -z "$out" ]; then ok; else ko "gate asked for the verifier twice: $out"; fi

echo "hooks: $pass passed, $failed failed"
[ "$failed" = 0 ]
