#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Render coverage.json (summarize.sh) as GitHub-flavoured markdown: totals plus
# per-area tables, with the change against the baseline coverage.json of main
# when one is given. Used for the job summary and the sticky pull request
# comment. The coverage.json of a pull request is data from untrusted code, so
# every number goes through tonumber and area names are dropped unless they are
# plain path characters; nothing is passed through as markup.
#
# Usage: scripts/coverage/render.sh COVERAGE_JSON [BASE_JSON]

set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: $0 COVERAGE_JSON [BASE_JSON]" >&2
	exit 2
fi

base=null
if [ "$#" -eq 2 ] && [ -s "$2" ]; then
	base=$(jq -c . "$2")
fi

jq -r --argjson base "$base" '
	def one($x): ($x * 10 | round) as $n | ($n | fabs) as $a
		| (if $n < 0 then "-" else "" end) + "\($a / 10 | floor).\($a % 10)";
	def pct($x): if $x == null then "n/a" else one($x | tonumber) + "%" end;
	def delta($x; $y):
		if $y == null then "n/a"
		else ((($x | tonumber) - ($y | tonumber)) * 10 | round) as $d
			| if $d == 0 then "±0.0" elif $d > 0 then "+" + one($d / 10) else one($d / 10) end
		end;
	def row($label; $x; $y): "| \($label) | \(pct($x)) | \(delta($x; $y)) |";
	def okname: select(test("^[A-Za-z0-9_./-]+$"));
	def areas($label; $cur; $old):
		if ($cur | length) == 0 then empty else
			"<details><summary>\($label)</summary>\n",
			"| Area | Coverage | vs main |",
			"|---|---:|---:|",
			($cur | keys[] | okname as $k
				| "| `\($k)` | \(pct($cur[$k])) | \(if $old == null then "n/a" elif ($old[$k] // null) == null then "new" else delta($cur[$k]; $old[$k]) end) |"),
			"\n</details>\n"
		end;

	. as $c
	| ($base.go // null) as $bg
	| ($base.web // null) as $bw
	| "### Coverage\n",
	  "| | Coverage | vs main |",
	  "|---|---:|---:|",
	  row("Go, unit + integration"; $c.go.total; $bg.total? // null),
	  (if $c.go.unit != null then row("Go, unit tests only"; $c.go.unit; $bg.unit? // null) else empty end),
	  (if $c.go.integration != null then row("Go, integration tests only"; $c.go.integration; $bg.integration? // null) else empty end),
	  (if $c.web != null then
		row("Web, lines"; $c.web.lines; $bw.lines? // null),
		row("Web, functions"; $c.web.functions; $bw.functions? // null)
	   else empty end),
	  "",
	  (if $base == null then "_No baseline from `main` available yet; deltas appear once a `main` run has stored one._\n" else empty end),
	  areas("Go by area"; $c.go.areas // {}; $bg.areas? // null),
	  (if $c.web != null then areas("Web by area (lines)"; $c.web.areas // {}; $bw.areas? // null) else empty end)
' "$1"
