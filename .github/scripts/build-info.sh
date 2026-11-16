#!/usr/bin/env bash
set -euo pipefail

version=${GITHUB_REF##*/}
commit_sha=$(git rev-parse HEAD)
build_date=$(date -u +%Y-%m-%d)
branch=$(git branch --show-current)
if [[ "$GITHUB_REF" == refs/heads/* ]]; then
  branch=${GITHUB_REF#refs/heads/}
fi

printf 'version=%s\ncommit_sha=%s\nbuild_date=%s\nbranch=%s\n' \
  "$version" "$commit_sha" "$build_date" "${branch:-unknown}"
