<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->


# Local OSV mirror

Use the local OSV mirror to keep [OSV](https://osv.dev) advisories in your database. When an advisory is published or changed, OpenArtifact matches it against the package versions you already store, so findings appear without anyone starting a scan. The mirror is off by default.

Without the mirror, the `osv` scanner asks `api.osv.dev` about each package when you scan it. With the mirror, an ecosystem is answered locally once its first import has completed.

## Enabling it

```yaml
osvMirror:
  enabled: true
```

or `OPENARTIFACT_OSV_MIRROR_ENABLED=true`. The first import downloads every enabled ecosystem; the npm and PyPI archives are the largest, so allow disk space in `tempDir` and some minutes. Watch progress in the card at the top of **Security** in the web UI, with [`GET /api/v1/vuln/osv-mirror`](#status-endpoint) or the [metrics](#osv-mirror-metrics).

| YAML key (`osvMirror`) | Environment variable (`OPENARTIFACT_OSV_MIRROR_`) | Default | |
|---|---|---|---|
| `enabled` | `ENABLED` | `false` | turns the mirror on |
| `baseUrl` | `BASE_URL` | `https://osv-vulnerabilities.storage.googleapis.com` | the archive is fetched from `<baseUrl>/<Ecosystem>/all.zip`. Private hosts are allowed |
| `ecosystems` | `ECOSYSTEMS` | all supported | comma list of OSV ecosystem names |
| `interval` | `INTERVAL` | `24h` | Go duration between imports, at least `1h` |
| `tempDir` | `TEMP_DIR` | OS temp dir | where the archive is spooled while it is imported |
| `maxArchiveBytes` | `MAX_ARCHIVE_BYTES` | 2 GiB | larger downloads abort; so does an archive that decompresses to more than 32 times this |

## How it works

- **Download**: once per `interval`, OpenArtifact downloads the export `<baseUrl>/<Ecosystem>/all.zip` of each enabled ecosystem and imports it. With several replicas, only one imports at a time.
- **Unchanged exports**: a conditional request means an unchanged export costs one request.
- **Size limits**: a download larger than `maxArchiveBytes` is aborted, and so is an archive that decompresses to more than 32 times that. An advisory record larger than 16 MiB is skipped. The archive is spooled to `tempDir`, not held in memory.
- **Integrity**: when the export comes from Google Cloud Storage (the default), its CRC32C checksum is verified and a mismatch fails the import. This catches a corrupted transfer only. OSV does not sign its export, so the check does not prove who produced it. An export served without that header is imported unchecked.
- **Incremental import**: records not newer than the last import are skipped. An interrupted import resumes where it stopped.
- **Re-matching**: after an import, every stored package version named by a changed advisory is matched again, together with versions stored since the last re-match. This covers hosted repositories and the proxy cache.
- **Removed advisories**: an advisory that is no longer in the export was removed at the source. The import deletes it, re-matches the packages it affected and closes the findings it caused. An empty export never clears the mirror.
- **Withdrawn advisories**: an advisory that is withdrawn but still listed stays stored and stops matching.
- **New ecosystems**: when you add an ecosystem to `ecosystems`, advisories already stored for another ecosystem are applied again for it on the next import.

### What you see

- Results are stored like a manual `osv` scan, so the [security inbox](findings.md), [VEX decisions](sbom.md#vex), [exports](scanning.md#exporting-findings-sarif-csv-json) and [quarantine policies](scanning.md#quarantine) work unchanged.
- The mirror never changes the quarantine flag of a package.
- When no advisory affects a version any more, its result shows 0 vulnerabilities.
- The mirror writes the newest `osv` scan of a package, so a mirror result can replace an older manual scan of the same package in the inbox.
- The first import of an ecosystem is a silent baseline. Findings are created, but no per-finding webhook or audit entry is produced; one summary line is logged.
- After that, each package version with new advisories gets one [`vulnerability.new` webhook](../operations/webhooks.md) (up to 50 advisories listed) and a `scan.vuln_detected` audit entry.
- Advisories that are withdrawn or stop affecting a version are recorded as `scan.vuln_resolved` (audit only, no webhook). See the [audit log](../operations/monitoring.md#audit-log).
- Finding ids are OSV ids. CVE aliases are kept, and findings carry them as `aliases`. The [KEV and EPSS signals](exploit-signals.md) use them to find the CVE of a finding that stored only a GHSA id.

## Air-gapped installs

Point `baseUrl` at an internal HTTP(S) server that serves the same layout as the public bucket: one `all.zip` per ecosystem under `<Ecosystem>/`, using the OSV ecosystem names as directory names (`crates.io`, `Go`, `Maven`, `npm`, `NuGet`, `PyPI`, `RubyGems`). Any static file server works; it should send `ETag` or `Last-Modified` so unchanged files are not downloaded again.

Build the copy on a connected host and move it across, then repeat on whatever schedule you want updates:

```sh
for eco in crates.io Go Maven npm NuGet PyPI RubyGems; do
  mkdir -p /srv/osv/$eco
  curl -fsSL -o /srv/osv/$eco/all.zip "https://osv-vulnerabilities.storage.googleapis.com/$eco/all.zip"
done
# serve /srv/osv with nginx, Caddy or object storage, then:
#   OPENARTIFACT_OSV_MIRROR_BASE_URL=https://osv.internal.example
```

With the mirror populated, the `osv` scanner needs no outbound access for the ecosystems it covers. Ecosystems the mirror does not cover (below) still need `api.osv.dev` for manual scans.

## Supported ecosystems and matching

| OSV ecosystem | Repository formats |
|---|---|
| `crates.io` | cargo |
| `Go` | go |
| `Maven` | maven |
| `npm` | npm |
| `NuGet` | nuget |
| `PyPI` | pypi |
| `RubyGems` | rubygems |

Not mirrored: `apk`, `deb`, `rpm` and the other distribution-scoped ecosystems (Alpine, Debian, Rocky, Ubuntu and so on), formats without an OSV ecosystem (conda, Helm), advisories whose affected ranges are `GIT` ranges or `ecosystem_specific` ranges. The online API still serves these through a manual scan.

Matching handles `SEMVER` and `ECOSYSTEM` ranges with the ordering of each ecosystem (semantic versioning for crates.io, Go and npm; the native ordering of Maven, NuGet, PyPI and RubyGems), plus exact `versions` lists. Package names are normalised per ecosystem before comparing (Maven and NuGet case-insensitively; PyPI as in PEP 503; and so on).

## Status endpoint

`GET /api/v1/vuln/osv-mirror` (admin) returns `enabled`, `source_url`, `interval_seconds`, an `ecosystems` list (`ecosystem`, `populated`, `advisories`, `high_water`, `last_checked_at`, `last_imported_at`, `last_import_seconds`, `last_rematch_seconds`, `last_error`, `rematch_pending`) and the `attribution`. It works while the mirror is disabled (`enabled: false`).

## OSV mirror metrics

| Metric | Type | Labels |
|---|---|---|
| `openartifact_osv_mirror_last_import_timestamp_seconds` | gauge | `ecosystem` |
| `openartifact_osv_mirror_advisories` | gauge | `ecosystem` |
| `openartifact_osv_mirror_imports_total` | counter | `ecosystem`, `result` (`imported`, `unchanged`, `error`) |
| `openartifact_osv_mirror_rematch_duration_seconds` | histogram | |
| `openartifact_osv_mirror_findings_total` | counter | `change` (`new`, `updated`, `resolved`) |

Details are in [Monitoring](../operations/monitoring.md#metric-families). A growing gap to `last_import_timestamp_seconds` or `imports_total{result="error"}` means the export is unreachable.

## Attribution and license

The advisory data is from [OSV](https://osv.dev) and is licensed under [CC-BY 4.0](https://creativecommons.org/licenses/by/4.0/). The web UI shows the attribution next to the mirror status, and findings that come from it should keep it when you republish them. See the [OSV data documentation](https://google.github.io/osv.dev/data/) for the sources and terms of each advisory.
