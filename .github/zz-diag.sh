#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
# Temporary diagnostic, removed before merge: runs inside the fuzz image as /src/build.sh.
cd "$SRC/openartifact"
module=$(go list -m)
$CC $CFLAGS -c .clusterfuzzlite/gosigfuzz.c -o "${GOPATH:-/root/go}/gosigfuzz/gosigfuzz.o"

cat > /tmp/tt <<'EOF'
#!/bin/bash
s=$(date +%s.%N)
"$@"
rc=$?
e=$(date +%s.%N)
echo "$(basename "$1") ${TOOLEXEC_IMPORTPATH:-none} $s $e" >> /tmp/tools.log
exit $rc
EOF
chmod +x /tmp/tt
export GOFLAGS="-toolexec=/tmp/tt"

for spec in "internal/proxy FuzzNuGetParsePath" "pkg/deb FuzzParseControlFile" \
  "internal/format/conda FuzzReadPackageIndex" "internal/format/conda FuzzParsePackageFilename" \
  "internal/sbom FuzzGenerateDocuments" "internal/proxy FuzzNuGetParsePath"; do
  set -- $spec
  dir=$1
  fn=$2
  name=$(echo "${dir}_${fn}" | tr -c 'A-Za-z0-9_\n-' '_')
  grep -lE '^package [A-Za-z0-9_]+_test$' "$dir"/*_test.go 2>/dev/null | xargs -r rm
  : > /tmp/tools.log
  t0=$(date +%s.%N)
  compile_native_go_fuzzer_v2 "$module/$dir" "$fn" "$name" > /tmp/diag-build.log 2>&1 || { tail -20 /tmp/diag-build.log; exit 1; }
  t1=$(date +%s.%N)
  rm -f "$name.a"
  awk -v t0="$t0" -v t1="$t1" -v name="$name" '
    { n[$1]++; sum[$1] += $4 - $3; if (min == "" || $3 < min) min = $3; if ($4 > max) max = $4 }
    END {
      printf "DIAG %s wall=%.1f before_go=%.1f go_span=%.1f after_go=%.1f\n", name, t1 - t0, min - t0, max - min, t1 - max
      for (k in n) printf "DIAG   tool %s n=%d sum_s=%.1f\n", k, n[k], sum[k]
    }' /tmp/tools.log
  awk '$1 == "compile" { printf "%s %.2f\n", $2, $4 - $3 }' /tmp/tools.log | sort -k2 -nr | head -4 | sed 's/^/DIAG   slowest compile /'
  ls -l "$OUT/$name" | awk '{print "DIAG   binary bytes " $5}'
done
