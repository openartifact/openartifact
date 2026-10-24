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

# <file>:func FuzzX(f *testing.F)  -> one line per target. Files behind a build
# tag (integration tests) are not compiled without it and are skipped.
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
done < <(grep -rHoE --include='*_test.go' --exclude-dir=web --exclude-dir=node_modules --exclude-dir=.git \
  '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_]+ \*testing\.F\)' . | sort)

if [ "$built" -eq 0 ]; then
  echo "no fuzz targets found" >&2
  exit 1
fi

echo "built $built fuzz targets"
