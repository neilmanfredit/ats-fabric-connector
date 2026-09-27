#!/usr/bin/env bash
# Fails if any tracked or staged file matches a pattern reserved for
# local-only, sensitive or excluded material (build brief section 2.4).
#
# Usage:
#   check-sensitive-files.sh staged   Check the index (pre-commit hook default)
#   check-sensitive-files.sh tracked  Check the full tree (CI default)
#
# With no argument, both are checked.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# Patterns are matched against repo-relative paths with `case`, so they use
# shell globs, not regex. Keep this list in sync with section 2.4 and the
# corresponding .gitignore section.
PATTERNS=(
  "briefs/*"
  "*BUILD_BRIEF*"
  "*.brief.md"
  ".env"
  ".env.*"
  "*.token"
  "*.pem"
  "*.key"
  "*.pfx"
  "secrets/*"
  "token_output*"
  "fabric/state/*"
  "fabric/verification/results/*"
  "*.duckdb"
  "*.duckdb.wal"
  "CLAUDE.local.md"
  ".claude/settings.local.json"
  "CLAUDE.md"
  "AGENTS.md"
  ".claude/*"
  "skills/*"
)

# *.parquet is sensitive everywhere except pkg/**/testdata/.
is_allowed_parquet() {
  case "$1" in
    pkg/*/testdata/*.parquet|pkg/*/testdata/*/*.parquet) return 0 ;;
    *) return 1 ;;
  esac
}

matches_pattern() {
  local path="$1"
  local pattern
  for pattern in "${PATTERNS[@]}"; do
    case "$path" in
      $pattern) return 0 ;;
    esac
  done
  case "$path" in
    *.parquet)
      if ! is_allowed_parquet "$path"; then
        return 0
      fi
      ;;
  esac
  return 1
}

check_file_list() {
  local label="$1"
  shift
  local -a bad=()
  local path
  for path in "$@"; do
    [ -z "$path" ] && continue
    if matches_pattern "$path"; then
      bad+=("$path")
    fi
  done
  if [ "${#bad[@]}" -gt 0 ]; then
    echo "check-sensitive-files: forbidden ${label} file(s) found:" >&2
    printf '  %s\n' "${bad[@]}" >&2
    return 1
  fi
  return 0
}

MODE="${1:-both}"
FAILED=0

if [ "$MODE" = "staged" ] || [ "$MODE" = "both" ]; then
  mapfile -d '' -t STAGED < <(git diff --cached --name-only -z || true)
  if ! check_file_list "staged" "${STAGED[@]:-}"; then
    FAILED=1
  fi
fi

if [ "$MODE" = "tracked" ] || [ "$MODE" = "both" ]; then
  mapfile -d '' -t TRACKED < <(git ls-files -z || true)
  if ! check_file_list "tracked" "${TRACKED[@]:-}"; then
    FAILED=1
  fi
fi

if [ "$FAILED" -ne 0 ]; then
  exit 1
fi

echo "check-sensitive-files: clean"
