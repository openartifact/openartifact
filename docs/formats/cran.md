<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# CRAN (R)

Host R packages in a CRAN-style repository that `install.packages()` and `available.packages()` read, and upload source and binary packages with a plain HTTP `PUT`. Use a hosted repository for your own packages, a proxy to cache a CRAN mirror, and a group to put both behind one URL.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/cran/REPO` |
| Index | `HOST/repos/cran/REPO/src/contrib/PACKAGES` |
| Format id | `cran` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://cloud.r-project.org` |

See [hosted, proxy, group](../repositories/hosted-proxy-group.md) for the repository types. The repository is served under `/repos/cran/REPO`, so it never collides with another format.

## Client setup

The web UI connect panel of a repository shows the same commands with your own URL.

```r
# ~/.Rprofile (or Rprofile.site)
options(repos = c(oa = "HOST/repos/cran/REPO"))
install.packages("mypackage")
```

For a private repository put the credentials in the URL (the username is ignored, the token is the password):

```r
options(repos = c(oa = "https://<USERNAME>:<TOKEN>@HOST/repos/cran/REPO"))
```

Add `CRAN = "https://cloud.r-project.org"` to `repos` only if you want R to ask CRAN directly as well. A group with a CRAN proxy member already serves both.

## Publish

Build the package and upload the file with `PUT`:

```sh
R CMD build mypackage
curl -u <USERNAME>:<TOKEN> -T mypackage_1.0.0.tar.gz HOST/repos/cran/REPO/src/contrib/mypackage_1.0.0.tar.gz
```

Binary packages go to the tree of the platform and R release (`R CMD INSTALL --build` makes them):

```sh
curl -u <USERNAME>:<TOKEN> -T mypackage_1.0.0.zip HOST/repos/cran/REPO/bin/windows/contrib/4.5/mypackage_1.0.0.zip
curl -u <USERNAME>:<TOKEN> -T mypackage_1.0.0.tgz HOST/repos/cran/REPO/bin/macosx/big-sur-arm64/contrib/4.5/mypackage_1.0.0.tgz
```

The answer is `201` with `{"id","package","version","path","md5","sha256","size"}`.

- The file name must be `<package>_<version>.tar.gz` (source), `.zip` (Windows) or `.tgz` (macOS), and the path must be the tree it belongs to. The archive's `DESCRIPTION` is read: its `Package` and `Version` must match the file name and the archive's top directory. Anything else is `400`.
- A package is at most 1 GiB (`413`).
- An existing path is `409`. Releases are immutable: bump `Version`.
- A binary joins the version of its source. Only a source upload sets the title, license and other descriptive fields of a version.
- The `License` field is checked against the repository's [license policy](../repositories/hosted-proxy-group.md) at upload. Alternatives separated by `|` are each checked and `+ file LICENSE` is dropped. R spells licenses its own way (`GPL-3`, `MIT + file LICENSE`), so a policy must list the names as the packages write them.
- The [storage quota](../repositories/cleanup-and-quotas.md) is enforced on upload (`507`) and released on delete.

## Install and consume

Relative to `HOST/repos/cran/REPO`:

| Request | Behaviour |
|---|---|
| `GET /src/contrib/PACKAGES`, `.gz`, `.rds` | index of the source tree |
| `GET /src/contrib/<pkg>_<ver>.tar.gz` | source package |
| `GET /src/contrib/Archive/<pkg>/` | HTML listing of the files of one package, as `remotes::install_version` and renv read it |
| `GET /src/contrib/Archive/<pkg>/<pkg>_<ver>.tar.gz` | the same file as the canonical path |
| `GET /bin/windows/contrib/<R>/PACKAGES` and the file | Windows binaries, for example `4.5` |
| `GET /bin/macosx/[<flavour>/]contrib/<R>/PACKAGES` and the file | macOS binaries |
| `PUT` on any file path above | upload |
| `DELETE` on any file path above | delete one file, `204` |

### Index

Each `PACKAGES` lists the newest version of every package of its tree with the fields of its `DESCRIPTION` that R needs (`Depends`, `Imports`, `LinkingTo`, `Suggests`, `License`, `NeedsCompilation`, `MD5sum`, ...). Older versions are not listed; they are served at their canonical path and under `Archive/`, as on CRAN.

`PACKAGES`, `PACKAGES.gz` and `PACKAGES.rds` are rendered from the stored package rows when asked. There is no generation worker and no lock: an upload is in the index as soon as it is stored. The `ETag` follows the set of live files, so a client revalidating with `If-None-Match` gets `304` while nothing changed.

## Proxy

A CRAN proxy mirrors a CRAN mirror (`https://cloud.r-project.org` unless `upstream_url` says otherwise).

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"cran-mirror","format":"cran","repo_type":"proxy"}'
```

```r
options(repos = c(oa = "https://<USERNAME>:<TOKEN>@HOST/repos/cran/cran-mirror"))
install.packages("glue")
```

Uploading to a proxy and every other write answer `405`.

- **Indexes.** One fetch of the upstream `PACKAGES` serves `PACKAGES`, `.gz` and `.rds`, cached for `cache_ttl_seconds` and served stale while the upstream fails. A body that is not an index (an HTML error page) is `502` and not cached.
- **Source packages.** A file listed in the cached index is checked against its `MD5sum` on first pull. A mismatch is `502` and nothing is stored. A verified file is immutable, so a second pull works with the upstream down.
- **Archived versions.** A version that is not in the index (`Archive/<pkg>/<pkg>_<ver>.tar.gz`) has nothing to verify against; it is fetched, cached for the repository TTL only and not pinned.
- **Binaries.** CRAN rebuilds binaries in place, so they are cached for the TTL only. A binary whose index entry is older than the file gives `502` until the index TTL expires and the index is fetched again.
- **Minimum age.** There is none: CRAN's index carries no publication date.
- **Curation.** [Rules](../repositories/hosted-proxy-group.md#package-curation) name the package and version. A matching version is refused for download and its record is left out of the index. Because an index lists only the newest version, a held newest version hides the package from the index until it is allowed; older versions stay reachable through `Archive/`.
- **Upstream down.** Cached indexes and files are always served.

## Group

A CRAN group is an ordered list of hosted and proxy CRAN members behind one repository URL.

- `PACKAGES`, `.gz` and `.rds` are built from the members' plain `PACKAGES`: the highest version of each package wins, the earlier member on a tie. A member that fails or serves an unreadable index while another contributes is ignored. With no contributor the group answers `404`, or `502` if a member failed.
- Files and `Archive/<pkg>/` listings come from the first member that has them.
- Hosted members win a package name: when a readable hosted member holds a package, the index lists only its hosted versions and downloads of that name come only from hosted members. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name). A failing hosted member answers `502`, never the proxy's package.
- Only the indexes, package files and `Archive/<pkg>/` listings are served. The `Archive/` root and everything else is `404`.
- Uploading to a group is `405`. Upload to a hosted member and read through the group.

## Limits and behaviour

- **Caching.** Indexes and listings are sent with `Cache-Control: public, max-age=60` for anonymous reads of a public repository and `private, no-cache` otherwise.
- **Delete.** Delete moves the file to the [trash](../operations/trash.md) (kind `cran_package`) by default. It leaves the indexes and downloads at once and the quota is released at purge. The last file of a package takes the package with it.
- **Immutable releases.** Every R version is a release, so with `immutable_releases` deleting a file is `409` unless an admin passes `?force=true`.
- **Quarantine.** Downloads of quarantined files are refused.
- **Vulnerabilities.** Scans use the OSV ecosystem `CRAN` (package URL type `cran`) online. There is no local mirror, and no dependency or manifest analysis of the package contents.
- **Not federated.** CRAN repositories cannot be federated.

## Troubleshooting

- **`install.packages` says "package ‘x’ is not available".** The index of the repository does not list it for your R version. Binary installs on Windows and macOS read the binary tree; fall back with `type = "source"`.
- **`401` from R.** The credentials are not in the URL. Put them in `options(repos = ...)` as shown above.
- **`409` on upload.** The path exists. Bump `Version`.
- **`502` on a proxy download.** The upstream file does not match its index `MD5sum`; retry after the index TTL, or check the upstream.
