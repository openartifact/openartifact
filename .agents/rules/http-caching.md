---
description: "ETag/304 and Cache-Control rules for format metadata endpoints"
globs:
  - internal/format/**
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# HTTP caching

- **HTTP caching:** metadata endpoints answer ETag/304 through `internal/format/base/cache.go` (after RBAC); `public` only for an anonymous read of a public repo, everything else `private, no-cache`. Never emit `public` for authenticated responses.
