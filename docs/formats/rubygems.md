<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# RubyGems

Use a gem server to push, yank and install gems with `gem` and `bundler`. It can be hosted, a proxy of a public gem server such as rubygems.org, or a group of both. It serves the compact index (what bundler and current `gem` read), the older Marshal spec indexes, gemspecs, gem download, info and search.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO/` |
| Format id | `rubygems` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://rubygems.org` |

## Client setup

The web UI connect panel shows the same commands with your own URL.

```sh
gem sources --add HOST/repos/REPO/
GEM_HOST_API_KEY="Bearer <TOKEN>" gem push my-gem-1.0.0.gem --host HOST/repos/REPO
```

In a Gemfile, use the repository URL as the source:

```ruby
source "HOST/repos/REPO/"
```

The server accepts `Authorization: Bearer <TOKEN>`, or Basic with the token as the password. `gem` sends the API key verbatim as the header value, so it carries the `Bearer ` prefix. See [authentication](../security/authentication.md).

## Publish

`POST /api/v1/gems` with the raw `.gem` as the body. Success is `201`.

| Method and path | Purpose |
|---|---|
| `POST /api/v1/gems` | push (body is the raw `.gem`) |
| `DELETE /api/v1/gems/yank` | yank; form fields `gem_name` and `version` |

### Push validation

- The body is at most 200 MiB (`413` above it). An invalid gem archive is `400 invalid gem file`.
- The metadata needs a valid `name` and `version`. The platform defaults to `ruby`. Invalid identifiers are `400`. A version starts with a digit and holds no `-`.
- An existing name, version and platform is `409 gem version already exists`. Gems are immutable once pushed.
- A gem whose file name (`<name>-<version>[-<platform>]`) another gem already resolves to is `409`. `a-1-2.gem` is both `a` `1` platform `2` and `a-1` `2`. A download or gemspec request for an already ambiguous file name is `409` too.
- `metadata.gz` is limited to 4 MiB uncompressed. Marshal metadata nested deeper than 64 levels is not decoded as a gemspec. The raw metadata is used instead, like any other unparseable payload.

### Yank

- Yank needs `gem_name` and `version` (`400` otherwise; body limit 64 KiB).
- Yanked gems drop out of the spec indexes, the info responses and search.
- A version with nothing to yank is `404`.
- With `immutable_releases` on the repository, yanking a release is `409`. An admin may force it with `?force=true`.

The [storage quota](../repositories/cleanup-and-quotas.md) is enforced on push.

## Install and consume

`bundle install` and `gem install` ask for `versions`, then `info/<gem>` for the gems they need, then the `.gem`. `gem install` also reads the gemspec.

Endpoints, relative to `HOST/repos/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /versions`, `/names`, `/info/<gem>` | compact index: gems and versions, gem names, and per version the runtime dependencies and the SHA-256 of the `.gem` |
| `GET /specs.4.8.gz`, `/latest_specs.4.8.gz`, `/prerelease_specs.4.8.gz` | gzipped Marshal indexes (latest is the newest release per name and platform) |
| `GET /quick/Marshal.4.8/<file>` | gemspec |
| `GET /gems/<file>.gem` | gem download |
| `GET /api/v1/gems/<gem>.json` | gem info |
| `GET /api/v1/search.json?query=` | search |

Info and search return snake_case entries: `name`, `version`, `platform`, `summary`, `description`, `homepage`, `authors`, `licenses`, `sha256`, `downloads`, `size`, `created_at`.

### Compact index

All three repository types serve the compact index the way rubygems.org does, because bundler's incremental update depends on it.

- `ETag` is the MD5 of the file. `Repr-Digest` and `Digest` carry the SHA-256. `If-None-Match` is answered `304`.
- Compact index files are never gzip-encoded, so the digests describe the bytes on the wire.
- **Hosted:** files are generated from the stored gem metadata at request time. They are regenerated whole, so `Range` is ignored and the full file is answered (`200`). bundler accepts that.
- A hosted info line carries the runtime dependencies (`name:>= 1.0&< 3`) and `checksum`. The optional `ruby:` and `rubygems:` requirements are not stored and not emitted. Yanked gems are omitted.
- A gem whose name, version or platform the line format cannot carry is listed in the legacy specs only.

## Proxy

Create a proxy with `repo_type: "proxy"`. If you omit `upstream_url`, it defaults to `https://rubygems.org`. Any server that speaks the compact index works.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"rubygems-org","format":"rubygems","repo_type":"proxy"}'
```

Clients use `HOST/repos/REPO/` as the source, as shown in [Client setup](#client-setup).

| Path | Handling |
|---|---|
| `versions`, `info/<gem>`, `names` | cached verbatim for `cache_ttl_seconds`; stale copies are served while the upstream is down, up to `max_stale_seconds` |
| `gems/<file>.gem` | cached forever once verified |
| `quick/Marshal.4.8/<file>.gemspec.rz`, `specs.4.8.gz`, `latest_specs.4.8.gz`, `prerelease_specs.4.8.gz` | cached after checking that they hold a Marshal 4.8 stream |

- **Compact index files** are checked as compact index files before they are stored. An HTML login page from a misconfigured upstream is `502` and is not cached.
- **Ranges:** the proxy honors `Range` (`206`, `416`, `If-Range`), so bundler fetches only the new tail of the append-only `versions` file.
- **Revalidation** after the TTL refetches the whole file from the upstream. This is about 25 MB for rubygems.org. Raise `cache_ttl_seconds` for an air-gapped mirror.
- **Gem files:** the bytes must match the `checksum:` of the version's info line. A mismatch is `502` and is never stored. An upstream without a compact index, or an info line without a checksum, is cached for the TTL only.
- **Ambiguous names:** a file name does not say where the gem name ends (`a-1-2.gem` is `a-1` 2 or `a` 1-2), so the proxy looks up every reading.
- A version the info file does not list is `404` without contacting the upstream.
- The proxy is read-only: push and yank answer `405`. Upstream credentials are stored encrypted and sent only to the upstream host.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The RubyGems specifics:

- **Spelling:** rules name the gem as written in the info file path and the version without its platform, so one rule covers every platform build.
- **Age source:** the compact index has no times. The proxy reads `created_at` of each version from the upstream API `/api/v1/versions/<gem>.json`, once per gem while the gate is on. It is cached like the other metadata.
- **The gate needs that API.** An upstream that answers `404` for it cannot date its versions, so they pass. An API that fails (`5xx`, not JSON) makes the request fail with `502` rather than serve an ungated file.
- **Hidden version:** it is left out of `info/<gem>`. Its `.gem` and gemspec answer `403`. Under minimum age they are served if already cached. Curation blocks cached files too.
- **Unknown to the API:** a version the API does not list counts as too new.
- **No version left:** the gem is `404`. bundler and `gem` then resolve to an older release.
- **Not filtered:** `versions`, `names` and the legacy `specs.4.8.gz` family are served as the upstream sends them. `versions` still names a hidden version, but it cannot be resolved or downloaded.
- For a gem with hidden versions, the MD5 in `versions` differs from the narrowed info file. bundler then fetches that gem's info on every run (a `304` when unchanged) until the version has aged in.
- The narrowed info file gets its own `ETag`, `Repr-Digest` and `Range` support.

## Group

A group is an ordered list of hosted and RubyGems proxy members. Clients use `HOST/repos/GROUP/` as the source. Group repositories are read-only.

- Hosted members win a gem name first. For a name a readable hosted member holds, the versions, info file and `.gem` of that name come from hosted members only, unless the group sets `merge_proxy_versions`. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- The `.gem` and gemspec file name is ambiguous (see above). The gem is hosted-owned if the hosted members hold any reading of the name.
- `info/<gem>`: one member's file is served as it is. Several are merged into the union of their versions, and the earlier member wins a duplicate.
- Of the proxy members, only the first one that has the gem takes part. A group of several mirrors does not fetch every gem from each of them.
- `versions` is built from the members' `versions` files under the same rules. For a gem held by several members, the line carries the MD5 of the merged info file, as bundler requires. At most 5000 gems may be held by several members in one request (`502` beyond it). `names` is the union.
- `.gem` files and gemspecs are served by the first member that has them.
- A group of one readable member passes everything through to it, ranges included.
- A proxy member's gate applies through the group. A hidden `.gem` is `403` and the group does not try the next member.
- A member that fails makes the answer uncacheable (`Cache-Control: no-store`, no validator).
- The legacy `specs.4.8.gz` family is not merged and answers `404`. `gem search -r` and `gem list -r` do not work against a group. `gem install` and bundler do. Use a single member's repository for them.

## Limits and behaviour

- Push body: 200 MiB. Yank body: 64 KiB. `metadata.gz`: 4 MiB uncompressed.
- Gems are immutable once pushed. Yank hides a gem from indexes and search.
- Hosted `versions` is cached per repository state.
- RubyGems has no repository signature. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `401` on `gem push` | the API key lacks the `Bearer ` prefix | set `GEM_HOST_API_KEY="Bearer <TOKEN>"` |
| `400 invalid gem file` | the body is not a gem archive | rebuild with `gem build` |
| `409 gem version already exists` | gems are immutable | bump the version |
| `409` on download or gemspec | the file name could mean two different gems | request the gem through a repository that holds only one of them |
| `409` on yank | `immutable_releases` is on | ask an admin to yank with `?force=true` |
| `405` on push or yank | the repository is a proxy or a group | push to a hosted repository |
| `502` for a `.gem` through a proxy | the checksum did not match, or the upstream sent an HTML page | retry, then check `upstream_url` |
| `403` on a `.gem` | minimum age or curation holds it back | see [Minimum age and curation](#minimum-age-and-curation) |
| `gem search -r` fails against a group | the legacy specs indexes are not merged | search a member repository |
| `502` on a group `versions` request | more than 5000 gems are held by several members | split the group so fewer gems overlap |
