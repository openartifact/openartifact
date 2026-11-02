<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Go modules

Use OpenArtifact as a GOPROXY: it serves `list`, `.info`, `.mod`, `.zip` and `@latest`. A hosted repository holds your private modules and accepts module zips by `PUT`. A proxy mirrors an upstream GOPROXY, and a group combines several repositories behind one URL.

| Item | Value |
|---|---|
| GOPROXY URL | `HOST/repos/REPO` |
| Format id | `go` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://proxy.golang.org` |

See [hosted, proxy, group](../repositories/hosted-proxy-group.md) for the repository types.

## Client setup

The web UI connect panel of a repository shows the same commands with your own URL.

For a hosted repository of private modules:

```sh
go env -w GOPROXY=HOST/repos/REPO,direct
go env -w GONOSUMDB=*
```

For a proxy or group:

```sh
go env -w GOPROXY=HOST/repos/REPO
```

- A hosted repository holds private modules that the public checksum database does not know. `GONOSUMDB=*` stops the go command from looking them up there. `,direct` lets the go command fall back to the module's own origin for anything the repository lacks.
- A proxy or group serves public modules. Leave `GONOSUMDB` unset and do not add `,direct`, so the go command keeps verifying what it receives against the checksum database.
- Private repositories need a token on every request. See [authentication](../security/authentication.md).

## Publish

Go has no publish command, so upload the zip yourself. The version is the last path element, without a `.zip` suffix:

```sh
curl -X PUT -H "Authorization: Bearer <TOKEN>" --data-binary @v1.0.0.zip \
  HOST/repos/REPO/example.com/mymod/@v/v1.0.0
```

The answer is `201` with `{version, module}`.

`PUT /repos/REPO/*` is shared with npm publish. The server gives the request to the Go handler when the path is `<module>/@v/<version>`, and to npm otherwise.

Validation:

- The body is at most 100 MiB (`413` above it).
- The version must be a canonical Go module version: `vMAJOR.MINOR.PATCH` with an optional `-pre-release` and, for a v2+ module without a `go.mod`, `+incompatible`. `v1`, `v1.2`, `latest` or other build metadata answer `400 invalid module version`.
- The body must be a zip with the module's `go.mod` at `<module>@<version>/go.mod` (at most 4 MiB). This is the layout `go mod` and `golang.org/x/mod/zip` produce. Otherwise the answer is `400`.
- Files that only end in `go.mod` (`foo-go.mod`) are not the `go.mod`. `go.mod` files of nested modules are ignored.
- The `module` line of that `go.mod` must equal the module path of the URL (`400` otherwise).
- Over the [storage quota](../repositories/cleanup-and-quotas.md#what-counts) the answer is `507`. A module version counts as one artifact with the size of its zip plus its `go.mod`.

The zip and the `go.mod` are stored separately by content. The `.info` time is the time the version was uploaded. Each version's zip is stored under its own sha256. A zip whose stored content has no valid sha256 is never served: the download answers `404` and the server logs a warning. To repair such records, run `openartifact-server -repair-gomod`; see [upgrading](../getting-started/upgrading.md#050).

## Install and consume

Run `go get` or `go mod download` with the GOPROXY from [client setup](#client-setup). Requests go to `/repos/REPO/<module>/`.

The module path and the version are in GOPROXY case-encoding (`!x` is uppercase `X`). Module `github.com/Example/MyMod` at `v1.0.0-RC1` is:

```
github.com/!example/!my!mod/@v/v1.0.0-!r!c1.info
```

The server decodes both before any lookup and stores the plain forms. A path that is not valid case-encoding (an unencoded uppercase letter, a stray `!`) answers `404` (`400` on upload). Upload bodies carry plain versions.

| Method and path | Result |
|---|---|
| `GET @v/list` | one version per line, highest first by SemVer precedence |
| `GET @v/<version>.info` | JSON `{Version, Time}` |
| `GET @v/<version>.mod` | stored `go.mod` |
| `GET @v/<version>.zip` | module zip |
| `GET @latest` | `.info` of the latest version |
| `PUT @v/<version>` | upload (hosted only) |

### Version ordering

`list` and `@latest` rank versions by Semantic Versioning precedence, not as strings. `v1.10.0` is above `v1.9.0`, and a release is above its pre-releases (`v1.0.0` > `v1.0.0-rc.1`).

`@latest` is the highest release. Only when a module has no release at all (pre-releases and pseudo-versions only) is it the highest pre-release. This is the Go proxy rule: a pre-release never wins over a release, however new.

<a id="proxy-and-group-repositories"></a>
## Proxy

Create a proxy with `repo_type: "proxy"` and `format: "go"`. `upstream_url` defaults to `https://proxy.golang.org`.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"gomirror","format":"go","repo_type":"proxy"}'
export GOPROXY=HOST/repos/gomirror
go mod download
```

Proxies and groups are read-only. `PUT` answers `405`.

- **Forwarding.** Requests go to `upstream_url` in the case-encoded form the go command uses (`github.com/!azure/x` goes upstream as `github.com/%21azure/x`). A path that is not a valid module path or version, a `.mod` or `.zip` of a non-canonical version, and everything outside `<module>/@v/{list,<version>.info,.mod,.zip}` and `<module>/@latest` (including `sumdb/...`) answers `404`.
- **Caching.** `@v/list`, `@latest` and the `.info` of a query such as `master` are cached for `cache_ttl_seconds` and served stale (`Warning: 110`) when the upstream fails. `.info`, `.mod` and `.zip` of a canonical version (a release, pre-release, pseudo-version or `+incompatible`) are immutable: fetched once, never revalidated, and served with the upstream down.
- **Body checks.** A body that fails its check is refused with `502` and not stored. `list` must be one valid semantic version per line. `.info` must be a JSON object whose `Version` equals the requested version. `.mod` must be a `go.mod` with a `module` line. `.zip` must pass the module zip checks (files under `<module>@<version>/`, size and path limits).
- **Credentials.** Upstream credentials are sent only to the upstream origin.
- **Checksum database.** `GET /sumdb/...` is not proxied, so clients reach the checksum database directly. Air-gapped clients need their own mirror.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The Go specifics are:

**Minimum age.** The GOPROXY protocol dates a version only in its `.info`, so the proxy reads those files (small, immutable, cached). A version whose `.info` `Time` is younger than `min_age_hours` is not offered.

- `.info` of a too-new version, or of a query such as `master` that resolves to a too-new revision, answers `403`.
- `.mod` and `.zip` of a too-new version answer `403` before they are fetched. This costs one cached `.info` lookup per version.
- `@latest` is the upstream's answer, unless that version is too new. Then it is the `.info` of the newest remaining release (a pre-release when no release is left), and `404` when none is old enough.
- `@v/list` leaves out too-new versions. The proxy looks at the `.info` of the newest versions, highest first, up to the first release that is old enough, with at most 32 lookups per request. Everything below that release is taken to be old enough. `go get module@latest` and `@v1` pick an older release by themselves.
- A backport published recently below that release stays in the list but is refused with `403` at its `.info`.
- A version without a `Time` or without a `.info` is not held back. Files already cached are served as they are.
- `Time` is the commit time of the revision, which the module's author controls. It dates what the repository says, not when the upstream first saw the version.
- The `ETag` of the list is the digest of what is sent, so a version that has aged enough appears without a stale `304`.
- A group with the proxy as a member lists and serves only what the proxy does.

**Curation.** Rules name the module path as `go.mod` shows it (decoded: `example.com/Mod`, not `example.com/!mod`) and a version with its `v` (`v1.2.3`, `v1.0.0-rc.1`, `v2.0.0+incompatible`).

- A version the rules make unavailable is left out of `@v/list`.
- `@latest` becomes the newest version left (`404` when none).
- `.info`, `.mod` and `.zip` of that version answer `403`, even when already cached. A query such as `master` is judged by the version its `.info` names.
- No `.info` lookup is made just for curation.

### Checksum database verification

Every `.mod` and `.zip` is checked against the Go checksum database before it is stored. The server uses the same protocol and checks as the go command: the `h1:` hash of the zip (and of the `go.mod`) must equal the recorded one.

| Checksum database | Result |
|---|---|
| records the module version | hash must match; otherwise `502`, not cached |
| answers `404` or `410` (a private or not-yet-recorded module) | structure checks only |
| unreachable or erroring | refused (`502`), not cached; a copy already cached keeps being served |

The database is chosen with the standard `GOSUMDB` environment variable of the server process:

- unset means `sum.golang.org` (built-in key);
- `sum.golang.google.cn` is accepted;
- `<name>+<hash>+<key> [url]` selects another database;
- `off` disables the lookup (structure checks only).

An invalid value is logged and the default is used.

Limits:

- A mismatching file is refused. There is no way to tell a compromised upstream from a stale checksum database entry. Both give `502`.
- The latest signed tree head is kept in memory only. After a restart the server trusts the database's current head again (the go command keeps it on disk).
- Modules that are not in the checksum database are only structure-checked. Their files are cached for `cache_ttl_seconds` instead of as immutable, so a version published to the database later is verified when the entry expires.
- These checks protect the shared cache. They do not replace the client-side check, which must stay on: set neither `GONOSUMDB` nor `GOFLAGS=-insecure` for public modules.

## Group

A go group answers `<module>/@v/list` with the union of all readable members' lists: SemVer order, highest first, deduplicated, anything that is not a version dropped. A member that fails is skipped as long as another answers.

All other paths, including `@latest`, are first-hit in member order. So `@latest` is the first member's answer. The go command only asks for it for modules without tagged releases.

Members are hosted go repositories and go proxies.

Hosted members win a module path first. If a readable hosted member holds the module, `@v/list` lists only hosted versions, and `.info`, `.mod`, `.zip` and `@latest` come only from hosted members. Set `merge_proxy_versions` to `true` to merge again; see [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).

A merged list that left out a failed member is served with `Cache-Control: no-store` and without an `ETag`. A complete merge carries an `ETag` and revalidates. Groups are read-only: `PUT` answers `405`.

## Limits and behaviour

| Limit or behaviour | Value |
|---|---|
| Upload body | 100 MiB, `413` above it |
| `go.mod` inside the zip | 4 MiB |
| Over quota | `507` |
| Upload to a proxy or group | `405` |
| Download of a version with no valid zip digest | `404` |

- A [cleanup policy](../repositories/cleanup-and-quotas.md#cleanup-policies) or the trash purge releases both the zip and the `go.mod` of a version.
- Storage is described in [storage](../operations/storage.md).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `400 invalid module version` on upload | version is not canonical (`v1`, `v1.2`, `latest`) | use `vMAJOR.MINOR.PATCH` |
| `400` on upload | zip has no `<module>@<version>/go.mod`, or its `module` line differs from the URL | rebuild the zip with `go mod` tooling and check the path |
| `404` on a path with uppercase letters | the path is not case-encoded | write `X` as `!x` in module path and version |
| `PUT` answers `405` | target is a proxy or group | upload to a hosted repository |
| `403` on `.info`, `.mod` or `.zip` | minimum age or a curation rule hides the version | wait, or ask an admin to change the rule |
| `502` on a proxy download | body check or checksum database mismatch, or database unreachable | retry later; check `GOSUMDB` on the server |
| `go get` reports checksum errors against a proxy | `GONOSUMDB` or `,direct` set for a public-module proxy | use the proxy and group setup above |
| a version answers `404` and the log warns about a missing digest | the stored zip has no valid sha256 | run `openartifact-server -repair-gomod`, then upload again what it could not repair |
