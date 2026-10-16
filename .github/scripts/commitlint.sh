#!/usr/bin/env bash
# Checks commit subjects against Conventional Commits, as release-please
# reads them (CONTRIBUTING.md, "Commit messages").
#
#   commitlint.sh <rev-range>     every non-merge commit in the range (CI, make commitlint)
#   commitlint.sh --file <path>   one message file (the .githooks/commit-msg hook)
#
# A subject is "<type>[(<scope>)][!]: <description>", at most 100 characters.
# git's own "Revert "..."" subjects pass. The hook also lets through merge
# subjects and fixup!/squash! commits, which never reach main as such.
set -euo pipefail

types='feat|fix|perf|deps|revert|docs|refactor|test|build|ci|chore|style'
pattern="^(${types})(\([a-z0-9][a-z0-9._/-]*\))?!?: [^ ].*"
max=100

check() { # subject, label
  local subject=$1 label=$2
  if [[ ${#subject} -gt $max ]]; then
    echo "${label}: subject is ${#subject} characters (max ${max}): ${subject}" >&2
    return 1
  fi
  if [[ $subject =~ ^Revert\ \" ]] || [[ $subject =~ $pattern ]]; then
    return 0
  fi
  echo "${label}: not a Conventional Commit subject: ${subject}" >&2
  return 1
}

usage() {
  echo "usage: $0 <rev-range> | --file <commit-msg-file>" >&2
  exit 2
}

if [[ $# -eq 2 && $1 == --file ]]; then
  # First line that is neither blank nor a git comment.
  subject=$(grep -v -e '^#' -e '^[[:space:]]*$' "$2" | head -n 1 || true)
  if [[ $subject =~ ^(Merge\ |fixup!\ |squash!\ |amend!\ ) ]]; then
    exit 0
  fi
  if ! check "$subject" "commit message"; then
    echo "Expected <type>[(scope)][!]: <description>, with type one of: ${types//|/, }" >&2
    exit 1
  fi
  exit 0
fi

[[ $# -eq 1 && $1 != -* ]] || usage

# Outside the loop, so a bad range fails the script rather than checking nothing.
shas=$(git rev-list --no-merges "$1")

failed=0
count=0
for sha in $shas; do
  count=$((count + 1))
  subject=$(git log -1 --format=%s "$sha")
  check "$subject" "${sha:0:12}" || failed=$((failed + 1))
done

if [[ $failed -gt 0 ]]; then
  echo "${failed} of ${count} commits fail. Expected <type>[(scope)][!]: <description>, with type one of: ${types//|/, }" >&2
  echo "Reword them (git rebase -i) before merging; see CONTRIBUTING.md." >&2
  exit 1
fi
echo "${count} commits OK"
