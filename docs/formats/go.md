<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Go modules

GOPROXY: serves `list`, `.info`, `.mod`, `.zip` and `@latest`. Hosted repositories accept module zips by `PUT`; proxy repositories mirror an upstream GOPROXY and group repositories combine several.

| | |
|---|---|
| Repo URL | `GOPROXY=HOST/repos/REPO` |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) and [below](#proxy-and-group-repositories) |

## Client setup

`internal/clientsetup` produces:

```sh
go env -w GOPROXY=HOST/repos/REPO,direct
go env -w GONOSUMDB=*
```

`GONOSUMDB=*` suits hosted repositories of private modules; do not set it for proxies and groups of public modules, where the checksum database should keep verifying what the client receives. Private repositories need a token on every request; see [authentication](../security/authentication.md).

## Endpoints

All under `/repos/REPO/<module>/`, with the module path **and the version** in GOPROXY case-encoding (`!x` = uppercase `X`): module `github.com/Example/MyMod` at `v1.0.0-RC1` is `github.com/!example/!my!mod/@v/v1.0.0-!r!c1.info`. The server decodes both before any lookup and stores the plain forms; a path that is not valid case-encoding (an unencoded uppercase letter, a stray `!`) answers `404` (`400` on upload). Bodies carry plain, unencoded versions.

| Method and path | Result |
|---|---|
| `GET @v/list` | one version per line, highest first by SemVer precedence |
| `GET @v/<version>.info` | JSON `{Version, Time}` |
| `GET @v/<version>.mod` | stored `go.mod` |
| `GET @v/<version>.zip` | module zip |
| `GET @latest` | `.info` of the latest version |
| `PUT @v/<version>` | upload (body = module zip), `201` with `{version, module}` |

## Version ordering

`list` and `@latest` rank versions by Semantic Versioning precedence, not as strings: `v1.10.0` is above `v1.9.0`, and a release is above its pre-releases (`v1.0.0` > `v1.0.0-rc.1`). `@latest` is the highest release; only when a module has no release at all (pre-releases and pseudo-versions only) is it the highest pre-release. This is the Go proxy rule: a pre-release never wins over a release, however new.

## Publish

Go has no publish command; upload the zip yourself. The version is the last path element, **without** a `.zip` suffix:

```sh
curl -X PUT -H "Authorization: Bearer <TOKEN>" --data-binary @v1.0.0.zip \
  HOST/repos/REPO/example.com/mymod/@v/v1.0.0
```

`PUT /repos/REPO/*` is shared with npm publish: `cmd/server/main.go` sends the request to the Go handler when the tail has the form `<module>/@v/<file>` (not `@latest`), otherwise to npm.

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on upload (`507` over quota); a module version counts as one artifact with the size of its zip plus its `go.mod`, and a [cleanup policy](../repositories/cleanup-and-quotas.md#cleanup-policies) or the trash purge releases both.

## Validation

- Body limit 100 MiB (`413` above it).
- The version must be a canonical Go module version: `vMAJOR.MINOR.PATCH` with an optional `-pre-release` and, for a v2+ module without a `go.mod`, `+incompatible`. `v1`, `v1.2`, `latest` or other build metadata answer `400 invalid module version`.
- The body must be a zip with the module's `go.mod` at `<module>@<version>/go.mod` (max 4 MiB), the layout `go mod` and `golang.org/x/mod/zip` produce; otherwise `400`. Files that merely end in `go.mod` (`foo-go.mod`) are not `go.mod`, and `go.mod` files of nested modules are ignored.
- The `module` line of that `go.mod` must equal the module path of the URL (`400` otherwise): the zip is never stored under a path it does not declare.
- The zip and the `go.mod` are stored as separate content-addressed blobs. The `.info` time is the record's creation time.

Versions stored before the case-encoding fix under their encoded form (an uppercase version uploaded as `v1.0.0-!r!c1` was stored with the `!`) are no longer reachable; upload them again under the same URL, which now stores the decoded `v1.0.0-RC1`.

Each version's zip is stored under its own sha256. Up to 0.4.1 every upload registered its zip without a digest, so the zips of all versions on an instance collapsed onto the first one stored: every Go repository served that one zip (`go get` failed with `zip for ...@vX has unexpected file ...@vY/go.mod`), a cross-repository and cross-organization source disclosure. Now:

- A zip whose blob has no valid sha256 is never served (or redirected to a presigned URL): the download answers `404` and the server logs a warning naming the repository, module and version.
- At every start a background repair relinks such versions (trashed ones included) to their real zip, which it finds in the blob store by content, and drops the shared database record without deleting any stored object. It runs on one replica at a time and exits at once when nothing is affected. Run it by hand with `openartifact-server -repair-gomod`; it prints what it repaired and every `repository: module@version` it could not.
- A version that cannot be matched to exactly one stored zip stays `404`: delete it and upload it again. The repair finds orphaned zips by listing the blob store; every backend can do that except `oci`: the OCI Distribution specification has no operation that enumerates the blobs of a repository (only `HEAD`/`GET` of a blob whose digest is already known), and an orphaned zip is by definition a digest that no database row records, so no database query can stand in for the listing. On `oci` only the zip of the shared record itself is recovered and the other affected versions must be uploaded again.

See the security note in [upgrading](../getting-started/upgrading.md#050-unreleased).

## Proxy and group repositories

```sh
export GOPROXY=HOST/repos/REPO          # a go proxy or a go group; no ",direct"
go mod download                          # GOSUMDB stays at its default
```

Create a proxy with `repo_type: "proxy"`, `format: "go"`; `upstream_url` defaults to `https://proxy.golang.org`. Proxies and groups are read-only: `PUT` answers `405` (the hosted upload route is not reachable through them).

### Proxy behavior

- Requests are forwarded to `upstream_url` unchanged in the case-encoded form the go command uses (`github.com/!azure/x` goes upstream as `github.com/%21azure/x`). A path that is not a valid module path or version in GOPROXY encoding, a `.mod`/`.zip` of a non-canonical version, and everything outside `<module>/@v/{list,<version>.info,.mod,.zip}` and `<module>/@latest` (including `sumdb/...`) answers `404`.
- `@v/list`, `@latest` and `.info` of a query such as `master` are cached for `cache_ttl_seconds` and served stale (`Warning: 110`) when the upstream fails. `.info`, `.mod` and `.zip` of a canonical version (a release, pre-release, pseudo-version or `+incompatible`) are immutable: fetched once, never revalidated, and served from the cache with the upstream down.
- Bodies are checked before they are cached; a body that fails is refused with `502` and not stored: `list` must be one valid semantic version per line, `.info` a JSON object whose `Version` equals the requested version, `.mod` a `go.mod` with a `module` line, `.zip` a zip that `golang.org/x/mod/zip.CheckZip` accepts (files under `<module>@<version>/`, size and path limits).
- Upstream credentials are sent only to the upstream origin.

### Verification against the checksum database

Every `.mod` and `.zip` is also checked against the Go checksum database before it is stored, using the same protocol and checks as the go command (signed tree heads, tile hashes and inclusion proofs from `golang.org/x/mod/sumdb`): the `h1:` hash of the zip (and of the `go.mod`) must equal the recorded one.

| Checksum database | Result |
|---|---|
| records the module version | hash must match; otherwise `502`, not cached |
| answers `404`/`410` (a private or not-yet-recorded module) | structure checks only (above) |
| unreachable or erroring | refused (`502`), not cached; a copy already cached keeps being served |

The database is chosen with the standard `GOSUMDB` environment variable of the server process: unset is `sum.golang.org` (built-in key), `sum.golang.google.cn` is accepted, `<name>+<hash>+<key> [url]` selects another database, and `off` disables the lookup (structure checks only). An invalid value is logged and the default is used. Lookups go through the SSRF-checked HTTP client.

Limits:

- A mismatching file is refused, but there is no way to tell a compromised upstream from a stale checksum database entry; both give `502`.
- The latest signed tree head is kept in memory only. After a restart the server trusts the database's current head again (the go command keeps it on disk).
- Modules that are not in the checksum database are only structure-checked, and their files are cached for the repository TTL (`cache_ttl_seconds`) rather than as immutable, so a version published to the database later is verified when the entry expires; the go client's own `GOSUMDB`/`GONOSUMDB`/`GOPRIVATE` settings still decide what it verifies. Checks protect the shared cache; they do not replace the client-side check, which must stay on (set neither `GONOSUMDB` nor `GOFLAGS=-insecure`).
- `GET /sumdb/<name>/...` is not proxied, so clients reach the checksum database directly; air-gapped clients need their own mirror.

### Group behavior

A go group answers `<module>/@v/list` with the union of all readable members' lists (SemVer order, highest first, deduplicated, anything that is not a version dropped); a member that fails is skipped as long as another answers. All other paths, including `@latest`, are first-hit in member order, so `@latest` is the first member's answer; the go command only asks for it for modules without tagged releases. Members are hosted go repositories and go proxies.

Related: [storage](../operations/storage.md).
