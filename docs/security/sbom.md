<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# SBOM and VEX

Use this page to generate a software bill of materials (SBOM) or a vulnerability exploitability statement (VEX) for an artifact, sign it, and hand it to other tools. SBOM formats are CycloneDX and SPDX 2.3 and 3.0. VEX formats are OpenVEX and CycloneDX VEX.

The documents are built from data OpenArtifact already holds: the artifact's properties, the package listing, the latest license scan and the latest vulnerability scan. You can [sign them as in-toto attestations](#signed-sbom-and-vex-attestations) and [push them to Dependency-Track](dependency-track.md).

All endpoints are admin only. Generation is manual: nothing is stored on upload. See [Scanning](scanning.md) for how the scans run.

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

Documents are stored in the blob store.

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

- **Subject**: the package named by `artifact_id` (split at the last `@` into name and version), typed from the repository format, with a [purl](https://github.com/package-url/purl-spec) (`pkg:npm/%40babel/core@7.0.0`, `pkg:maven/group/artifact@1.0`, ...). A deb or rpm carries its namespace like its dependencies do (`pkg:deb/debian/openssl@3.0.11-1~deb12u2`, `pkg:rpm/redhat/openssl@3.0.7-24.el9`): the one the package file tells (the maintainer of a deb, the vendor or release tag of an rpm) and, when the file is not read, the one the version tells (an Ubuntu suffix, `.el9`, `.fc39`). An rpm whose version has no distribution tag and that has no readable vendor gets no namespace. A purl encodes every character outside letters, digits and `.-_~`, so an epoch is written `1%3A1.2.11`. The tool (`OpenArtifact`, with the server version) is recorded in the metadata.
- **Hash**: the `sha256` of the package file when the package listing identifies exactly one match for the name and version. It is omitted rather than guessed when the match is ambiguous.
- **Components**: the dependencies the stored package file declares (table below), merged with the entries of the artifact's `dependencies` property, a JSON array such as `[{"name":"left-pad","version":"1.3.0","license":"MIT"}]` (a dependency may also carry `purl`, `sha256`, `license` and `licenses`). An entry the property lists keeps its property data; the file adds the rest, deduplicated by purl. The document includes the dependency graph (`dependencies` in CycloneDX, `DEPENDS_ON` relationships in SPDX): every component hangs directly off the packaged artifact, transitive edges are not reconstructed.
- **Licenses**: from the latest license scan and the `license` / `licenses` properties. A value that is an SPDX identifier or expression (`MIT OR Apache-2.0`, `GPL-2.0-or-later WITH Classpath-exception-2.0`) is canonicalised against the SPDX license list 3.29.0 and emitted as `license.id` / `expression`. Any other name is kept as `license.name` in CycloneDX and as a `LicenseRef-*` with its text in `hasExtractedLicensingInfos` in SPDX. `NOASSERTION` appears only when nothing is known.
- **Vulnerabilities** (CycloneDX): the findings of the latest completed or partial vulnerability scan, with `id`, `source` (`NVD`, `GITHUB`, `OSV`), `ratings` (severity, and the CVSS score when the scanner provided one), `cwes`, an `advisories` link, `affects` and an `analysis` block (below). SPDX documents carry the advisories as `SECURITY` external references.
- `metadata` in the request is rejected: components come from the stored properties, not from the caller.

### Components read from the package file

`artifact_id` is either `name@version` or the id of a row of the package listing (`GET …/packages`); the second form also gives the document the package's real name, version and hash. The package file is found through the listing (for Maven, the `.pom` is preferred over the jar next to it; a jar falls back to the `pom.xml` it embeds). It is read from the blob store with the same bounded reader the AI analysis and license scan use.

| Format | Read from | Notes |
|---|---|---|
| cargo | `[dependencies]`, `[build-dependencies]` and `[target.*.dependencies]` of `Cargo.toml`; `Cargo.lock` when the crate ships one | the lockfile pins versions and adds locked packages; only `=x.y.z` pins a version in `Cargo.toml` |
| conan | quoted `name/version` references in the `requires` of `conanfile.py` | `tool_requires` and `test_requires` are skipped; the recipe is never executed |
| conda | `depends` of `info/index.json` | only `==x.y.z` pins a version |
| deb | `Pre-Depends` and `Depends` of the control file | the first of a set of alternatives; purls use the namespace `ubuntu` when the version or maintainer says so, `debian` otherwise |
| docker | not read from the manifest | components come from the `dependencies` property; the SBOM a build pushes with an image is [ingested](#container-image-sboms) instead |
| raw, terraform | not read | components come from the `dependencies` property only |
| go | `require` of `go.mod` (the `.mod` file or the one inside the module zip) | exact versions |
| helm | `Chart.yaml` and `requirements.yaml` dependencies, `Chart.lock` when present | the lock pins versions |
| ivy | `ivy.xml` dependencies | |
| maven, sbt | `<dependencies>` of the pom (`compile` and `runtime` scopes) | `${property}` references and `dependencyManagement` versions are resolved; unresolved ones leave the version out |
| npm | `package.json` (`dependencies`, `optionalDependencies`), `npm-shrinkwrap.json` / `package-lock.json` when the tarball ships one | the lockfile pins exact versions and adds the installed transitive packages (dev-only entries are left out) and their licenses |
| nuget | `.nuspec` dependency groups | only `[x.y.z]` pins a version |
| pypi | `Requires-Dist` of `METADATA` / `PKG-INFO` | requirements gated on an `extra` are skipped; only `==` pins a version |
| rpm | `Requires` of the header | rpmlib features, file paths and shared-object or boolean capabilities are skipped; the purl namespace is derived from `Vendor` or the release tag |
| rubygems | runtime dependencies of the gemspec | only `= x.y.z` pins a version |

A dependency whose manifest gives only a version range has no `version` and a purl without one; the range is kept in the CycloneDX component property `openartifact:version_range`. A file that cannot be read (damaged, not the archive its name says) leaves the properties as the only source and is logged. A file that cannot be read **safely** (a manifest or lockfile over 8 MiB, a decompressed stream over the limit, an archive with two candidates for the same manifest) fails the request with `422 unsafe_artifact`. At most 10 000 dependencies are listed per artifact. See [properties](../repositories/properties.md).

## Container image SBOMs

A container build usually publishes an SBOM next to its image. OpenArtifact reads it when it is pushed to a hosted docker repository, stores it as an SBOM report of the image and uses its packages for [vulnerability scans](scanning.md#container-images) and license evaluation. The image is identified by its manifest digest (`sha256:<hex>`), which is the `artifact_id` of the report and of the scans.

An SBOM is recognised in three forms:

| Pushed as | How it is found |
|---|---|
| a cosign SBOM tag, `sha256-<hex>.sbom` (`cosign attach sbom`) | the layers of the tagged manifest hold the raw SPDX or CycloneDX document |
| a cosign attestation tag, `sha256-<hex>.att` (`cosign attest`) | each layer is a DSSE envelope; the in-toto statement must have the predicate type `https://spdx.dev/Document` or `https://cyclonedx.org/bom`, with or without a version suffix (`/v1.5`) |
| an OCI referrer (`subject` set) | the `artifactType` is `application/spdx+json`, `text/spdx+json`, `application/vnd.cyclonedx+json` or `text/spdx`, or a Sigstore bundle whose `dev.sigstore.bundle.predicateType` annotation is one of the predicates above |

A layer may be bare SPDX 2.x JSON, SPDX 3 JSON, SPDX tag-value or CycloneDX JSON, or any of these inside a Sigstore bundle, DSSE envelope or in-toto statement (cosign's SPDX predicate wrapper, `{"Data": ...}`, included). A statement with another predicate type (provenance, vulnerability scan) is ignored. Parsing is bounded: a document or envelope over 32 MiB, or one that names more than 100 000 components, is refused as a whole instead of being read in part, and at most 16 layers of a manifest are read. A document that cannot be read never fails the push; it is logged.

Every ingested document is kept as written (without its envelope). An image holds one report per SBOM format and spec version, so an SPDX 2.3 and a CycloneDX 1.5 document sit side by side, and a newer document of the same format and version replaces the older; the packages of all reports are merged, without duplicates, when a scan or a gate reads them. Signatures and attestations that OpenArtifact indexes itself are not read as SBOMs.

The components' Package URLs and licenses are what the scans use: see the [table of ecosystems](scanning.md#container-images). Components without a Package URL, and Alpine (`apk`) packages, are listed in the SBOM but not scanned.

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

Open **Security** (`/security?type=vuln`), filter by the repository and open a finding (or the scan details), or open the **Security** tab of the artifact page: every finding has a **VEX decision** button (status, justification for `not_affected`, impact and action statements) and shows its recorded status. The **VEX decisions** section below the scans lists the repository's statements with a delete action and downloads the OpenVEX or CycloneDX VEX document of the repository (or of one artifact from its findings drawer).

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

**Settings > Security > Attestations** (admin only) shows the signing key: its id, algorithm, whether it comes from a key file or was generated, the public key to copy or download and the URL it is served from, and a `cosign` command to verify a bundle. It also switches the attachment of signed attestations to the images of each hosted docker repository on or off. When attestations are not enabled, or the server has no `OPENARTIFACT_SECRET_KEY` to protect a generated key, the page says so.

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

- **Unset (default):** OpenArtifact generates a P-256 key on first use and stores it encrypted in the database. This needs `OPENARTIFACT_SECRET_KEY` ([configuration](../getting-started/configuration.md)). Without it, the attestation endpoints return `409` with code `secret_key_required`.
- **`attestation.keyPath` / `ATTESTATION_KEY_PATH`:** use your own key file instead. It must be an unencrypted PKCS#8 or SEC1 PEM private key, ECDSA (P-256, P-384 or P-521) or Ed25519.
- Replicas converge on one key, and restoring the database restores the key.

The **public key** is served without authentication at `HOST/.well-known/openartifact/attestation-key.pem`. Admins also get its id and algorithm from `GET /api/v1/admin/attestation`.

To rotate the key, set a new `keyPath`, or delete the `attestation.private_key` system setting to generate a new one. Earlier attestations verify only against the key that signed them, so keep the old public keys.

### Verify an SBOM with cosign

[cosign](https://github.com/sigstore/cosign) v2.5 or later verifies the bundle against the public key, offline. OpenArtifact does not use Sigstore's transparency log, so tell cosign to skip it.

```sh
curl -sS HOST/.well-known/openartifact/attestation-key.pem -o attestation-key.pem
curl -sS -H 'Authorization: Bearer <TOKEN>' -o sbom.cdx.json.sigstore.json \
  'HOST/api/v1/repositories/REPO/sbom/<ID>/download?attest=bundle'
cosign verify-blob-attestation --bundle sbom.cdx.json.sigstore.json --key attestation-key.pem \
  --type cyclonedx --insecure-ignore-tlog my-lib-1.2.3.tgz
```

- Use `--type spdxjson` for an `spdx-json` SBOM. For SPDX 3, use the predicate URI as the type.
- When no package hash is known, the stored SBOM file is the subject. Download the plain SBOM with `curl ... -o sbom.cdx.json '.../sbom/<ID>/download'` and pass `sbom.cdx.json` instead of the package.
- `cosign` prints `Verified OK` and exits 0. A modified package fails with `provided artifact digests do not match digests in statement`, and another key fails the signature check.

### Verify a VEX with cosign

```sh
curl -sS -H 'Authorization: Bearer <TOKEN>' -o vex.openvex.json.sigstore.json \
  'HOST/api/v1/repositories/REPO/vex?artifact_id=my-lib@1.2.3&attest=bundle'
cosign verify-blob-attestation --bundle vex.openvex.json.sigstore.json --key attestation-key.pem \
  --type openvex --insecure-ignore-tlog my-lib-1.2.3.tgz
```

#### VEX bundles with an unknown subject

When the VEX covers no package whose hash OpenArtifact knows, its subject is the digest of the VEX document itself. Every request generates a new document, so verify it against the predicate inside the bundle you downloaded:

```sh
python3 - <<'EOF'
import base64, json
payload = base64.b64decode(json.load(open("vex.openvex.json.sigstore.json"))["dsseEnvelope"]["payload"])
open("vex.predicate.json", "wb").write(payload[payload.index(b'"predicate":') + len(b'"predicate":'):-1])
EOF
cosign verify-blob-attestation --bundle vex.openvex.json.sigstore.json --key attestation-key.pem \
  --type openvex --insecure-ignore-tlog vex.predicate.json
```

### Verify the DSSE envelope without cosign

`?attest=true` returns the plain DSSE envelope, which in-toto tooling and DSSE libraries read. The payload is the Statement:

```sh
jq -r .payload sbom.cdx.json.intoto.json | base64 -d | jq '{_type, predicateType, subject}'
```

The envelope's `keyid` is empty, so give verifiers the public key explicitly.

### Attach to an image (hosted docker repositories)

OpenArtifact can sign the SBOM or VEX of an image and attach it to the image as an OCI referrer. `cosign tree`, `cosign verify-attestation` and `oras discover` find it next to the image.

The attachment is idempotent: an image carries at most one SBOM and one VEX referrer per predicate type. Attaching the same document again changes nothing (`created` is `false`). A regenerated document replaces the earlier referrer, and registry GC reclaims the old blobs.

Attach a stored SBOM (`cyclonedx`, `spdx-json` or `spdx3-json`), or the VEX of one image. You need an admin token and the [signing key](#the-signing-key).

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" HOST/api/v1/repositories/REPO/sbom/<report-id>/attach
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"artifact_id":"REPO/IMAGE@TAG"}' HOST/api/v1/repositories/REPO/vex/attach
```

The VEX is OpenVEX by default; send `"format": "cyclonedx"` for CycloneDX. The answer has `subject`, `referrer_digest`, `created` and `replaced`.

To attach automatically, switch it on per repository (off by default):

```sh
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"enabled":true}' HOST/api/v1/repositories/REPO/attestation-attach
```

With it on, generating an SBOM for an image attaches it. A finished vulnerability scan attaches a CycloneDX SBOM and the OpenVEX document. A failure is logged and never fails the scan or the generation.

Then discover and verify:

```sh
oras discover HOST/REPO/IMAGE@sha256:<digest>
cosign tree HOST/REPO/IMAGE@sha256:<digest>
curl -O HOST/.well-known/openartifact/attestation-key.pem
cosign verify-attestation --key attestation-key.pem --type cyclonedx \
  --insecure-ignore-tlog HOST/REPO/IMAGE@sha256:<digest>
```

Details:

- The attestation subject is the image manifest digest.
- The referrer is an OCI 1.1 image manifest with `artifactType` `application/vnd.dev.sigstore.bundle.v0.3+json` and one layer holding the Sigstore bundle.
- It counts against the repository's [storage quota](../repositories/cleanup-and-quotas.md) and appears in the referrers API with the annotations `dev.sigstore.bundle.predicateType` and `dev.openartifact.attestation.kind` (`sbom` or `vex`).
- It lives and dies with the image under [registry GC](../formats/docker.md): deleting the tag and collecting removes it.

Other formats have no referrers. Use `?attest=bundle` and verify the package file with `cosign verify-blob-attestation` as above.

### Format details

- The signature is over `PAE(payloadType, payload)`, which is `DSSEv1 <len(type)> <type> <len(payload)> <payload>`.
- ECDSA signs the SHA-2 hash matching the curve (SHA-256, SHA-384 or SHA-512 for P-256, P-384, P-521), as Sigstore requires. Ed25519 signs the encoded bytes directly.

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
