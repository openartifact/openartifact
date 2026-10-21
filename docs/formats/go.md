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

All under `/repos/REPO/<module>/`, with the module path in GOPROXY case-encoding (`!x` = uppercase `X`).

| Method and path | Result |
|---|---|
| `GET @v/list` | one version per line |
| `GET @v/<version>.info` | JSON `{Version, Time}` |
| `GET @v/<version>.mod` | stored `go.mod` |
| `GET @v/<version>.zip` | module zip |
| `GET @latest` | `.info` of the latest version |
| `PUT @v/<version>` | upload (body = module zip), `201` with `{version, module}` |

## Publish

Go has no publish command; upload the zip yourself. The version is the last path element, **without** a `.zip` suffix:

```sh
curl -X PUT -H "Authorization: Bearer <TOKEN>" --data-binary @v1.0.0.zip \
  HOST/repos/REPO/example.com/mymod/@v/v1.0.0
```

`PUT /repos/REPO/*` is shared with npm publish: `cmd/server/main.go` sends the request to the Go handler when the tail has the form `<module>/@v/<file>` (not `@latest`), otherwise to npm.

## Validation

- Body limit 100 MiB (`413` above it).
- The body must be a zip containing a `go.mod` (the first entry whose name ends in `go.mod`, max 4 MiB); otherwise `400 invalid go module`. The module path comes from the URL, not from `go.mod`.
- The zip and the `go.mod` are stored as separate content-addressed blobs. The `.info` time is the record's creation time.

Related: [storage](../operations/storage.md).
