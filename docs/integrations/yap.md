<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Publishing yap packages

[yap](https://github.com/M0Rf30/yap) builds `.deb` and `.rpm` packages (and `.apk` / `.pkg.tar.zst`) from one `PKGBUILD` recipe. It does not host packages or generate repository indexes. OpenArtifact does both: upload the built file and the Debian or RPM repository indexes (`Packages`, `Release`, `repomd.xml`) are regenerated for you, and optionally signed ([Debian](../formats/debian.md), [RPM](../formats/rpm.md)).

## 1. Create the repositories

Two hosted repositories, one per format (creating repositories needs an administrator; see the [API](api.md)):

```sh
HOST=https://artifacts.example.com
curl -sS -X POST "$HOST/api/v1/repositories" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"acme-deb","format":"deb","public":false}'
curl -sS -X POST "$HOST/api/v1/repositories" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"acme-rpm","format":"rpm","public":false}'
```

Mint a token with `write` permission on both repositories (a `deploy` token is enough for CI; see [Authentication](api.md)). Keep it in a file or CI secret, never on a command line.

## 2. Build with yap

```sh
yap build ubuntu-jammy .      # -> artifacts/*.deb
yap build rocky-9 .           # -> artifacts/*.rpm
```

Package name, version and architecture come from the recipe and end up in the control file or RPM header; OpenArtifact reads them from there.

## 3. Publish

With `yap publish` (yap from the release that includes it, M0Rf30/yap#248):

```sh
export YAP_OA_URL=https://artifacts.example.com
export YAP_OA_TOKEN_FILE=$HOME/.config/oa-token   # or YAP_OA_TOKEN
export YAP_OA_DEB_REPO=acme-deb
export YAP_OA_RPM_REPO=acme-rpm

yap publish artifacts/            # every .deb and .rpm in the directory
yap publish --dry-run artifacts/  # show what would be uploaded
```

The same settings are flags (`--url`, `--token-file`, `--deb-repo`, `--rpm-repo`, `--distribution` default `stable`, `--component` default `main`, `--retries`, `--timeout`) or a `publish` block in `yap.json` (`url`, `debRepo`, `rpmRepo`, `distribution`, `component`; the token is never read from the file). `--insecure-http` is needed only to send the token over plain HTTP to a non-loopback host. Each upload is verified against the SHA-256 the server reports. `.apk` and `.pkg.tar.*` files go to a hosted raw repository (`--apk-repo`, `--pacman-repo`); OpenArtifact has no native apk or pacman format.

`yap publish` is a thin client of the same routes you can call with `curl`:

```sh
# Debian: POST /api/v1/repositories/REPO/debs
curl -fsS -H "Authorization: Bearer $TOKEN" \
  -F file=@artifacts/acme_1.0.0_amd64.deb -F distribution=stable -F component=main \
  "$HOST/api/v1/repositories/acme-deb/debs"

# RPM: POST /api/v1/repositories/REPO/packages
curl -fsS -H "Authorization: Bearer $TOKEN" \
  -F file=@artifacts/acme-1.0.0-1.x86_64.rpm \
  "$HOST/api/v1/repositories/acme-rpm/packages"
```

Packages are immutable. `yap publish` of the same bytes again is a no-op (reported as `unchanged`); the same name and version with different bytes is `409` (a raw `curl` upload of an existing file is also `409`). Bump the release in the recipe instead.

## 4. Install on clients

Debian and Ubuntu:

```sh
# the repository's public key (served when a signing key applies to the repository)
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL -H "Authorization: Bearer $TOKEN" "$HOST/repos/acme-deb/public.key" |
  sudo gpg --dearmor -o /etc/apt/keyrings/acme.gpg
echo "deb [signed-by=/etc/apt/keyrings/acme.gpg] $HOST/repos/acme-deb stable main" |
  sudo tee /etc/apt/sources.list.d/acme.list
sudo apt update && sudo apt install acme
```

Without a signing key `public.key` is `404`: use `[trusted=yes]` instead of `signed-by=` (testing only). For private repositories put the token into `/etc/apt/auth.conf.d/acme.conf` (`machine HOST login USERNAME password <TOKEN>`).

Fedora, RHEL and Rocky:

```ini
# /etc/yum.repos.d/acme.repo
[acme]
name=acme
baseurl=https://artifacts.example.com/repos/acme-rpm
enabled=1
gpgcheck=0
repo_gpgcheck=1
gpgkey=https://artifacts.example.com/repos/acme-rpm/public.key
username=USERNAME
password=<TOKEN>
```

```sh
sudo dnf install acme
```

`repo_gpgcheck=1` and `gpgkey` need signing enabled on the server (`GPG_ENABLED=true`); otherwise set `repo_gpgcheck=0` and drop `gpgkey`. Package files are not re-signed by OpenArtifact: keep `gpgcheck=0` unless yap signed the RPM itself.

## Verifying a round trip

`scripts/smoke-formats.sh` publishes and installs a deb, an rpm and more in throwaway containers; see the verification section of `AGENTS.md`.
