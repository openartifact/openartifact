#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Picks the fuzz targets a pull request can affect. Run from the repository root
# by clusterfuzzlite-pr.yml, with the changed files (git diff --name-only -z) on
# stdin. Prints one of
#   none           no target is affected: nothing to build or fuzz
#   <name> lines   the fuzz targets to build (binary names, see build.sh)
#
# A target is affected when its package, or a package it imports (transitively,
# in this module), contains a changed file. Files that are not Go files count for
# the closest package above them (testdata seeds, go:embed assets, SQL). The
# direct import lists come from `go list -e`, which needs no module download.
#
# go.mod and go.sum: when GOMOD_DIFF names a file holding `git diff -U0` of
# them, only the modules whose requirement or checksum changed count: a target
# is affected when it links a package of one of them (the import graph then
# covers dependencies too, so the workflow runs `go mod download` first). A
# change to the go or toolchain directive, to replace/exclude/retract lines, to
# the fuzzing setup (.clusterfuzzlite/, clusterfuzzlite-pr.yml), or to go.mod or
# go.sum without GOMOD_DIFF, affects every target.
#
# FUZZ_MAX_TARGETS (default 30, 0 = no limit) bounds the build and run time: when
# more targets are affected the nearest ones win (the package with the change
# first, then the packages importing it, then theirs, ...), at most one more per
# package before a second one of the same package is taken. The rest is left to the
# daily batch run (clusterfuzzlite-batch.yml), which fuzzes every target.
#
# When COVERAGE_DIR points at the fuzzer_stats directory of the last batch
# coverage report, targets whose report lists no covered line of any changed Go
# file are dropped too (ClusterFuzzLite's own pruning of this kind cannot read
# the report: the file names in it start with the coverage build's workspace,
# not with the image's source directory). Progress goes to stderr.

mapfile -d '' changed

max=${FUZZ_MAX_TARGETS:-30}
case "$max" in
  '' | *[!0-9]*)
    echo "FUZZ_MAX_TARGETS must be a number, got '$max'" >&2
    exit 2
    ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Modules whose requirement or checksum changed (one per line), and whether the
# change reaches every target.
everything=0
: > "$work/changedmods"
gomod_changed=0
for f in "${changed[@]}"; do
  case "$f" in
    .clusterfuzzlite/* | .github/workflows/clusterfuzzlite-pr.yml) everything=1 ;;
    go.mod | go.sum) gomod_changed=1 ;;
  esac
done
if [ "$gomod_changed" = 1 ]; then
  if [ -n "${GOMOD_DIFF:-}" ] && [ -f "$GOMOD_DIFF" ]; then
    # Lines of the diff body only. go.sum: "<module> <version>[/go.mod] <hash>";
    # go.mod: "[require ]<module> <version> [// indirect]" or a directive.
    awk '
      /^(\+\+\+|---) / { gomod = ($2 ~ /go\.mod$/); next }
      /^[-+]/ {
        line = substr($0, 2)
        gsub(/^[ \t]+|[ \t]+$/, "", line)
        n = split(line, w, /[ \t]+/)
        if (line == "" || line == ")" || line ~ /^\/\// || line ~ /^require[ \t]*\($/) next
        if (gomod && w[1] ~ /^(module|go|toolchain|replace|exclude|retract|tool|godebug|ignore)$/) { print "*"; next }
        if (gomod && w[1] == "require") { print w[2]; next }
        print w[1]
      }
    ' "$GOMOD_DIFF" | sort -u > "$work/changedmods"
    if grep -qxF '*' "$work/changedmods"; then
      everything=1
    fi
  else
    everything=1
  fi
fi

# <dir>|<import path>|<imports>|<test imports, external test imports>
# -e: keep going when a dependency is not downloaded; the module's own import
# lists are complete either way. -deps adds the dependencies, only needed to
# follow a changed module down to the packages that import it.
listargs=(./...)
[ -s "$work/changedmods" ] && listargs=(-deps ./...)
go list -e -f '{{.Dir}}|{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}' "${listargs[@]}" \
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

# Distance of every fuzz package to the change: 0 when the package itself holds a
# changed file, 1 when it imports one that does, and so on (breadth first, so the
# first hit is the nearest). A package also counts as changed when it belongs to
# a module whose requirement or checksum changed. A change to everything puts
# every fuzz package at distance 0.
# Output: <package directory>\t<distance>
if [ "$everything" = 1 ]; then
  echo "the change reaches every fuzz target (go directive, fuzzing setup or go.mod/go.sum without a module list)" >&2
  sed 's|^\./||; s|$|\t0|' "$work/fuzzdirs" | sort > "$work/affected"
else
  awk -F'|' '
    FILENAME == ARGV[1] { fuzz[$0] = 1; next }
    FILENAME == ARGV[2] { changed[$0] = 1; next }
    FILENAME == ARGV[3] { mods[++nmods] = $0; next }
    {
      dir[$2] = $1
      pkg[$1] = $2
      imp[$2] = $3
      testimp[$2] = $4
    }
    function isChanged(p,    i) {
      if (dir[p] in changed) return 1
      for (i = 1; i <= nmods; i++)
        if (p == mods[i] || index(p, mods[i] "/") == 1) return 1
      return 0
    }
    END {
      for (d in fuzz) {
        # The target binary links the package and its imports; the test files of
        # the package (where the target lives) add their imports.
        delete seen
        head = 1
        tail = split(pkg[d] " " imp[pkg[d]] " " testimp[pkg[d]], queue, " ")
        for (i = 1; i <= tail; i++) depth[i] = (i == 1 ? 0 : 1)
        hit = -1
        while (head <= tail) {
          p = queue[head]; dp = depth[head]; head++
          if (p in seen || !(p in dir)) continue
          seen[p] = 1
          if (isChanged(p)) { hit = dp; break }
          n = split(imp[p], parts, " ")
          for (i = 1; i <= n; i++) { queue[++tail] = parts[i]; depth[tail] = dp + 1 }
        }
        if (hit >= 0) print (d == "." ? "." : substr(d, 3)) "\t" hit
      }
    }
  ' "$work/fuzzdirs" "$work/changeddirs" "$work/changedmods" "$work/packages" | sort > "$work/affected"
fi

# Narrow the packages down to targets with the coverage of the last batch run
# (fuzzer_stats/<target>.json under $COVERAGE_DIR, see below).
: > "$work/selected"
while IFS= read -r line; do
  file=${line%%:*}
  dir=$(dirname "$file")
  dir=${dir#./}
  dist=$(awk -F'\t' -v d="$dir" '$1 == d { print $2; exit }' "$work/affected")
  [ -n "$dist" ] || continue

  fn=${line#*:func }
  fn=${fn%%(*}
  name=$(echo "${dir}_${fn}" | tr -c 'A-Za-z0-9_\n-' '_')

  # Files the target links, with their covered region count. The batch coverage
  # build writes them as <workspace>/build-out/src/openartifact/<path>; the part
  # before <path> depends on where the report was built.
  stats="${COVERAGE_DIR:-}/$name.json"
  if [ "$everything" != 1 ] && [ -n "${COVERAGE_DIR:-}" ] && [ -f "$stats" ] \
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
  printf '%s\t%s\t%s\n' "$dist" "$dir" "$name" >> "$work/selected"
done < <(grep -rHoE --include='*_test.go' --exclude-dir=web --exclude-dir=node_modules --exclude-dir=.git \
  '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_]+ \*testing\.F\)' . | sort)

if [ ! -s "$work/selected" ]; then
  echo none
  exit 0
fi

# Rank by distance, then by the position inside the package (the first target of
# every package at that distance before the second of any), then by name; keep
# the first $max.
sort -t$'\t' -k1,1n -k2,2 -k3,3 "$work/selected" \
  | awk -F'\t' '{ print $1 "\t" ++n[$2] "\t" $3 }' \
  | sort -t$'\t' -k1,1n -k2,2n -k3,3 > "$work/ranked"
total=$(wc -l < "$work/ranked")
if [ "$max" -gt 0 ] && [ "$total" -gt "$max" ]; then
  echo "::warning title=Fuzz targets limited::$total targets are affected, fuzzing the nearest $max (FUZZ_MAX_TARGETS); the daily batch run covers the rest" >&2
  head -n "$max" "$work/ranked" > "$work/ranked.cut"
  mv "$work/ranked.cut" "$work/ranked"
fi
cut -f3 "$work/ranked" | sort
