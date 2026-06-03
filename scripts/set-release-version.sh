#!/usr/bin/env bash
# Pin launcher production VERSION to a release tag or digest alias.
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: $0 <release-tag-or-digest-alias> [env-file]" >&2
  exit 1
fi

release="$1"
env_file="${2:-.env.production}"

if [ ! -f "$env_file" ]; then
  echo "Missing env file: $env_file" >&2
  exit 1
fi

tmp="$(mktemp)"
awk -F= -v rel="$release" '
BEGIN { updated=0 }
$1=="VERSION" {
  print "VERSION=" rel
  updated=1
  next
}
{ print $0 }
END {
  if (!updated) {
    print "VERSION=" rel
  }
}
' "$env_file" > "$tmp"
mv "$tmp" "$env_file"

echo "Pinned VERSION=$release in $env_file"
