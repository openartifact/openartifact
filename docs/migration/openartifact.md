<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Migrating from OpenArtifact 0.14

Release 0.15 starts from a squashed baseline of the database migrations, so a 0.14 database cannot be upgraded in place. To move to 0.15, run a fresh 0.15 instance beside the 0.14 one and copy the content and the configuration across with `oa`:

- `oa import openartifact` reads a running 0.14 instance through its public REST and native-protocol APIs and republishes every hosted repository into the 0.15 instance through the normal upload endpoints of each format.
- `oa import config openartifact` reads the configuration of the 0.14 instance and writes [declarative manifests](../integrations/declarative.md) plus a mapping report, which `oa apply` then loads into 0.15.

Nothing is written to the 0.14 instance, and nothing needs to be stopped there: it keeps serving while it is copied.

The sections below describe the OpenArtifact source (shared flags, behavior and checksum rules are those of the [Nexus and Artifactory importer](nexus-artifactory.md)); the [runbook](#runbook-014-to-015) is the end-to-end procedure.

## OpenArtifact source

```sh
# every hosted repository, same names; the source token is read from a file
oa import openartifact --source-url https://oa014.example.com \
  --source-token-file ~/.oa014-token --all --dry-run

# a selection, with renaming and a state file for resuming
oa import openartifact --source-url https://oa014.example.com \
  --source-token-file ~/.oa014-token \
  --repo libs-release:releases --repo images --state-file oa014.state
```

Syntax: `oa [global flags] import openartifact --source-url URL (--all | --repo SRC[:DST]...) [flags]`. The global flags `--server` and `--token` select and authenticate the **destination** (the 0.15 instance); `--source-url` and `--source-token-file` select and authenticate the **source** (the 0.14 instance). The source token is read from the file, from stdin with `-`, or from `$OA_IMPORT_SOURCE_TOKEN`. It needs read access to every repository you copy (an admin token reads everything); plain HTTP is refused except for loopback hosts. The destination token needs the `manage` action, otherwise the original creation times are ignored (see below).

| Flag | Meaning |
|---|---|
| `--all` | Import every hosted repository of the source under its own name. Proxy and group repositories are skipped with a log line: they hold no data (see [Proxy and group repositories](#proxy-and-group-repositories)). Cannot be combined with `--repo`. |
| `--formats F,F` | With `--all`: only repositories of these formats, for example `--formats maven,docker`. |
| `--repo SRC[:DST]` | One repository, repeatable, as in the other sources. Must be hosted. |
| `--dry-run` | Counts what would be copied, with **no writes** to the destination: per repository and format the number of assets listed and the number that would be uploaded, already present, resumed from the state file or ignored, and their bytes. The source and the destination are read. |
| `--state-file F` | Resume file, as for the other sources. |
| `--create-repos` | Create missing destination repositories as hosted repositories of the same format. Not needed after `oa apply` of the [config manifests](#configuration). |
| `--properties` | Artifact properties to copy. For this source the default is `*` (every property of the artifact, including `deb.*` and `build.*`); `none` disables it. |
| `--deb-distribution D[,D...]` | Debian distributions to look for the placement of each `.deb` in (default `stable`), see [Debian placement](#debian-placement). |
| `--deb-component C` | Fallback component for a `.deb` the source's indexes do not name. |
| `--path-prefix`, `--concurrency`, `--retries`, `--retry-max-wait`, `--quiet` | As in the [flag table](nexus-artifactory.md#flags). |

`--format json` prints the summary with a `repositories[]` array (`source`, `target`, `format`, `assets`, `uploaded`, `bytes`, `skipped_present`, `skipped_state`, `ignored`, `failed`), next to the totals. The per-repository table is also printed in table mode when more than one repository is copied.

### What is copied

For each hosted repository the importer lists the packages through the REST API (paged, in a stable order), reads the metadata the listing lacks, downloads the bytes and uploads them to the destination with the format's own upload. The destination therefore validates, indexes and stores them exactly as it does for a client publish, and builds its own repository metadata (Maven `maven-metadata.xml`, apt `dists/`, yum `repodata/`, Helm `index.yaml` and so on).

| Format | Read from the source | Published into the destination |
|---|---|---|
| `ansible` | REST download of the collection tarball | Galaxy v3 collection upload |
| `apk` | REST download of the `.apk` (branch, architecture from the listing) | `PUT <branch>/<repo>/<arch>/<file>.apk` |
| `cargo` | REST download of the `.crate`; **yanked** state from the sparse index (`/repos/<repo>/index/...`) | `crates/new`, then the yank is replayed |
| `composer` | REST download of the dist `.zip` with the listed name and version | Composer upload |
| `conan` | native Conan 2 API: recipe and package revisions and their files, one by one | Conan v2 per-file `PUT`, manifest last |
| `conda` | REST download | Conda channel upload |
| `deb` | REST download; distribution and component from the source's `dists/<dist>/Release` and `Packages` | `POST .../debs` |
| `docker` | the source's registry API (`/v2/<repo>/...`): every tag with its manifest, child manifests, config, layers, and OCI referrers (signatures, SBOMs, attestations) | registry push; digests are unchanged. A tag pushed without an image name (`<repo>:<tag>`) stays without one |
| `go` | REST download of the module zip | `@v/<version>` upload |
| `helm` | REST download of the chart | `charts` upload |
| `ivy`, `maven`, `sbt` | REST download of each file | `PUT` of each file at its path |
| `nix` | REST download of the NAR plus the `.narinfo` read natively | `PUT nar/<file>`, then `PUT <hash>.narinfo` |
| `npm` | REST download of the tarball | npm publish (oldest version first) |
| `nuget` | REST download of the `.nupkg` with its id and version | NuGet push |
| `pacman` | REST download | pacman upload |
| `pypi` | REST download | `legacy` upload |
| `raw` | REST download | `PUT` at the same path |
| `rpm` | REST download | `POST .../packages` |
| `rubygems` | REST download | `api/v1/gems` |
| `terraform` | REST download of modules and providers; the provider **protocols** from `/repos/<repo>/<type>/versions` | module or provider upload with `?protocols=` |

Every one of the 22 hosted formats of 0.14 has an importer. The exceptions that matter are in [What is not reachable](#what-is-not-reachable).

### Creation times

Every upload carries `X-OA-Import-Created-At` with the source row's `created_at`, so age-based [cleanup policies](../repositories/cleanup-and-quotas.md) behave as they did. The server honors the header only for a principal with the `manage` action ([server contract](nexus-artifactory.md#server-contract)). `updated_at` is the import time.

### Idempotent and resumable

- Assets are matched by SHA-256 against what the destination already holds (Maven and raw by path and SHA-256), so a re-run uploads nothing that is already there, with or without `--state-file`. With `--state-file` an asset recorded as done is skipped without asking the destination at all.
- **Shared storage.** The importer sends the bytes through the destination's normal upload, which stores blobs by SHA-256, so the blob store's own deduplication applies when 0.14 and 0.15 share one object-storage bucket: content that is already stored is not written twice. The importer never writes to the source. Keep a separate database for 0.15; the migration is **not** a way to point 0.15 at the 0.14 database.
- A copy whose digest does not match the source is removed and reported as a failure, as for the other sources.

### Debian placement

The API lists the packages of a deb repository but not its distributions. The importer reads `dists/<dist>/Release` and `dists/<dist>/<component>/binary-<arch>/Packages` of the source for every distribution in `--deb-distribution` and places each `.deb` where the index names it. A `.deb` listed in several distributions is copied once, into the first one found. A `.deb` that no index of the listed distributions names falls back to the first `--deb-distribution` and to `--deb-component`. Pass every distribution of the repository (`--deb-distribution jammy,noble`); a distribution that does not exist is ignored.

### Proxy and group repositories

Proxy and group repositories hold no data of their own, so the content import skips them. `oa import config` recreates their definitions (upstream URL, cache settings, group members and order, merge settings); a proxy cache fills again from its upstream. They are recreated empty by design.

## Configuration

```sh
oa import config openartifact --source-url https://oa014.example.com \
  --source-token-file ~/.oa014-token --output ./oa014-config
oa --server https://oa015.example.com --token "$OA_TOKEN" apply -f ./oa014-config -R --validate-only
oa --server https://oa015.example.com --token "$OA_TOKEN" apply -f ./oa014-config -R
```

`oa import config` runs the declarative exporter against the **source** (with an admin token), with members, and writes the result with the same layout as `oa export -o DIR`, one file per object under a directory per kind. Every user is written with `must_change_password: true`. It also writes `mapping-report.json` into the directory (a file `oa apply` does not read) and prints a summary of it (the report itself with the global `--format json`). Optional `--kind K` (repeatable) restricts the export to one kind.

| Category | In the manifests |
|---|---|
| Repositories | name, format, type (hosted, proxy, group), description, visibility, immutable releases, presigned downloads, trash retention, quota, cleanup (retention) policies, proxy upstream URL / user name / cache TTLs, group members and merge setting, rpm `sign_packages` |
| Users | name, email, full name, disabled; **no password** |
| Groups | name, description, members |
| Permission targets | filters, path pattern, actions, user and group assignments |
| Projects | policy, members, group grants |
| Webhooks | global and per repository, with a placeholder for the secret |
| SSO providers | definition, with placeholders for the client secrets |
| Storage tiers and tier policies | rank and rules; a tier names a backend that must already exist on the destination |

Only what the source's REST API can read and the declarative API can write is carried. Artifact **properties** travel with the content (`oa import openartifact --properties`).

### The mapping report

Every exported object is listed in `mapping-report.json` and in the summary with one status:

| Status | Meaning |
|---|---|
| `translated` | carried over faithfully (informational notes may apply, such as "the content is copied by `oa import`" or "recreated empty: the proxy cache is not copied") |
| `approximated` | recreated, but something differs and the reason says what to do: users have no password; secrets are placeholders (`secretRef` to an environment variable such as `OA_WEBHOOK_NOTIFY_SECRET`, to export before `oa apply`); a proxy that authenticates to its upstream needs its `upstream_password` added; a repository that signs packages gets a signing key of its own; a storage tier names a backend that must exist on the destination |
| `skipped` | not written; the reason says why |

The report states explicitly, in its `out_of_scope` list, what is not carried: scan results, vulnerability findings and SBOMs (the new instance scans what you import), the audit log, user sessions, API and CI tokens, user passwords and hashes, secrets (placeholders only), GPG, apk and nix signing keys, storage backend definitions and server configuration, proxy caches, the trash, and the built-in objects every instance has.

## What is not reachable

- **Secrets and credentials.** Passwords, password hashes, tokens, webhook and SSO secrets, proxy upstream passwords and signing keys are write-only. Create new tokens and passwords on 0.15; clients must trust the signing keys 0.15 generates (apt, yum, apk and nix repositories sign with keys of the instance or the repository; see [signing](../security/signing.md)).
- **SSO accounts** are exported as local users without a password; they link again when the user signs in through the same provider if `link_by_email` is set.
- **Docker.** Untagged manifests that are not OCI referrers of a tag cannot be listed through the registry API, so they are not copied. Everything reachable from a tag is.
- **Go.** A row that has only a `.mod` and no zip (a module known by its go.mod alone) has no content to republish.
- **Quarantined and blocked packages** cannot be downloaded from the source, so they fail with the source's error; release or delete them first.
- **Scan results, audit log, sessions, tokens**: see the mapping report.
- **Storage backends and server configuration** (environment, Helm values) are not managed through the API: configure 0.15 yourself.

## Runbook: 0.14 to 0.15

1. **Deploy 0.15 beside 0.14.** Give 0.15 its own database (a new, empty one) and its own URL. It may use the same object-storage bucket as 0.14, in which case the blob store's SHA-256 deduplication applies to content that is already stored. Do not point 0.15 at the 0.14 database. Create an admin token on each instance (`oa auth token create`): the configuration export reads users, permission targets and other admin-only objects from 0.14, and `oa apply` and the content import need `manage` on 0.15.
2. **Configuration.** Run `oa import config openartifact` against 0.14, read `mapping-report.json` and the summary, add the missing secrets (export the `OA_*` environment variables named in the report, add proxy `upstream_password` values), then `oa apply -f DIR -R --validate-only` and `oa apply -f DIR -R` on 0.15. This creates the repositories (empty), users, groups, permission targets, projects, webhooks and the rest. Users are created without a password: set one with an admin password reset, or let them sign in through SSO.
3. **Content, with a dry run first.**

   ```sh
   oa --server https://oa015.example.com --token "$OA_TOKEN" import openartifact \
     --source-url https://oa014.example.com --source-token-file ~/.oa014-token \
     --all --deb-distribution jammy,noble --dry-run
   ```

   Check the per-repository counts against what you expect, then run the same command without `--dry-run` and with `--state-file oa014.state`. Re-run it until it reports no failures; a re-run copies only what is missing. To keep 0.14 writable during the copy, run it once now and again just before the switch: the second run copies the delta.
4. **Verify.** Compare the per-repository counts of the import (`--format json` `repositories[]`) with `oa repo packages` or the web UI of both instances, and the re-run summary (`uploaded: 0`). Pull a few artifacts of each format through a real client against 0.15. Check that proxy and group repositories resolve.
5. **Switch clients and DNS.** Point DNS or the ingress at 0.15, update the clients that used a token or a signing key (tokens are new; apt, yum, apk and nix clients must trust the new repository keys). Stop publishing to 0.14 and run the import once more to take the last uploads.
6. **Decommission 0.14.** Keep it read-only for as long as you need its audit log and scan history; when you stop, shut it down, drop its database and, if it did not share a bucket with 0.15, its bucket. Do not delete a shared bucket: 0.15 uses it.
