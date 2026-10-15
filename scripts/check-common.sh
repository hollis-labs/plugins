# shellcheck shell=bash
# Guards shared by the three monorepos; sourced by check-layout after _common.sh.
# Sets `fail=1` through bad() on any violation.

# tracked_common FILES: checks that hold for every repository kind.
#   - no .gitmodules, no LFS filter in .gitattributes, no tracked file over MAX_BYTES
#   - nothing loose at the root except ROOT_FILES; only RESERVED_DIRS and project dirs
#   - project directory names are valid
#   - go.work / go.work.sum are not allowed outside imported archive content (kind-specific)
#   - scan-public.allow.d entries have a rule, a glob and a reason, with no address or secret-like text
check_common() {
  local f top size
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    [ -e "$REPO_ROOT/$f" ] || continue
    case "$(basename "$f")" in
      .gitmodules) bad "$f: submodules are not allowed; an owner decision is needed" ;;
      .gitattributes)
        if grep -Eq 'filter=lfs' "$REPO_ROOT/$f" 2>/dev/null; then
          bad "$f: Git LFS is not allowed; an owner decision is needed"
        fi
        ;;
    esac
    size="$(wc -c <"$REPO_ROOT/$f")"
    if [ "$size" -gt "$MAX_BYTES" ]; then
      bad "$f: $size bytes, over the 10 MiB limit; an owner decision is needed"
    fi
    top="${f%%/*}"
    if [ "$top" = "$f" ]; then
      is_root_file "$f" || bad "$f: loose file at the repository root (allowed: $ROOT_FILES)"
    elif ! is_reserved "$top"; then
      valid_name "$top" || bad "$f: '$top' is not a valid directory name (lower case, digits, '.', '_', '-')"
    fi
  done <<EOF2
$(repo_files)
EOF2

  # Allowlist hygiene: RULE-ID PATH-GLOB REASON; the reason must not carry values or addresses.
  local a n line reason
  for a in "$REPO_ROOT"/scan-public.allow.d/*.allow "$REPO_ROOT"/scan-public.allow; do
    [ -f "$a" ] || continue
    n=0
    while IFS= read -r line || [ -n "$line" ]; do
      n=$((n + 1))
      case "$line" in '' | '#'*) continue ;; esac
      read -r _ _ reason <<<"$line"
      if [ -z "${reason:-}" ]; then
        bad "${a#"$REPO_ROOT"/}:$n: want 'RULE-ID PATH-GLOB REASON'; a reason is required"
        continue
      fi
      if grep -Eq '([0-9]{1,3}\.){3}[0-9]{1,3}|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}|[A-Za-z0-9_-]{32,}|://' <<<"$reason"; then
        bad "${a#"$REPO_ROOT"/}:$n: the reason looks like it holds an address, URL or secret value; describe, do not quote"
      fi
    done <"$a"
  done
}
