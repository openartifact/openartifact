<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Hugging Face

Use a Hugging Face repository to download models, datasets and spaces through your own server. `hf download`, `huggingface_hub` and `transformers` talk to OpenArtifact instead of `huggingface.co`, every file is cached once per commit, and curation and minimum-age rules decide what may be served.

| Item | Value |
|---|---|
| Client endpoint | `HF_ENDPOINT=https://HOST/repos/REPO` (no trailing slash) |
| Format id | `huggingface` |
| Repository types | [proxy](#create-a-proxy-repository) and [group](#group-repositories) only |
| Default upstream (proxy) | `https://huggingface.co` |

There are no hosted repositories of this format: creating one is refused. Nothing is uploaded to OpenArtifact, and the proxy keeps no package list. Cached files are shown in the repository's cache listing and are evicted with the usual [proxy cache controls](../repositories/hosted-proxy-group.md#cache-ttls). The [package trash](../operations/trash.md), cleanup policies, federation and promotion do not apply.

## Create a proxy repository

A public model needs no token upstream. Create the proxy as an administrator.

Web UI: choose **New repository**, type **Proxy**, format **Hugging Face**.

API:

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"hf","format":"huggingface","repo_type":"proxy"}'
```

Declarative config, applied with `oa apply -f hf.yaml` ([declarative config](../integrations/declarative.md)):

```yaml
apiVersion: openartifact.io/v1
kind: Repository
metadata:
  name: hf
spec:
  format: huggingface
  repo_type: proxy
  upstream_password:
    secretRef: {env: HF_UPSTREAM_TOKEN}
```

The repository fields are described in [creating repositories](../repositories/hosted-proxy-group.md#creating-repositories). For this format `upstream_password` holds a Hugging Face access token, and `upstream_username` is ignored. Set the token for [gated and private repositories](#gated-and-private-repositories).

## Client setup

Create an OpenArtifact API token as described in [authentication](../security/authentication.md); read access is enough. The `huggingface_hub` client sends it as `Authorization: Bearer <TOKEN>`. A public repository, or a server with `allowAnonymousRead`, is read without a token. The web UI connect panel shows the same commands with your own URL.

```sh
export HF_ENDPOINT=https://HOST/repos/REPO
export HF_TOKEN=<TOKEN>
export HF_HUB_DOWNLOAD_TIMEOUT=600
```

`HF_TOKEN` is the OpenArtifact token, not the Hugging Face one. Raise `HF_HUB_DOWNLOAD_TIMEOUT` (default 10 seconds) for large files: the server sends the first byte of a file only after it has fetched the whole file from the upstream ([cold downloads](#caching-and-revision-pinning)).

Download with the `hf` CLI:

```sh
hf download hf-internal-testing/tiny-random-bert
hf download hf-internal-testing/tiny-random-bert config.json
hf download SOME_ORG/SOME_DATASET --repo-type dataset
```

Download from Python:

```python
from huggingface_hub import snapshot_download
snapshot_download("hf-internal-testing/tiny-random-bert")

from transformers import AutoModel
AutoModel.from_pretrained("hf-internal-testing/tiny-random-bert")
```

Xet transfer is not used: the proxy removes the Xet headers from its answers, so the client falls back to plain HTTP downloads and you need no extra setting.

## Gated and private repositories

For a gated or private model, create a Hugging Face access token with read permission and store it as `upstream_password` of the proxy. Send it in a `PATCH /api/v1/repository/REPO` or set it in the web UI proxy settings. The token is stored encrypted and never returned.

- The server sends the token only to the configured upstream host, never to the CDN hosts the upstream redirects file downloads to.
- The token's access is the access of everyone who can read the proxy. Give the proxy its own read-only token, and grant the OpenArtifact repository only to the people who may see those models.
- A token without access to a model gives the client `403` (`X-Error-Code: GatedRepo`) or `401` with the upstream's error code.
- Changing the upstream host removes the stored token; see [upstream URL and credentials](../repositories/hosted-proxy-group.md#upstream-url-and-credentials).

## Caching and revision pinning

A model is addressed by a revision: a branch, a tag, a reference such as `refs/pr/1` (sent as `refs%2Fpr%2F1`) or a commit hash.

- **Resolution.** The server resolves a branch, tag or reference against the upstream to a commit hash and caches the answer for the repository's cache TTL (`cache_ttl_seconds`). Every response carries `X-Repo-Commit: <commit hash>`.
- **Per-commit cache.** A file is cached under its commit and never fetched again. A second download of the same revision is served from the cache without contacting the upstream while the reference answer has not expired.
- **After the TTL.** Only the small reference lookup is repeated. If the branch moved, the server fetches just the files that changed; a different commit has its own cache entries.
- **Verification.** The server checks the SHA-256 of an LFS file, or the git blob hash of any other file (both come from the upstream `ETag` or `X-Linked-Etag`), before it caches the file. A mismatch answers `502` and nothing is cached.
- **Cold downloads.** A file that is not cached is fetched completely into the cache before the first byte goes to the client. Large files are spooled to disk and streamed from storage, never held in memory. A file larger than the proxy body limit of 4 GiB is refused.
- **HEAD.** A `HEAD` request answers `200` with `X-Repo-Commit`, `ETag`, `X-Linked-Etag` (the upstream's), `X-Linked-Size` and `Content-Length`. It never redirects.

To make a build reproducible, pin a commit hash: `hf download ORG/MODEL --revision COMMIT`.

### Supported requests

All paths are relative to `HOST/repos/REPO`. `ID` is `ORG/NAME`, or a single name such as `gpt2`. Datasets use the prefix `datasets/` and spaces `spaces/` in the file path.

| Request | Purpose |
|---|---|
| `GET`, `HEAD /ID/resolve/REVISION/PATH` | Download a model file. `datasets/ID/resolve/...` and `spaces/ID/resolve/...` for the others. |
| `GET /api/models/ID`, `/api/datasets/ID`, `/api/spaces/ID` | Repository info: commit hash `sha` and the file list `siblings`. `?blobs=true` is honoured, other query parameters are ignored. |
| `GET /api/models/ID/revision/REVISION` | The same for one revision. |
| `GET /api/models/ID/tree/REVISION[/PATH]` | Directory listing with `recursive`, `expand` and cursor pagination. The `Link` header points at OpenArtifact, and `xetHash` is removed from entries. |

## Minimum age and curation

Rules and `min_age_hours` follow [Minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Names.** A rule names a repository as on the Hub, in lower case: `org/model`. A dataset is `datasets/org/name` and a space `spaces/org/name`. A glob such as `datasets/*` matches all datasets.
- **Versions.** The version is the full commit hash. Use `*` to match any.
- **Minimum age date.** The server uses the commit date.
- **Branches and tags.** A branch or tag resolves to the newest commit that is old enough. The server walks the commit history, up to 500 commits back. `main` therefore stays on the previous revision until the newer commit has aged.
- **Explicit commits.** A commit hash younger than the minimum age answers `403` with the reason. If no commit of a reference is old enough, the answer is `403` as well.

## Group repositories

A group is a plain first-hit list of Hugging Face proxies. For each request the server tries the members in order and serves the first answer that is not `404` or a server error. A member that answers `401` or `403` (a gated or private model it has no access to) ends the request with that answer. Nothing is merged.

- Give the members different upstreams, for example an internal Hub first and the public one second. A member that does not know a model answers `404` and the next one is asked.
- Repository info, file listings and downloads of one model should come from the same member. That holds when the members hold disjoint sets of models.
- Members must be Hugging Face proxies; a hosted member does not exist for this format.
- A group is read-only.

Create it as for every format:

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"hf-all","format":"huggingface","repo_type":"group","member_repos":["hf-private","hf"]}'
```

## Limits and behaviour

- **Range requests.** A request with a `Range` header is answered whole (`200`). The client then restarts the download from the beginning.
- **Xet.** Xet transfer is not used; see [client setup](#client-setup).
- **Not supported.** Uploads, commits and discussions, `POST` on `paths-info`, `refs`, search and list endpoints (`/api/models?search=`), `whoami` and `raw/` answer `404` or `405`.
- **Large files.** Files over 4 GiB are refused. Raise the client timeout for big files.
- **Quota and cache.** Proxy caches are not metered by [storage quotas](../repositories/cleanup-and-quotas.md#what-counts).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| The client times out on a large file | The first byte is sent after the whole file is cached, and the default timeout is 10 seconds. | Set `HF_HUB_DOWNLOAD_TIMEOUT=600` or higher. |
| `403` with `X-Error-Code: GatedRepo` | The upstream token has no access to the model, or the model is gated and no token is set. | Accept the model's terms on the Hub, then set a token with access as `upstream_password`. |
| `401` | The OpenArtifact token is missing or lacks read access, or the upstream token is invalid. | Set `HF_TOKEN` to an OpenArtifact token; check `upstream_password`. |
| `404` with `X-Error-Code: RepoNotFound` | The model does not exist on the upstream, or the token cannot see it. | Check the repository id and the token. |
| `404` with `X-Error-Code: RevisionNotFound` | The branch, tag or commit does not exist. | Check the revision. |
| `404` with `X-Error-Code: EntryNotFound` | The file is not in that revision. | Check the file name on the Hub. |
| `502` on a file | The file's checksum differs from the one the upstream announced. Nothing was cached. | Retry; if it persists, report it to the upstream. |
| `403` for a revision | Minimum age or curation holds it back. | Wait until it is old enough, use an older commit, or change the rules. |
| `main` is older than the Hub | The newest commit is younger than `min_age_hours`, or the reference answer is still cached. | Wait, or lower the minimum age; the cache TTL bounds the delay otherwise. |
| A download restarts after an interruption | Range requests are answered whole. | Download again; the cached file is served at once. |
