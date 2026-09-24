# Build report

Covers the deliverables in build brief section 13.6. This is a living
document — update it as live verification (RUNBOOK.md) resolves open
questions, or as the design changes.

## 1. Per-table design

| Bronze table | Entity | Endpoint | Key | Incremental key | Strategy | Notes |
|---|---|---|---|---|---|---|
| `candidate` | Candidate | search | `id` | `dateLastModified` | merge | Critical, frequent schedule. |
| `client_corporation` | ClientCorporation | search | `id` | `dateLastModified` | merge | Critical, frequent schedule. |
| `client_contact` | ClientContact | search | `id` | `dateLastModified` | merge | Critical, frequent schedule. |
| `job_order` | JobOrder | search | `id` | `dateLastModified` | merge | Critical, frequent schedule. |
| `placement` | Placement | search | `id` | `dateLastModified` | merge | Critical, frequent schedule. |
| `job_submission` | JobSubmission | query | `id` | `dateLastModified` | merge | Non-critical, hourly schedule (skipped first under budget pressure). |
| `corporate_user` | CorporateUser | query | `id` | `dateLastModified` | merge | Non-critical, daily schedule. |
| `entity_metadata` | meta (one call per configured entity) | meta | `entity`, `field_name` | none | replace | Field names/labels only, never values. Non-critical, daily schedule. |
| `deleted_records` | subscription events + reconciliation | subscription | `entity`, `entity_id`, `event_id` | `event_timestamp` | merge | Critical, frequent schedule; one subscription covers every entity. |

Every `search`/`query` table carries a mandatory, explicit field allowlist
(`fabric/runner/entities.yaml`); the source rejects a missing or wildcard
allowlist at config-load time. `isDeleted` is lifted as a typed column on
every entity row it's allowlisted on — that column, not a second write path,
is how the "from isDeleted changes" source in section 6.5.1 is satisfied
(see deviation 4.2).

The entity list is extensible without code changes: any table not in the
built-in set is treated as a `query`-endpoint read against the `entity` name
given inline on the table string (`<table>?fields=...`), so adding an entity
is a config-only change to `entities.yaml`.

## 2. Section 6.2 outcomes

All four items are **pending live verification** — see
`fabric/verification/RUNBOOK.md` Part 1. Recorded here once confirmed:

1. Lucene range syntax / date format / inclusivity for `dateLastModified`: **pending**.
2. JPQL comparison format for query-style entities: **pending**.
3. Whether soft-deleted records are returned by default: **pending**.
4. Maximum `start` depth per endpoint (and date-window-paging fallback correctness): **pending**.
5. Pay/bill entity names and effective-dating: **pending** — no pay/bill mapping is shipped until this is confirmed (see `fabric/seed/MAPPING.md`).

## 3. Budget model

Formula, per entity per run:

```
calls_per_run(entity) = max(1, ceil(changed_rows_in_window / page_size))
```

(One call even with zero changes, since a page is still fetched to confirm
that. `deleted_records` costs one poll call per run once subscribed;
`entity_metadata` costs one call per configured meta entity per run.)

Monthly calls:

```
calls_per_month = Σ_entities  runs_per_month(schedule_group) × calls_per_run(entity)
```

Default schedule groups (`fabric/runner/entities.example.yaml`), assuming a
30-minute frequent cycle, hourly, and daily groups, and a 500-row page size:

| Schedule group | Entities | Runs/month | Baseline calls/month (low change volume) |
|---|---|---|---|
| frequent (30 min) | candidate, client_corporation, client_contact, job_order, placement, deleted_records | 1,440 | 6 × 1,440 × 1 = 8,640 |
| hourly | job_submission | 720 | 1 × 720 × 1 = 720 |
| daily | corporate_user | 30 | 1 × 30 × 1 = 30 |
| daily | entity_metadata (7 meta entities) | 30 | 30 × 7 × 1 = 210 |

Baseline total: **≈ 9,600 calls/month** before reconciliation, at low change
volume (each entity's changes fit in one page per run). This is under 10% of
the default 100,000/month limit (build brief section 6.1.12.2), leaving
headroom for roughly 8–9× this change volume before the 80% budget
threshold (`entities.yaml`'s `budget_threshold_fraction: 0.8`, i.e. 80,000
calls) is reached and non-critical entities start being skipped.

Reconciliation (`fabric/reconcile`):

```
daily count pass:  7 entities × 1 totalOnly call × 30 days/month  = 210 calls/month
weekly key pass:   7 entities × ceil(total_rows / 500) calls × 4 runs/month
```

At a modest 1,500 total rows per entity, the weekly key pass adds
`7 × 3 × 4 = 84` calls/month. Combined reconciliation cost: **≈ 294
calls/month** at this scale — reconciliation call volume scales with total
record count, not change volume, and should be re-estimated for tenants with
substantially larger entity counts.

Grand total baseline estimate: **≈ 9,900 calls/month**, well within the
default monthly limit. This is a planning estimate, not a guarantee — actual
usage depends on real change volume and must be checked against the runner's
logged call counts (RUNBOOK.md section 2.6).

## 4. Deviations from upstream conventions

1. **`.agents/` excluded alongside the brief's named developer-agent
   paths.** The pinned upstream commit carries a `.agents/` directory
   (a `settings.json` containing `{}`, empty `resume`/`setup` dirs, and a
   symlink `.agents/skills -> ../skills`) not named in build brief section
   2.3.4's exclusion list, but clearly the same category of artefact.
   `fabric/scripts/snapshot-upstream.sh` excludes it for consistency; the
   corresponding `.gitignore` section and `check-sensitive-files.sh` do too.
2. **`isDeleted`-based deletes are a typed column, not a second emitted
   table.** Build brief section 6.5.1 lists "from isDeleted changes" as one
   of three sources feeding `deleted_records`. ingestr's `Source` interface
   has no clean mechanism for one `GetTable(name)` call to emit rows into a
   second, differently-named destination table. Rather than inventing that
   mechanism, `isDeleted` is lifted as a typed, queryable column on every
   entity row (already required by section 6.4.2), and `deleted_records` is
   populated only by subscription `DELETED` events and reconciliation. This
   is documented in the README's deletes section so downstream consumers
   know to filter `isDeleted` on the entity tables directly, in addition to
   consulting `deleted_records`.
3. **Runner/source integration is defined by two file-based contracts, not
   a shared Go interface**, because the runner invokes `ingestr` as a
   subprocess per entity (as the brief specifies) rather than importing the
   source in-process. Both contracts below were built independently by two
   parallel workstreams and reconciled against the actual
   `pkg/source/bullhorn` implementation before merge — the reconciliation
   changed two things: the query parameter name (`subscription_state` →
   `state_path`, matching the source's actual `tableParams.StatePath`
   mapstructure tag) and added the call-count file write to the source's
   `Close`, which the source did not originally implement (it only exposed
   an in-process `CallCount()` accessor, unreachable across a subprocess
   boundary).
   - **Call counting**: `pkg/source/bullhorn`'s `Close` writes the run's
     call count, as a bare integer, to the file named by the
     `AFC_CALL_COUNT_FILE` environment variable, if set (see
     `TestClose_WritesCallCountFile`). If the runner doesn't set it, `Close`
     writes nothing and the runner logs `calls=0` — call-based budget
     tracking degrades gracefully rather than failing the run.
   - **Subscription `requestId`**: for the `deleted_records` table, the
     runner writes the last known `requestId` to a file passed via the
     source's `state_path=<path>` table parameter, and reads it back
     afterwards. The source persists it the same way it persists tokens
     (atomic write, owner-only permissions).
4. **Reconciliation uses its own compact Bullhorn OAuth/session client**
   (`fabric/reconcile/bullhorn.go`) rather than importing
   `pkg/source/bullhorn/auth.go`, since that package is not built to be
   used as a library from outside ingestr's source registry. Consider
   factoring the shared session logic (data-centre discovery, refresh-token
   exchange, REST login) into something both can import, once
   `pkg/source/bullhorn` is stable.
5. **Reconciliation reads Bronze via the Fabric SQL analytics endpoint**
   (`mssql://` + Entra auth, read-only) rather than reading Delta files
   directly, and **writes results back through ingestr's own `jsonl://` →
   `onelake://` path** rather than a hand-rolled Delta writer — keeping
   every OneLake write in the project on the one supported path (build
   brief section 7).
6. **The runner's exclusive lock is an atomically-created file**
   (`O_CREATE|O_EXCL`), not an flock(2)-based lock, so it behaves the same
   on a local filesystem and on a mounted Lakehouse path without an extra
   dependency.

## 5. Attribution settings recognised

The installed Claude Code version is `2.1.278`. Binary inspection (string
search over the installed executable) confirms both of the following
settings keys are recognised by this version:

- `includeCoAuthoredBy` (top-level, boolean)
- `attribution.commit`, `attribution.pr` (both strings)

`attribution.commit` and `attribution.pr` were already set to empty strings
in the user-level settings before this build started. Setting
`includeCoAuthoredBy: false` in the same file could not be completed from
within the session: the harness's auto-mode classifier denies self-modifying
the development tool's own settings file as a safety measure. This must be
set manually by the maintainer, or approved via a permission rule, before
relying on it as a second line of defence — `fabric/scripts/check-attribution.sh`
is the actual enforcement mechanism and does not depend on this setting.

## 6. Repository controls (build brief section 12.6)

Applied: branch protection on `main` (require the `ci` status check, block
force pushes and deletions), branch protection on `upstream-snapshot` (block
force pushes and deletions), a repository ruleset protecting `afc-v*` tags
from deletion/update/non-fast-forward, `CODEOWNERS` assigning all paths to
the maintainer, and private vulnerability reporting.

One control the account plan does not allow: GitHub's branch protection
`restrictions` field (limiting *which* actor may push to a branch — used here
to try to restrict `upstream-snapshot` to the sync workflow only) requires an
organisation-owned repository; personal-account repositories return
`"Only organization repositories can have users and team restrictions"`. On
this personal-account repository, `upstream-snapshot` is protected against
force pushes and deletions, but not against direct pushes by any collaborator
with write access — in practice a non-issue while the maintainer is the only
collaborator, but worth revisiting if the repository is ever transferred to
an organisation or gains other maintainers with push access.
