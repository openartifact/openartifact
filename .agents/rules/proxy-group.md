---
description: "Proxy and group repositories: allowed formats, repotype policy, dispatch middleware, docker /v2 exception"
globs:
  - internal/proxy/**
  - internal/group/**
  - internal/repodispatch/**
  - internal/repotype/**
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Proxy and group repos

- **Proxy/group repos:** only maven, sbt, ivy, deb, rpm, apk, docker, npm, pypi, cargo, go, helm, nuget, conan, rubygems, conda and raw can be proxies; only maven, sbt, ivy, deb, rpm, apk, docker, npm, pypi, cargo, go, helm, nuget, conan, rubygems, conda and raw can be groups (deb/rpm/apk groups merge their metadata in `internal/worker/group_metadata*.go` and serve it from `internal/group/debrpm.go` / `apk.go`; an apk group re-signs each merged index with its own `internal/apkkeys` key, and is not in `hostedwins.go`: the merge drops proxy entries of hosted names; `internal/repotype` is the single policy, enforced at creation). `internal/repodispatch` middleware serves them in front of the format handlers; hosted repos pass straight through. A new proxy/group format must be added to `repotype` and to the dispatch map in `cmd/server/main.go`. Docker is the exception to path mirroring: its proxies/groups are served only under `/v2` (`repodispatch.OCIMiddleware` → `proxy.ServeOCI` / `group.ServeOCI`) and `/repos/{name}` answers 404 for them; everything read-only goes through `ocipath.RejectNonPull`.
