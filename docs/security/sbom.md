<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# SBOM and VEX

Per-artifact software bills of materials (CycloneDX, SPDX 2.3 and 3.0) and vulnerability exploitability statements (OpenVEX, CycloneDX VEX), built from the data OpenArtifact already holds: the artifact's stored properties, the package listing, the latest license scan and the latest vulnerability scan. Either can be [signed as an in-toto attestation](#signed-sbom-and-vex-attestations) and [pushed to Dependency-Track](dependency-track.md). All endpoints are **admin only** and generation is **manual**: nothing is stored on upload (the optional Dependency-Track push builds its documents on the fly). See [Scanning](scanning.md) for how the scans themselves run.

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
| `GET …/sbom/{id}/download` | the document, served as attachment `sbom.cdx.json`, `sbom.spdx.json`, `sbom.spdx3.json` or `sbom.spdx`; with `?attest=true` or `?attest=bundle` a [signed attestation](#signed-sbom-and-vex-attestations) of it |
| `DELETE …/sbom/{id}` | delete |

| `format` | Document | `spec_version` |
|---|---|---|
| `cyclonedx` | CycloneDX JSON (`application/json`) | `1.4`, `1.5` (default), `1.6` |
| `spdx-json` | SPDX JSON (`application/spdx+json`) | `2.3` |
| `spdx3-json` | SPDX 3.0 JSON-LD (`application/ld+json`) | `3.0.1` |
| `spdx` | SPDX tag-value (`text/spdx`) | `2.3` |

Existing callers that pass `"spec_version":"1.4"` or `"2.3"` keep getting those versions. Documents are stored in the blob store.

### SPDX 3.0

`spdx3-json` produces an SPDX 3.0.1 JSON-LD document: a `SpdxDocument` with a `software_Sbom` root, the described package as `software_Package` (purl, sha256 hash, primary purpose), its dependencies as packages linked by `dependsOn` relationships, licenses as `simplelicensing_LicenseExpression` (or `expandedlicensing_CustomLicense` for a non-SPDX name) linked by `hasConcludedLicense` / `hasDeclaredLicense`, and the vulnerabilities of the latest scan as `security_Vulnerability` with a VEX assessment relationship per finding that carries the derived or recorded status. Only 3.0.1 is supported.

The output is checked against the official [spdx3-validate](https://github.com/spdx/spdx3-validate) (it fetches the SPDX 3.0.1 schema and SHACL model from spdx.org):

```sh
pip install spdx3-validate
oa sbom generate REPO --artifact-id my-lib@1.2.3 --format spdx3-json
oa sbom download REPO <ID> sbom.spdx3.json
spdx3-validate --json sbom.spdx3.json -s 3.0.1
```

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

## Signed SBOM and VEX attestations

Add `?attest=true` or `?attest=bundle` to an SBOM download or a VEX request to get the document wrapped in an [in-toto attestation](https://github.com/in-toto/attestation) (a Statement v1) signed as a [DSSE](https://github.com/secure-systems-lab/dsse) envelope. The document itself is the predicate:

| Document | `predicateType` | `cosign --type` |
|---|---|---|
| CycloneDX SBOM or VEX | `https://cyclonedx.org/bom` | `cyclonedx` |
| SPDX 2.3 JSON | `https://spdx.dev/Document` | `spdxjson` |
| SPDX 3.0 JSON-LD | `https://spdx.dev/Document/v3.0` | `https://spdx.dev/Document/v3.0` |
| OpenVEX | `https://openvex.dev/ns` | `openvex` |

SPDX tag-value cannot be attested (it is not JSON): the request answers `400`. `?attest=` with any other value answers `400`; without a configured service it answers `501`.

| `attest` | Response | Content type | File |
|---|---|---|---|
| `true` | the DSSE envelope (`payloadType` `application/vnd.in-toto+json`) | `application/vnd.dsse.envelope.v1+json` | `sbom.cdx.json.intoto.json` |
| `bundle` | a Sigstore bundle (v0.3) holding that envelope and the public key hint | `application/vnd.dev.sigstore.bundle.v0.3+json` | `sbom.cdx.json.sigstore.json` |

The statement's **subjects** are the described package (named by its purl, with the `sha256` of the package file when the package listing identifies exactly one match) and, for a stored SBOM, the SBOM file itself. So the attestation verifies against the package you have downloaded, and a VEX attestation against the packages the VEX speaks about. When no package hash is known a VEX attestation is about the digest of the signed document.

### The signing key

- `attestation.keyPath` / `ATTESTATION_KEY_PATH`: an unencrypted PKCS#8 or SEC1 PEM private key, **ECDSA** (P-256, P-384 or P-521) or **Ed25519**.
- Unset: OpenArtifact generates an ECDSA P-256 key on first use and stores it **encrypted** in the database (with `OPENARTIFACT_SECRET_KEY` set, see [configuration](../getting-started/configuration.md); without it the key is stored in plaintext, like every other stored secret). Replicas converge on one key, and a restore of the database restores the key.

The ECDSA hash follows the curve (SHA-256, SHA-384, SHA-512), as Sigstore requires; Ed25519 signs the DSSE pre-authentication encoding directly. The **public key** is served without authentication at `HOST/.well-known/openartifact/attestation-key.pem` (admins also get its id and algorithm from `GET /api/v1/admin/attestation`). Rotating the key means setting a new `keyPath`, or deleting the `attestation.private_key` system setting to generate a new one; attestations signed earlier keep verifying against the key that signed them, so keep the old public keys.

### Verify with cosign

[cosign](https://github.com/sigstore/cosign) v2.5+ (including v3) verify the bundle against the public key, offline. OpenArtifact does not use Sigstore's transparency log, so tell cosign to skip it:

```sh
curl -sS HOST/.well-known/openartifact/attestation-key.pem -o attestation-key.pem

# SBOM: CycloneDX (use --type spdxjson for spdx-json)
curl -sS -H 'Authorization: Bearer <TOKEN>' -o sbom.cdx.json.sigstore.json \
  'HOST/api/v1/repositories/REPO/sbom/<ID>/download?attest=bundle'
cosign verify-blob-attestation --bundle sbom.cdx.json.sigstore.json --key attestation-key.pem \
  --type cyclonedx --insecure-ignore-tlog my-lib-1.2.3.tgz
# when no package hash is known the stored SBOM file is a subject too: verify the plain download instead
#   curl ... -o sbom.cdx.json 'HOST/api/v1/repositories/REPO/sbom/<ID>/download'  and pass sbom.cdx.json

# VEX
curl -sS -H 'Authorization: Bearer <TOKEN>' -o vex.openvex.json.sigstore.json \
  'HOST/api/v1/repositories/REPO/vex?artifact_id=my-lib@1.2.3&attest=bundle'
cosign verify-blob-attestation --bundle vex.openvex.json.sigstore.json --key attestation-key.pem \
  --type openvex --insecure-ignore-tlog my-lib-1.2.3.tgz
```

When the VEX speaks about no package whose hash OpenArtifact knows, its subject is the digest of the VEX document itself. Every request generates a new document, so verify it against the predicate carried in the very bundle you downloaded:

```sh
python3 - <<'EOF'
import base64, json
payload = base64.b64decode(json.load(open("vex.openvex.json.sigstore.json"))["dsseEnvelope"]["payload"])
open("vex.predicate.json", "wb").write(payload[payload.index(b'"predicate":') + len(b'"predicate":'):-1])
EOF
cosign verify-blob-attestation --bundle vex.openvex.json.sigstore.json --key attestation-key.pem \
  --type openvex --insecure-ignore-tlog vex.predicate.json
```

`cosign` prints `Verified OK` and exits 0; a modified package fails with `provided artifact digests do not match digests in statement`, and another key fails the signature check. The same bundle also works with `--type https://cyclonedx.org/bom` (the full predicate URI), and for SPDX 3 use the URI as the type.

### Verify the DSSE envelope without cosign

`?attest=true` returns the plain DSSE envelope, the format in-toto tooling and any DSSE library read (for example `go-securesystemslib/dsse`). The payload is the Statement:

```sh
jq -r .payload sbom.cdx.json.intoto.json | base64 -d | jq '{_type, predicateType, subject}'
```

The signature is over `PAE(payloadType, payload)` = `DSSEv1 <len(type)> <type> <len(payload)> <payload>`, ECDSA over the SHA-2 hash matching the curve (or Ed25519 over the PAE itself); the envelope's `keyid` is empty, so verifiers must be given the public key explicitly.

### Attaching to an OCI image

For a **hosted docker repository** OpenArtifact can sign the SBOM and VEX document of an image and attach them to it as OCI 1.1 referrers: an image manifest with a `subject` pointing at the image, `artifactType` `application/vnd.dev.sigstore.bundle.v0.3+json`, an empty config and one layer holding the Sigstore bundle (the same bundle `?attest=bundle` returns). `cosign tree`, `cosign verify-attestation` and `oras discover` find it next to the image. The attached manifests go through the registry's own paths: they count against the repository's [storage quota](../repositories/cleanup-and-quotas.md), appear in the referrers API with their `artifactType` and annotations (`dev.sigstore.bundle.predicateType`, `dev.openartifact.attestation.kind` = `sbom` or `vex`), and live and die with the image under [registry GC](../formats/docker.md) (deleting the tag and collecting it removes them).

The attestation subject is the image manifest digest. The attachment is idempotent per image, kind and predicate type: an image carries at most one SBOM and one VEX referrer per predicate type. Attaching a byte-identical document again changes nothing (`created` is false); the generated documents carry a timestamp and a fresh serial number, so attaching a regenerated one replaces the earlier referrer, whose blobs registry GC reclaims.

Attach on demand (admin; needs the [attestation key](#the-signing-key)):

```sh
# a stored SBOM (cyclonedx, spdx-json or spdx3-json)
curl -X POST -H "Authorization: Bearer $TOKEN" HOST/api/v1/repositories/REPO/sbom/<report-id>/attach
# the VEX document of one image (openvex by default, or "format": "cyclonedx")
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"artifact_id":"REPO/IMAGE@TAG"}' HOST/api/v1/repositories/REPO/vex/attach
```

The answer carries `subject`, `referrer_digest`, `created` (false when it was already attached) and `replaced`. Or attach automatically, per repository (off by default):

```sh
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":true}' HOST/api/v1/repositories/REPO/attestation-attach
```

With it on, generating an SBOM for an image attaches it, and a finished vulnerability scan attaches a CycloneDX SBOM and the OpenVEX document; a failure is logged and never fails the scan or the generation.

Discover and verify:

```sh
oras discover HOST/REPO/IMAGE@sha256:<digest>
cosign tree HOST/REPO/IMAGE@sha256:<digest>
curl -O HOST/.well-known/openartifact/attestation-key.pem
cosign verify-attestation --key attestation-key.pem --type cyclonedx \
  --insecure-ignore-tlog HOST/REPO/IMAGE@sha256:<digest>
```

Other formats have no referrers; use `?attest=bundle` and verify the package file with `cosign verify-blob-attestation` as above.

## Using the documents

### Dependency-Track

OpenArtifact can push the BOM on upload and after each scan, and the VEX decisions after Dependency-Track's analysis, by itself: see [Dependency-Track](dependency-track.md). By hand:

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
- SPDX is generated as 2.3 (JSON and tag-value) and 3.0.1 (JSON-LD); other SPDX versions are rejected.
- Attestations are signed with one instance-wide key and are not logged in a transparency log (`--insecure-ignore-tlog`).
