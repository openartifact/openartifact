---
description: "Background workers: Postgres advisory locks and the coalescing metadata RegenQueue"
globs:
  - internal/worker/**
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Workers

- **Workers** taking Postgres advisory locks must hold them on a pinned connection (or use xact locks), not the `*sql.DB` pool. Metadata regeneration (npm/maven/deb/rpm) goes through the coalescing `RegenQueue` (`internal/worker/regen_queue.go`: one running + one pending run per key, shared worker cap, lock-busy retry, drain on `Stop()`); never spawn a goroutine per upload.
