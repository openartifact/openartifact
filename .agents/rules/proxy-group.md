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

- **Proxy/group repos:** only maven, sbt, ivy, deb, rpm, apk, pacman, docker, npm, pypi, cargo, go, helm, nuget, conan, rubygems, conda, terraform and raw can be proxies; only maven, sbt, ivy, deb, rpm, apk, pacman, docker, npm, pypi, cargo, go, helm, nuget, conan, rubygems, conda, terraform and raw can be groups (deb/rpm/apk/pacman groups merge their metadata in `internal/worker/group_metadata*.go` and serve it from `internal/group/debrpm.go` and `pacman.go` / `apk.go`; an apk group re-signs each merged index with its own `internal/apkkeys` key, and is not in `hostedwins.go`: the merge drops proxy entries of hosted names; `internal/repotype` is the single policy, enforced at creation). `internal/repodispatch` middleware serves them in front of the format handlers; hosted repos pass straight through. A new proxy/group format must be added to `repotype` and to the dispatch map in `cmd/server/main.go`. Docker is the exception to path mirroring: its proxies/groups are served only under `/v2` (`repodispatch.OCIMiddleware` → `proxy.ServeOCI` / `group.ServeOCI`) and `/repos/{name}` answers 404 for them; everything read-only goes through `ocipath.RejectNonPull`.
- **Terraform proxy/group** (`internal/proxy/terraform*.go`, `internal/group/terraform.go`): the proxy handles every hosted registry path itself and stores everything in `proxy_cache_entries` (no schema). The repository name is the upstream namespace (provider types need it). Module archives and provider files live on other hosts and are fetched on first use through download tickets: `terraform.Minter` mints them, the hosted `/terraform/dl/` route redeems them and hands a proxy repository's ticket to `proxy.Service.ServeTerraformDownload` (`terraform.ProxyServer`). Provider zips must match the document `shasum` and the upstream `SHA256SUMS` before they are stored and keep the upstream's signing keys; module archives are pinned to the digest of the first copy; module sources are `github.com` repositories (mapped to codeload tarballs) and plain archive URLs, both with `//subdir`; they are repacked by `terraform.RepackModuleArchive` (wrapper stripped, hosted archive limits, no links) before the pin applies, and other getters answer 501. Every parser has a fuzz target with seeds in `internal/proxy/testdata/fuzz` and terms in `.clusterfuzzlite/dicts/internal_proxy.dict`.
