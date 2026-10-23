<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# SBOM and VEX

Per-artifact software bills of materials (CycloneDX, SPDX) and vulnerability exploitability statements (OpenVEX, CycloneDX VEX), built from the data OpenArtifact already holds: the artifact's stored properties, the package listing, the latest license scan and the latest vulnerability scan. All endpoints are **admin only** and generation is **manual**: nothing is produced on upload. See [Scanning](scanning.md) for how the scans themselves run.

## SBOM

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/sbom/generate -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"my-lib@1.2.3","format":"cyclonedx"}'
curl -sS -OJ -H 'Authorization: Bearer <TOKEN>' HOST/api/v1/repositories/REPO/sbom/<ID>/download
# or
oa sbom generate REPO --artifact-id my-lib@1.2.3 --format spdx-json
oa sbom download REPO <ID> sbom.json
```

| Endpoint | |
|---|---|
| `POST …/sbom/generate` | `artifact_id`, `format`, optional `spec_version`; returns 201 |
| `GET …/sbom`, `GET …/sbom/{id}` | list / report (format, spec version, content type, component count, timestamps) |
| `GET …/sbom/{id}/download` | the document, served as attachment `sbom.cdx.json`, `sbom.spdx.json` or `sbom.spdx` |
| `DELETE …/sbom/{id}` | delete |

| `format` | Document | `spec_version` |
|---|---|---|
| `cyclonedx` | CycloneDX JSON (`application/json`) | `1.4`, `1.5` (default), `1.6` |
| `spdx-json` | SPDX JSON (`application/spdx+json`) | `2.3` |
| `spdx` | SPDX tag-value (`text/spdx`) | `2.3` |

Existing callers that pass `"spec_version":"1.4"` or `"2.3"` keep getting those versions. Documents are stored in the blob store.

### What a document contains

- **Subject**: the package named by `artifact_id` (split at the last `@` into name and version), typed from the repository format, with a [purl](https://github.com/package-url/purl-spec) (`pkg:npm/%40babel/core@7.0.0`, `pkg:maven/group/artifact@1.0`, ...). The tool (`OpenArtifact`, with the server version) is recorded in the metadata.
- **Hash**: the `sha256` of the package file when the package listing identifies exactly one match for the name and version. It is omitted rather than guessed when the match is ambiguous.
- **Components**: the dependencies the stored package file declares (table below), merged with the entries of the artifact's `dependencies` property, a JSON array such as `[{"name":"left-pad","version":"1.3.0","license":"MIT"}]` (a dependency may also carry `purl`, `sha256`, `license` and `licenses`). An entry the property lists keeps its property data; the file adds the rest, deduplicated by purl. The document includes the dependency graph (`dependencies` in CycloneDX, `DEPENDS_ON` relationships in SPDX): every component hangs directly off the packaged artifact, transitive edges are not reconstructed.
- **Licenses**: from the latest license scan and the `license` / `licenses` properties. A value that is an SPDX identifier or expression (`MIT OR Apache-2.0`, `GPL-2.0-or-later WITH Classpath-exception-2.0`) is canonicalised against the SPDX license list 3.29.0 and emitted as `license.id` / `expression`. Any other name is kept as `license.name` in CycloneDX and as a `LicenseRef-*` with its text in `hasExtractedLicensingInfos` in SPDX. `NOASSERTION` appears only when nothing is known.
- **Vulnerabilities** (CycloneDX): the findings of the latest completed or partial vulnerability scan, with `id`, `source` (`NVD`, `GITHUB`, `OSV`), `ratings` (severity, and the CVSS score when the scanner provided one), `cwes`, an `advisories` link, `affects` and an `analysis` block (below). SPDX documents carry the advisories as `SECURITY` external references.
- `metadata` in the request is rejected: components come from the stored properties, not from the caller.

### Components read from the package file

`artifact_id` is either `name@version` or the id of a row of the package listing (`GET …/packages`); the second form also gives the document the package's real name, version and hash. The package file is found through the listing (for Maven, the `.pom` is preferred over the jar next to it; a jar falls back to the `pom.xml` it embeds). It is read from the blob store with the same bounded reader the AI analysis and license scan use.

| Format | Read from | Notes |
|---|---|---|
| npm | `package.json` (`dependencies`, `optionalDependencies`), `npm-shrinkwrap.json` / `package-lock.json` when the tarball ships one | the lockfile pins exact versions and adds the installed transitive packages (dev-only entries are left out) and their licenses |
| pypi | `Requires-Dist` of `METADATA` / `PKG-INFO` | requirements gated on an `extra` are skipped; only `==` pins a version |
| maven, sbt | `<dependencies>` of the pom (`compile` and `runtime` scopes) | `${property}` references and `dependencyManagement` versions are resolved; unresolved ones leave the version out |
| ivy | `ivy.xml` dependencies | |
| cargo | `[dependencies]`, `[build-dependencies]` and `[target.*.dependencies]` of `Cargo.toml`; `Cargo.lock` when the crate ships one | the lockfile pins versions and adds locked packages; only `=x.y.z` pins a version in `Cargo.toml` |
| go | `require` of `go.mod` (the `.mod` file or the one inside the module zip) | exact versions |
| nuget | `.nuspec` dependency groups | only `[x.y.z]` pins a version |
| rubygems | runtime dependencies of the gemspec | only `= x.y.z` pins a version |
| helm | `Chart.yaml` and `requirements.yaml` dependencies, `Chart.lock` when present | the lock pins versions |
| conda | `depends` of `info/index.json` | only `==x.y.z` pins a version |
| deb | `Pre-Depends` and `Depends` of the control file | the first of a set of alternatives; purls use the namespace `ubuntu` when the version or maintainer says so, `debian` otherwise |
| rpm | `Requires` of the header | rpmlib features, file paths and shared-object or boolean capabilities are skipped; the purl namespace is derived from `Vendor` or the release tag |
| conan | quoted `name/version` references in the `requires` of `conanfile.py` | `tool_requires` and `test_requires` are skipped; the recipe is never executed |
| docker, raw | not read | components come from the `dependencies` property only |

A dependency whose manifest gives only a version range has no `version` and a purl without one; the range is kept in the CycloneDX component property `openartifact:version_range`. A file that cannot be read (damaged, not the archive its name says) leaves the properties as the only source and is logged. A file that cannot be read **safely** (a manifest or lockfile over 8 MiB, a decompressed stream over the limit, an archive with two candidates for the same manifest) fails the request with `422 unsafe_artifact`. At most 10 000 dependencies are listed per artifact. See [properties](../repositories/properties.md).

## VEX

A VEX document states whether the vulnerabilities found in an artifact actually affect it. OpenArtifact builds it on demand from the latest scan and the recorded decisions.

```sh
curl -sS -H 'Authorization: Bearer <TOKEN>' 'HOST/api/v1/repositories/REPO/vex?artifact_id=my-lib@1.2.3' -o vex.openvex.json
curl -sS -H 'Authorization: Bearer <TOKEN>' 'HOST/api/v1/repositories/REPO/vex?format=cyclonedx&spec_version=1.6' -o vex.cdx.json
oa vex get REPO --artifact-id my-lib@1.2.3 --output vex.openvex.json
```

| Endpoint | |
|---|---|
| `GET …/vex` | `format` `openvex` (default, 0.2.0) or `cyclonedx` with optional `spec_version` `1.4`/`1.5`/`1.6`; `artifact_id` limits it to one artifact, otherwise every scanned artifact of the repository is included |
| `GET …/vex/statements` | recorded decisions (optional `artifact_id`) |
| `PUT …/vex/statements` | record or replace the decision for one `artifact_id` + `vuln_id` |
| `DELETE …/vex/statements/{id}` | delete a decision; the vulnerability falls back to its derived status |

### In the web UI

Open **Security → Vulnerability scans**, pick the repository and use **Details** on a scan: every finding has a **VEX decision** button (status, justification for `not_affected`, impact and action statements) and shows its recorded status. The **VEX decisions** section below the scans lists the repository's statements with a delete action and downloads the OpenVEX or CycloneDX VEX document of the repository (or of one artifact from its findings drawer).

### Status of a vulnerability

In order:

1. a **recorded decision** wins;
2. `fixed` when the scan's `fixed_in` is less than or equal to the artifact version;
3. `affected` when the vulnerability scan is **quarantined** (the statement carries an action statement saying so);
4. otherwise `under_investigation`.

`not_affected` is never inferred. It exists only as an explicit decision and must carry a justification:

```sh
oa vex set REPO --artifact-id my-lib@1.2.3 --vuln CVE-2024-1234 --status not_affected \
  --justification vulnerable_code_not_in_execute_path --impact "the parser is never reached"
```

| Field | Values |
|---|---|
| `status` | `affected`, `not_affected`, `fixed`, `under_investigation` |
| `justification` | `component_not_present`, `vulnerable_code_not_present`, `vulnerable_code_not_in_execute_path`, `vulnerable_code_cannot_be_controlled_by_adversary`, `inline_mitigations_already_exist`; required for `not_affected`, rejected for other statuses |
| `impact_statement`, `action_statement` | free text |

Decisions are stored per repository, are part of the [backup](../operations/backup.md) and are removed with the repository. The same decisions feed the `analysis` block of the CycloneDX SBOM, so the SBOM and the VEX never disagree. In CycloneDX the justification is mapped to the CycloneDX vocabulary (`code_not_present`, `code_not_reachable`, `requires_environment`, `protected_by_mitigating_control`, ...).

## Using the documents

### Dependency-Track

Upload the CycloneDX SBOM; the dependencies appear as components with their purls and licenses (verified against Dependency-Track 4.14):

```sh
oa sbom generate REPO --artifact-id my-lib@1.2.3
oa sbom download REPO <ID> bom.json
curl -sS -X POST https://dtrack/api/v1/bom -H "X-Api-Key: $DT_KEY" \
  -F projectName=my-lib -F projectVersion=1.2.3 -F autoCreate=true -F bom=@bom.json
```

Dependency-Track does not import the `vulnerabilities` section of an uploaded BOM, and it applies a VEX (`curl -X POST .../api/v1/vex -F project=<uuid> -F vex=@vex.cdx.json`) only to vulnerabilities its own analyzers (internal, OSV, GitHub advisories, ...) already found on the project; with none, it logs `does not have any vulnerabilities; Skipping VEX import`. Enable an analyzer in Dependency-Track so its findings can be matched against the OpenArtifact VEX decisions. The packaged artifact itself is the BOM subject (`metadata.component`), not an entry of `components`.

### Trivy and Grype

Use the OpenVEX document to suppress findings that are `not_affected` or `fixed`:

```sh
oa vex get REPO --artifact-id my-lib@1.2.3 --output vex.openvex.json
trivy image --vex vex.openvex.json my-image
grype my-image --vex vex.openvex.json
```

## Limitations

- Generation is per artifact for SBOMs; a VEX document may cover a whole repository.
- A Maven pom that declares no license takes the one of its parent pom stored in the same repository or a group it belongs to (see [Maven parent poms](licenses.md#maven-parent-poms)); the SBOM shows it once the pom's license scan result exists, which an upload records. Dependency licenses come from lockfiles or the artifact's properties.
- Components are what the manifest and lockfile declare, not a resolved install: a package without a lockfile lists its direct dependencies only, and the graph is flat. Dependency hashes and licenses come from lockfiles or properties.
- Vulnerabilities in the SBOM come from the latest completed or partial scan; a repository that was never scanned yields `under_investigation` only for the findings it has, and none otherwise.
- The OpenVEX document and statement ids are generated per request; timestamps are the time of generation.
- SPDX documents are 2.3 only.
