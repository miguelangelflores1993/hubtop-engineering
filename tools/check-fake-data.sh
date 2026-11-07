#!/usr/bin/env bash
# Fails if tracked files contain data that looks real: emails outside the
# reserved example domains, hosted Supabase project URLs, or names of the
# private product's infrastructure. AGENTS.md "Hard rules".
#   tools/check-fake-data.sh
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

# reserved for docs and tests (RFC 2606 / 6761) plus GitHub's noreply addresses
allowed_email='@(example\.(com|org|net|test)|[a-z0-9.-]+\.example|users\.noreply\.github\.com|anthropic\.com)$'
found=0

emails=$(git grep -IhoE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' -- . ':!tools/check-fake-data.sh' \
  | grep -viE "$allowed_email" | sort -u)
if [ -n "$emails" ]; then
  echo '✗ emails outside the example domains:'
  printf '%s\n' "$emails" | sed 's/^/  /'
  found=1
fi

hosted=$(git grep -nIE '[a-z0-9]{20}\.supabase\.(co|in)' -- . ':!tools/check-fake-data.sh')
if [ -n "$hosted" ]; then
  echo '✗ hosted Supabase project URLs:'
  printf '%s\n' "$hosted" | sed 's/^/  /'
  found=1
fi

if [ "$found" = 0 ]; then echo 'check-fake-data: ok'; fi
exit "$found"
