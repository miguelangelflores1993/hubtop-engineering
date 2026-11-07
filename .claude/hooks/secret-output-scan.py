#!/usr/bin/env python3
"""PostToolUse hook: redact secrets from a tool's output before the model sees
it and before it lands in the transcript.

Detection, in order:
  1. gitleaks (`gitleaks stdin`), when it is installed: its full rule set;
  2. a small built-in pattern list, always: the most common key shapes, so the
     hook still protects a machine without gitleaks.

The replacement keeps the shape the tool returned (string, list or dict):
Claude Code ignores a plain-text replacement for structured outputs.

Fail-open on its own errors (the output passes unchanged and a warning is
printed to stderr): a scanner that blocks every tool call when it breaks would
get disabled, which is worse.
"""
import json
import re
import shutil
import subprocess
import sys

BUILTIN = [
    ('aws-access-key', re.compile(r'\b(?:AKIA|ASIA)[0-9A-Z]{16}\b')),
    ('github-token', re.compile(r'\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b')),
    ('github-pat', re.compile(r'\bgithub_pat_[A-Za-z0-9_]{60,}\b')),
    ('anthropic-key', re.compile(r'\bsk-ant-[A-Za-z0-9_-]{20,}')),
    ('openai-key', re.compile(r'\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}')),
    ('stripe-key', re.compile(r'\b[rs]k_live_[A-Za-z0-9]{20,}\b')),
    ('slack-token', re.compile(r'\bxox[abpors]-[A-Za-z0-9-]{10,}')),
    ('google-api-key', re.compile(r'\bAIza[0-9A-Za-z_-]{35}\b')),
    ('jwt', re.compile(r'\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}')),
    ('private-key', re.compile(r'-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]+?-----END [A-Z ]*PRIVATE KEY-----')),
]


def strings(node):
    if isinstance(node, str):
        yield node
    elif isinstance(node, dict):
        for value in node.values():
            yield from strings(value)
    elif isinstance(node, list):
        for item in node:
            yield from strings(item)


def rewrite(node, swap):
    if isinstance(node, str):
        return swap(node)
    if isinstance(node, list):
        return [rewrite(item, swap) for item in node]
    if isinstance(node, dict):
        return {key: rewrite(value, swap) for key, value in node.items()}
    return node


def gitleaks(text):
    """Secret value -> label, from gitleaks' JSON report on stdin."""
    if not text.strip() or not shutil.which('gitleaks'):
        return {}
    done = subprocess.run(
        ['gitleaks', 'stdin', '--no-banner', '--log-level', 'error',
         '--report-format', 'json', '--report-path', '-'],
        input=text.encode(), capture_output=True, timeout=20)
    try:
        findings = json.loads(done.stdout) if done.returncode == 1 else []
    except ValueError:
        return {}
    return {f['Secret']: f"gitleaks {f['RuleID']}" for f in findings if f.get('Secret')}


def builtin(text):
    found = {}
    for label, pattern in BUILTIN:
        for match in pattern.finditer(text):
            found.setdefault(match.group(0), label)
    return found


def main():
    event = json.load(sys.stdin)
    response = event.get('tool_response')
    tool = event.get('tool_name', 'the tool')
    if isinstance(response, dict) and (response.get('type') in ('image', 'pdf') or response.get('isImage')):
        return
    text = '\n'.join(strings(response))
    secrets = {**builtin(text), **gitleaks(text)}
    if not secrets:
        return
    counts = {}

    def hide(value):
        # longest first, so a secret that contains another is replaced whole
        for secret in sorted(secrets, key=len, reverse=True):
            if secret in value:
                label = secrets[secret]
                counts[label] = counts.get(label, 0) + value.count(secret)
                value = value.replace(secret, f'<secret hidden: {label}>')
        return value

    redacted = rewrite(response, hide)
    labels = ', '.join(sorted(counts))
    print(json.dumps({
        'systemMessage': f'🔒 Secrets hidden from the output of {tool}: {labels}.',
        'hookSpecificOutput': {
            'hookEventName': 'PostToolUse',
            'updatedToolOutput': redacted,
            'additionalContext': (
                f'The output of {tool} contained secrets ({labels}); they were replaced with '
                '<secret hidden: …> before you saw it. Do not try to read them another way. '
                'If a secret was printed by a command, tell the user it should be rotated.'),
        }}, ensure_ascii=False))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:  # noqa: BLE001 - fail-open, see docstring
        print(f'secret-output-scan: internal error, output not scanned: {error}', file=sys.stderr)
