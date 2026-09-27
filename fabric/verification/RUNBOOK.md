# Live verification runbook

This runbook is for the human operator (build brief section 2.2.3). It is
the only place live Bullhorn behaviour is checked — the development session
never calls a Bullhorn endpoint, holds credentials, or reads response
content. Every command below returns **aggregates only**: counts, column
names and types, durations, call volumes, error codes. Never paste raw
record content, credentials or tenant identifiers into an issue, PR, or this
runbook's results files.

Results go in `fabric/verification/results/` (git-ignored, never committed;
build brief section 2.4.3). Copy the templates below into a dated file per
check, e.g. `fabric/verification/results/2026-09-24-open-questions.md`.

## Part 1 — Confirming open questions (build brief section 6.2)

These four items are assumed in the code but not confirmed against a live
tenant. Confirm each once, then update `fabric/REPORT.md` and, if the code
needs to change, open a PR against `main`.

### 1.1 Lucene range syntax, date format, and inclusivity for `dateLastModified`

```
ingestr ingest --debug \
  --source-uri="bullhorn://?client_id=$BULLHORN_CLIENT_ID&refresh_token=$BULLHORN_REFRESH_TOKEN&username=$BULLHORN_USERNAME&data_center=$BULLHORN_DATA_CENTER&rate_limit=1&token_output=/tmp/token.json" \
  --source-table="candidate?fields=id,dateLastModified" \
  --dest-uri="duckdb:///tmp/afc_verify.duckdb" --dest-table=main.candidate_probe \
  --interval-start="<a known-changed record's timestamp minus 1s>" \
  --interval-end="<same timestamp plus 1s>" --yes
```

Template:

```
Date: <date>
Operator: <name>
Query syntax tried: <exact query string, redact nothing sensitive is in it>
Rows returned: <count>
Inclusive on lower bound? <yes/no>
Inclusive on upper bound? <yes/no>
Date format Bullhorn accepted: <e.g. epoch ms, ISO-8601>
Notes:
```

### 1.2 JPQL comparison format for query-style entities

Same idea against a `query`-endpoint table (e.g. `job_submission`), varying
the `where` operator (`>`, `>=`, `BETWEEN`) and recording which forms
Bullhorn accepts and their exact date-literal syntax.

### 1.3 Whether top-level soft-deleted records are returned by default

```
ingestr ingest --debug \
  --source-uri="bullhorn://...&rate_limit=1&token_output=/tmp/token.json" \
  --source-table="candidate?fields=id,isDeleted" \
  --dest-uri="duckdb:///tmp/afc_verify.duckdb" --dest-table=main.candidate_deleted_probe --yes
duckdb /tmp/afc_verify.duckdb -c "SELECT isDeleted, COUNT(*) FROM main.candidate_deleted_probe GROUP BY 1"
```

Template:

```
Date: <date>
Entity tested: <entity>
Soft-deleted rows present without an explicit filter? <yes/no>
Count of isDeleted=true rows: <count>
Count of isDeleted=false rows: <count>
Notes:
```

### 1.4 Maximum `start` depth per endpoint

For one high-volume entity, page through with `count`/`start` until Bullhorn
errors or stops returning new rows. Record the endpoint, the `count` value
used, and the `start` value at which behaviour changed. If a ceiling exists,
confirm the fallback (paging by advancing the date window instead of the
offset) returns the full expected count with no duplicates.

```
Date: <date>
Entity/endpoint: <entity>
count used: <n>
start ceiling observed (if any): <n or "none found up to X">
Total rows via offset paging: <count>
Total rows via date-window paging: <count>
Match? <yes/no>
```

### 1.5 Pay and bill entity names and effective-dating

Ask the Bullhorn account team or a Bullhorn admin (not the REST API) for the
pay/bill entity names licensed on this tenant, and whether they are
effective-dated. Do not guess a live query against an unconfirmed entity
name.

```
Date: <date>
Pay/bill entities licensed: <list, or "none">
Effective-dated? <yes/no/unknown>
Notes:
```

## Part 2 — Acceptance checks (build brief section 11.2)

Run these after the seed and the first scheduled runner/reconcile cycle.

### 2.1 Seed + first incremental merge, no duplicate keys

```
fabric/seed/run-seed.sh candidate
# wait for the runner's next scheduled candidate run, or invoke it directly
AFC_SCHEDULE_GROUP=frequent ./afc-runner
```

Then, via the Fabric SQL analytics endpoint:

```sql
SELECT id, COUNT(*) FROM bullhorn.candidate GROUP BY id HAVING COUNT(*) > 1;
```

```
Date: <date>
Duplicate id count: <should be 0>
```

### 2.2 Updates flow through

Have an admin change one field on a known test record in the Bullhorn UI,
wait for the next scheduled run, then check:

```sql
SELECT dateLastModified FROM bullhorn.candidate WHERE id = <test id>;
```

```
Date: <date>
dateLastModified before: <value>
dateLastModified after: <value>
Time from UI change to row landing in Bronze: <duration>
```

### 2.3 Soft and hard deletes reach `deleted_records`

Soft-delete one test record; if licensed, hard-delete another. Wait for the
runner's `deleted_records` run.

```sql
SELECT * FROM bullhorn.deleted_records WHERE entity_id IN (<ids>);
```

```
Date: <date>
Soft delete captured? <yes/no>, event_type: <value>
Hard delete captured? <yes/no>, event_type: <value>
```

### 2.4 New fields become columns

Add a field to an entity's allowlist in `entities.yaml` that has never been
selected before, run the entity, and confirm the column appears without
failing the load (build brief section 7.2).

```
Date: <date>
Field added: <name>
Load succeeded without error? <yes/no>
Column present with correct type? <yes/no>
```

### 2.5 Forced failure leaves state unchanged, next run recovers

Kill the runner mid-run (e.g. `kill -9` the process during a large entity's
read), then re-run.

```
Date: <date>
Watermark for the killed entity before: <value>
Watermark for the killed entity after the killed run: <should be unchanged>
Watermark after the next successful run: <advanced>
Any duplicate or missing rows from the recovery run? <describe>
```

### 2.6 Measured calls match the budget model

Run a full cycle (all entities) once, and compare the runner's logged call
count against `fabric/REPORT.md`'s budget model formula.

```
Date: <date>
Entities run: <list>
Calls logged by the runner: <total>
Calls predicted by the budget model for this change volume: <total>
Difference and likely cause: <notes>
```
