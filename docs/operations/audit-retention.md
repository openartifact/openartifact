<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Audit log retention

The audit log (the `audit_logs` table, shown under Admin, Audit) records every administrative and security-relevant action, and a busy installation writes to it constantly: `auth.token_validate` and `http.request` entries alone can reach millions of rows a year. By default **nothing is ever deleted**, so the table grows without bound. Set a retention period to have a background worker delete old entries.

## Enable it

| Environment variable | YAML key | Default | Meaning |
|---|---|---|---|
| `OPENARTIFACT_AUDIT_RETENTION_DAYS` | `audit.retentionDays` | `0` | Entries older than this many days are deleted. `0` keeps them forever (the default, so upgrading changes nothing). Must not be negative |
| `OPENARTIFACT_AUDIT_PURGE_INTERVAL` | `audit.purgeInterval` | `1h` | Pause between retention passes. Must be positive |
| `OPENARTIFACT_AUDIT_PURGE_BATCH_SIZE` | `audit.purgeBatchSize` | `5000` | Entries one delete statement removes. Must be positive |

```yaml
audit:
  retentionDays: 365
```

The setting is configuration only (no admin API or UI setting) and is read at startup: restart the server to change it.

## Export before you delete

Deletion is permanent and **nothing is exported automatically**. If you must keep the history (compliance, forensics), archive it before turning the retention on, and keep archiving on a schedule shorter than the retention. The export streams the whole log, so it works on a large table:

```sh
oa audit export --format jsonl --until 365d -o audit-before-$(date -u +%F).jsonl
```

See [audit export](monitoring.md#export) for the formats, the filters and shipping the log to a SIEM. A continuous export to Splunk, Elasticsearch or Loki (same page) is the better answer when the log is an evidence trail: the retention then only bounds the database.

## How it works

- A pass runs at startup and then every `audit.purgeInterval`. It computes a cutoff of now minus `retentionDays` and deletes entries created before it, **oldest first** (ordered by `created_at`, then `id`, served by the existing `idx_audit_logs_created_at` index; no migration is needed).
- Each delete statement removes at most `audit.purgeBatchSize` entries and commits on its own, so no transaction grows large, locks stay short and WAL is produced in small steps. Between full batches the pass waits on a 200 ms ticker, which bounds the load on the database; a pass ends when a batch comes back short, so a backlog of millions of rows is worked off over minutes without any single heavy statement.
- Rows another transaction is touching are skipped for that batch (`SKIP LOCKED`) instead of making the pass wait.
- A pass is bounded to 30 minutes; the next one continues with the oldest remaining entries. Stopping the server mid-pass loses nothing, every committed batch stays deleted.
- With several replicas, **one** runs the pass: it holds a Postgres advisory lock on a pinned connection for the whole pass, and a replica that finds the lock taken skips the pass and tries again at its next interval.
- Entries without a `created_at` are never deleted.
- Deleting audit entries is not itself recorded in the audit log (the table only accepts the named actions); the server log carries an `audit retention finished` line with the number of deleted entries and batches whenever a pass deleted something, and the metrics below count them.

## Metrics

| Metric | Type | Meaning |
|---|---|---|
| `openartifact_audit_retention_deleted_total` | counter | audit entries deleted by the retention worker (per replica) |
| `openartifact_audit_retention_last_run_timestamp_seconds` | gauge | Unix time the last pass finished without error on this replica, including a pass that found nothing to delete; `0` until one has. With replicas, only the one that holds the lock updates it, so alert on `max(...)` |

For example, to be told when retention is enabled but has not completed a pass for a day:

```text
time() - max(openartifact_audit_retention_last_run_timestamp_seconds) > 86400
```

## Related

- [Monitoring: audit log](monitoring.md#audit-log): events, filters, export and shipping.
- [Trash](trash.md): retention of deleted artifacts, a separate setting.
