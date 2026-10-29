#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# go: PUT a module zip to a public repository, then go get it through GOPROXY and build against it.
# Image: GO_IMAGE (default golang:latest, the newest go).
# Usage: OA_TOKEN=... scripts/smoke/go.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init go golang:latest "$@"
# The go command never sends credentials over plain http (only GOAUTH over
# https), so the consumed repository is a public one; uploads still use the token.
smoke_create_repo "$SMOKE_REPO-pub" go '{"public":true}'

smoke_run "$(cat <<'SCRIPT'
set -eu
go version
mod="example.com/$PKG"
v="v$VERSION"
mkdir -p /tmp/zipper /tmp/c && cd /tmp/zipper
printf 'module zipper\n\ngo 1.20\n' > go.mod
cat > main.go <<'EOT'
package main

import (
	"archive/zip"
	"os"
)

func main() {
	mod, v, out := os.Args[1], os.Args[2], os.Args[3]
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	w := zip.NewWriter(f)
	for name, body := range map[string]string{
		"go.mod": "module " + mod + "\n\ngo 1.20\n",
		"lib.go": "package smoke\n\nconst Marker = \"" + mod + "\"\n",
	} {
		e, err := w.Create(mod + "@" + v + "/" + name)
		if err != nil {
			panic(err)
		}
		e.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	f.Close()
}
EOT
go run . "$mod" "$v" /tmp/m.zip
curl -fsS -X PUT -H "Authorization: Bearer $OA_TOKEN" --data-binary @/tmp/m.zip \
  "$OA_URL/repos/$REPO/$mod/@v/$v" >/dev/null
export GOPROXY="$OA_URL/repos/$REPO" GONOSUMDB='*' GOSUMDB=off GOFLAGS=-mod=mod
cd /tmp/c
printf 'module consumer\n\ngo 1.20\n' > go.mod
cat > main.go <<EOT
package main

import (
	"fmt"

	smoke "$mod"
)

func main() { fmt.Print(smoke.Marker) }
EOT
go get "$mod@$v"
[ "$(go run .)" = "$mod" ]
SCRIPT
)" -e REPO="$SMOKE_REPO-pub"
