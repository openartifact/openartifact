<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Reference

Lookup pages generated from the code, so they cannot drift from it: CI regenerates them (`go run ./cmd/docgen -check`) and fails when a committed page differs. To change one, change its source and run `make docs-gen`.

| Page | Source | Contents |
|---|---|---|
| [Configuration keys](config.md) | `internal/config` | Every YAML key with its environment variable, type, default and description |
| [Metrics](metrics.md) | registered Prometheus collectors | Name, type, labels and help of every metric served on `/metrics` |
| [CLI commands](cli.md) | `oa help` | Every `oa` command with its usage |
| [REST API](api/README.md) | `internal/api/openapi.json` | Every endpoint per tag: parameters, request bodies, responses and schemas |

How-to pages that explain behaviour, with prose and examples: [configuration](../getting-started/configuration.md), [monitoring](../operations/monitoring.md), [CLI](../integrations/cli.md) and [REST API](../integrations/api.md).
