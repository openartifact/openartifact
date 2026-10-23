---
description: "Disaster-recovery row journal: the table registry, sidecar blobs and openartifact rebuild"
globs:
  - internal/recovery/**
  - migrations/**
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Recovery journal

Docs: `docs/operations/rebuild-from-storage.md`.

- **A new package table needs a journal entry.** Any table with a foreign key to `blobs` or `repositories` goes into `Tables` in `internal/recovery/tables.go` (parents before children, `Exclude` for generated columns, secrets and volatile counters, `BlobRefs` for every `blobs.id` column) and gets the `recovery_journal` trigger (`recovery_trg_*`, see migration 068) in a new migration. If it is deliberately not journaled, add it to `NotJournaled` with the reason. `TestRegistryCoversSchema` (integration) fails otherwise.
- **Never put a secret in a sidecar.** Sidecars are plain JSON in the blob store. Encrypted or hashed secret columns go into `Exclude`.
- **Table and column names in dynamic SQL come from the registry only**, never from sidecar content. The rebuild intersects the keys of a sidecar row with the live columns of the registry table.
- **A column added to a journaled table** is picked up automatically (the sidecar is `to_jsonb` of the row). A column dropped or renamed later must keep old sidecars loadable: the rebuild ignores unknown keys, and a rename needs the new column to tolerate its default.
- **Sidecars have no `blobs` row**, so the journal deletes them directly with the blob store; that is the one exception to "delete blobs only via `storage.SafeDeleteBlob`".
- **Sidecar format changes** bump `SidecarMagic`; readers skip (and report) versions they do not know, they never guess.
- **The rebuild is merge-only.** It never UPDATEs or DELETEs a row. Do not add a code path that does.
- **Metadata names on the blob store** (`oakind`, `oatable`) are lowercase letters only; S3 lower-cases and Azure restricts user metadata names.
- **Row images are taken under `SET LOCAL TIME ZONE 'UTC'`**; keep that so the row hash is stable across replicas.
