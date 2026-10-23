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

- **Migrations are forward-only.** Never edit an applied file (`001_initial_schema.sql`); the migrator aborts on checksum drift. Add `00N_<topic>.sql`. Keep transactional files short on large tables (metadata-only `ADD COLUMN`, `ADD CONSTRAINT … NOT VALID`, `SET LOCAL lock_timeout`); put index builds and `VALIDATE CONSTRAINT` in a following `00N_<topic>.no_tx.sql`, which the migrator splits and runs one statement at a time (so `CREATE INDEX CONCURRENTLY` works) and which must be re-runnable (`IF [NOT] EXISTS`, drop invalid indexes first), see `018_trash_indexes.no_tx.sql`.
- **sqlc:** edit `queries/*.sql`, run `make generate`; never hand-edit `dbsqlc/`.
