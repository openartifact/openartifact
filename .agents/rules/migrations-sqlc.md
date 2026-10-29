---
description: "Forward-only SQL migrations (incl. .no_tx.sql) and sqlc query workflow"
globs:
  - migrations/**
  - internal/database/**
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Migrations and sqlc

- **Migrations are forward-only.** `001_baseline.sql` is the schema of release 0.15, squashed from the migrations of the 0.x line; it contains no data migration, a database written by 0.14 or older is refused with `ErrLegacySchema` and upgraded with `oa import` (`docs/getting-started/upgrading.md`). Once released, never edit an applied file (`001_baseline.sql` included): the migrator aborts on checksum drift. Add `002_<topic>.sql` (then `003_…`). Keep transactional files short on large tables (metadata-only `ADD COLUMN`, `ADD CONSTRAINT … NOT VALID`, `SET LOCAL lock_timeout`); put index builds and `VALIDATE CONSTRAINT` in a following `00N_<topic>.no_tx.sql`, which the migrator splits and runs one statement at a time (so `CREATE INDEX CONCURRENTLY` works) and which must be re-runnable (`IF [NOT] EXISTS`, drop invalid indexes first). A new package format needs no table: it writes the generic `packages`/`package_versions`/`package_files` rows (`docs/design/format-plugins.md`); its only schema change is the `repositories.format` check constraint.
- **sqlc:** edit `queries/*.sql`, run `make generate`; never hand-edit `dbsqlc/`.
