// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"slices"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []string
	}{
		{"empty", "", nil},
		{"only whitespace and comments", "  \n-- nothing\n/* still nothing; */\n", nil},
		{"single without terminator", "SELECT 1", []string{"SELECT 1"}},
		{"single with terminator", "SELECT 1;", []string{"SELECT 1"}},
		{"several", "SELECT 1; SELECT 2;\nSELECT 3", []string{"SELECT 1", "SELECT 2", "SELECT 3"}},
		{"empty statements dropped", ";;SELECT 1;;\n;", []string{"SELECT 1"}},
		{
			"line comment hides semicolon",
			"SELECT 1 -- a; b\n; SELECT 2",
			[]string{"SELECT 1 -- a; b", "SELECT 2"},
		},
		{
			"comment between statements stays with the next one",
			"SELECT 1;\n-- next\nSELECT 2;\n-- trailing",
			[]string{"SELECT 1", "-- next\nSELECT 2"},
		},
		{
			"nested block comment",
			"SELECT /* a; /* b; */ c; */ 1; SELECT 2",
			[]string{"SELECT /* a; /* b; */ c; */ 1", "SELECT 2"},
		},
		{
			"string literal with semicolon and doubled quote",
			"SELECT 'a;b''c;'; SELECT 2",
			[]string{"SELECT 'a;b''c;'", "SELECT 2"},
		},
		{
			"escape string with backslash quote",
			`SELECT E'a\';b'; SELECT 2`,
			[]string{`SELECT E'a\';b'`, "SELECT 2"},
		},
		{
			"plain string keeps backslash literal",
			`SELECT 'a\'; SELECT 2`,
			[]string{`SELECT 'a\'`, "SELECT 2"},
		},
		{
			"identifier ending in e is not an escape string",
			`SELECT name'a\'; SELECT 2`,
			[]string{`SELECT name'a\'`, "SELECT 2"},
		},
		{
			"quoted identifier",
			`SELECT "a;""b" FROM t; SELECT 2`,
			[]string{`SELECT "a;""b" FROM t`, "SELECT 2"},
		},
		{
			"anonymous dollar quote",
			"DO $$ BEGIN PERFORM 1; PERFORM 2; END $$; SELECT 2",
			[]string{"DO $$ BEGIN PERFORM 1; PERFORM 2; END $$", "SELECT 2"},
		},
		{
			"tagged dollar quote containing another",
			"DO $body$ BEGIN EXECUTE $$ SELECT 1; $$; END $body$; SELECT 2",
			[]string{"DO $body$ BEGIN EXECUTE $$ SELECT 1; $$; END $body$", "SELECT 2"},
		},
		{
			"positional parameter is not a dollar quote",
			"SELECT $1; SELECT $2",
			[]string{"SELECT $1", "SELECT $2"},
		},
		{
			"dollar inside identifier is not a dollar quote",
			"SELECT a$$b; SELECT 2",
			[]string{"SELECT a$$b", "SELECT 2"},
		},
		{
			"unterminated literal runs to the end",
			"SELECT 'abc; SELECT 2",
			[]string{"SELECT 'abc; SELECT 2"},
		},
		{
			"unterminated dollar quote runs to the end",
			"DO $$ BEGIN; SELECT 2",
			[]string{"DO $$ BEGIN; SELECT 2"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitStatements(c.script)
			if !slices.Equal(got, c.want) {
				t.Errorf("splitStatements(%q)\n got %q\nwant %q", c.script, got, c.want)
			}
		})
	}
}
