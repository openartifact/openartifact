#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Picks the fuzz targets a pull request can affect. Run from the repository root
# by clusterfuzzlite-pr.yml, with the changed files (git diff --name-only -z) on
# stdin. Prints one of
#   all            build every target: go.mod/go.sum or the fuzzing setup changed
#   none           no target is affected: nothing to build or fuzz
#   <dir> lines    the package directories (relative to the root) whose fuzz
#                  targets to build
#
# A target is affected when its package, or a package it imports (transitively,
# in this module), contains a changed file. Files that are not Go files count for
# the closest package above them (testdata seeds, go:embed assets, SQL). The
# direct import lists come from `go list -e`, which needs no module download.

mapfile -d '' changed

for f in "${changed[@]}"; do
  case "$f" in
    go.mod | go.sum | .clusterfuzzlite/* | .github/workflows/clusterfuzzlite-pr.yml)
      echo all
      exit 0
      ;;
  esac
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# <dir>|<import path>|<imports>|<test imports, external test imports>
# -e: keep going when a dependency is not downloaded; the module's own import
# lists are complete either way.
go list -e -f '{{.Dir}}|{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}' ./... \
  | sed "s|^$PWD/|./|; s|^$PWD|.|" > "$work/packages"

# Directories holding a fuzz target (same discovery as build.sh).
grep -rHoE --include='*_test.go' --exclude-dir=web --exclude-dir=node_modules --exclude-dir=.git \
  '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_]+ \*testing\.F\)' . | cut -d: -f1 | xargs -r -n1 dirname | sort -u > "$work/fuzzdirs"

# Package directory of every changed file: the closest ancestor that is a package.
cut -d'|' -f1 "$work/packages" | sort -u > "$work/pkgdirs"
: > "$work/changeddirs"
for f in "${changed[@]}"; do
  d=$(dirname "./$f")
  until [ "$d" = . ] || grep -qxF "$d" "$work/pkgdirs"; do
    d=$(dirname "$d")
  done
  echo "$d" >> "$work/changeddirs"
done
sort -u -o "$work/changeddirs" "$work/changeddirs"

awk -F'|' '
  FILENAME == ARGV[1] { fuzz[$0] = 1; next }
  FILENAME == ARGV[2] { changed[$0] = 1; next }
  {
    dir[$2] = $1
    pkg[$1] = $2
    imp[$2] = $3
    testimp[$2] = $4
  }
  END {
    for (d in fuzz) {
      # The target binary links the package and its imports; the test files of
      # the package (where the target lives) add their imports.
      delete seen
      top = split(pkg[d] " " imp[pkg[d]] " " testimp[pkg[d]], stack, " ")
      hit = 0
      while (top > 0 && !hit) {
        p = stack[top--]
        if (p in seen || !(p in dir)) continue
        seen[p] = 1
        if (dir[p] in changed) { hit = 1; break }
        n = split(imp[p], parts, " ")
        for (i = 1; i <= n; i++) stack[++top] = parts[i]
      }
      if (hit) print (d == "." ? "." : substr(d, 3))
    }
  }
' "$work/fuzzdirs" "$work/changeddirs" "$work/packages" | sort > "$work/affected"

if [ -s "$work/affected" ]; then
  cat "$work/affected"
else
  echo none
fi
