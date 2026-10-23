---
description: "ListRepoComponents two-stage query shape, per-format union branches and matching indexes"
globs:
  - internal/database/queries/components.sql
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Package listing

- **Package listing:** `ListRepoComponents` is two-stage (each union branch takes its top `limit+offset` rows by `(created_at DESC, id)` before `blobs` is joined for the page); keep that shape and the matching `(repository_id, created_at DESC, id)` indexes when adding a branch.
- **Package listing (branches):** `ListRepoComponents` (`queries/components.sql`) unions every per-format table. Adding a format or a package table means adding a branch there (and to `CountRepoComponents`), or its packages silently vanish from the UI.
