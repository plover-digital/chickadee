#!/usr/bin/env bash
# Fail before build/install/evidence if the checkout does not represent HEAD.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -n $(git status --porcelain=v1 --untracked-files=all) ]]; then
  echo 'Vulnerability evidence requires a clean committed checkout (including untracked files).' >&2
  exit 1
fi
# Ignored generated Go/assembly/module files can still be Go build inputs.
# Ordinary ignored binary and report outputs are allowed.
if [[ -n $(git ls-files --others --ignored --exclude-standard -- '*.go' '*.s' '*.S' '*.syso' '*.c' '*.h' go.mod go.sum go.work go.work.sum) ]]; then
  echo 'Vulnerability evidence refuses ignored untracked Go build inputs.' >&2
  exit 1
fi
case ${GOFLAGS:-} in
  *-overlay*|*-modfile*)
    echo 'Vulnerability evidence refuses external source/module overrides in GOFLAGS.' >&2
    exit 1 ;;
esac
