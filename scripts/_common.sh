# shellcheck shell=bash
# Shared helpers for the scripts in this directory. Source it; do not run it.

set -euo pipefail

# A user's CDPATH makes `cd` print the directory, which corrupts $(cd ...) captures.
unset CDPATH

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=repo.conf
. "$SCRIPT_DIR/repo.conf"

# Each Go module is built with its own go.mod only. A developer's go.work must not leak in.
export GOWORK=off

die() {
  echo "error: $*" >&2
  exit 1
}

# A file larger than this anywhere in a source's history stops an import (owner decision).
export MAX_BYTES=$((10 * 1024 * 1024))

# Directories that are repository infrastructure, not imported repositories.
RESERVED_DIRS="scripts .github scan-public.allow.d docs"
# Files that may sit at the repository root.
ROOT_FILES="AGENTS.md README.md LICENSE .gitignore .gitleaksignore"

is_reserved() {
  local d
  for d in $RESERVED_DIRS; do
    [ "$d" = "$1" ] && return 0
  done
  return 1
}

is_root_file() {
  local f
  for f in $ROOT_FILES; do
    [ "$f" = "$1" ] && return 0
  done
  return 1
}

# Print what a commit could contain: tracked files plus untracked files that are not ignored.
repo_files() {
  git -C "$REPO_ROOT" ls-files --cached --others --exclude-standard
}

# Print the top-level directories that hold imported repositories, one per line.
project_dirs() {
  repo_files | awk -F/ 'NF > 1 { print $1 }' | sort -u | while IFS= read -r d; do
    is_reserved "$d" || echo "$d"
  done
}

# Print the Go modules (every plugin): <name> for every <name>/go.mod.
modules() {
  local d
  for d in $(project_dirs); do
    [ -f "$REPO_ROOT/$d/go.mod" ] && echo "$d"
  done
  return 0
}

valid_module() {
  local all
  all="$(modules)"
  grep -qxF -- "$1" <<<"$all"
}

# Escape the dots in a path for use inside an extended regular expression.
re_escape() {
  printf '%s' "$1" | sed 's/\./\\./g'
}

# Names of imported repositories: lower case, digits, '.', '_' and '-', not starting with a dot.
valid_name() {
  grep -Eq '^[a-z0-9][a-z0-9._-]*$' <<<"$1"
}
