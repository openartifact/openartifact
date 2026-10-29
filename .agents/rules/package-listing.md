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

- **Package listing:** `ListRepoComponents` is two-stage: each branch takes its top `limit+offset` rows by `(created_at DESC, id)` before name, version, format, attrs and blobs are resolved for the page. Keep that shape.
- **Branches:** there is **one** generic hosted branch over `package_files` (live, `listed`) joined to `package_versions` and `packages`, plus two pseudo-branches: proxy-cache entries and docker tags. A new format adds no branch to `ListRepoComponents` or `CountRepoComponents`; its rows list as soon as its store writes `package_files` with `listed = true` (and fills `name`, `arch`, `size_bytes` the way the listing reads them). Only a new *kind* of pseudo-source (not a format) needs a branch.
- **Indexes:** stage 1 reads `package_files` alone through `idx_package_files_listing` (`repository_id, created_at DESC, id` where live and listed) when `format_filter`, `name_filter` and `q` are all empty; otherwise it joins versions and packages, with a separate `name_filter` equality branch (`uq_packages_name`). Related indexes: `idx_package_files_version`, `idx_packages_name_trgm`. Do not add a per-format index for listing. A rare `q` or a `format_filter` matching nothing scans the whole repository; that is accepted and documented.
