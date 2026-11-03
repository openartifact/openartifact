#!/usr/bin/env sh
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Writes the shell completion scripts of the oa CLI to completions/ (the release
# archives ship them; .goreleaser.yml runs this as a before hook). They come from
# `oa completion SHELL`, so they always match the command tree of this commit.
#
# Usage: scripts/completions.sh [outdir]   (default: completions)
set -eu

out="${1:-completions}"
cd "$(dirname "$0")/.."

mkdir -p "$out"
go run ./cmd/oa completion bash >"$out/oa.bash"
go run ./cmd/oa completion zsh >"$out/_oa"
go run ./cmd/oa completion fish >"$out/oa.fish"
go run ./cmd/oa completion powershell >"$out/oa.ps1"
