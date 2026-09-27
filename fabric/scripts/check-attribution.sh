#!/usr/bin/env bash
# Scans commit messages and supplied text for AI attribution markers, which
# must never appear in tracked history (build brief section 2.3): Co-Authored-By
# trailers naming an AI tool, "Generated with" footers, and robot emoji markers.
set -euo pipefail

# Case-insensitive; matches a Co-Authored-By trailer naming a known AI tool,
# a "Generated with/by <tool>" footer, or a robot emoji marker.
FORBIDDEN_REGEX='co-authored-by:[^ ]* *(claude|anthropic|copilot|cursor|chatgpt|openai|gemini|codeium)|generated (with|by)[^.]*(claude|anthropic|copilot|cursor|chatgpt|openai|gemini|ai\b)|🤖'

scan_text() {
  local label="$1"
  local text="$2"
  if printf '%s' "$text" | grep -iEq "$FORBIDDEN_REGEX"; then
    echo "check-attribution: forbidden AI attribution marker found in ${label}:" >&2
    printf '%s\n' "$text" | grep -iE "$FORBIDDEN_REGEX" >&2
    return 1
  fi
  return 0
}

usage() {
  cat >&2 <<'EOF'
Usage:
  check-attribution.sh commit-msg-file <path>   Check a single commit message file
  check-attribution.sh range <rev-range>        Check every commit message in a rev range
  check-attribution.sh env <VAR_NAME>           Check the content of an environment variable
  check-attribution.sh text <string>            Check a literal string argument
  check-attribution.sh stdin                    Check text piped on stdin
EOF
}

if [ "$#" -lt 1 ]; then
  usage
  exit 2
fi

MODE="$1"
shift
FAILED=0

case "$MODE" in
  commit-msg-file)
    [ "$#" -eq 1 ] || { usage; exit 2; }
    scan_text "commit message ($1)" "$(cat "$1")" || FAILED=1
    ;;
  range)
    [ "$#" -eq 1 ] || { usage; exit 2; }
    while IFS= read -r -d $'\x1e' msg; do
      [ -z "$msg" ] && continue
      scan_text "commit in range $1" "$msg" || FAILED=1
    done < <(git log "$1" --format=%B%x1e 2>/dev/null || true)
    ;;
  env)
    [ "$#" -eq 1 ] || { usage; exit 2; }
    scan_text "environment variable $1" "${!1:-}" || FAILED=1
    ;;
  text)
    [ "$#" -eq 1 ] || { usage; exit 2; }
    scan_text "supplied text" "$1" || FAILED=1
    ;;
  stdin)
    scan_text "stdin" "$(cat)" || FAILED=1
    ;;
  *)
    usage
    exit 2
    ;;
esac

if [ "$FAILED" -ne 0 ]; then
  exit 1
fi

echo "check-attribution: clean"
