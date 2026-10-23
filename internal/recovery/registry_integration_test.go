// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"context"
	"database/sql"
	"slices"
	"testing"
)

func queryStrings(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}

		out = append(out, s)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

// checkParentOrder asserts every foreign key between journaled tables points to an earlier table.
func checkParentOrder(t *testing.T, db *sql.DB) {
	t.Helper()

	order := map[string]int{}
	for i, tbl := range Tables {
		order[tbl.Name] = i
	}

	rows, err := db.QueryContext(context.Background(), `
		SELECT conrelid::regclass::text, confrelid::regclass::text FROM pg_constraint
		WHERE contype = 'f' AND connamespace = current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			t.Fatal(err)
		}

		ci, cok := order[child]
		pi, pok := order[parent]

		if cok && pok && child != parent && pi > ci {
			t.Errorf("%s references %s but is inserted before it", child, parent)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestRegistryCoversSchema fails when a table that references blobs or
// repositories (a package table, by that definition) is neither journaled nor
// listed in NotJournaled with a reason, and checks that the registry matches
// the live schema.
func TestRegistryCoversSchema(t *testing.T) {
	db := newTestDB(t)

	t.Run("every blob or repository referencing table is classified", func(t *testing.T) {
		referencing := queryStrings(t, db, `
			SELECT DISTINCT c.conrelid::regclass::text FROM pg_constraint c
			WHERE c.contype = 'f' AND c.confrelid IN ('blobs'::regclass, 'repositories'::regclass)
			  AND c.connamespace = current_schema()::regnamespace`)

		for _, name := range referencing {
			_, journaled := Lookup(name)
			_, exempt := NotJournaled[name]

			if !journaled && !exempt {
				t.Errorf("table %s references blobs/repositories but is in neither recovery.Tables nor recovery.NotJournaled "+
					"(add it to internal/recovery/tables.go and migration 068's trigger list, or exempt it with a reason)", name)
			}
		}
	})

	t.Run("exempt tables exist", func(t *testing.T) {
		all := queryStrings(t, db, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`)

		for name := range NotJournaled {
			if !slices.Contains(all, name) {
				t.Errorf("NotJournaled lists %s, which does not exist", name)
			}
		}
	})

	t.Run("registry columns exist and generated ones are excluded", func(t *testing.T) {
		for i := range Tables {
			tbl := &Tables[i]
			cols := queryStrings(t, db, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`, tbl.Name)

			if len(cols) == 0 {
				t.Errorf("table %s does not exist", tbl.Name)

				continue
			}

			for _, c := range slices.Concat(tbl.Exclude, tbl.BlobRefs, keyNames(tbl)) {
				if !slices.Contains(cols, c) {
					t.Errorf("%s: column %s does not exist", tbl.Name, c)
				}
			}

			for _, g := range queryStrings(t, db, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND is_generated <> 'NEVER'`, tbl.Name) {
				if !tbl.isExcluded(g) {
					t.Errorf("%s: generated column %s must be excluded", tbl.Name, g)
				}
			}

			pk := queryStrings(t, db, `
				SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
				WHERE i.indisprimary AND i.indrelid = $1::regclass ORDER BY a.attname`, tbl.Name)

			names := keyNames(tbl)
			slices.Sort(names)

			if !slices.Equal(pk, names) {
				t.Errorf("%s: registry key %v, primary key %v", tbl.Name, names, pk)
			}
		}
	})

	t.Run("parents come before children", func(t *testing.T) { checkParentOrder(t, db) })

	t.Run("every journaled table has the journal trigger", func(t *testing.T) {
		have := queryStrings(t, db, `SELECT c.relname FROM pg_trigger g JOIN pg_class c ON c.oid = g.tgrelid WHERE g.tgname = 'recovery_journal' AND NOT g.tgisinternal`)

		for _, tbl := range Tables {
			if !slices.Contains(have, tbl.Name) {
				t.Errorf("table %s has no recovery_journal trigger (migration 068)", tbl.Name)
			}
		}

		if len(have) != len(Tables) {
			t.Errorf("%d tables have the trigger, registry has %d", len(have), len(Tables))
		}
	})
}

func keyNames(t *Table) []string {
	out := make([]string, len(t.Key))
	for i, k := range t.Key {
		out[i] = k.Name
	}

	return out
}
