#!/usr/bin/env bash
# Idempotently recreates the repository structure from the build brief
# (section 3.3). Never overwrites a file that already exists.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

DIRS=(
  ".github/ISSUE_TEMPLATE"
  ".github/workflows"
  "pkg/source/bullhorn/testdata"
  "docs/supported-sources"
  "fabric/githooks"
  "fabric/seed/mappings"
  "fabric/runner"
  "fabric/reconcile"
  "fabric/verification/results"
  "fabric/deploy"
  "fabric/scripts"
  "fabric/state"
)

FILES=(
  "README.md"
  "NOTICE"
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
  "pkg/source/bullhorn/bullhorn_test.go"
  "docs/supported-sources/bullhorn.md"
  "fabric/REPORT.md"
  "fabric/githooks/commit-msg"
  "fabric/githooks/pre-commit"
  "fabric/seed/MAPPING.md"
  "fabric/seed/run-seed.sh"
  "fabric/runner/entities.example.yaml"
  "fabric/runner/main.go"
  "fabric/runner/runner_test.go"
  "fabric/reconcile/main.go"
  "fabric/reconcile/reconcile_test.go"
  "fabric/verification/RUNBOOK.md"
  "fabric/deploy/Dockerfile"
  "fabric/deploy/containerapp-job.example.yaml"
)

for d in "${DIRS[@]}"; do
  mkdir -p "$d"
done

for f in "${FILES[@]}"; do
  if [ ! -e "$f" ]; then
    mkdir -p "$(dirname "$f")"
    : > "$f"
    echo "bootstrap: created empty placeholder $f" >&2
  fi
done

echo "bootstrap: structure present"
