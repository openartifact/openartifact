<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# RubyGems

Gem server: compact index (what bundler and current `gem` read), Marshal spec indexes, gemspecs, gem download, push, yank, info and search. Hosted repositories are served from the database, proxy repositories mirror a public gem server (default `https://rubygems.org`), group repositories merge both.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
gem sources --add HOST/repos/REPO/
GEM_HOST_API_KEY="Bearer <TOKEN>" gem push my-gem-1.0.0.gem --host HOST/repos/REPO
```

The server accepts `Authorization: Bearer <TOKEN>` or Basic with the token as password. `gem` sends the API key verbatim as the header value, so it carries the `Bearer ` prefix. See [authentication](../security/authentication.md).

## Endpoints

Relative to `HOST/repos/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /versions`, `/names`, `/info/<gem>` | compact index: gems and their versions with the MD5 of each info file, gem names, and per version the runtime dependencies and the SHA-256 of the `.gem` |
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

Specs and the compact index are generated from the stored gem metadata at request time.

## Compact index

`bundle install` and `gem install` ask for `versions`, then `info/<gem>` for the gems they need, then the `.gem` (and `gem install` the gemspec). All three kinds of repository serve them the way rubygems.org does, because bundler's incremental update depends on it:

- `ETag` is the MD5 of the file (bundler before 2.5 checks the bytes it assembled against it); `Repr-Digest` and `Digest` carry the SHA-256. `If-None-Match` is answered `304`. Compact index files are never gzip-encoded, so the digests describe the bytes on the wire.
- **Proxy:** `versions`, `info/<gem>` and `names` are cached verbatim for `cache_ttl_seconds` and checked as compact index files before they are stored (an HTML login page from a misconfigured upstream is a `502`, not cached). They honor `Range` (`206`, `416`, `If-Range`), so bundler fetches only the new tail of the append-only `versions` file. Revalidation after the TTL refetches the whole file from the upstream (a 25 MB download for rubygems.org); raise `cache_ttl_seconds` for an air-gapped mirror.
- **Hosted and group:** the files are regenerated whole, so `Range` is ignored and the full file is answered (`200`); bundler accepts that. Hosted `versions` is cached per repository state. A hosted info line carries the runtime dependencies (`name:>= 1.0&< 3`) and `checksum`; the optional `ruby:` and `rubygems:` requirements are not stored and not emitted. Yanked gems are omitted. A gem whose name, version or platform the line format cannot carry is listed in the legacy specs only.

## Proxy repositories

`upstream_url` defaults to `https://rubygems.org`; any server speaking the compact index works. Clients use the repository URL as their source (`gem sources --add HOST/repos/REPO/`, `source "HOST/repos/REPO/"` in a Gemfile).

| Path | Handling |
|---|---|
| `versions`, `info/<gem>`, `names` | cached verbatim, `cache_ttl_seconds`; stale copies are served while the upstream is down, up to `max_stale_seconds` |
| `gems/<file>.gem` | cached forever once verified: the bytes must match the `checksum:` of the version's info line (a mismatch is `502` and is never stored). A file name does not say where the gem name ends (`a-1-2.gem` is `a-1` 2 or `a` 1-2), so every reading is looked up. An upstream without a compact index, or an info line without checksum, is cached for the TTL only |
| `quick/Marshal.4.8/<file>.gemspec.rz`, `specs.4.8.gz`, `latest_specs.4.8.gz`, `prerelease_specs.4.8.gz` | cached verbatim after checking that they hold a Marshal 4.8 stream |

A version the info file does not list is `404` without contacting the upstream for the file. The usual proxy rules apply: read-only (`405` on push and yank), quarantine and scanning of gems, RBAC, SSRF-safe upstream client, upstream credentials stored encrypted and sent only to the upstream host.

## Group repositories

Members are read in priority order.

- **Hosted wins:** for a gem name a readable hosted member holds, the versions, info file and `.gem` of that name come from the hosted members only (dependency confusion), unless the group sets `merge_proxy_versions`. The file name of a `.gem` or gemspec is ambiguous (see above): the gem is hosted-owned if the hosted members hold any reading of the name.
- `info/<gem>`: one member's file is served as it is; several are merged into the union of their versions, the earlier member winning a duplicate. Of the proxy members only the first one that has the gem takes part, so a group of several mirrors does not fetch every gem from each of them. A member that fails leaves the answer uncacheable (`Cache-Control: no-store`, no validator).
- `versions` is built from the members' `versions` files under the same rules; for a gem held by several members the line carries the MD5 of the merged info file, as bundler requires. At most 5000 gems may be held by several members in one request (`502` beyond it). `names` is the union.
- `gems/<file>.gem` and gemspecs are served by the first member that has them.
- A group of one readable member passes everything through to it, ranges included.
- The legacy `specs.4.8.gz` family is not merged and answers `404`: `gem search -r` and `gem list -r` do not work against a group, `gem install` and `bundler` do. Use the repository of a single member for them.
