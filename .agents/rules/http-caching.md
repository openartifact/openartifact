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

- **Origin in validators:** a document that embeds the request origin (`PublicBase`/`publicurl.Resolve`) must put `base.OriginTag(origin)` (or the origin itself) in its ETag and in any render-cache key; relative documents must not contain it. `publicurl.Resolve` adds the `Vary` header, so call it before the headers are written.
- **HTTP caching:** metadata endpoints answer ETag/304 through `internal/format/base/cache.go` (after RBAC); `public` only for an anonymous read of a public repo, everything else `private, no-cache`. Never emit `public` for authenticated responses.
