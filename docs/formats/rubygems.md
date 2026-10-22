<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# RubyGems

Hosted gem server: Marshal spec indexes, gemspecs, gem download, push, yank, info and search.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` |
| Repository types | hosted only. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
gem sources --add HOST/repos/REPO/
GEM_HOST_API_KEY=<TOKEN> gem push my-gem-1.0.0.gem --host HOST/repos/REPO
```

The server accepts `Authorization: Bearer <TOKEN>` or Basic with the token as password. `gem` sends the API key verbatim as the header value, so if push answers `401` use `GEM_HOST_API_KEY="Bearer <TOKEN>"`. See [authentication](../security/authentication.md).

## Endpoints

Relative to `HOST/repos/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /specs.4.8.gz`, `/latest_specs.4.8.gz`, `/prerelease_specs.4.8.gz` | gzipped Marshal indexes (latest = newest release per name and platform) |
| `GET /quick/Marshal.4.8/<file>` | gemspec |
| `GET /gems/<file>.gem` | gem download |
| `POST /api/v1/gems` | push (body = raw `.gem`) |
| `DELETE /api/v1/gems/yank` | yank, form fields `gem_name` and `version` |
| `GET /api/v1/gems/<gem>.json` | gem info |
| `GET /api/v1/search.json?query=` | search |

## Push validation

- Body limit 200 MiB (`413` above it); invalid gem archive is `400 invalid gem file`.
- Metadata must contain a valid `name` and `version`; platform defaults to `ruby`. Invalid identifiers give `400`. A version starts with a digit and holds no `-`.
- A gem whose file name (`<name>-<version>[-<platform>]`) another gem already resolves to (`a-1-2.gem` is both `a` `1` platform `2` and `a-1` `2`) is `409`; a download or gemspec request for an already ambiguous file name is `409` too.
- `metadata.gz` is limited to 4 MiB uncompressed. A Marshal 4.8 payload nested deeper than 64 levels is not decoded as a gemspec (it falls back to the raw metadata, like any other unparseable payload) instead of exhausting the stack.
- An existing name/version/platform is `409 gem version already exists`: gems are immutable once pushed.
- Yank requires `gem_name` and `version` (`400` otherwise; body limit 64 KiB). Yanked gems drop out of the spec indexes, the info and the search responses. Yanking a release of a repository with `immutable_releases` is `409` (an admin may force it with `?force=true`); a version with nothing to yank is `404`.
- Info and search return snake_case entries (`name`, `version`, `platform`, `summary`, `description`, `homepage`, `authors`, `licenses`, `sha256`, `downloads`, `size`, `created_at`).

Specs are generated from the stored gem metadata at request time.
