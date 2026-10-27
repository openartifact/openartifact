#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Picks the fuzz targets a pull request can affect. Run from the repository root
# by clusterfuzzlite-pr.yml, with the changed files (git diff --name-only -z) on
# stdin. Prints one of
#   all            build every target: go.mod/go.sum or the fuzzing setup changed
#   none           no target is affected: nothing to build or fuzz
#   <name> lines   the fuzz targets to build (binary names, see build.sh)
#
# A target is affected when its package, or a package it imports (transitively,
# in this module), contains a changed file. Files that are not Go files count for
# the closest package above them (testdata seeds, go:embed assets, SQL). The
# direct import lists come from `go list -e`, which needs no module download.
#
# When COVERAGE_DIR points at the fuzzer_stats directory of the last batch
# coverage report, targets whose report lists no covered line of any changed Go
# file are dropped too (ClusterFuzzLite's own pruning of this kind cannot read
# the report: the file names in it start with the coverage build's workspace,
# not with the image's source directory). Progress goes to stderr.

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

printf '%s\n' "${changed[@]}" > "$work/changedfiles"

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

# Narrow the packages down to targets with the coverage of the last batch run
# (fuzzer_stats/<target>.json under $COVERAGE_DIR, see below), then print the names.
: > "$work/selected"
while IFS= read -r line; do
  file=${line%%:*}
  dir=$(dirname "$file")
  dir=${dir#./}
  grep -qxF "$dir" "$work/affected" || continue

  fn=${line#*:func }
  fn=${fn%%(*}
  name=$(echo "${dir}_${fn}" | tr -c 'A-Za-z0-9_\n-' '_')

  # Files the target links, with their covered region count. The batch coverage
  # build writes them as <workspace>/build-out/src/openartifact/<path>; the part
  # before <path> depends on where the report was built.
  stats="${COVERAGE_DIR:-}/$name.json"
  if [ -n "${COVERAGE_DIR:-}" ] && [ -f "$stats" ] \
    && jq -r '.data[0].files[] | "\(.filename)\t\(.summary.regions.covered)"' "$stats" 2> /dev/null \
      | sed -n 's|^.*/src/openartifact/||p' > "$work/stats" && [ -s "$work/stats" ]; then
    # Kept when a changed file is covered by the target, is unknown to the report
    # (new file) or is not a Go file (seeds, embedded assets).
    if ! awk -F'\t' '
      FILENAME == ARGV[1] { cov[$1] = $2; next }
      { if ($0 !~ /\.go$/ || !($0 in cov) || cov[$0] > 0) { keep = 1; exit } }
      END { exit !keep }
    ' "$work/stats" "$work/changedfiles"; then
      echo "coverage: $name does not cover the change, skipped" >&2
      continue
    fi
  fi
  echo "$name" >> "$work/selected"
done < <(grep -rHoE --include='*_test.go' --exclude-dir=web --exclude-dir=node_modules --exclude-dir=.git \
  '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_]+ \*testing\.F\)' . | sort)

if [ -s "$work/selected" ]; then
  sort "$work/selected"
else
  echo none
fi
