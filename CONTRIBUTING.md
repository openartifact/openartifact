<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Contributing to OpenArtifact

Thanks for your interest. Development happens at
<https://github.com/openartifact/openartifact>; open issues and pull requests
there.

For security problems do **not** open an issue; see [SECURITY.md](SECURITY.md).

## Development setup

Requirements: Go 1.27, [bun](https://bun.sh) 1.4.4, PostgreSQL 18 (or Docker
for the integration tests), and optionally `golangci-lint` v2.15.0. `sqlc`,
`govulncheck` and `actionlint` are pinned in `tools/go.mod` and run through
`go tool -modfile=tools/go.mod <name>` (Dependabot keeps them current).

```sh
make build        # builds the web UI and bin/openartifact
make test         # unit tests with the race detector
make lint         # golangci-lint
make check        # fmt + vet + lint
make test-integration
```

## Workflow

1. Fork the repository and create a topic branch.
2. Keep changes focused; add or update tests for behavior changes.
3. Run `make check` and `make test` before opening a pull request.
4. If you change SQL in `internal/database/queries/` or the schema in
   `migrations/`, run `make generate` (sqlc) and commit the regenerated
   `internal/database/dbsqlc/` output; CI fails on drift.
5. If you change the HTTP API, update `internal/api/openapi.json`, and
   regenerate the web client types with `cd web && bun run generate-api`.

6. Document user-visible changes (feature, format, config key, CLI command,
   API area) in `docs/`; the README is only an overview. See
   [docs/development](docs/development/README.md) for the toolchain, CI and
   architecture.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/), e.g.
`feat(npm): support abbreviated packuments` or `fix(auth): reject expired sessions`.

## Licensing

OpenArtifact is licensed under AGPL-3.0-or-later. By contributing you agree
that your contribution is licensed under the same terms. No DCO sign-off or
CLA is required. The project is [REUSE](https://reuse.software/) compliant:
new source files need the SPDX header used by existing files
(`reuse annotate --copyright "The OpenArtifact Authors" --license
AGPL-3.0-or-later --exclude-year <file>`), and CI runs `reuse lint`.
