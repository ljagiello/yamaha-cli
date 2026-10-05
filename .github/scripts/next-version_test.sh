#!/usr/bin/env bash
# Tests next-version.sh in throwaway git repos:
#   .github/scripts/next-version_test.sh
set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/next-version.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Keep the user's git config (signing, hooks, identity) out of the repos.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

failures=0

# check WANT BUMP COMMIT...
# Builds a repo with one commit per COMMIT, oldest first; each COMMIT is a
# comma-separated list of tags to put on it, or "-" for none. The last
# commit is HEAD. WANT is the expected output, or "fail" for an error exit.
check() {
  local want=$1 bump=$2 dir got commit tag tags
  shift 2
  dir=$(mktemp -d "$tmp/repo.XXXXXX")
  git -C "$dir" init -q -b main
  for commit in "$@"; do
    git -C "$dir" commit -q --allow-empty -m "$commit"
    if [ "$commit" != - ]; then
      IFS=, read -ra tags <<<"$commit"
      for tag in "${tags[@]}"; do
        git -C "$dir" tag "$tag"
      done
    fi
  done
  got=$(cd "$dir" && "$script" "$bump" 2>/dev/null) || got=fail
  if [ "$got" = "$want" ]; then
    echo "ok   $bump after [$*] -> $got"
  else
    echo "FAIL $bump after [$*] -> $got, want $want"
    failures=$((failures + 1))
  fi
}

# No release tags: count from v0.0.0.
check v0.0.1 patch -
check v0.1.0 minor -
check v1.0.0 major -

check v0.1.1 patch v0.1.0 -
check v0.1.10 patch v0.1.9 -
check v1.2.4 patch v1.2.3 -
check v1.3.0 minor v1.2.3 -
check v2.0.0 major v1.2.3 -

# Tags that are not vMAJOR.MINOR.PATCH are ignored, even when they look higher.
check v0.0.1 patch v1.2,foo,v1.2.3-rc.1 -
check v1.3.0 minor v1.2.3 v2.0,foo,v9.0.0-rc.1,v1.09.0 -

# Highest by version, not by name (v0.9.0 > v0.10.0) or creation order.
check v0.11.0 minor v0.10.0 v0.9.0 -

# A commit is released once; a prerelease tag on HEAD doesn't count.
check fail patch v0.1.0
check fail patch v0.1.0 foo,v0.2.0
# GoReleaser then publishes v1.0.0, not the rc, via git.prerelease_suffix.
check v1.0.0 major v0.9.9 v1.0.0-rc.1

check fail bogus -

[ "$failures" -eq 0 ]
