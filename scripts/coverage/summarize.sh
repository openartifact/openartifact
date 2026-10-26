#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Turn Go and web coverage data into coverage.json (totals and per-area
# numbers; the input of render.sh and gate.sh) plus self-contained HTML reports.
#
# Usage: scripts/coverage/summarize.sh --out DIR --go-merged FILE
#          [--go-unit FILE] [--go-integration FILE] [--web-lcov FILE]
#
# Go profiles must already be filtered (merge-go.sh does that, also for a single
# profile). Writes DIR/coverage.json, DIR/go.html (annotated sources, needs the
# module checkout and a Go toolchain) and DIR/web.html (per-file table).
#
# An "area" is the first two path elements of a Go package (internal/worker,
# cmd/server; internal/format/deb folds into internal/format) or of a web
# source file (src/pages, src/lib).

set -euo pipefail

out="" merged="" unit="" integ="" lcov=""
while [ "$#" -gt 0 ]; do
	case $1 in
	--out) out=$2 ;;
	--go-merged) merged=$2 ;;
	--go-unit) unit=$2 ;;
	--go-integration) integ=$2 ;;
	--web-lcov) lcov=$2 ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
	shift 2
done
if [ -z "$out" ] || [ -z "$merged" ]; then
	echo "usage: $0 --out DIR --go-merged FILE [--go-unit FILE] [--go-integration FILE] [--web-lcov FILE]" >&2
	exit 2
fi

cd "$(dirname "$0")/../.."
mkdir -p "$out"

module=$(awk '$1 == "module" { print $2; exit }' go.mod)

# go_stats PROFILE: "area statements covered" per area. A statement counts as
# covered when any run executed its block.
go_stats() {
	awk -v mod="$module/" '
		NR == 1 { next }
		{
			file = $1; sub(/:.*/, "", file)
			sub("^" mod, "", file)
			n = split(file, p, "/")
			area = (n >= 3) ? p[1] "/" p[2] : (n == 2 ? p[1] : ".")
			key = $1 " " $2
			if (!(key in seen)) { seen[key] = 1; total[area] += $2 }
			if ($3 > 0 && !(key in hit)) { hit[key] = 1; cov[area] += $2 }
		}
		END { for (a in total) printf "%s\t%d\t%d\n", a, total[a], cov[a] }
	' "$1"
}

# web_stats LCOV: "area lines_found lines_hit funcs_found funcs_hit" per area.
# Test files, the test harness and the generated API client are not product code.
web_stats() {
	awk '
		/^SF:/ { f = substr($0, 4); skip = (f ~ /(^|\/)__tests__\// || f ~ /^test\// || f ~ /^src\/api\/generated\// || f ~ /\.(test|spec)\.[tj]s$/); next }
		skip { next }
		/^FNF:/ { fnf = substr($0, 5) + 0 }
		/^FNH:/ { fnh = substr($0, 5) + 0 }
		/^LF:/ { lf = substr($0, 4) + 0 }
		/^LH:/ { lh = substr($0, 4) + 0 }
		/^end_of_record/ {
			n = split(f, p, "/")
			area = (n >= 3) ? p[1] "/" p[2] : (n == 2 ? p[1] : ".")
			printf "%s\t%s\t%d\t%d\t%d\t%d\n", f, area, lf, lh, fnf, fnh
			fnf = fnh = lf = lh = 0
		}
	' "$1"
}

go_json() { # PROFILE -> {statements, covered, pct, areas{}}
	go_stats "$1" | jq -Rn '
		[inputs | split("\t") | {a: .[0], s: (.[1] | tonumber), c: (.[2] | tonumber)}] as $r
		| def pct(c; s): if s == 0 then 0 else (c * 10000 / s | round) / 100 end;
		{
			statements: ($r | map(.s) | add // 0),
			covered: ($r | map(.c) | add // 0)
		}
		| .pct = pct(.covered; .statements)
		| .areas = ($r | map({key: .a, value: {statements: .s, covered: .c, pct: pct(.c; .s)}}) | from_entries)'
}

go_all=$(go_json "$merged")
go_unit=null go_integ=null
[ -n "$unit" ] && go_unit=$(go_json "$unit" | jq -c 'del(.areas)')
[ -n "$integ" ] && go_integ=$(go_json "$integ" | jq -c 'del(.areas)')

web=null
if [ -n "$lcov" ]; then
	web_tsv=$(web_stats "$lcov")
	web=$(printf '%s\n' "$web_tsv" | jq -Rn '
		[inputs | select(length > 0) | split("\t") | {a: .[1], lf: (.[2] | tonumber), lh: (.[3] | tonumber), ff: (.[4] | tonumber), fh: (.[5] | tonumber)}] as $r
		| def pct(c; s): if s == 0 then 0 else (c * 10000 / s | round) / 100 end;
		{
			lines: pct($r | map(.lh) | add // 0; $r | map(.lf) | add // 0),
			functions: pct($r | map(.fh) | add // 0; $r | map(.ff) | add // 0),
			areas: ($r | group_by(.a) | map({
				key: .[0].a,
				value: {
					lines: pct(map(.lh) | add; map(.lf) | add),
					functions: pct(map(.fh) | add; map(.ff) | add)
				}
			}) | from_entries)
		}')
fi

jq -n --argjson all "$go_all" --argjson unit "$go_unit" --argjson integ "$go_integ" --argjson web "$web" '
	{go: {total: $all.pct, statements: $all.statements, covered: $all.covered,
	      unit: ($unit | if . == null then null else .pct end),
	      integration: ($integ | if . == null then null else .pct end),
	      areas: ($all.areas | map_values(.pct))},
	 web: (if $web == null then null else {lines: $web.lines, functions: $web.functions,
	       areas: ($web.areas | map_values(.lines))} end)}' >"$out/coverage.json"

go tool cover -html="$merged" -o "$out/go.html"

if [ -n "$lcov" ]; then
	printf '%s\n' "$web_tsv" | awk -F'\t' '
		function esc(s) { gsub(/&/, "\\&amp;", s); gsub(/</, "\\&lt;", s); gsub(/>/, "\\&gt;", s); return s }
		function pct(h, f) { return f == 0 ? "-" : sprintf("%.1f%%", h * 100 / f) }
		BEGIN {
			print "<!doctype html><meta charset=\"utf-8\"><title>OpenArtifact web coverage</title>"
			print "<style>body{font:14px system-ui,sans-serif;margin:2rem}table{border-collapse:collapse}th,td{padding:.2rem .8rem;border-bottom:1px solid #ccc;text-align:right}td:first-child,th:first-child{text-align:left;font-family:monospace}</style>"
			print "<h1>Web coverage (bun test)</h1><table><tr><th>File</th><th>Lines</th><th>Hit/found</th><th>Functions</th></tr>"
		}
		NF >= 6 { printf "<tr><td>%s</td><td>%s</td><td>%d/%d</td><td>%s</td></tr>\n", esc($1), pct($4, $3), $4, $3, pct($6, $5) }
		END { print "</table>" }
	' >"$out/web.html"
fi
