# Data Replication → API field mapping

This maps Bullhorn Data Replication (B5) columns to the REST API field names
used by `pkg/source/bullhorn`, for the seed load's `mappings/*.sql` files.

Bullhorn's public documentation for Data Replication (the KB article at B5)
describes its purpose and constraints but does not publish an exact
column-by-column schema — that comes from the "Bullhorn Data Replication
Schema" spreadsheet Bullhorn provides to customers with Data Replication
enabled, which is not publicly accessible and cannot be fetched from this
development session (build brief section 2.2.1). The mappings below follow
the well-documented convention that Data Replication mirrors the same
underlying application data the REST API reads, so table names match entity
names and most scalar column names match API field names directly. Anything
not confirmed this way is marked **to confirm via runbook**.

## Confirmed from B5 (kb.bullhorn.com, understandingDataMirror.htm)

- Data Replication "mirrors the same data an admin would see in the Bullhorn
  application, including full SSNs" — **SSNs and other national identifiers
  are not encrypted in Data Replication**. The seed mapping queries below
  never select these fields, matching the allowlist exclusion in build brief
  section 2.2.5.
- Some entities refresh only once per day as part of the "Master process":
  the article names `CorporationDepartment`, `BusinessSector`, `Categories`,
  `Skills`, `Specialties`, `PrivateLabel` and `Field Maps`. None of these are
  in the default table set (build brief section 6.4), so none of the nine
  seeded tables are expected to be daily-refresh-only — **to confirm via
  runbook** for the specific tenant, since refresh cadence can be configured
  per Bullhorn instance.
- Data Replication is "designed for business intelligence and data
  analysis", not as "a data source for payroll, billing, or similar systems"
  — this is why the seed load only covers the initial full extract, and all
  ongoing incremental loads go through the API source (README section
  "Why an API-based feed").

## General mapping rules

1. Table name = entity name (e.g. the `Candidate` API entity maps to a
   `Candidate` table in the replication database).
2. A scalar (non-association, non-computed) API field maps to a
   same-named column, matching the case Bullhorn uses in its schema
   (typically PascalCase in SQL, camelCase in the API — `mappings/*.sql`
   aliases every column to the camelCase API name).
3. To-many associations (e.g. a Candidate's certifications, a JobOrder's
   submissions) are not columns on the root table in either the API or the
   replication database — they live in join/bridge tables. The seed load
   does not attempt to replicate to-many associations; the API source keeps
   them as JSON on the owning row where the API itself embeds them, and the
   seed's first incremental API window (section 8.4) picks up what the seed
   query does not cover.
4. Custom fields appear as `customText1`..`customTextN`,
   `customDate1`..`customDateN` etc. in both the API and the replication
   schema. Only include a custom field in a mapping query if it is also in
   the corresponding entity's field allowlist in `fabric/runner/entities.yaml`.
5. `isDeleted` and `dateLastModified` are expected to exist as columns on
   every top-level, non-lookup entity table — **to confirm via runbook**
   (section 6.2.2: whether soft-deleted rows are included by default in a
   plain `SELECT` against the replication table, or excluded by a view).

## Per-table mapping

| Bronze table | Replication table (assumed) | Notes |
|---|---|---|
| `candidate` | `Candidate` | Scalar fields map 1:1 by name. `owner` and `source`/`candidateSource` are foreign-key columns in the replication schema (`ownerID` or similar) rather than the API's embedded object — **to confirm via runbook**: exact FK column name and whether the seed query needs a join to `CorporateUser` for the owner's name, or whether the runner is content with just the ID (recommended, to keep the seed query simple and match the API's own `id`-only embedded reference when fields aren't expanded). |
| `client_corporation` | `ClientCorporation` | `industryList` is a many-to-many association in the API; not selected by the seed query (see rule 3). |
| `client_contact` | `ClientContact` | `clientCorporation` is a foreign key (`clientCorporationID` or similar) — **to confirm via runbook** for exact column name. |
| `job_order` | `JobOrder` | `clientCorporation` likewise a foreign key. `numOpenings` commonly appears as `numOpenings` or `openings` in the replication schema — **to confirm via runbook**. |
| `placement` | `Placement` | `candidate`, `jobOrder`, `clientCorporation` are all foreign keys. Placement is effective-dated in some Bullhorn configurations (pay/bill add-ons) — **to confirm via runbook** (section 6.2.4). |
| `job_submission` | `JobSubmission` | `candidate`, `jobOrder`, `sendingUser` are foreign keys. |
| `corporate_user` | `CorporateUser` | `departments` is a many-to-many association; not selected by the seed query. |
| `entity_metadata` | *(none)* | Field metadata is not present in Data Replication; this table is populated only by the API source's `meta` endpoint, never seeded. |
| `deleted_records` | *(none)* | Populated only by the API source's subscription reads and by `fabric/reconcile`, never seeded — Data Replication has no equivalent of Bullhorn's event stream. |

## Pay and bill entities (build brief section 6.2.4)

Bullhorn's pay and bill functionality (where licensed) introduces additional
entities not covered by the default table set — their exact names
(commonly `Timesheet`/`PayableCharge`/`InvoiceTerm`-shaped, naming varies by
Bullhorn edition) and whether they are effective-dated like some Placement
configurations, are **to confirm via runbook**. Do not add a pay/bill
mapping query until confirmed live, since guessing table or column names
against a live production database risks a mapping error that silently
undercounts or misclassifies financial data.
