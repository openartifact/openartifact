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
| `auth` | `login [--server URL] [--username U] [--password-stdin]`, `logout`, `whoami`, `passwd [--password-stdin]` (stdin: current password, then new, one per line), `token list`, `token create --name N [--kind access\|deploy]`, `token revoke <id>` |
| `repo` | `list`, `create <name> [--format FMT] [--description D] [--public]` (default format `npm`), `show <name>`, `update <name> [--description D] [--public]`, `delete <name>`, `packages <name>`, `members <name>` |
| `artifact` | `list <repo>`, `upload <repo> <file>`, `delete <repo> <id>` |
| `search` | `[--repo R] [--fmt F] [--limit N] <query>` (default limit 50; put flags before the query) |
| `user` *admin* | `list`, `create --username U --email E [--password-stdin] [--full-name N] [--admin]`, `show <id>`, `update <id> [--email E] [--full-name N] [--disabled]`, `delete <id>` |
| `group` *admin* | `list`, `create <name>`, `show <id>`, `delete <id>`, `members <id>`, `add-member <id> <user-id>`, `rm-member <id> <user-id>` |
| `org` | `list`, `create <name> [--display-name N] [--description D]`, `show <name>`, `delete <name>`, `members <name>`, `add-member <name> <user-id> [--role member\|owner]`, `rm-member <name> <user-id>` |
| `token` *admin* | `list` (all tokens), `revoke <id>` |
| `gpg` *admin* | `list`, `import --key <armored-file>`, `generate --name N --email E [--bits 4096]`, `delete <fingerprint>` |
| `storage` *admin* | `list`, `create --name N [--type filesystem\|s3\|gcs\|azure\|garage\|seaweedfs\|oci] [--config JSON] [--default]`, `show <name>`, `update <name> [--config JSON] [--default]`, `delete <name>`, `test <name>` |
| `backup` *admin* | `list`, `create [--type full\|metadata-only]`, `show <id>`, `download <id> <dest>`, `restore <id>` |
| `audit` *admin* | `list [--limit N] [--action A] [--user U]` (default limit 50) |
| `webhook` | `list`, `create --url U [--secret S] [--active=false]`, `show <id>`, `update <id> [--url U] [--active=false]`, `delete <id>`, `test <id>`, `deliveries <id>`, `repo list <repo>`, `repo create <repo> --url U [--secret S]` |
| `sbom` | `list <repo>`, `generate <repo>`, `show <repo> <id>`, `download <repo> <id> <dest>`, `delete <repo> <id>` |
| `vuln` | `scan <repo>`, `list <repo>`, `show <repo> <id>`, `quarantine <repo> <id>`, `delete <repo> <id>` |
| `license` | `scan <repo>`, `list <repo>`, `show <repo> <id>`, `delete <repo> <id>` |
| `secret` | `scan <repo>`, `list <repo>`, `show <repo> <id>`, `quarantine <repo> <id>`, `delete <repo> <id>` |
| `cleanup` | `list <repo>`, `create <repo> --name N [--type keep-last-n\|keep-by-age] [--keep-count N] [--keep-days N] [--enabled=false]`, `show <repo> <id>`, `update <repo> <id> [--enabled=false] [--keep-count N] [--keep-days N]`, `delete <repo> <id>`, `run <repo> <id>` |
| `quota` | `show <repo>`, `set <repo> --limit N [--unit MB\|GB\|TB]` (default `GB`), `delete <repo>` |
| `props` | `list <repo> <artifact-id>`, `set <repo> <artifact-id> <key> <value>`, `delete <repo> <artifact-id> <key>`, `search <repo> <key> <value>` |
| `delta` | `list <repo> <pkg-id>`, `get <repo> <pkg-id> --from <sha256> <dest>` |
| `import` | `nexus\|artifactory ...`, see [Importing](#importing-from-nexus-or-artifactory) |
| `system` | `info` (version, commit, build time, Go version, OS, arch) |
| `version`, `help` | local only, no server call |

Boolean flags use Go syntax: `--public`, `--admin`, `--disabled`, and `--active=false` / `--enabled=false` to turn off a default-true flag.

### Transfers

Uploads and downloads stream through the CLI without buffering whole files:

| Command | Behavior |
|---|---|
| `oa artifact upload <repo> <file>` | multipart upload. `*.deb` goes to `POST /api/v1/repositories/{repo}/debs`; any other file goes to `.../packages` (the RPM upload endpoint). Other formats are published with their native client (`npm publish`, `mvn deploy`, `curl -T`, ...), see [formats](../formats/README.md) |
| `oa backup download <id> <dest>` | writes to a temp file next to `<dest>`, syncs, then renames onto `<dest>` |
| `oa sbom download <repo> <id> <dest>` | same |
| `oa delta get <repo> <pkg-id> --from <sha256> <dest>` | same; downloads the delta patch from the given base version |

There are no top-level `oa upload` / `oa download` commands.

### Importing from Nexus or Artifactory

`oa import nexus|artifactory --source-url URL --repo SRC[:DST]...` copies hosted Maven, Debian, RPM and raw repositories from a Nexus 3 or Artifactory server. The source password or token is read from a file, stdin or `OA_IMPORT_SOURCE_TOKEN`, never from the command line. Flags, supported formats, resume and checksum behavior are in [Migrating from Nexus or Artifactory](../migration/nexus-artifactory.md).

## Known limitations

These come from comparing `cmd/oa/main.go` with the server handlers; use the UI, the [REST API](api.md) or a native client for these cases until the CLI is fixed.

| Command | Problem |
|---|---|
| `repo show`, `repo update`, `repo delete` | call `/api/v1/repositories/{name}`; the server serves these under `/api/v1/repository/{name}`, so they fail. `repo update` also resets `public` to `false` when `--public` is omitted |
| `auth passwd`, `auth token create`, `auth token revoke` | the server refuses these for API-token callers (403), and the CLI always authenticates with a token. Use the UI |
| `auth token list` | reads a `tokens` key; the server returns `data`, so the list is empty |
| `license delete` | no `DELETE .../license/scans/{id}` route exists |
| `gpg list`, `import`, `generate`, `delete` | the server endpoints are stubs: `list` always returns an empty list, the other three return `501 not_implemented` |
| `cleanup create`, `cleanup update` | send `type`, `keep_count`, `keep_days`; the server reads `keep_last_n`, `keep_since_days` and ignores the rest |
| `quota set` | sends `limit_bytes`; the server reads `max_size_bytes`, so the request changes nothing |
| `webhook create`, `webhook update` | send `active`; the server reads `enabled` |
| `storage create --config` | the JSON (including credentials) is a command-line argument and is visible in the process list |
