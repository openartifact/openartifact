<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Copy and move artifacts between repositories

Copy a package version, or one artifact, from a hosted repository into another hosted repository of the same format, or move it. No bytes are copied: both repositories reference the same stored file.

Use it to publish a build from a staging repository to a release repository, or to correct an upload that went to the wrong repository. For a whole sealed set of artifacts with approvals, use a [release bundle promotion](release-bundles.md#promoting-a-bundle) instead.

## Copy and move with the CLI

Copy `com.acme:core` 1.4.0 from `staging` to `releases`:

```bash
oa cp staging releases com.acme:core@1.4.0
```

Check first what would happen. Nothing is written:

```bash
oa cp staging releases com.acme:core@1.4.0 --dry-run
```

Move it: the copy is made, then the source files go to the trash of `staging`:

```bash
oa mv staging releases com.acme:core@1.4.0
```

Name several versions in one command. They all succeed or none of them does. Select one artifact by its content instead of a version with `--sha256` and, when several artifacts share the digest, `--path`:

```bash
oa cp staging releases --sha256 <HEX_DIGEST> --path com/acme/core/1.4.0/core-1.4.0.jar
```

For a Docker repository give the manifest digest. `--path` can name a tag to pick between images that share the digest. Every tag of the chosen manifest is copied.

`--format json` prints the report. The flags are in the [CLI reference](../reference/cli.md).

## Copy and move in the web UI

Open a package version in a hosted repository and choose **Copy to…** or **Move to…** in the **More** menu. The dialog lists the hosted repositories of the same format that you can write. Choosing one shows the dry-run result. Confirm to run it. **Move to…** needs the delete permission on the source.

## What is copied

- Every live file of the version, with the metadata the format keeps for it.
- The artifact's [properties](properties.md).
- A Docker image: the manifest and the tags that point to it. A multi-architecture index also brings its child manifests.

Not copied:

- Scan results. The target treats a copy as an upload: its webhooks fire and its scan-on-upload and license policies run. A copy that a blocking license policy of the target refuses is not made.
- The creation time. A copy is dated when it is made.

The target's generated metadata (package indexes, packuments, repodata) is refreshed in the background.

## When the target already has the content

| Target holds | Result |
|---|---|
| Nothing at these coordinates | `copied` |
| The same content | `existing`, nothing is written |
| Different content | `409 conflict`, nothing is changed |

Administrators can send `--force` (`"force": true` in the API) to replace the conflicting content. The replaced files go to the trash of the target and can be restored. `--force` also lets a move take a release out of a source repository that has [immutable releases](../operations/trash.md).

## What a move does

A move is a copy followed by moving the source files into the source repository's [trash](../operations/trash.md), in one transaction. Restore them from there within the retention period; their properties come back with them.

A move is refused with `409` when:

- a sealed [release bundle](release-bundles.md) holds the file, because the bundle would lose its content;
- the version is a release and the source has immutable releases (unless forced);

A quarantined source artifact is refused with `403` for a copy and a move. When a request is refused, nothing is changed.

For Docker, a move trashes the manifests you named and their tags. The child manifests of an index and the blobs stay in the source repository until its garbage collection finds them unused.

## Permissions

| Operation | Needs |
|---|---|
| Copy | `read` on the source and `write` on the target |
| Move | The same, plus `delete` on the source |
| `force` | An administrator |

Permissions are checked for every artifact at its path, and project roles count through each repository's project. A caller who cannot read the source or the target gets `404`. A token with limited scopes is limited the same way. Proxy and group repositories, and a target of another format, are refused with `400`.

## Use the API

`POST /api/v1/repositories/{source}/copy` and `POST /api/v1/repositories/{source}/move` take the same body:

```json
{
  "target": "releases",
  "items": [
    {"package": "com.acme:core", "version": "1.4.0"},
    {"sha256": "<HEX_DIGEST>", "path": "com/acme/other/2.0/other-2.0.jar"}
  ],
  "dry_run": false,
  "force": false
}
```

An item is a package and version (every live file of that version), or a `sha256` with an optional `path`. A request carries 1 to 500 items. The response counts `copied`, `existing` and `replaced` artifacts, and lists them per item. A dry run returns the same report and writes nothing. The endpoints are in the [REST API reference](../reference/api/repositories.md).

## Audit and webhooks

- An `artifact.copied` or `artifact.moved` audit event records the source, the target and the items. Each copied artifact also gets an `artifact.uploaded` event in the target, and each moved artifact an `artifact.deleted` event in the source.
- The [webhooks](../operations/webhooks.md) `artifact.uploaded` (target) and, for a move, `artifact.deleted` (source) fire per artifact.
- The quota of the target is charged for the copies.

## Not supported

- Proxy-cache artifacts cannot be adopted into a hosted repository.
- A copy never changes the format.
- There is no copy of a whole repository or of a path prefix: name the versions.
