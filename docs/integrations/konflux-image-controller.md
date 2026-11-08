<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Konflux image-controller

Let the unmodified Konflux image-controller create the image repositories, pull and push credentials and permissions of your components in OpenArtifact. OpenArtifact answers the subset of the Quay API the controller uses, so the controller thinks it talks to a Quay registry.

Use this when you run Konflux and want its builds to push to OpenArtifact instead of Quay. Nothing in Konflux changes except the `quaytoken` Secret.

## Enable the API

1. Set a secret-encryption key. The API hands a robot account's token out again on request, so the server stores robot tokens sealed and refuses to start without `OPENARTIFACT_SECRET_KEY`.
2. Create a hosted docker repository. It plays the part of the Quay organization, for example `konflux`.
3. Turn the API on and, optionally, limit the repositories a client may use:

```bash
export OPENARTIFACT_SECRET_KEY=<LONG-RANDOM-STRING>
export OPENARTIFACT_QUAY_API_ENABLED=true
export OPENARTIFACT_QUAY_API_ORGANIZATIONS=konflux
```

The API is then served at `/quay/api/v1`. The keys are listed under [Quay-compatible API](../getting-started/configuration.md#quay-compatible-api-quayapi).

## Point the image-controller at OpenArtifact

Create an access token with the global `manage` permission (see [authentication](../security/authentication.md#api-tokens)) and put it in the Secret the image-controller reads:

```bash
kubectl -n image-controller create secret generic quaytoken \
  --from-literal=quaytoken=<TOKEN> \
  --from-literal=organization=konflux \
  --from-literal=quayapiurl=https://openartifact.example.com/quay/api/v1
```

Set `QUAY_IMAGE_DEFAULT_VISIBILITY` on the controller to match the repository (see [visibility](#visibility-belongs-to-the-organization)). The environment variables `QUAY_API_URL`, `QUAY_ORG` and `QUAY_TOKEN` work the same way as the Secret keys, and `QUAY_ADDITIONAL_CA` carries a CA bundle when OpenArtifact uses a private certificate.

A component then pushes to `openartifact.example.com/konflux/<NAMESPACE>/<COMPONENT>`. Each `ImageRepository` creates the image `<NAMESPACE>/<COMPONENT>` in the `konflux` repository.

## What the controller can do

| Quay call | What OpenArtifact does |
|---|---|
| Create repository | Records the image. Creating one that exists answers `Repository already exists`, which the controller treats as success |
| Get, list, delete repository | Reads, lists and removes the image records |
| Create, get, list, delete robot account | A robot is a service account with a deploy token. Reading a robot returns its token, as Quay does |
| Regenerate robot token | Revokes the robot's tokens and issues a new one |
| Set or remove a robot's or user's permission | Grants read or read and write on that one image |
| Create, list and delete teams, members and team permissions | A team is a group; its permission is granted on that one image |
| List and delete tags | Lists the image's tags; a delete goes through the [trash](../operations/trash.md) |
| Create, list, update and delete notifications | Stored and returned, never delivered. See [limits](#limits) |

Errors use the Quay shape, `{"error_message": "..."}`, with the same text in `message` where the controller reads it.

## What a robot can reach

A robot's token is a deploy token. It reaches the registry only for the images it was granted:

- `read` allows pulls of that image.
- `write` allows pulls and pushes of that image. It never allows deleting a tag or a manifest.
- The management API refuses it: a deploy token never has the `manage` permission, so a robot cannot use `/quay/api/v1` either.

The token that drives the API is more powerful. It can create robots and grant them access to any image of an allowed repository, so treat it like an admin credential and give it only to the image-controller. Set `OPENARTIFACT_QUAY_API_ORGANIZATIONS` to limit it to the repositories you intend to use.

Deleting a robot removes its service account, tokens and grants.

## Visibility belongs to the organization

Quay sets visibility per image. OpenArtifact sets it per hosted repository, through the repository's `public` setting. The API reports the repository's value as `is_public` for every image.

A create-repository request whose `visibility` differs from the repository's is refused with a `400` that names the mismatch, and so is a change-visibility request to the other value. The same value succeeds. Make the repository's visibility and `QUAY_IMAGE_DEFAULT_VISIBILITY` agree.

## Deleting an image

Deleting a repository through the API removes the image record, its tags, its permissions and its notifications. It does not delete manifests or layers: docker [garbage collection](../formats/docker.md#garbage-collection) reclaims them once nothing refers to them and the grace period (`OPENARTIFACT_DOCKER_GC_GRACE_PERIOD`, default `24h`) has passed. Tag deletes go through the trash and respect immutable releases (`409`).

## Limits

- **Notifications are not delivered.** Quay `repo_push` payloads do not map onto OpenArtifact's event formats, so the API only stores what the controller sends. Use [webhooks](../operations/webhooks.md) on the repository to react to pushes.
- **No `admin` role.** A permission is `read` or `write`; any other role is refused with `400`.
- **Organizations are repositories.** The `organization` value must name a hosted docker repository. Proxy and group repositories are refused.
- **Team roles are not modelled.** Every team member is a plain member; the role in a create-team request is ignored.
- **The Quay surface is not in the OpenAPI document.** It follows the Quay API, not the OpenArtifact conventions (snake_case envelopes, `{error, code}` errors), so it is described here and in Quay's own reference rather than in [the REST API](api.md).
- **A changed secret key makes stored robot tokens unreadable.** Reading such a robot answers `500`; regenerate its token.
