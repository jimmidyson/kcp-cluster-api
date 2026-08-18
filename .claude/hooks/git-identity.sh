#!/usr/bin/env bash
# Keep commits attributed to the person whose session made them.
#
# An agent session runs in a container whose git identity is the agent's, not
# the contributor's. Committing with it has two consequences, both of which
# AGENTS.md ("Commit authorship") forbids: the commit is attributed to a party
# who cannot review or answer for it, and GitHub adds a `Co-authored-by:`
# trailer naming the agent when the pull request is squash merged, because the
# author it found is not an account it recognises.
#
# This hook does not decide who the contributor is — it cannot, and guessing
# would be worse than failing. It reports the problem and refuses the commit
# until the session has set an identity.
#
# Modes:
#   session-start   advisory; tells the session what to set, before it commits
#   guard-commit    blocking; refuses `git commit` while the identity is unset
set -euo pipefail

readonly AGENT_EMAIL='noreply@anthropic.com'

# identity_unset reports whether git would author a commit as nobody in
# particular: no email configured, or the agent container's default.
identity_unset() {
  local email
  email="$(git config user.email 2>/dev/null || true)"
  [ -z "${email}" ] || [ "${email}" = "${AGENT_EMAIL}" ]
}

instructions() {
  cat <<'MSG'
The git author identity for this session is still the agent container's
default, so a commit made now would be attributed to the agent rather than to
the person running the session.

Resolve the acting GitHub user — the GitHub MCP server's `get_me` returns the
account this session authenticates as — and set the identity from it:

  git config user.name  "<their name, or their login>"
  git config user.email "<id>+<login>@users.noreply.github.com"

The noreply address is deliberate: it links the commit to the GitHub account
without publishing a private address, and it works for contributors who have
"block command line pushes that expose my email" enabled.

Do not hardcode one person's identity in this repository or in a shared cloud
environment: every contributor's session reads the same files, and one
hardcoded identity would misattribute everybody else's work. See AGENTS.md,
"Commit authorship".
MSG
}

case "${1:-session-start}" in
session-start)
  # Advisory only. A session that never commits is not a problem, and failing
  # session startup over one would be a poor trade.
  if identity_unset; then
    instructions
  fi
  ;;
guard-commit)
  # PreToolUse hands the tool call on stdin. Matching the raw payload keeps
  # this dependency-free — no jq, no python — on whatever machine a
  # contributor runs Claude Code from. A false positive costs a check that
  # passes anyway.
  input="$(cat)"
  case "${input}" in
  *'git commit'*) ;;
  *) exit 0 ;;
  esac

  if identity_unset; then
    instructions >&2
    # 2 is the blocking exit status: the tool call does not run, and stderr is
    # returned to the session so it can fix the identity and retry.
    exit 2
  fi
  ;;
*)
  echo "unknown mode: ${1}" >&2
  exit 1
  ;;
esac
