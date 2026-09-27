#!/usr/bin/env bash
# Populates the current branch's working tree from a pinned or latest-tagged
# commit of bruin-data/ingestr, strips the excluded upstream developer-agent
# files and any accidentally-present sensitive paths, verifies the result,
# and commits the snapshot (build brief sections 2.3.4, 2.4.3, 3.2, 12.3).
#
# The branch receiving the commit (normally `upstream-snapshot`) must contain
# nothing but this snapshot: no fabric/ tooling, no README replacement. Those
# live only on `main` and feature branches, added on top after the snapshot.
set -euo pipefail

UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/bruin-data/ingestr.git}"
REPO_ROOT="$(git rev-parse --show-toplevel)"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat >&2 <<'EOF'
Usage: snapshot-upstream.sh (--sha <sha> | --tag <tag>)
  --sha <sha>   Import this exact upstream commit (used for the initial import).
  --tag <tag>   Import this exact upstream tag.
With no argument, resolves and imports the latest upstream release tag
(used by the weekly afc-upstream-sync workflow).

Run this with the working tree checked out on the branch that should receive
the snapshot commit (normally `upstream-snapshot`), with no other pending
changes: the script deletes everything in the working tree except .git.
EOF
}

REF_KIND=""
REF_VALUE=""
case "${1:-}" in
  --sha)
    REF_KIND="sha"
    REF_VALUE="${2:?missing sha}"
    ;;
  --tag)
    REF_KIND="tag"
    REF_VALUE="${2:?missing tag}"
    ;;
  "")
    REF_KIND="tag"
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 2
    ;;
esac

if [ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]; then
  echo "snapshot-upstream: working tree is not clean; commit or stash first" >&2
  exit 1
fi

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

echo "snapshot-upstream: cloning ${UPSTREAM_URL}" >&2
git clone --quiet "$UPSTREAM_URL" "$WORKDIR/upstream"

if [ "$REF_KIND" = "tag" ] && [ -z "$REF_VALUE" ]; then
  REF_VALUE="$(git -C "$WORKDIR/upstream" tag --list --sort=-v:refname | head -n1)"
  if [ -z "$REF_VALUE" ]; then
    echo "snapshot-upstream: no upstream tags found" >&2
    exit 1
  fi
  echo "snapshot-upstream: resolved latest tag ${REF_VALUE}" >&2
fi

git -C "$WORKDIR/upstream" checkout --quiet "$REF_VALUE"
RESOLVED_SHA="$(git -C "$WORKDIR/upstream" rev-parse HEAD)"
rm -rf "$WORKDIR/upstream/.git"

# Excluded upstream developer-agent files/dirs (section 2.3.4). `.agents` is
# not named explicitly in the brief but is the same category of artefact
# (settings.json, resume/setup dirs, a symlink into skills/) — excluded for
# consistency; see fabric/REPORT.md deviations.
for p in CLAUDE.md AGENTS.md .claude skills .agents; do
  rm -rf "${WORKDIR:?}/upstream/${p}"
done

# Defensive: also strip anything matching the section 2.4 sensitive/local
# patterns, in case a future upstream release ever introduces one.
SENSITIVE_GLOBS=(
  "briefs" "*BUILD_BRIEF*" "*.brief.md"
  ".env" ".env.*" "*.token" "*.pem" "*.key" "*.pfx" "secrets" "token_output*"
  "*.duckdb" "*.duckdb.wal"
  "CLAUDE.local.md"
)
for g in "${SENSITIVE_GLOBS[@]}"; do
  find "$WORKDIR/upstream" -mindepth 1 -name "$g" -exec rm -rf {} + 2>/dev/null || true
done

echo "snapshot-upstream: clearing working tree" >&2
find "$REPO_ROOT" -mindepth 1 -maxdepth 1 ! -name '.git' -exec rm -rf {} +

echo "snapshot-upstream: copying upstream tree" >&2
cp -a "$WORKDIR/upstream/." "$REPO_ROOT/"

cd "$REPO_ROOT"
git add -A

if [ "$REF_KIND" = "sha" ]; then
  MSG="Import upstream ingestr at ${RESOLVED_SHA}"
else
  MSG="Import upstream ingestr ${REF_VALUE} at ${RESOLVED_SHA}"
fi

echo "snapshot-upstream: verifying" >&2
"$SCRIPT_DIR/check-sensitive-files.sh" staged
"$SCRIPT_DIR/check-attribution.sh" text "$MSG"

git commit --quiet -m "$MSG"
echo "snapshot-upstream: committed: ${MSG}" >&2
