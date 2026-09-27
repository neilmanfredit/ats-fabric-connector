#!/usr/bin/env bash
# Loads each entity's initial full extract from Bullhorn Data Replication
# through ingestr's mssql:// source (query: prefix) into the same OneLake
# Bronze tables the API source writes to, and records the initial watermark
# for each table (build brief section 8).
#
# Run this off-peak, after a database administrator has reviewed the mapping
# queries in fabric/seed/mappings/ for locking impact (section 8.5). This
# script does not enforce that review; it is an operational precaution.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

: "${MSSQL_URI:?Set MSSQL_URI to the Data Replication mssql:// connection string}"
: "${ONELAKE_URI:?Set ONELAKE_URI to onelake://<workspace>/<lakehouse>?use_azure_default_credential=true}"
INGESTR_BIN="${AFC_INGESTR_BIN:-ingestr}"

export INGESTR_DISABLE_TELEMETRY=true
export DISABLE_TELEMETRY=true

# Tables that refresh only once per day in Data Replication (build brief
# section 8.4) get their first API window backdated by at least 24h from the
# seed watermark, so no change in that window is missed. None of the default
# nine tables are documented as daily-only (see MAPPING.md) — add an entry
# here per-tenant if the runbook finds otherwise, e.g. DAILY_REFRESH_TABLES[job_order]=1.
declare -A DAILY_REFRESH_TABLES=()

MAPPINGS_DIR="fabric/seed/mappings"
STATE_DIR="fabric/state/seed"
mkdir -p "$STATE_DIR"

seed_table() {
  local table="$1"
  local sql_file="$MAPPINGS_DIR/${table}.sql"
  if [ ! -f "$sql_file" ]; then
    echo "run-seed: no mapping for table '$table' at $sql_file" >&2
    return 1
  fi

  local query
  query="$(cat "$sql_file")"
  local started_at
  started_at="$(date -u +%s)"

  echo "run-seed: seeding $table" >&2
  "$INGESTR_BIN" ingest \
    --source-uri="$MSSQL_URI" \
    --source-table="query:${query}" \
    --dest-uri="$ONELAKE_URI" \
    --dest-table="bullhorn.${table}" \
    --primary-key=id \
    --incremental-strategy=merge \
    --yes

  local watermark_ms=$((started_at * 1000))
  if [ -n "${DAILY_REFRESH_TABLES[$table]:-}" ]; then
    watermark_ms=$((watermark_ms - 86400000)) # back up 24h (section 8.4)
  fi
  echo "$watermark_ms" >"$STATE_DIR/${table}.json"
  echo "run-seed: recorded initial watermark for $table: ${watermark_ms}ms" >&2
}

DEFAULT_TABLES=(candidate client_corporation client_contact job_order placement job_submission corporate_user)

# Accepts table names positionally, or via one or more --entity <table> pairs
# (the latter matches the README's quick-start examples).
TABLES=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --entity)
      TABLES+=("${2:?--entity requires a table name}")
      shift 2
      ;;
    *)
      TABLES+=("$1")
      shift
      ;;
  esac
done
if [ "${#TABLES[@]}" -eq 0 ]; then
  TABLES=("${DEFAULT_TABLES[@]}")
fi

for t in "${TABLES[@]}"; do
  seed_table "$t"
done

echo "run-seed: done" >&2
