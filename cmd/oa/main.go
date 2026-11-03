// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command oa is the OpenArtifact command-line client. The commands live in
// internal/oacli, which the server binary also embeds (`openartifact oa`, or a
// link named oa), so the container image ships one executable.
package main

import (
	"os"

	"github.com/openartifact/openartifact/internal/oacli"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(oacli.Main(version, os.Args[1:]))
}
