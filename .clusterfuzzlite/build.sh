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
# When CFL_EXTRA_FUZZ_DIRS is set (pull requests, see select-targets.sh) only
# the targets in those package directories are built.
#
# Targets are compiled in parallel, at most min(nproc, available memory / 1.5 GiB)
# at a time (FUZZ_BUILD_JOBS overrides). Each target builds into its own scratch
# directory and the results are merged into $OUT in sorted order, so the output
# does not depend on scheduling. The first failure stops the remaining
# targets from starting and fails the build.
#
# Next to every fuzzer binary the build also writes, when present:
#   <name>_seed_corpus.zip   from <pkg>/testdata/fuzz/<FuzzX>/ (Go corpus files
#                            encoded by the image's addStdLibCorpusToFuzzer: the
#                            OSS-Fuzz Go shim ignores f.Add and testdata/fuzz)
#   <name>.dict              .clusterfuzzlite/dicts/<name>.dict, else the package
#                            dictionary .clusterfuzzlite/dicts/<package path>.dict
#
# Run from the repository root inside the base-builder-go image, see
# docs/development/README.md.

cd "$SRC/openartifact"

export module
module=$(go list -m)

# $LIB_FUZZING_ENGINE links the image's precompiled gosigfuzz.o into every
# target. Recompile it from our copy, which makes libFuzzer wait for the Go
# runtime before it reads the coverage counters (see gosigfuzz.c).
sigfuzz_obj="${GOPATH:-/root/go}/gosigfuzz/gosigfuzz.o"
$CC $CFLAGS -c .clusterfuzzlite/gosigfuzz.c -o "$sigfuzz_obj"

# Scratch space on the filesystem of $OUT so that the final merge is a rename.
export work
work=$(mktemp -d "$OUT/.build.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir "$work/log" "$work/out"

# <file>:func FuzzX(f *testing.F)  -> one line per target.
targets="$work/targets"
grep -rHoE --include='*_test.go' --exclude-dir=web --exclude-dir=node_modules --exclude-dir=.git \
  '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_]+ \*testing\.F\)' . | sort > "$targets"

# Pull requests build only the targets the change can affect: the workflow lists
# their package directories (select-targets.sh) in CFL_EXTRA_FUZZ_DIRS, base64
# encoded because ClusterFuzzLite passes the variable through the environment.
# Unset (batch runs, local builds) builds every target.
if [ -n "${CFL_EXTRA_FUZZ_DIRS:-}" ]; then
  total=$(wc -l < "$targets")
  echo "$CFL_EXTRA_FUZZ_DIRS" | base64 -d > "$work/dirs"
  while IFS= read -r line; do
    dir=$(dirname "${line%%:*}")
    printf '%s|%s\n' "${dir#./}" "$line"
  done < "$targets" \
    | awk -F'|' 'FILENAME == ARGV[1] { keep[$0] = 1; next } $1 in keep { sub(/^[^|]*\|/, ""); print }' "$work/dirs" - > "$targets.sel"
  mv "$targets.sel" "$targets"
  echo "pull request: building $(wc -l < "$targets") of $total fuzz targets, the ones affected by the change"
fi

# The fuzz builder compiles the package's test files as ordinary sources, which
# fails when the directory also holds an external test package (`package x_test`).
# The fuzz targets live in the package itself, so those files are not needed here
# (this is the image's copy of the tree, not the checkout).
while IFS= read -r dir; do
  grep -lE --include='*_test.go' '^package [A-Za-z0-9_]+_test$' "$dir"/*_test.go 2>/dev/null | xargs -r rm -v
done < <(cut -d: -f1 "$targets" | xargs -n1 dirname | sort -u)

# build_target <file>:func FuzzX(f *testing.F)
# Builds one target into $work/out/<name>. A failure prints the log and exits 255,
# which makes xargs start no further job.
build_target() {
  local file=${1%%:*} decl=${1#*:}
  local dir fn pkg name out log

  # Files behind a build tag (integration tests) are not compiled without it
  # and are skipped.
  if grep -q '^//go:build' "$file"; then
    echo "skip $file: build-tagged" > "$work/log/$(echo "$file" | tr -c 'A-Za-z0-9_\n-' '_').skip"
    return 0
  fi

  dir=$(dirname "$file")
  dir=${dir#./}
  fn=${decl#func }
  fn=${fn%%(*}

  pkg="$module"
  [ "$dir" = "." ] || pkg="$module/$dir"

  name=$(echo "${dir}_${fn}" | tr -c 'A-Za-z0-9_\n-' '_')
  out="$work/out/$name"
  log="$work/log/$name.log"
  mkdir "$out"

  if ! (
    set -eu
    OUT="$out" compile_native_go_fuzzer_v2 "$pkg" "$fn" "$name"
    # The coverage build writes no .a; the fuzzing build leaves it in the cwd.
    rm -f "$name.a"

    # Dictionary: per target, else per package.
    local pkgname dict
    pkgname=$(echo "$dir" | tr -c 'A-Za-z0-9_\n-' '_')
    for dict in ".clusterfuzzlite/dicts/$name.dict" ".clusterfuzzlite/dicts/$pkgname.dict"; do
      if [ -f "$dict" ]; then
        cp "$dict" "$out/$name.dict"
        break
      fi
    done

    # Seed corpus. The tool stages the zip in $TMPDIR and renames it into $OUT,
    # so $TMPDIR must be on the same filesystem. A seed it cannot encode is
    # skipped with a message, which would silently shrink the corpus: fail.
    if [ -d "$dir/testdata/fuzz/$fn" ]; then
      mkdir "$out/.tmp"
      TMPDIR="$out/.tmp" OUT="$out" addStdLibCorpusToFuzzer -verbose \
        -dir "$dir/testdata/fuzz/$fn" -fuzzer_name "$name" | tee "$out/.tmp/seeds.log"
      if ! grep -q '^\[ZipCorpusFromGoFuzzCases\] Skipped: 0 files$' "$out/.tmp/seeds.log"; then
        echo "$dir/testdata/fuzz/$fn: seeds the fuzz builder cannot encode" >&2
        exit 1
      fi
      rm -rf "$out/.tmp"
    fi
  ) > "$log" 2>&1; then
    echo "::error::building $name failed" >&2
    cat "$log" >&2
    exit 255
  fi
  [ -e "$out/$name" ] || { echo "$name: compile produced no binary" >> "$log"; cat "$log" >&2; exit 255; }
}
export -f build_target

jobs=${FUZZ_BUILD_JOBS:-}
if [ -z "$jobs" ]; then
  jobs=$(nproc)
  # About 1.5 GiB per concurrent build (compile + link of a Go c-archive).
  mem_kb=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
  if [ -r /sys/fs/cgroup/memory.max ] && [ "$(cat /sys/fs/cgroup/memory.max)" != max ]; then
    cg_kb=$(($(cat /sys/fs/cgroup/memory.max) / 1024))
    [ "$cg_kb" -lt "$mem_kb" ] && mem_kb=$cg_kb
  fi
  mem_jobs=$((mem_kb / 1572864))
  [ "$mem_jobs" -lt "$jobs" ] && jobs=$mem_jobs
  [ "$jobs" -ge 1 ] || jobs=1
fi
echo "building $(wc -l < "$targets") fuzz targets with $jobs parallel jobs"

# One line per target; a failing job exits 255 and xargs starts no further job.
set +e
xargs -d '\n' -n1 -P "$jobs" bash -c 'build_target "$1"' _ < "$targets"
status=$?
set -e

# Logs in a stable order, whatever the scheduling was.
for f in "$work"/log/*; do
  [ -e "$f" ] && { echo "=== ${f##*/}"; cat "$f"; }
done
if [ "$status" -ne 0 ]; then
  echo "fuzz target build failed (xargs status $status)" >&2
  exit 1
fi

# Merge the per-target directories into $OUT. The coverage build keeps JSON
# registries in $OUT (which targets are native Go fuzzers, their function names
# and parameter types); every job wrote its own entry, merge them.
built=0
for dir in "$work"/out/*/; do
  built=$((built + 1))
  for f in "$dir"*; do
    case "${f##*/}" in
      *.json) ;;
      *) mv "$f" "$OUT/" ;;
    esac
  done
done
for name in $(find "$work/out" -name '*.json' -printf '%f\n' | sort -u); do
  # shellcheck disable=SC2046
  jq -S -s 'if (.[0] | type) == "array" then add | sort else add end' \
    $(find "$work/out" -name "$name" | sort) > "$OUT/$name"
done

if [ "$built" -eq 0 ]; then
  echo "no fuzz targets found" >&2
  exit 1
fi

echo "built $built fuzz targets"
