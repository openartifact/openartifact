<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Go modules

Hosted GOPROXY: serves `list`, `.info`, `.mod`, `.zip` and `@latest`, and accepts module zips by `PUT`.

| | |
|---|---|
| Repo URL | `GOPROXY=HOST/repos/REPO` |
| Repository types | hosted only (proxy and group are rejected). See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
go env -w GOPROXY=HOST/repos/REPO,direct
go env -w GONOSUMDB=*
```

Private repositories need a token on every request; see [authentication](../security/authentication.md).

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

## Validation

- Body limit 100 MiB (`413` above it).
- The version must be a canonical Go module version: `vMAJOR.MINOR.PATCH` with an optional `-pre-release` and, for a v2+ module without a `go.mod`, `+incompatible`. `v1`, `v1.2`, `latest` or other build metadata answer `400 invalid module version`.
- The body must be a zip with the module's `go.mod` at `<module>@<version>/go.mod` (max 4 MiB), the layout `go mod` and `golang.org/x/mod/zip` produce; otherwise `400`. Files that merely end in `go.mod` (`foo-go.mod`) are not `go.mod`, and `go.mod` files of nested modules are ignored.
- The `module` line of that `go.mod` must equal the module path of the URL (`400` otherwise): the zip is never stored under a path it does not declare.
- The zip and the `go.mod` are stored as separate content-addressed blobs. The `.info` time is the record's creation time.

Versions stored before the case-encoding fix under their encoded form (an uppercase version uploaded as `v1.0.0-!r!c1` was stored with the `!`) are no longer reachable; upload them again under the same URL, which now stores the decoded `v1.0.0-RC1`.

Each version's zip is stored under its own sha256. Before this was fixed every upload registered its zip without a digest, so the zips of all versions on an instance collapsed onto the first one stored (`go get` failed with `zip for ...@vX has unexpected file ...@vY/go.mod`). Such versions have to be deleted and uploaded again.

Related: [storage](../operations/storage.md).
