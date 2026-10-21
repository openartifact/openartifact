<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# CLI (`oa`)

`oa` is a single-binary command-line client for the OpenArtifact REST API: repositories, users, tokens, scans, webhooks, backups and the Nexus/Artifactory importer.

## Install

```sh
make build-cli          # builds bin/oa (version injected via -ldflags -X main.version)
go build -o bin/oa ./cmd/oa
```

Release builds (`.goreleaser.yml`) ship `oa_<version>_<os>_<arch>` archives for linux, darwin and windows (amd64, arm64). Source: [`cmd/oa`](../../cmd/oa).

```sh
oa version              # prints "oa version <version>" ("dev" for an untagged build)
oa help                 # full command list (also: oa with no arguments, -h, --help)
```

## Server, token and config file

| Setting | Flag | Env | Default |
|---|---|---|---|
| Server URL | `--server URL` | `OA_SERVER` | value saved by `oa auth login`, else `http://localhost:8080` |
| API token | `--token TOKEN` | `OA_TOKEN` | value saved by `oa auth login` |
| Output format | `--format table\|json` | n/a | `table` |

Precedence: flag, then environment variable, then the config file. Global flags go **before** the command (`oa --format json repo list`).

The config file is `~/.config/oa/config.json` on every OS (it does not follow `XDG_CONFIG_HOME` or the platform config dir). It holds `{"server": "...", "token": "..."}`. The directory is created with mode `0700` and the file is written atomically with mode `0600`.

Output: `--format json` prints indented JSON; anything else prints aligned tables. Errors go to stderr as `Error (<status>): <message>` and the exit code is 1. HTTP redirects that downgrade https to http are refused, and credentials are stripped when a redirect leaves the original host.

## Login

```sh
oa auth login --server http://localhost:8080      # prompts for username and password (no echo)
oa auth whoami
oa auth logout                                    # clears the saved token locally
```

Passwords are never accepted on the command line (`--password` is rejected). Non-interactive:

```sh
printf '%s\n' "$PW" | oa auth login --username alice --password-stdin
```

How it works:

1. Leave the username blank to paste an existing API token instead.
2. Otherwise the CLI signs in with `POST /api/v1/auth/login` on a temporary cookie session. The password is not stored.
3. If the account must change its password (bootstrap `admin`, reset account), the CLI asks for a new one first (with `--password-stdin`: the second stdin line) and signs in again.
4. It creates an `access` API token named `oa-cli` (`POST /api/v1/auth/tokens`), and saves server and token to the config file.

`oa auth logout` only clears the local token; it does not revoke the `oa-cli` token on the server (revoke it in the UI under Settings, Tokens). For CI, skip login: create a token in the UI and set `OA_SERVER` / `OA_TOKEN`. See [authentication](../security/authentication.md) for token kinds and scopes.

## Commands

Commands marked *admin* need the global `manage` permission. IDs are the values shown by the matching `list`/`show` command.

| Command | Subcommands and key flags |
|---|---|
| `auth` | `login [--server URL] [--username U] [--password-stdin]`, `logout`, `whoami`, `passwd [--password-stdin]` (stdin: current password, then new, one per line), `token list`, `token create --name N [--kind access\|deploy] [--username U] [--password-stdin]`, `token revoke <id> [--username U] [--password-stdin]` |
| `repo` | `list`, `create <name> [--format FMT] [--description D] [--public]` (default format `npm`), `show <name>`, `update <name> [--description D] [--public[=false]]` (only the flags you pass are sent), `delete <name>`, `packages <name>`, `members <name>` (the member repositories of a group repository) |
| `artifact` | `list <repo>`, `upload <repo> <file>`, `delete <repo> <id>` |
| `search` | `[--repo R] [--fmt F] [--limit N] <query>` (default limit 50; put flags before the query) |
| `user` *admin* | `list`, `create --username U --email E [--password-stdin] [--full-name N]`, `show <id>`, `update <id> [--email E] [--full-name N] [--disabled[=false]]`, `delete <id>` |
| `group` *admin* | `list`, `create <name> [--description D]`, `show <id>`, `delete <id>`, `members <id>`, `add-member <id> <user-id>`, `rm-member <id> <user-id>` |
| `org` | `list`, `create <name> [--display-name N] [--description D]`, `show <name>`, `delete <name>`, `members <name>`, `add-member <name> <user-id> [--role member\|owner]`, `rm-member <name> <user-id>` |
| `token` *admin* | `list` (all tokens), `revoke <id>` |
| `gpg` *admin* | `list`, `import --key <armored-file>`, `generate --name N --email E [--bits 4096]`, `delete <fingerprint>` |
| `storage` *admin* | `list`, `create --name N [--type filesystem\|s3\|gcs\|azure\|garage\|seaweedfs\|oci] [--config JSON \| --config-file FILE\|-] [--default]`, `show <name>`, `update <name> [--config JSON \| --config-file FILE\|-] [--default[=false]]`, `delete <name>`, `test <name>` (exits 1 when the backend check fails) |
| `backup` *admin* | `list`, `create [--type full\|metadata-only]`, `show <id>`, `download <id> <dest>`, `restore <archive-file>` (uploads an archive written by `backup download`) |
| `audit` *admin* | `list [--limit N] [--action A] [--actor A]` (default limit 50; pages through the server's 100-row pages) |
| `webhook` | `list`, `create --name N --url U --events E,E [--secret S] [--active=false]`, `show <id>`, `update <id> [--name N] [--url U] [--events E,E] [--active[=false]]`, `delete <id>`, `test <id>`, `deliveries <id>`, `repo list <repo>`, `repo create <repo> --name N --url U --events E,E [--secret S]` |
| `sbom` | `list <repo>`, `generate <repo> --artifact-id ID [--format cyclonedx\|spdx]`, `show <repo> <id>`, `download <repo> <id> <dest>`, `delete <repo> <id>` |
| `vuln` | `scan <repo> --artifact-id ID --package P --version V --ecosystem E [--scanner osv\|trivy\|grype]`, `list <repo>`, `show <repo> <id>`, `quarantine <repo> <id>`, `delete <repo> <id>` |
| `license` | `scan <repo> --artifact-id ID`, `list <repo>`, `show <repo> <id>` |
| `secret` | `scan <repo> --artifact-id ID [--content-file FILE\|-]`, `list <repo>`, `show <repo> <id>`, `quarantine <repo> <id>`, `delete <repo> <id>` |
| `cleanup` | `list <repo>`, `create <repo> --name N (--keep-count N \| --keep-days N) [--name-pattern REGEX] [--schedule CRON] [--enabled=false]`, `show <repo> <id>`, `update <repo> <id> [same flags]` (only the flags you pass are sent), `delete <repo> <id>`, `run <repo> <id>` |
| `quota` | `show <repo>`, `set <repo> [--limit N [--unit MB\|GB\|TB]] [--max-artifacts N] [--warn-percent P]` (default unit `GB`; at least one flag), `delete <repo>` |
| `props` | `list <repo> <artifact-id>`, `set <repo> <artifact-id> <key> <value>`, `delete <repo> <artifact-id> <key>`, `search <repo> <key> <value>` |
| `delta` | `list <repo> <pkg-id>`, `get <repo> <pkg-id> --from <sha256> <dest>` |
| `import` | `nexus\|artifactory ...`, see [Importing](#importing-from-nexus-or-artifactory) |
| `system` | `info` (version, status, commit, build time, Go version, OS, arch, uptime, database and storage status; admins get the detailed endpoint, other users only version and status) |
| `version`, `help` | local only, no server call |

Boolean flags use Go syntax: `--public`, `--disabled`, and `--active=false` / `--enabled=false` to turn off a default-true flag. Update commands send only the flags you pass, so `--public=false` or `--disabled=false` change just that field.

`auth passwd`, `auth token create` and `auth token revoke` sign in with your account password on a temporary cookie session (the server refuses these for API-token callers). The password is prompted without echo, or read from stdin with `--password-stdin`; `--username` defaults to the saved token's owner.

`storage create|update --config` is for non-secret settings only: the CLI rejects an inline config containing a key that looks like a credential (`secret`, `password`, `key`, `token`, `cred`, `connstr`, `sas`), because argv is visible in the process list. Put credentials in a file and pass `--config-file FILE`, or `--config-file -` to read the JSON from stdin.

### Transfers

Uploads and downloads stream through the CLI without buffering whole files:

| Command | Behavior |
|---|---|
| `oa artifact upload <repo> <file>` | multipart upload. `*.deb` goes to `POST /api/v1/repositories/{repo}/debs`; any other file goes to `.../packages` (the RPM upload endpoint). Other formats are published with their native client (`npm publish`, `mvn deploy`, `curl -T`, ...), see [formats](../formats/README.md) |
| `oa artifact upload --presigned [--path P] [--content-type T] <repo> <file>` | raw repositories on S3/GCS storage: hashes the file, asks `POST /api/v1/repositories/{repo}/uploads` for a presigned PUT, uploads the bytes straight to the object store (the OpenArtifact token is never sent there) and calls `.../uploads/{id}/complete`, which reads the object back from storage, verifies size and sha256 and records the artifact. The complete call has no response-header timeout and is retried while the server answers `409 upload_in_progress`. `--path` defaults to the file name. See [Presigned URLs](../operations/storage.md#presigned-urls) |
| `oa backup download <id> <dest>` | writes to a temp file next to `<dest>`, syncs, then renames onto `<dest>` |
| `oa sbom download <repo> <id> <dest>` | same |
| `oa delta get <repo> <pkg-id> --from <sha256> <dest>` | same; downloads the delta patch from the given base version |

There are no top-level `oa upload` / `oa download` commands.

### Importing from Nexus or Artifactory

`oa import nexus|artifactory --source-url URL --repo SRC[:DST]...` copies hosted Maven, Debian, RPM and raw repositories from a Nexus 3 or Artifactory server. The source password or token is read from a file, stdin or `OA_IMPORT_SOURCE_TOKEN`, never from the command line. Flags, supported formats, resume and checksum behavior are in [Migrating from Nexus or Artifactory](../migration/nexus-artifactory.md).

## Known limitations

| Command | Problem |
|---|---|
| `gpg list`, `import`, `generate`, `delete` | the server endpoints are stubs: `list` always returns an empty list, the other three return `501 not_implemented` |
| `webhook create --secret` | the secret is a command-line argument and is visible in the process list |
