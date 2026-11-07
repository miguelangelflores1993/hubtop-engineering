#!/usr/bin/env bash
# Tests for bin/change-size and bin/agent-review, in a throwaway git repo.
# Run from anywhere: tools/tests/receipts-test.sh
set -uo pipefail

src=$(cd "$(dirname "$0")/../.." && pwd)
pass=0 failed=0
ok() { pass=$((pass + 1)); }
ko() { failed=$((failed + 1)); echo "FAIL: $1" >&2; }
same() { # description want got
  if [ "$3" = "$2" ]; then ok; else ko "$1: want '$2', got '$3'"; fi
}
expect() { # description want-exit command...
  local desc=$1 want=$2
  shift 2
  "$@" >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then ok; else ko "$desc: want exit $want, got $got"; fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp" || exit 1
git init -q -b main && git config user.email t@example.test && git config user.name test
mkdir -p bin tools apps/api supabase/migrations
cp "$src/bin/change-size" "$src/bin/agent-review" bin/
cp "$src/tools/sensitive-paths.ere" tools/
printf 'package main\n' >apps/api/main.go
git add -A && git commit -qm init
git checkout -qb feature
AR=bin/agent-review
CS=bin/change-size

# docs only: no code, no receipts needed
printf '# notes\n' >notes.md && git add -A && git commit -qm docs
same 'docs-only diff should have no code id' 'no code' "$($AR id --base main)"
expect 'check passes without code' 0 $AR check --base main
same 'docs-only level should be low' low "$($CS --base main --level)"

# code change: check fails until both receipts exist
printf 'package main\n\nfunc a() {}\n' >apps/api/main.go && git commit -qam code
expect 'check fails with no receipts' 1 $AR check --base main
expect 'approve refuses a wrong verdict' 1 sh -c "echo 'LGTM' | $AR approve --base main"
expect 'stage 1 saves APPROVED' 0 sh -c "echo 'APPROVED: go test' | $AR approve --base main"
expect 'check still fails with stage 1 only' 1 $AR check --base main
expect 'stage 2 needs a level' 2 sh -c "echo 'NO BLOCKERS' | $AR approve --stage review --base main"
expect 'stage 2 saves NO BLOCKERS' 0 sh -c "echo 'NO BLOCKERS' | $AR approve --stage review --level low --base main"
expect 'check passes with both receipts' 0 $AR check --base main

# uncommitted code: approve refuses
printf '// wip\n' >>apps/api/main.go
expect 'approve refuses uncommitted code' 1 sh -c "echo 'APPROVED' | $AR approve --base main"
git checkout -q -- apps/api/main.go

# a new commit, even whitespace, invalidates both receipts
printf 'package main\n\nfunc a()  {}\n' >apps/api/main.go && git commit -qam space
expect 'whitespace change invalidates receipts' 1 $AR check --base main

# sensitive path: level high, and stage 2 refuses a lower level
printf 'create table t();\n' >supabase/migrations/001_t.sql && git add -A && git commit -qm migration
same 'migration level should be high' high "$($CS --base main --level)"
expect 'stage 2 refuses a level below the diff' 1 sh -c "echo 'NO BLOCKERS' | $AR approve --stage review --level low --base main"
expect 'stage 2 accepts the right level' 0 sh -c "echo 'NO BLOCKERS' | $AR approve --stage review --level high --base main"

# size caps
expect 'small diff is under the caps' 0 $CS --base main
seq 1 1100 | sed 's/^/\/\/ line /' >apps/api/big.go && git add -A && git commit -qm big
expect 'over 1000 production lines fails' 1 $CS --base main
mkdir -p apps/api/testdata && git mv apps/api/big.go apps/api/testdata/big.go && git commit -qm move
expect 'test files do not count as production' 0 $CS --base main

echo "receipts: $pass passed, $failed failed"
[ "$failed" = 0 ]
