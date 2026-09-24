# Bullhorn

The `bullhorn` source reads ATS/CRM data from the Bullhorn REST API: candidates,
client corporations, client contacts, job orders, placements, job submissions,
corporate users, entity metadata, and deletions.

## Data

The default table set is:

| Table | What it holds |
|---|---|
| `candidate` | Candidate records |
| `client_corporation` | Client company records |
| `client_contact` | Contacts at client companies |
| `job_order` | Open and filled positions |
| `placement` | Confirmed placements |
| `job_submission` | Candidate submissions to a job order |
| `corporate_user` | Bullhorn users (recruiters, staff) |
| `entity_metadata` | Field names, labels and data types for the entities you configure — never field values |
| `deleted_records` | Deletions surfaced by Bullhorn's event subscriptions, so you can apply them downstream yourself |

Every table other than `entity_metadata` and `deleted_records` carries only the
fields you explicitly allowlist — wildcard field selection is not supported,
in line with Bullhorn's data-minimisation requirements. Fields outside the
allowlist, and any nested object or list Bullhorn returns, are not flattened:
nested values land as a JSON column so nothing is silently dropped.

Additional Bullhorn entities beyond the default set can be read with an
`entity=<BullhornEntity>` parameter on the table name (see the configuration
reference in the main README) — useful for entities such as Lead or
Opportunity that aren't in the default table set.

## Deletions

Bullhorn distinguishes soft-deleted records (`isDeleted`, still returned by
most reads and passed through as a normal column) from hard deletions, which
only appear as `DELETED` events on an event subscription. `deleted_records`
captures the latter. Applying a delete to your own downstream tables is your
responsibility — this source and the OneLake destination only ever append to
Bronze, they never remove rows.

## Incremental behaviour

`candidate`, `client_corporation`, `client_contact`, `job_order`, `placement`,
`job_submission` and `corporate_user` are incremental on `dateLastModified`
and loaded with a merge strategy keyed on `id`. `entity_metadata` is replaced
in full on every run. `deleted_records` is incremental on `event_timestamp`.

## Usage notes

- Each user is responsible for complying with Bullhorn's API Fair Use Policy,
  including its restriction on connecting AI or LLM tooling to the API
  without Bullhorn's written permission.
- A REST session is established once per run and reused for every table; it
  is not re-created per request.
- See the main README for the full `bullhorn://` URI reference, rate-limit
  configuration and the runner that schedules incremental runs against your
  API budget.
