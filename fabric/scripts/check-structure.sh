#!/usr/bin/env bash
# Confirms the repository matches the structure in the build brief (section
# 3.3). Called by the pre-commit hook and by CI.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

REQUIRED_PATHS=(
  "README.md"
  "NOTICE"
  "LICENSE"
  "THIRD_PARTY_LICENSES.txt"
  "SECURITY.md"
  "CONTRIBUTING.md"
  ".github/CODEOWNERS"
  ".github/ISSUE_TEMPLATE/tester-report.yml"
  ".github/ISSUE_TEMPLATE/bug-report.yml"
  ".github/pull_request_template.md"
  ".github/workflows/afc-ci.yml"
  ".github/workflows/afc-upstream-sync.yml"
  ".github/workflows/afc-release.yml"
  ".github/workflows/afc-sandbox-verify.yml"
  "pkg/source/bullhorn/register.go"
  "pkg/source/bullhorn/bullhorn.go"
  "pkg/source/bullhorn/auth.go"
  "pkg/source/bullhorn/testdata"
  "pkg/source/bullhorn/bullhorn_test.go"
  "docs/supported-sources/bullhorn.md"
  "fabric/REPORT.md"
  "fabric/githooks/commit-msg"
  "fabric/githooks/pre-commit"
  "fabric/seed/MAPPING.md"
  "fabric/seed/mappings"
  "fabric/seed/run-seed.sh"
  "fabric/runner/entities.example.yaml"
  "fabric/runner/main.go"
  "fabric/runner/runner_test.go"
  "fabric/reconcile/main.go"
  "fabric/reconcile/reconcile_test.go"
  "fabric/verification/RUNBOOK.md"
  "fabric/deploy/Dockerfile"
  "fabric/deploy/containerapp-job.example.yaml"
  "fabric/scripts/bootstrap.sh"
  "fabric/scripts/check-structure.sh"
  "fabric/scripts/check-attribution.sh"
  "fabric/scripts/check-sensitive-files.sh"
  "fabric/scripts/snapshot-upstream.sh"
)

MISSING=()
for p in "${REQUIRED_PATHS[@]}"; do
  if [ ! -e "$p" ]; then
    MISSING+=("$p")
  fi
done

if [ "${#MISSING[@]}" -gt 0 ]; then
  echo "check-structure: missing required path(s):" >&2
  printf '  %s\n' "${MISSING[@]}" >&2
  exit 1
fi

echo "check-structure: clean"
