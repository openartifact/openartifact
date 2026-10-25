#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Builds every native Go fuzz target (`func FuzzX(f *testing.F)` in a *_test.go
# file) of the module with compile_native_go_fuzzer_v2. No list to maintain:
# a new target is picked up by the next run. The fuzzer binary is named
# <package path>_<FuzzX> with every character outside [A-Za-z0-9_-] replaced
# by `_` (ClusterFuzzLite rejects other characters).
#
# Run from the repository root inside the base-builder-go image, see
# docs/development/fuzzing.md.

cd "$SRC/openartifact"

module=$(go list -m)
built=0

# $LIB_FUZZING_ENGINE links the image's precompiled gosigfuzz.o into every
# target. Recompile it from our copy, which makes libFuzzer wait for the Go
# runtime before it reads the coverage counters (see gosigfuzz.c).
sigfuzz_obj="${GOPATH:-/root/go}/gosigfuzz/gosigfuzz.o"
$CC $CFLAGS -c .clusterfuzzlite/gosigfuzz.c -o "$sigfuzz_obj"

# <file>:func FuzzX(f *testing.F)  -> one line per target.
targets=$(mktemp)
grep -rHoE --include='*_test.go' --exclude-dir=web --exclude-dir=node_modules --exclude-dir=.git \
  '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_]+ \*testing\.F\)' . | sort > "$targets"

# The fuzz builder compiles the package's test files as ordinary sources, which
# fails when the directory also holds an external test package (`package x_test`).
# The fuzz targets live in the package itself, so those files are not needed here
# (this is the image's copy of the tree, not the checkout).
while IFS= read -r dir; do
  grep -lE --include='*_test.go' '^package [A-Za-z0-9_]+_test$' "$dir"/*_test.go 2>/dev/null | xargs -r rm -v
done < <(cut -d: -f1 "$targets" | xargs -n1 dirname | sort -u)

# Files behind a build tag (integration tests) are not compiled without it and
# are skipped.
while IFS=: read -r file decl; do
  if grep -q '^//go:build' "$file"; then
    echo "skip $file: build-tagged"
    continue
  fi

  dir=$(dirname "$file")
  dir=${dir#./}
  fn=${decl#func }
  fn=${fn%%(*}

  pkg="$module"
  [ "$dir" = "." ] || pkg="$module/$dir"

  name=$(echo "${dir}_${fn}" | tr -c 'A-Za-z0-9_\n-' '_')

  compile_native_go_fuzzer_v2 "$pkg" "$fn" "$name"
  built=$((built + 1))
done < "$targets"

rm -f "$targets"

if [ "$built" -eq 0 ]; then
  echo "no fuzz targets found" >&2
  exit 1
fi

echo "built $built fuzz targets"
