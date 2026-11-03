// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// fakeDebStore is a repository's deb files in memory, counting what the cache
// reads from it.
type fakeDebStore struct {
	files []models.DebPackageStamp
	// controlReads are the ids whose controls were read, in order.
	controlReads []string
}

func (s *fakeDebStore) ListDebPackageStamps(context.Context, string) ([]models.DebPackageStamp, error) {
	out := slices.Clone(s.files)
	for i := range out {
		out[i].ControlFields = nil
	}

	return out, nil
}

func (s *fakeDebStore) ListDebPackagesWithControls(_ context.Context, _ string, ids []string) ([]models.DebPackageStamp, error) {
	var out []models.DebPackageStamp

	for _, f := range s.files {
		if slices.Contains(ids, f.ID) {
			s.controlReads = append(s.controlReads, f.ID)
			out = append(out, f)
		}
	}

	return out, nil
}

func debStamp(id, name, version, description string) models.DebPackageStamp {
	return models.DebPackageStamp{
		ID: id, Name: name, Version: version, Arch: "amd64", Filename: name + "_" + version + "_amd64.deb",
		DistributionID: "d1", Distribution: "bookworm", ComponentName: "main", SHA256: id, Size: 10,
		ControlFields: map[string]string{"Description": description},
		ControlRev:    1,
	}
}

func stanzaTexts(files []debFile) string {
	var b strings.Builder

	for _, f := range files {
		b.WriteString(f.text)
	}

	return b.String()
}

func TestDebStanzaCacheReadsOnlyWhatChanged(t *testing.T) {
	ctx := context.Background()
	c := newDebStanzaCache(1 << 20)
	db := &fakeDebStore{files: []models.DebPackageStamp{
		debStamp("1", "alpha", "1.0", "first"),
		debStamp("2", "beta", "1.0", "second"),
	}}

	first, err := c.files(ctx, db, "repo")
	if err != nil {
		t.Fatal(err)
	}

	if len(first) != 2 || !strings.Contains(first[0].text, "Package: alpha") || !strings.Contains(first[1].text, "Description: second") {
		t.Fatalf("first listing = %q", stanzaTexts(first))
	}

	// Nothing changed: the same stanzas, no control read.
	db.controlReads = nil

	again, err := c.files(ctx, db, "repo")
	if err != nil {
		t.Fatal(err)
	}

	if stanzaTexts(again) != stanzaTexts(first) || len(db.controlReads) != 0 {
		t.Errorf("unchanged listing differs or read controls %v", db.controlReads)
	}

	// An upload is read, the rest is not.
	db.files = append(db.files, debStamp("3", "gamma", "1.0", "third"))

	added, err := c.files(ctx, db, "repo")
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 3 || !strings.Contains(added[2].text, "Package: gamma") {
		t.Fatalf("after an upload: %q", stanzaTexts(added))
	}

	if !slices.Equal(db.controlReads, []string{"3"}) {
		t.Errorf("an upload read the controls of %v, want only the new file", db.controlReads)
	}

	// A file whose control row changed is rendered again, and one deleted is gone.
	db.controlReads = nil
	db.files[0].ControlRev = 2
	db.files[0].ControlFields = map[string]string{"Description": "rewritten"}
	db.files = slices.Delete(db.files, 1, 2)

	changed, err := c.files(ctx, db, "repo")
	if err != nil {
		t.Fatal(err)
	}

	if len(changed) != 2 || !strings.Contains(changed[0].text, "Description: rewritten") || strings.Contains(stanzaTexts(changed), "beta") {
		t.Errorf("after a change and a delete: %q", stanzaTexts(changed))
	}

	if !slices.Equal(db.controlReads, []string{"1"}) {
		t.Errorf("a changed control row read %v, want only that file", db.controlReads)
	}
}

func TestDebStanzaCacheDoesNotMixRepositories(t *testing.T) {
	ctx := context.Background()
	c := newDebStanzaCache(1 << 20)
	a := &fakeDebStore{files: []models.DebPackageStamp{debStamp("1", "alpha", "1.0", "a")}}
	b := &fakeDebStore{files: []models.DebPackageStamp{debStamp("1", "beta", "2.0", "b")}}

	if _, err := c.files(ctx, a, "repo-a"); err != nil {
		t.Fatal(err)
	}

	got, err := c.files(ctx, b, "repo-b")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || !strings.Contains(got[0].text, "Package: beta") {
		t.Errorf("a file id shared by two repositories rendered %q", stanzaTexts(got))
	}
}

func TestDebStanzaCacheStaysWithinItsLimit(t *testing.T) {
	ctx := context.Background()
	c := newDebStanzaCache(4 << 10)

	for i := range 40 {
		db := &fakeDebStore{}
		for j := range 5 {
			db.files = append(db.files, debStamp(strconv.Itoa(j), fmt.Sprintf("pkg%d", j), "1.0", strings.Repeat("x", 100)))
		}

		got, err := c.files(ctx, db, fmt.Sprintf("repo-%d", i))
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 5 {
			t.Fatalf("repo %d listed %d files, want 5", i, len(got))
		}
	}

	if c.size > c.limit {
		t.Errorf("cache holds %d bytes over its limit of %d", c.size, c.limit)
	}

	if len(c.repos) == 0 || len(c.repos) == 40 {
		t.Errorf("cache holds %d of 40 repositories, want some evicted and the recent kept", len(c.repos))
	}

	// A repository too large for the cache is served but not kept.
	big := &fakeDebStore{}
	for j := range 100 {
		big.files = append(big.files, debStamp(strconv.Itoa(j), fmt.Sprintf("big%d", j), "1.0", strings.Repeat("y", 200)))
	}

	got, err := c.files(ctx, big, "big")
	if err != nil || len(got) != 100 {
		t.Fatalf("big repository: %d files, %v", len(got), err)
	}

	if _, kept := c.repos["big"]; kept || c.size > c.limit {
		t.Errorf("an oversized repository was kept (size %d, limit %d)", c.size, c.limit)
	}
}
