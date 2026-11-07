#!/usr/bin/env bash
# Stop hook: the close gate. If the checkout has changed code (apps/, packages/,
# supabase/), run the cheap checks before the session may end:
#   1. test integrity: no deleted test files, no newly skipped tests;
#   2. dart analyze on each touched Dart package;
#   3. go vet on each touched Go package.
# A failure blocks the close and tells the agent what failed. To avoid loops,
# the same failure twice, or a third block in a row, escalates to the user
# instead of blocking. When everything is green and the diff touches a
# sensitive path (tools/sensitive-paths.ere), it asks once per session for the
# verifier agent.
#
# Fail-visible on purpose: an error in this script warns and exits 0, because
# a broken gate can't honour its own cap. Don't change it to exit 2.
# Requires: git, jq. Dart and Go checks run only when the tool is installed.
set -uo pipefail

cache="${XDG_CACHE_HOME:-$HOME/.cache}/hubtop-engineering/close-gate"
areas=(apps packages supabase)

say() { jq -n --arg m "$1" '{systemMessage:$m}'; }
die() { say "close-gate: internal error, not blocking: $1"; exit 0; }
block() { jq -n --arg r "$1" '{decision:"block",reason:$r}'; exit 0; }

input=$(cat)
parsed=$(printf '%s' "$input" | jq -r '
  (.session_id // "nosession"), (.cwd // ""), (.stop_hook_active // false),
  ((.background_tasks // []) | length)') || die 'invalid JSON input'
{ IFS= read -r sid; IFS= read -r cwd; IFS= read -r active; IFS= read -r nbg; } <<<"$parsed"
sid=$(printf '%s' "$sid" | tr -c 'A-Za-z0-9_-' _)

# Out of scope: not a repo, on main, or work still running in the background.
[ -n "$cwd" ] && cd "$cwd" 2>/dev/null || exit 0
root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$root" || exit 0
[ "$(git branch --show-current)" = main ] && exit 0
[ "$nbg" != 0 ] && exit 0

# Per-session state: last green fingerprint, consecutive blocks, last failure
# signature, and whether the verifier was already requested.
mkdir -p "$cache" || die "can't create $cache"
state="$cache/$sid.json"
out="$cache/$sid.out"
green='' blocks=0 last_sig='' asked='' have_state=0
if [ -f "$state" ] && st=$(jq -r '(.green // ""), (.blocks // 0), (.last_sig // ""), (.verifier // "")' "$state" 2>/dev/null); then
  { IFS= read -r green; IFS= read -r blocks; IFS= read -r last_sig; IFS= read -r asked; } <<<"$st"
  have_state=1
fi
case "$blocks" in '' | *[!0-9]*) blocks=0 ;; esac
# Claude Code sets stop_hook_active when a Stop hook already blocked this turn.
# Without our own state we can't tell a loop from a retry: let it through.
[ "$active" = true ] && [ "$have_state" = 0 ] && exit 0

# Changed code since the merge base: committed, staged, unstaged, untracked.
base=$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD main 2>/dev/null) \
  || die 'no origin/main or main to compare against'
untracked=$(git ls-files --others --exclude-standard -- "${areas[@]}") || die 'git ls-files failed'
changed=$({ git diff --no-renames --name-only "$base" -- "${areas[@]}"; printf '%s\n' "$untracked"; } \
  | sed '/^$/d' | sort -u)
[ -z "$changed" ] && exit 0

# Fingerprint of the diff, untracked contents included: skip a diff already green.
fp=$({
  git diff --no-renames --no-ext-diff --no-color "$base" -- "${areas[@]}"
  printf '%s\n' "$untracked" | while IFS= read -r f; do
    [ -f "$f" ] && { printf '%s\0' "$f"; cat "$f"; }
  done
} | shasum -a 256 | cut -d' ' -f1)
[ -n "$green" ] && [ "$fp" = "$green" ] && exit 0

save_state() { # green blocks last_sig
  # shellcheck disable=SC2015 # die exits; any failure in the chain should reach it
  jq -n --arg g "$1" --argjson b "$2" --arg s "$3" --arg v "$asked" \
    '{green:$g,blocks:$b,last_sig:$s,verifier:$v}' >"$state.tmp" && mv "$state.tmp" "$state" \
    || die "can't save state"
}

fail() { # name readable-command
  local sig first
  sig=$({ printf '%s\n' "$1"; tail -n 20 "$out" | tr -d '0-9'; } | shasum -a 256 | cut -d' ' -f1)
  if [ "$sig" = "$last_sig" ] || [ "$blocks" -ge 2 ]; then
    first=$(sed -n '/[^[:space:]]/{p;q;}' "$out" | cut -c1-160)
    say "close-gate: $1 keeps failing; escalate to the user: $first"
    exit 0
  fi
  save_state "$green" $((blocks + 1)) "$sig"
  block "close-gate: $1 failed ($2)
$(tail -n 12 "$out" | cut -c1-300)
Fix it and close again. If you are stopping on purpose (for example to ask the user something), say so: the next close with the same failure won't block and will warn the user instead."
}

run() { # name dir command...
  local name=$1 dir=$2
  shift 2
  (cd "$dir" && "$@") >"$out" 2>&1 </dev/null || fail "$name" "$*"
}

# 1. Test integrity: deleted test files, or skip markers added to test files.
skip_re='skip:|@[Ss]kip|solo: true|t\.Skip\(|\.only\('
test_re='(/test/|/integration_test/|_test\.dart$|_test\.go$|\.feature$|_test\.sql$)'
test_files=$(printf '%s\n' "$changed" | grep -E "$test_re")
{
  git diff --no-renames --name-only --diff-filter=D "$base" -- "${areas[@]}" \
    | grep -E '(_test\.dart|_test\.go|\.feature|_test\.sql)$' | sed 's/^/deleted test: /'
  if [ -n "$test_files" ]; then
    # shellcheck disable=SC2086
    git diff --no-renames --no-ext-diff --no-color -U0 "$base" -- $test_files \
      | grep -E '^\+' | grep -vE '^\+\+\+ ' | grep -E "$skip_re" | sed 's/^/added line: /'
  fi
  printf '%s\n' "$untracked" | grep -E "$test_re" | while IFS= read -r f; do
    [ -f "$f" ] && grep -IHE "$skip_re" "$f" | sed 's/^/added line: /'
  done
} >"$out" 2>/dev/null
[ -s "$out" ] && fail integrity 'tests deleted or skipped'

# 2. Dart: analyze each touched package (the nearest folder with a pubspec.yaml).
if command -v dart >/dev/null; then
  pkgs=$(printf '%s\n' "$changed" | grep -E '\.dart$' | while IFS= read -r f; do
    d=${f%/*}
    while [ "$d" != "${d%/*}" ] && [ ! -f "$d/pubspec.yaml" ]; do d=${d%/*}; done
    [ -f "$d/pubspec.yaml" ] && echo "$d"
  done | sort -u)
  # shellcheck disable=SC2086
  [ -n "$pkgs" ] && run dart-analyze "$root" dart analyze $pkgs
fi

# 3. Go: vet each touched package of the API module.
if command -v go >/dev/null && [ -f apps/api/go.mod ]; then
  godirs=$(printf '%s\n' "$changed" | grep -E '^apps/api/.*\.go$' | grep -vE '(^|/)testdata/' \
    | while IFS= read -r f; do
      d=${f%/*}
      [ -d "$d" ] || continue
      rel=${d#apps/api}
      rel=${rel#/}
      echo "./$rel"
    done | sed 's#^\./$#.#' | sort -u)
  # shellcheck disable=SC2086
  [ -n "$godirs" ] && run go-vet "$root/apps/api" go vet $godirs
fi

# Green. If the diff touches schema, auth or authorization, ask for the
# verifier once per session.
sensitive=$(cat "$root/tools/sensitive-paths.ere" 2>/dev/null) || die 'missing tools/sensitive-paths.ere'
hits=$(printf '%s\n' "$changed" | grep -E "$sensitive")
if [ -n "$hits" ] && [ -z "$asked" ]; then
  asked=1
  save_state "$fp" 0 ''
  block "close-gate: the checks pass, but the diff touches schema, auth or authorization:
$(printf '%s\n' "$hits" | head -n 10)
Before closing, launch the verifier agent with base $base, the acceptance criteria and the risk, and act only on findings with evidence. One pass: this request won't repeat in this session."
fi
save_state "$fp" 0 ''
exit 0
