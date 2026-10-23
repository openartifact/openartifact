<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# License metadata

How OpenArtifact finds the licenses of a package, and what it does with licenses it cannot resolve. Policies, quarantine and the scan endpoints are in [Scanning](scanning.md#license-scans-and-policies).

## The package file is the source of truth

An upload request carries claims about the package: twine's form fields, npm's publish JSON, cargo's publish metadata. A client that sends none (`curl`, minimal tools) or sends false ones would otherwise decide what [license policies](scanning.md#blocking-licenses) see. OpenArtifact therefore reads the license from the uploaded file itself:

- on **upload**, before anything is stored, and the result is what a `block` policy is enforced on;
- on a **license scan** and when license **properties** change, from the file stored in the blob store.

The request's own fields are only a **fallback**, used when the file is readable but declares no license. They are never merged with the file's licenses and never override them. A file whose licenses cannot be read at all is a different case, described [below](#files-whose-license-cannot-be-read).

## What is read, per format

| Format | File read | Order |
|---|---|---|
| PyPI | wheel: `*.dist-info/METADATA`; sdist: `PKG-INFO` of a `.tar.gz` or `.zip` | `License-Expression` ([PEP 639](https://peps.python.org/pep-0639/)); else a short, single-line `License:` (at most 100 characters, not `UNKNOWN`: longer values are the license text); else the `License ::` classifiers that name a license. The first step that yields something wins. Form fallback: `license_expression`, `license`, `classifiers` |
| npm | `package.json` of the tarball (depth one: `package/package.json`) | `license` (a string or `{"type": …}`), else the legacy `licenses` array. Fallback: the version manifest in the publish JSON |
| Cargo | `Cargo.toml` of the `.crate` | `package.license`; a legacy `MIT/Apache-2.0` is read as `MIT OR Apache-2.0`. `license-file` alone declares nothing. Fallback: `license` in the publish metadata |
| NuGet | the root `.nuspec` of the `.nupkg` | `<license type="expression">`, else a `<licenseUrl>` that names one license (`https://licenses.nuget.org/<expression>`, or a canonical apache.org, opensource.org, gnu.org URL). Any other URL names no license. `<license type="file">` declares nothing |
| Maven | the uploaded `.pom`, or the `META-INF/maven/**/pom.xml` embedded in a stored `.jar` | each `<license><name>`; the `<url>` when a license has no name and the URL is a canonical one |
| RubyGems | `metadata.gz` of the `.gem` | the gemspec `licenses` |

Other formats keep their own readers (Debian, RPM, Conda, Helm, Docker, Ivy; see [Scanning](scanning.md#blocking-licenses)); Conan, Go and raw packages declare no license, so use [properties](../repositories/properties.md).

## Names that are not SPDX

Licenses are kept as the package wrote them, then normalized: a trove classifier, a Maven name or a common alias that names exactly one license becomes its SPDX id (`License :: OSI Approved :: MIT License` → `MIT`, `The Apache Software License, Version 2.0` → `Apache-2.0`). Names that do not identify one license are **not** guessed: `BSD`, the classifier `BSD License` (2, 3 or 4 clause?), `GNU General Public License (GPL)` (which version?), `Artistic License`, `Public Domain` stay as written.

A license that is not a valid SPDX expression of known identifiers after this step is **unresolved**:

- the scan lists it in `unresolved_licenses` and the web UI marks it;
- it is evaluated as written: a deny list entry or an allow list entry that spells it the same way (case-insensitive) matches it, nothing else does. For example, Django 3.0's wheel declares `License: BSD`; a policy denying `BSD` matches it, one denying `BSD-3-Clause` does not;
- under an allow list, an unresolved license is not allowed unless the list names it.

## Files whose license cannot be read

A client can send a file this server cannot read but that its own tools do read (a PyPI `.egg` or `.exe`, a damaged archive, a wheel with no `METADATA`, a `.pom` that does not parse). Trusting the request's fields for it would let those fields override the real license, so the outcome depends on the repository's policies:

| Repository has an enabled policy with action `block` | Upload | Scan, license property change |
|---|---|---|
| yes | refused: `422`, code `LICENSE_METADATA_UNREADABLE`, nothing stored | refused: `422 unreadable_artifact`; a property change is saved but not evaluated |
| no | admitted on the client's fields | evaluated on the artifact's license properties |

This applies to the package files of PyPI, npm, Cargo, NuGet, RubyGems and Maven `.pom` files; a Maven jar, a checksum or `maven-metadata.xml` is judged through its `.pom`. A readable file that simply declares no license is not refused: it cannot hide a license, and the client's fields are the fallback as before. To publish such a file under a blocking policy, add the license to the package metadata in a form this server reads.

Every license scan result says where its licenses came from in `license_source`:

- `artifact`: the licenses include ones read from the stored package file;
- `client_declared`: they only come from what a client declared (the artifact's [license properties](../repositories/properties.md)), because the file declares no license, cannot be read while no blocking policy applies, or is of a format that has no license metadata (raw, Go, Conan). The scan page labels these results.

Results stored before this field existed are `client_declared`.

## Hostile files

Package files are untrusted. Reading them never writes to disk and is bounded:

- each metadata file is limited to 1 MiB (4 MiB for a pom), and a decompressed stream to 512 MiB;
- only fixed member paths are looked at; names with `..`, a leading `/` or a backslash never match, so a traversal path cannot stand in for a metadata file;
- an archive with **two** candidates for the same metadata file (two `METADATA`, two `package.json`, two `.nuspec`) is refused, because installers differ on which one they read.

An upload that fails these limits answers `422` with code `LICENSE_METADATA_UNSAFE` and stores nothing. A scan or property change answers `422 unsafe_artifact`. A file that is merely malformed (not a zip, unparsable `Cargo.toml`) is treated as declaring nothing and the fallback applies.

## Limitations

- Maven licenses are not inherited from a `<parent>` pom.
- Without a blocking policy, a file that cannot be read is judged on the client's fields (marked `client_declared`).
- Debian, RPM, Conda, Helm, Docker and Ivy/sbt keep their own readers and are not covered by `LICENSE_METADATA_UNREADABLE`: a descriptor that does not parse declares no license there.
- The metadata the registry **serves** (npm packument, PyPI JSON, the Cargo index) is still the client's. Only the license policy decision and the license scan use the file's licenses.
- Existing license scan results are not rewritten: scan the artifact again to pick up the file's licenses.
