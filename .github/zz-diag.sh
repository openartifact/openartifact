#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
# Temporary diagnostic, removed before merge: runs inside the fuzz image as /src/build.sh.
cd "$SRC/openartifact"
module=$(go list -m)
$CC $CFLAGS -c .clusterfuzzlite/gosigfuzz.c -o "${GOPATH:-/root/go}/gosigfuzz/gosigfuzz.o"
go version
grep -lE '^package [A-Za-z0-9_]+_test$' internal/proxy/*_test.go | xargs -r rm
compile_native_go_fuzzer_v2 "$module/internal/proxy" FuzzNuGetProxyServe internal_proxy_FuzzNuGetProxyServe > /tmp/b.log 2>&1 || { tail -30 /tmp/b.log; exit 1; }
for d in $(find / -type d -path '*go-118-fuzz-build*' -name testing 2>/dev/null | head -3); do
  echo "DIAG shim $d"
  grep -n "func (t \*T) Cleanup" -A3 "$d/t.go" | sed 's/^/DIAG   /'
  grep -n "func (t \*T) Setenv" -A3 "$d/t.go" | sed 's/^/DIAG   /'
done
bin="$OUT/internal_proxy_FuzzNuGetProxyServe"
echo "DIAG run 1: crash input as is"
"$bin" .github/zz-crash 2>&1 | tail -12 | sed 's/^/DIAG   /' || true
echo "DIAG run 2: with OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true"
OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true "$bin" .github/zz-crash 2>&1 | tail -12 | sed 's/^/DIAG   /' || true
