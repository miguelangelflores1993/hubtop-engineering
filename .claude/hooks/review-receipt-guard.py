#!/usr/bin/env python3
"""PreToolUse hook on Bash: each review receipt is saved by the role that owns it.

- Stage 1 (`bin/agent-review approve`) is saved only by the `verifier` subagent.
- Stage 2 (`bin/agent-review approve --stage review`) is saved only by the main
  session (the lead), never by a subagent.
- Nothing writes to the receipts folder (`agent-reviews`) except
  `bin/agent-review`: redirections, tee, cp, mv, rm, touch, ln, `sed -i`,
  interpreter one-liners, `find -delete/-exec` and `cd agent-reviews && <write>`
  are all refused. Reads (cat, ls, grep, jq, git) pass, and quoted text is an
  argument, not a path: a commit message that names `agent-reviews` passes.
  A heredoc body is data (a PR description, say) unless it feeds a shell or
  an interpreter: then mentioning agent-review in it is refused.

Who is calling comes from `agent_type` in the hook event: the main session has
none, a subagent carries its name.

Limits: this is a barrier against simple shortcuts, not a security boundary.
Anyone determined can obfuscate a shell command. It fails closed: if the
command mentions agent-review and can't be parsed, it is refused.
"""
import json
import os
import re
import shlex
import sys

RECEIPTS = 'agent-reviews'
WRAPPERS = {'env', 'command', 'exec', 'sudo', 'time', 'nohup', 'builtin', 'nice'}
SHELLS = {'bash', 'sh', 'zsh'}
WRITERS = {'tee', 'cp', 'mv', 'install', 'touch', 'rm', 'ln', 'dd', 'truncate', 'rsync', 'mkdir'}
INTERPRETERS = re.compile(r'^(python[0-9.]*|perl|node|ruby)$')
FIND_ACTIONS = {'-delete', '-exec', '-execdir', '-ok', '-okdir', '-fprint'}
CONTROL = {';', '&&', '||', '|', '&', '(', ')', ';;', '|&'}
ASSIGNMENT = re.compile(r'^[A-Za-z_][A-Za-z0-9_]*=')
SUBSTITUTION = re.compile(r'\$\(([^()]*)\)|`([^`]*)`')

STAGE1 = ('The stage 1 receipt is saved only by the `verifier` subagent, with its APPROVED on the committed '
          'diff. Launch it (Agent with subagent_type `verifier`) instead of running '
          '`bin/agent-review approve` yourself. See AGENTS.md "Delivery pipeline".')
STAGE2 = ('The stage 2 receipt (`--stage review`) is saved by the lead from the main session, after running '
          '`/code-review <level>` on the diff. A subagent never saves it. See AGENTS.md "Delivery pipeline".')
WRITE = ('Review receipts are written only by `bin/agent-review approve`: don\'t create, copy, move or delete '
         'them any other way. Reading them (cat, ls, grep, jq) is fine.')
NONLITERAL = ('I can\'t tell what this command does with the review receipts. Use a literal `bin/agent-review` '
              'subcommand (approve, check, id) and literal paths, without eval, xargs, heredocs or command '
              'substitution around it.')
UNPARSABLE = 'review-receipt-guard could not parse this command, which mentions agent-review, and fails closed.'


class Deny(Exception):
    pass


def segments(command):
    """Split a command into simple-command segments, keeping quoting semantics."""
    lexer = shlex.shlex(command.replace('\n', ' ; '), posix=True, punctuation_chars=';&|()<>')
    lexer.whitespace_split = True
    lexer.commenters = '#'
    current = []
    for token in lexer:
        if token in CONTROL:
            if current:
                yield current
            current = []
        else:
            current.append(token)
    if current:
        yield current


def is_redirect(token):
    return token and set(token) <= set('<>&|') and '>' in token


HEREDOC = re.compile(r'(?<!<)<<-?\s*([\'"]?)([A-Za-z_][A-Za-z0-9_]*)\1')
EXECUTES = re.compile(r'(^|[;&|(]\s*|\s)(bash|sh|zsh|eval|source|\.|python[0-9.]*|perl|node|ruby)(\s|$)')


def strip_heredocs(command):
    """Drop heredoc bodies: they are stdin data, not commands. A body that feeds a
    shell or an interpreter is code, so if it mentions agent-review it is refused."""
    lines = command.split('\n')
    kept = []
    i = 0
    while i < len(lines):
        line = lines[i]
        kept.append(line)
        i += 1
        for match in HEREDOC.finditer(line):
            delimiter = match.group(2)
            body = []
            while i < len(lines) and lines[i].strip() != delimiter:
                body.append(lines[i])
                i += 1
            i += 1  # the delimiter line
            if 'agent-review' in '\n'.join(body) and EXECUTES.search(line[:match.start()]):
                raise Deny(NONLITERAL)
    return '\n'.join(kept)


def check(command, agent_type, depth=0):
    if depth > 5:
        raise Deny(NONLITERAL)
    command = strip_heredocs(command)
    # agent-review inside a substitution, eval, xargs or a heredoc: not literal
    for match in SUBSTITUTION.finditer(command):
        if 'agent-review' in (match.group(1) or match.group(2) or ''):
            raise Deny(NONLITERAL)
    if re.search(r'\b(eval|xargs)\b', command):
        raise Deny(NONLITERAL)

    in_receipts = False
    for words in segments(command):
        # redirections: the word after a `>`-like operator is a write target
        writes_redirect = False
        args = []
        skip_next = False
        for i, word in enumerate(words):
            if skip_next:
                skip_next = False
                continue
            if is_redirect(word):
                target = words[i + 1] if i + 1 < len(words) else ''
                skip_next = True
                if target.isdigit() or target in ('/dev/null', '/dev/stdout', '/dev/stderr'):
                    continue
                writes_redirect = True
                if RECEIPTS in target or in_receipts:
                    raise Deny(WRITE)
                continue
            if word.isdigit() and i + 1 < len(words) and is_redirect(words[i + 1]):
                continue  # the `2` of `2>`
            args.append(word)

        while args and (ASSIGNMENT.match(args[0]) or os.path.basename(args[0]) in WRAPPERS):
            args.pop(0)
        if not args:
            continue
        name = os.path.basename(args[0])
        rest = args[1:]
        mentions = any(RECEIPTS in arg for arg in rest)

        if name in SHELLS and '-c' in rest:
            index = rest.index('-c')
            if index + 1 < len(rest):
                check(rest[index + 1], agent_type, depth + 1)
            continue
        if name == 'cd':
            in_receipts = bool(rest) and RECEIPTS in rest[0]
            continue
        if name in WRITERS and (mentions or in_receipts):
            raise Deny(WRITE)
        if name == 'sed' and (mentions or in_receipts) and any(a.startswith('-i') or a.startswith('--in-place') for a in rest):
            raise Deny(WRITE)
        if INTERPRETERS.match(name) and (mentions or in_receipts or RECEIPTS in command):
            if any(a in ('-c', '-e') for a in rest):
                raise Deny(WRITE)
        if name == 'find' and (mentions or in_receipts) and any(a in FIND_ACTIONS for a in rest):
            raise Deny(WRITE)
        if writes_redirect and in_receipts:
            raise Deny(WRITE)

        if name == 'agent-review':
            sub = rest[0] if rest else ''
            if '$' in sub or sub not in ('approve', 'check', 'id'):
                raise Deny(NONLITERAL)
            if sub != 'approve':
                continue
            stage = 'verifier'
            if '--stage' in rest:
                index = rest.index('--stage')
                stage = rest[index + 1] if index + 1 < len(rest) else ''
            if stage == 'verifier' and agent_type != 'verifier':
                raise Deny(STAGE1)
            if stage == 'review' and agent_type:
                raise Deny(STAGE2)
            if stage not in ('verifier', 'review'):
                raise Deny(NONLITERAL)


def deny(reason):
    print(json.dumps({'hookSpecificOutput': {
        'hookEventName': 'PreToolUse',
        'permissionDecision': 'deny',
        'permissionDecisionReason': reason,
    }}))


def main():
    raw = sys.stdin.read()
    try:
        event = json.loads(raw)
        command = event.get('tool_input', {}).get('command', '')
    except (ValueError, AttributeError):
        if 'agent-review' in raw:
            deny(UNPARSABLE)
        return
    if 'agent-review' not in command:
        return
    try:
        check(command, event.get('agent_type') or '')
    except Deny as refusal:
        deny(str(refusal))
    except Exception:  # noqa: BLE001 - fail closed when the command mentions agent-review
        deny(UNPARSABLE)


if __name__ == '__main__':
    main()
