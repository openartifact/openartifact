<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Your first repository

Create a repository, make an access token, and publish and install one package. This page uses npm; every other format works the same way, with the commands of its [format page](../formats/README.md).

You need a running server ([installation](installation.md)), the `admin` password ([first login](installation.md#first-login)) and `npm`. The examples use `http://localhost:8080`; replace it with your server URL.

## 1. Create a hosted repository

1. Sign in at <http://localhost:8080> as `admin`. If the UI asks you to change the generated password, do that first.
2. Open **Repositories** and choose **New repository**.
3. Choose the format **npm**, keep the type **hosted**, and name it `npm-hosted`.
4. Create it.

The format and the type cannot change later. A hosted repository stores the packages you publish. [Proxy and group repositories](../repositories/hosted-proxy-group.md) mirror an upstream registry or combine several repositories.

## 2. Create an access token

Package clients sign in with a token, not with your password.

1. Open **Settings > Access tokens** and choose **Create token**.
2. Keep the type **Personal**, name the token `first-token` and pick the kind **Deploy: artifacts only**.
3. Create it and copy the secret. It is shown once.

A deploy token can publish and download packages but cannot call the management API, so it is the safe choice for clients and CI. Use **Access: acts as you** only for scripts that manage OpenArtifact itself. More about tokens: [authentication](../security/authentication.md#api-tokens).

## 3. Point npm at the repository

Run this in an empty project directory so that the settings stay local. The token is the one you copied:

```sh
npm config set registry http://localhost:8080/repos/npm-hosted/ --location=project
npm config set //localhost:8080/repos/npm-hosted/:_authToken <TOKEN> --location=project
```

Do not commit the resulting `.npmrc` file: it holds the token. The [client setup](../formats/client-setup.md) page shows the commands for every other client.

## 4. Publish a package

```sh
cat > package.json <<'EOF'
{ "name": "hello-oa", "version": "1.0.0", "description": "My first package" }
EOF
echo "module.exports = 'hello'" > index.js
npm publish
```

npm prints `+ hello-oa@1.0.0`. Open **Repositories > npm-hosted** to see the package. Publishing the same version again fails with `409`, because versions are immutable.

## 5. Install it

In another directory, use the same two `npm config set` commands, then:

```sh
npm init -y
npm install hello-oa
```

npm downloads `hello-oa` from your repository into `node_modules`.

## Do it from a terminal instead

Steps 1 and 2 also work through the API. Creating a token needs a login session, not another token:

```sh
HOST=http://localhost:8080
curl -sS -c jar -X POST $HOST/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<PASSWORD>"}' -o /dev/null
CSRF=$(awk '$6=="oa_csrf"{print $7}' jar)

curl -sS -b jar -X POST $HOST/api/v1/repositories -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' \
  -d '{"name":"npm-hosted","format":"npm","repo_type":"hosted"}'

curl -sS -b jar -X POST $HOST/api/v1/auth/tokens -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' -d '{"name":"first-token","kind":"deploy"}'
```

The last response contains the token once, in the `token` field. The [`oa` CLI](../integrations/cli.md) does the same with `oa auth login` and `oa repo create`.

## Next

- [Client setup](../formats/client-setup.md): commands for every package client.
- [Hosted, proxy and group repositories](../repositories/hosted-proxy-group.md): mirror an upstream and combine repositories.
- [RBAC](../security/rbac.md) and [projects](../security/projects.md): who may read and write what.
- [CI integration](../integrations/ci.md): least-privilege tokens for pipelines.
- [Web UI tour](web-ui.md).
