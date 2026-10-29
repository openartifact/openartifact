#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Test of select-targets.sh on a throwaway module: selection by changed package
# and reverse dependency, ranking and the FUZZ_MAX_TARGETS cap, go.mod handling.
# Run from anywhere: .clusterfuzzlite/select-targets_test.sh (CI job `go`).

script=$(cd "$(dirname "$0")" && pwd)/select-targets.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"

mkdir -p leaf mid top other docs
cat > go.mod << 'MOD'
module example.test/sel

go 1.22
MOD
echo 'package leaf' > leaf/leaf.go
printf 'package mid\n\nimport _ "example.test/sel/leaf"\n' > mid/mid.go
printf 'package top\n\nimport _ "example.test/sel/mid"\n' > top/top.go
# other imports a module that go.mod may bump; it is not downloaded here (go list -e).
printf 'package other\n\nimport _ "example.test/dep/sub"\n' > other/other.go
echo 'docs' > docs/readme.md

fuzz() { # <dir> <package> <target>...
  local dir=$1 pkg=$2
  shift 2
  {
    printf 'package %s\n\nimport "testing"\n' "$pkg"
    for t in "$@"; do printf '\nfunc %s(f *testing.F) { f.Fuzz(func(t *testing.T, b []byte) {}) }\n' "$t"; done
  } > "$dir/fuzz_test.go"
}
fuzz leaf leaf FuzzLeafA FuzzLeafB
fuzz mid mid FuzzMid
fuzz top top FuzzTop
fuzz other other FuzzOther

fail=0
check() { # <name> <want> <got>
  if [ "$2" = "$3" ]; then
    echo "ok   $1"
  else
    echo "FAIL $1"
    echo "  want: $2"
    echo "  got:  $3"
    fail=1
  fi
}

# sel <changed file>... : the selection on one line (the environment passes through).
sel() {
  local f
  for f in "$@"; do printf '%s\0' "$f"; done | bash "$script" 2> /dev/null | tr '\n' ' ' | sed 's/ $//'
}

diffs=$tmp/diffs
mkdir "$diffs"
cat > "$diffs/bump-dep" << 'DIFF'
--- a/go.mod
+++ b/go.mod
@@ -5,1 +5,1 @@
-	example.test/dep v1.0.0
+	example.test/dep v1.1.0
--- a/go.sum
+++ b/go.sum
@@ -0,0 +1,2 @@
+example.test/dep v1.1.0 h1:aaa=
+example.test/dep v1.1.0/go.mod h1:bbb=
DIFF
cat > "$diffs/bump-require-line" << 'DIFF'
--- a/go.mod
+++ b/go.mod
@@ -5,1 +5,1 @@
-require example.test/dep v1.0.0 // indirect
+require example.test/dep v1.1.0 // indirect
DIFF
cat > "$diffs/bump-go" << 'DIFF'
--- a/go.mod
+++ b/go.mod
@@ -3,1 +3,1 @@
-go 1.22
+go 1.23
DIFF
cat > "$diffs/bump-unrelated" << 'DIFF'
--- a/go.sum
+++ b/go.sum
@@ -0,0 +1,1 @@
+example.test/unrelated v1.0.0 h1:ccc=
DIFF

check "no Go change" "none" "$(sel docs/readme.md)"
check "leaf: itself, then its importers" \
  "leaf_FuzzLeafA leaf_FuzzLeafB mid_FuzzMid top_FuzzTop" "$(sel leaf/leaf.go)"
check "mid: itself and its importer, not what it imports" \
  "mid_FuzzMid top_FuzzTop" "$(sel mid/mid.go)"
check "top: only itself" "top_FuzzTop" "$(sel top/top.go)"
check "non-Go file counts for the closest package" \
  "top_FuzzTop" "$(sel top/testdata/fuzz/FuzzTop/seed)"

check "cap: the changed package first, importers after" \
  "leaf_FuzzLeafA leaf_FuzzLeafB" "$(FUZZ_MAX_TARGETS=2 sel leaf/leaf.go)"
check "cap: the nearest importer before a farther one" \
  "leaf_FuzzLeafA leaf_FuzzLeafB mid_FuzzMid" "$(FUZZ_MAX_TARGETS=3 sel leaf/leaf.go)"
check "cap 0 means no limit" \
  "leaf_FuzzLeafA leaf_FuzzLeafB mid_FuzzMid top_FuzzTop" "$(FUZZ_MAX_TARGETS=0 sel leaf/leaf.go)"
check "cap: the changed package outranks its importers" \
  "mid_FuzzMid" "$(FUZZ_MAX_TARGETS=1 sel mid/mid.go)"

check "go.mod without a module list: every target, capped to a spread" \
  "leaf_FuzzLeafA mid_FuzzMid other_FuzzOther top_FuzzTop" "$(FUZZ_MAX_TARGETS=4 sel go.mod go.sum)"
check "fuzzing setup: every target" \
  "leaf_FuzzLeafA leaf_FuzzLeafB mid_FuzzMid other_FuzzOther top_FuzzTop" "$(FUZZ_MAX_TARGETS=0 sel .clusterfuzzlite/build.sh)"
check "bumped module: only the targets linking it" \
  "other_FuzzOther" "$(GOMOD_DIFF=$diffs/bump-dep sel go.mod go.sum)"
check "bumped module in a require line" \
  "other_FuzzOther" "$(GOMOD_DIFF=$diffs/bump-require-line sel go.mod)"
check "go directive: every target" \
  "leaf_FuzzLeafA leaf_FuzzLeafB mid_FuzzMid other_FuzzOther top_FuzzTop" "$(FUZZ_MAX_TARGETS=0 GOMOD_DIFF=$diffs/bump-go sel go.mod)"
check "module nobody links: nothing" \
  "none" "$(GOMOD_DIFF=$diffs/bump-unrelated sel go.sum)"

exit "$fail"
