<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# CocoaPods

Use a CocoaPods repository to install pods through your own server. `pod install` reads the pod list and the podspecs from OpenArtifact instead of `cdn.cocoapods.org`, downloads the source archives from OpenArtifact, caches everything once, and curation rules decide which pod versions may be served.

| Item | Value |
|---|---|
| Client source | `source 'https://HOST/repos/REPO'` in the Podfile, or `pod repo add-cdn NAME https://HOST/repos/REPO` |
| Format id | `cocoapods` |
| Repository types | [proxy](#create-a-proxy-repository) only |
| Default upstream (proxy) | `https://cdn.cocoapods.org` |

There are no hosted or group repositories of this format: creating one is refused. A CocoaPods spec repository is a static tree of files that a proxy mirrors one request at a time, and CocoaPods has no upload protocol for it. The proxy keeps no package list. Cached files are shown in the repository's cache listing and are evicted with the usual [proxy cache controls](../repositories/hosted-proxy-group.md#cache-ttls). The [package trash](../operations/trash.md), cleanup policies, federation and promotion do not apply.

## Create a proxy repository

Create the proxy as an administrator. The default upstream is the public CDN, so no other field is needed.

Web UI: choose **New repository**, type **Proxy**, format **CocoaPods**.

API:

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"trunk","format":"cocoapods","repo_type":"proxy"}'
```

Declarative config, applied with `oa apply -f trunk.yaml` ([declarative config](../integrations/declarative.md)):

```yaml
apiVersion: openartifact.io/v1
kind: Repository
metadata:
  name: trunk
spec:
  format: cocoapods
  repo_type: proxy
```

The repository fields are described in [creating repositories](../repositories/hosted-proxy-group.md#creating-repositories). To mirror a private CDN spec repository, set `upstream_url` and, if it needs them, `upstream_username` and `upstream_password`. The credentials go only to the upstream host, never to the hosts the pod source archives live on.

## Client setup

Create an OpenArtifact API token as described in [authentication](../security/authentication.md); read access is enough. A public repository, or a server with `allowAnonymousRead`, is read without a token. The web UI connect panel shows the same snippets with your own URL.

CocoaPods downloads with `curl`, which takes credentials from `~/.netrc`. `machine` is the host name without the port, the user name is ignored and the token is the password:

```text
machine HOST login oa password <TOKEN>
```

Use the repository as the Podfile source:

```ruby
source 'https://HOST/repos/REPO'

platform :ios, '15.0'

target 'App' do
  pod 'Alamofire', '5.9.1'
end
```

Then run `pod install`. To search and read specs from the command line, register the repository once:

```sh
pod repo add-cdn openartifact https://HOST/repos/REPO
pod search Alamofire
pod spec cat Alamofire --version=5.9.1
pod repo update openartifact
```

A Podfile with several `source` lines can list the repository first and a private spec repository second. A pod that exists in more than one source is taken from the first.

## What is cached

All paths are relative to `HOST/repos/REPO`.

| Request | Behaviour |
|---|---|
| `CocoaPods-version.yml` | Cached for the repository's cache TTL. It tells the client how the pod list is sharded. |
| `all_pods.txt` | The list of pod names, cached and served as it is. |
| `all_pods_versions_X_Y_Z.txt` | The versions of the pods in one shard, cached for the cache TTL. With curation rules the server removes the hidden versions, and a pod with no version left. |
| `deprecated_podspecs.txt` | Cached for the cache TTL. |
| `Specs/X/Y/Z/NAME/VERSION/NAME.podspec.json` | Cached once, because a published podspec never changes. The server points its `source` at OpenArtifact. |
| `dl/X/Y/Z/NAME/VERSION/REF/FILE` | The source archive of a pod version. The server fetches it from the address in the upstream podspec, checks it, and caches it. |

Every document is checked before it is cached, so a login page from a captive portal is not stored. The server follows the redirects the CDN answers (the public CDN sends `Specs/` requests on to `cdn.jsdelivr.net`).

A source archive is downloaded from the address the podspec publishes, never from an address the client sends. A request for an archive that the podspec does not publish answers `404`.

### Source archives

The podspec says where the code of a pod comes from. The server serves two kinds of source and rewrites the podspec to point at itself:

- **HTTP source.** The archive is downloaded from the published URL. When the podspec gives a `sha256` or `sha1`, the download is refused unless it matches, and the archive is cached for good.
- **Git source of a GitHub repository at a `tag` or a `commit`.** The server downloads the tarball GitHub serves for that tag or commit. A commit never changes, so the archive is cached for good. A tag can move, so a tag archive is cached for the repository's cache TTL.

Other sources stay as the author published them, and the client fetches them directly from their origin: a git source on another host, a git source at a branch, and Subversion and Mercurial sources. They do not go through OpenArtifact, so a build without internet access cannot install such a pod. In a sample of 250 pods of the public CDN, 22 were of this kind.

## Minimum age and curation

Rules follow [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Names.** A rule names a pod as published, with its case: `Alamofire`. A glob such as `Firebase*` matches several pods.
- **Versions.** The version is the pod version, such as `5.9.1`. Use `*` to match any.
- **A hidden version** is missing from its shard, and its podspec and archive answer `403`.
- **`all_pods.txt`** lists every pod name, including pods that rules hide. A rule hides versions in the shard and refuses the podspec, so `pod install` of a hidden version fails with a clear error.
- **Minimum age.** The CDN publishes no dates, so `min_age_hours` holds nothing back for this format. Use curation rules to block versions.

## Limits and behaviour

- **Checksums change.** The server rewrites the `source` of each podspec, so the `SPEC CHECKSUMS` in a `Podfile.lock` differ from those you get from the public CDN, and they depend on the URL of your server. Run `pod install` against OpenArtifact for every developer and for CI, and commit the lock file that results. A lock file made against `cdn.cocoapods.org` makes `pod install` update the checksums on first use.
- **GitHub tarballs.** A git source becomes a tarball, which has no `.git` directory, no submodules and no files marked `export-ignore`. A pod that needs them cannot be installed through the proxy.
- **Sharded repositories only.** The upstream must publish a `CocoaPods-version.yml` with `prefix_lengths`, as the public CDN does. An upstream without it is not supported.
- **Pod names.** A name may contain a space, as a few published pods do.
- **Read only.** Everything that is not a `GET` or `HEAD` answers `405`.
- **Plain HTTP warning.** If your server is reached over `http`, `pod install` warns that pods are downloaded over an unencrypted protocol. Serve OpenArtifact over HTTPS.
- **Quota and cache.** Proxy caches are not metered by [storage quotas](../repositories/cleanup-and-quotas.md#what-counts).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `CDN: URL couldn't be downloaded ... 401` | `~/.netrc` has no entry for the host, or the token is wrong. | Add `machine HOST login oa password <TOKEN>` with the host name only, no port, no scheme. |
| `Unable to find a specification for 'NAME'` | The pod does not exist upstream, or every version is hidden by a rule. | Check the name and the curation rules. |
| `403` on a podspec or archive | Curation holds the version back. | Choose another version or change the rules. |
| `404` on `dl/...` | The URL does not match the cached podspec, for example a lock file or podspec copied from another server. | Run `pod repo update` and `pod install` again. |
| `502` on an archive | The archive does not match the checksum its podspec publishes, or the origin is unreachable. Nothing is cached. | Retry; if it persists, report it to the pod's author. |
| `pod install` changes `Podfile.lock` checksums | The lock was made against another source. | See [checksums change](#limits-and-behaviour); commit the new lock. |
| A pod download bypasses OpenArtifact | Its source is a branch, a non-GitHub git host or Subversion. | Mirror it separately, or pin a source that the proxy serves. |
| Creating a hosted or group repository is refused | The format is proxy only. | Create a proxy repository. |
