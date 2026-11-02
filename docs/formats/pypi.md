<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# PyPI

Use a Python package index to install with `pip` and upload with `twine`. It can be hosted, a proxy of an upstream index such as pypi.org, or a group of both. It serves the simple index (PEP 503, the standard HTML index that pip reads), the same index as JSON (PEP 691), the legacy upload API and a JSON package endpoint.

| Item | Value |
|---|---|
| Index URL | `HOST/repos/REPO/simple/` |
| Upload URL | `HOST/repos/REPO/legacy/` |
| Format id | `pypi` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://pypi.org/simple` |

## Client setup

The web UI connect panel shows the same commands with your own URL.

```sh
pip config set global.index-url HOST/repos/REPO/simple/
```

For a private repository, pip needs credentials for the index URL. Put the username and the token (as the password) in the URL userinfo, or use a `.netrc` entry for the host. The server ignores the username.

Upload with `twine`:

```ini
# ~/.pypirc
[distutils]
index-servers = openartifact

[openartifact]
repository = HOST/repos/REPO/legacy/
username = <USERNAME>
password = <TOKEN>
```

```sh
twine upload --repository openartifact dist/*
```

The upload URL must end in `/legacy/`.

## Publish

`twine` sends a multipart form to `POST /legacy/`. Success is `200`.

- `content` is the file (wheel or sdist). A missing part is `400 missing file`. The file is stored as sent, and the server computes its MD5 and SHA-256.
- Metadata fields read: `name`, `version`, `filetype`, `pyversion`, `requires_python`, `summary`, `author`, `author_email`, `license`, `keywords`, `classifiers` (newline-separated).
- `name` and `version` must be present, otherwise `400`.
- The file name must belong to them: `<dist>-<version>.tar.gz`, or `<dist>-<version>-<tags>.whl` with `-` in the version written as `_`. Otherwise `400`.
- Project names are normalized: runs of `-`, `_` and `.` collapse to one `-`, and the name is lower-cased. `My_Package` and `my-package` are one project.
- A file name that already exists in the repository is `409 file already exists`. Distributions are immutable: bump the version.
- The `409` also covers a file name another name and version pair already holds. `foo-1-2.tar.gz` is both `foo` `1-2` and `foo-1` `2`. A download of an already ambiguous file name is `409`.

Limits:

- The body is at most 512 MiB of file plus 1 MiB of form fields (`413` above it).
- The server reads the whole file into memory to compute its SHA-256.
- The repository [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced (`507` over quota).

Delete one distribution with `DELETE /files/<filename>` (`200`).

## Install and consume

Endpoints, relative to `HOST/repos/REPO`:

| Request | Behaviour |
|---|---|
| `GET /simple/` | HTML index of all project names |
| `GET /simple/<package>/` | HTML file list |
| `GET /files/<filename>` | download a distribution |
| `GET /json/<package>` | JSON with `info` and `releases` |

- The simple page links are relative: `../../files/<project>/<file>#sha256=<digest>`. The root page links `<project>/`. The JSON form (PEP 691) carries the same data.
- `data-requires-python` is set when the upload set it.
- `info` in `/json/<package>` holds name, version, summary, author, author_email and license, taken from the first stored file.
- `releases` lists per file the filename, `url` and digests. The `url` is absolute on the request origin, or on `BASE_URL` when set.
- Downloads of quarantined files are refused.

## Proxy

Create a proxy with `repo_type: "proxy"`. If you omit `upstream_url`, it defaults to `https://pypi.org/simple`. A proxy is not a path mirror: `upstream_url` is the simple index.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"pypi-org","format":"pypi","repo_type":"proxy"}'
```

```sh
pip install --index-url HOST/repos/PROXY/simple/ <package>
```

- **Pages:** `GET /simple/` and `GET /simple/<project>/` are fetched from the upstream (PEP 691 JSON preferred, HTML accepted) and cached for `cache_ttl_seconds`. They are served stale when the upstream is down. Project names that are not plain `[a-z0-9-]` after normalization are `404`.
- **Format negotiation:** the client gets HTML or JSON by its `Accept` header, regardless of what the upstream spoke. No preference gets HTML. If neither is acceptable the answer is `406`.
- **Link rewriting:** every file link points at the proxy as `../../files/<project>/<filename>`. The hash, `requires-python`, core metadata links, yanked state, size and upload time are kept. The real upstream URL is never exposed.
- **Downloads:** the proxy resolves the file name in the cached project page, so only files the upstream lists can be fetched. Files with unsafe names are dropped from the page. GPG signatures (`.asc`) are not mirrored.
- **Verification:** a distribution is fetched on first download and compared with the sha256 the index advertises. `.metadata` files are compared with the metadata hash. A mismatch is `502` and nothing is stored.
- **Immutable once cached:** verified files. A second install works with the upstream down. A file with no sha256 in the index is cached unverified, for the repository TTL only.
- **Credentials:** the upstream's Basic credentials go only to the upstream's own origin. Files on another origin, such as `files.pythonhosted.org`, are fetched without them.
- Uploads (`twine upload`) to a proxy are `405`.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The PyPI specifics:

- **Spelling:** rules name the project by its normalized name (lower case, runs of `-`, `_`, `.` as one `-`). They name the release by the version its file names spell, for example `2.32.0` for `requests-2.32.0.tar.gz` and `requests-2.32.0-py3-none-any.whl`.
- **Age source:** the `upload-time` field (from the JSON index extension PEP 700) of the upstream's JSON page. The proxy asks upstreams for JSON first. An upstream that serves only HTML states no times, so nothing is hidden.
- **Release age:** a release is the set of files of one version. It counts as published when its earliest file was uploaded. A wheel added late to an old release does not hide it. A release none of whose files has an `upload-time` passes.
- **Hidden release:** its files are left out of the simple page (HTML and JSON). A download of one answers `403`, and so does its `.metadata`. Under curation this holds even for cached files. Under minimum age, files already cached are served as before.
- **No release left:** the project page is `404`, as for an unknown project. The proxy never republishes the upstream's PEP 700 `versions` list, so a hidden release is not named anywhere.
- A matching allow rule exempts a release from the minimum age.
- A release that ages past the threshold appears within a few minutes.

## Group

A group is an ordered list of hosted and PyPI proxy members.

- `simple/` and `simple/<project>/` are merged across members. The file list is the union by filename. When several members list the same filename, the first member wins.
- Links are rewritten to `../../files/<project>/<filename>` on the group.
- A download comes from the member whose entry was listed.
- Hosted members win a project name first. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- A proxy member's minimum age and curation apply through the group.
- The group answers HTML or JSON by `Accept`, like a proxy.
- A failing member is ignored while another contributes, but the partial page is served with `Cache-Control: no-store`. With no contributor the group answers `404`, or `502` if a member failed.
- Group repositories are read-only. Use `--index-url HOST/repos/GROUP/simple/`.

## Limits and behaviour

- Distributions are immutable. Upload is limited to 512 MiB per file.
- Simple pages carry an `ETag`. `Cache-Control` is `public, max-age=60` for anonymous reads of a public repository and `private, no-cache` otherwise.
- PyPI repositories have no repository signature. Clients verify the `sha256` fragment on each file link. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `409 file already exists` | distributions are immutable, or the file name is ambiguous | bump the version |
| `400` on upload | name or version missing, or the file name does not match them | rebuild the distribution |
| `400 missing file` | the `content` part is absent | use `twine` and check `dist/*` |
| `413` on upload | file over 512 MiB | upload a smaller file |
| `405` on upload | the repository is a proxy or a group | upload to a hosted repository |
| pip gets `401` or `403` from a private index | no credentials for the index URL | put the token in the URL or a `.netrc` entry |
| `502` on a file through a proxy | the download did not match the index sha256 | retry, then check the upstream |
| `403` on a file, or a version is missing from the page | minimum age or curation holds the release back | see [Minimum age and curation](#minimum-age-and-curation) |
| `406` on a proxy page | the client accepts neither HTML nor JSON | send an `Accept` header for HTML or JSON |
