#!/usr/bin/env bash
# Usage: next-version.sh patch|minor|major
#
# Prints the tag that follows the highest vMAJOR.MINOR.PATCH tag in the
# current git repository, counting from v0.0.0 when there is none. Other
# tags (v1.2, v1.2.3-rc.1, foo) are ignored. Fails if HEAD already carries
# a release tag, so a commit is never released twice, and from v2 on if
# the go.mod module path doesn't end in /vMAJOR.
set -euo pipefail

bump=${1:-}
case $bump in
  patch | minor | major) ;;
  *)
    echo "usage: $0 patch|minor|major" >&2
    exit 2
    ;;
esac

release='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

head_tags=$(git tag --points-at HEAD)
while read -r tag; do
  if [[ $tag =~ $release ]]; then
    echo "HEAD is already released as $tag" >&2
    exit 1
  fi
done <<<"$head_tags"

# Ascending version order, so the last release tag is the highest.
tags=$(git tag --list --sort=v:refname)
major=0 minor=0 patch=0
while read -r tag; do
  if [[ $tag =~ $release ]]; then
    major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]} patch=${BASH_REMATCH[3]}
  fi
done <<<"$tags"

case $bump in
  major) major=$((major + 1)) minor=0 patch=0 ;;
  minor) minor=$((minor + 1)) patch=0 ;;
  patch) patch=$((patch + 1)) ;;
esac
next=v$major.$minor.$patch

# From v2 on, Go only accepts the tag if the module path ends in /vMAJOR.
if [ "$major" -ge 2 ]; then
  gomod=$(git rev-parse --show-toplevel)/go.mod
  module=$(sed -n 's/^module[[:space:]]*//p' "$gomod" 2>/dev/null || true)
  if [[ $module != */v$major ]]; then
    echo "module path must end in /v$major before releasing $next" \
      "(see go.dev/ref/mod#major-version-suffixes)" >&2
    exit 1
  fi
fi

echo "$next"
