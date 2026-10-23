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
- **Components**: the entries of the artifact's `dependencies` property, a JSON array such as `[{"name":"left-pad","version":"1.3.0","license":"MIT"}]`. A dependency may also carry `purl`, `sha256`, `license` and `licenses`. The document includes the dependency graph (`dependencies` in CycloneDX, `DEPENDS_ON` relationships in SPDX).
- **Licenses**: from the latest license scan and the `license` / `licenses` properties. A value that is an SPDX identifier or expression (`MIT OR Apache-2.0`, `GPL-2.0-or-later WITH Classpath-exception-2.0`) is canonicalised against the SPDX license list 3.29.0 and emitted as `license.id` / `expression`. Any other name is kept as `license.name` in CycloneDX and as a `LicenseRef-*` with its text in `hasExtractedLicensingInfos` in SPDX. `NOASSERTION` appears only when nothing is known.
- **Vulnerabilities** (CycloneDX): the findings of the latest completed or partial vulnerability scan, with `id`, `source` (`NVD`, `GITHUB`, `OSV`), `ratings` (severity, and the CVSS score when the scanner provided one), `cwes`, an `advisories` link, `affects` and an `analysis` block (below). SPDX documents carry the advisories as `SECURITY` external references.
- `metadata` in the request is rejected: components come from the stored properties, not from the caller.

The dependency list is only as good as the properties the artifact carries; OpenArtifact does not open package files. See [properties](../repositories/properties.md).

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
- A Maven pom that declares no license takes the one of its parent pom stored in the same repository or a group it belongs to (see [Maven parent poms](licenses.md#maven-parent-poms)); the SBOM shows it once the pom's license scan result exists, which an upload records. Dependency licenses are still only what the artifact's properties carry.
- Package contents are not inspected: components, dependency licenses and hashes depend on what the artifact's properties carry.
- Vulnerabilities in the SBOM come from the latest completed or partial scan; a repository that was never scanned yields `under_investigation` only for the findings it has, and none otherwise.
- The OpenVEX document and statement ids are generated per request; timestamps are the time of generation.
- SPDX documents are 2.3 only.
