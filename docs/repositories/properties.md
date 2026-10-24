<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Artifact properties

Properties are free-form string key/value pairs attached to an artifact. They can be read, replaced, patched, deleted and searched by exact key and value through the REST API and `oa props`.

## Model

- A property belongs to one artifact of one repository. The artifact is identified by `artifact_id`, a string of at most 1024 bytes (valid UTF-8, no control characters), URL-escaped in the path. The web UI and `oa import` use the package's id as shown in the package list (the `id` of the package, or the id returned by an upload: the `X-OA-Artifact-Id` response header on Maven `PUT`, `id` in the JSON body of Debian, RPM and raw uploads). The server does not check that the id names an existing package; properties are stored against `(repository, artifact_id)` and are deleted with the repository.
- Keys: 1-255 bytes, valid UTF-8, no control characters. Values: strings of at most 4096 bytes, valid UTF-8, no NUL. At most 100 properties per request. A non-string value is `400 bad_request`.
- A request body is a flat JSON map of strings, or the same map wrapped as `{"properties": {...}}` (the shape the web UI sends).
- Permissions are those of the repository, not the global `manage` permission: reading properties and searching need `read` on the repository, setting, replacing and removing them need `write`. Deploy tokens can use them, which is what `oa upload` does to record build properties from CI. See [RBAC](../security/rbac.md).

## API

| Method and path | Effect |
|---|---|
| `GET /api/v1/repositories/{repo}/artifacts/{artifact_id}/properties` | Returns the flat map of all properties (`{}` when none). |
| `PUT /api/v1/repositories/{repo}/artifacts/{artifact_id}/properties` | Replaces all properties atomically (existing keys not in the body are removed). Returns the new map. |
| `PATCH /api/v1/repositories/{repo}/artifacts/{artifact_id}/properties` | Upserts the given keys, leaves the others. Returns the full map. |
| `DELETE /api/v1/repositories/{repo}/artifacts/{artifact_id}/properties/{key}` | Removes one key; `204`, also when it did not exist. |
| `GET /api/v1/repositories/{repo}/artifacts/search?key=K&value=V` | Artifact ids whose property `K` equals `V` exactly: `{"artifact_ids": [...]}` (sorted, deduplicated). Both parameters are required. |

```sh
AID=<ARTIFACT_ID>

curl -X PATCH HOST/api/v1/repositories/REPO/artifacts/$AID/properties \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"build.name":"nightly","build.number":"482","vcs.revision":"9fceb02"}'

curl HOST/api/v1/repositories/REPO/artifacts/$AID/properties -H "Authorization: Bearer <TOKEN>"

curl "HOST/api/v1/repositories/REPO/artifacts/search?key=build.name&value=nightly" \
  -H "Authorization: Bearer <TOKEN>"

curl -X DELETE HOST/api/v1/repositories/REPO/artifacts/$AID/properties/vcs.revision \
  -H "Authorization: Bearer <TOKEN>"
```

## CLI

```sh
oa props list REPO ARTIFACT_ID
oa props set REPO ARTIFACT_ID KEY VALUE      # PATCH: one key
oa props delete REPO ARTIFACT_ID KEY
```

Search through the API (above); `oa props search` expects a list of objects, while the endpoint returns `{"artifact_ids": [...]}`.

## Properties that other features read

License detection and SBOM generation for an artifact id read these keys from its stored properties (instead of request data):

| Key | Used for |
|---|---|
| `license`, `License`, `spdx_id`, `spdxId` | License detection. Added to the licenses the stored package file declares ([License metadata](../security/licenses.md)); a property cannot hide them. |
| `licenses`, `Licenses` | Comma-separated list of licenses. |
| `dependencies` | JSON array of `{"name","version"}` objects: SBOM components, added to the dependencies read from the package file ([SBOM](../security/sbom.md#components-read-from-the-package-file)). |

Setting or changing one of the license keys through `PUT` or `PATCH` also checks the artifact against the repository's [license policies](../security/scanning.md#quarantine-after-upload): a `block` policy that the license violates quarantines the artifact, and only an admin approval releases it. See [scanning](../security/scanning.md) for the scan endpoints. [`oa import`](../migration/nexus-artifactory.md) copies Artifactory properties (`deb.*`, `rpm.*`, `build.name`, `build.number`, `vcs.*` by default) into properties of the imported packages.
