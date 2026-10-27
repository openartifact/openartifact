// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/pkg/pacman"
)

// upstreamPacman is a proxy member's section database for x86_64.
func upstreamPacman(t *testing.T, repoID, section string, entries ...pacman.DBEntry) fakeUpstream {
	t.Helper()

	var db bytes.Buffer
	if err := pacman.WriteDB(&db, entries, false); err != nil {
		t.Fatal(err)
	}

	return fakeUpstream{repoID + "|os/x86_64/" + section + ".db": db.Bytes()}
}

func upstreamEntry(name, version, sig string) pacman.DBEntry {
	return pacman.DBEntry{
		Name: name, Base: name, Version: version, Arch: "x86_64", Filename: pacman.PackageFileName(name, version, "x86_64", "zst"),
		SHA256: "ab" + name, CSize: 10, ISize: 20, PGPSig: sig,
	}
}

func readGroupDB(t *testing.T, md map[string][]byte) map[string]pacman.DBEntry {
	t.Helper()

	entries, err := pacman.ReadDB(bytes.NewReader(md["os/x86_64/db.tar.gz"]))
	if err != nil {
		t.Fatalf("group database: %v", err)
	}

	out := make(map[string]pacman.DBEntry, len(entries))
	for _, e := range entries {
		out[e.Name] = e
	}

	return out
}

func (f *groupFixture) pacmanPackage(repo *models.Repository, name, version string) *models.PacmanPackage {
	f.t.Helper()

	return storePacmanPackage(f.t, f.pg, f.blobs, repo.ID, name, version, name+version)
}

//nolint:gocyclo,cyclop // one scenario: merge, owners, signature, hosted wins, stale hook, unknown arch
func TestGroupPacmanMergesHostedAndProxy(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hosted := f.repo("arch-a", "pacman", "hosted")
	up := f.repo("extra", "pacman", "proxy")
	grp := f.repo("arch-group", "pacman", "group", up.ID, hosted.ID) // the proxy comes first

	mine := f.pacmanPackage(hosted, "tool", "1-1")
	f.pacmanPackage(hosted, "mine", "2-1")

	opener := upstreamPacman(t, up.ID, "extra",
		upstreamEntry("tool", "9-1", "UPSTREAMSIG"), // hosted wins despite the order and the newer version
		upstreamEntry("vim", "9.0-1", "VIMSIG"),
	)

	signer, entity := pacmanSigner(t)

	w := NewGroupMetadataWorker(f.pg, f.blobs, opener, nil, fixedResolver{signer}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "x86_64", nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	md := f.metadata(grp.ID)

	for _, path := range []string{"os/x86_64/db.tar.gz", "os/x86_64/db.tar.gz.sig", "os/x86_64/files.tar.gz", "os/x86_64/files.tar.gz.sig", "public.key"} {
		if _, ok := md[path]; !ok {
			t.Errorf("missing %s; have %v", path, keys(md))
		}
	}

	for _, name := range []string{"db", "files"} {
		if _, err := openpgp.CheckDetachedSignature(openpgp.EntityList{entity},
			bytes.NewReader(md["os/x86_64/"+name+".tar.gz"]), bytes.NewReader(md["os/x86_64/"+name+".tar.gz.sig"]), nil); err != nil {
			t.Errorf("%s signature does not verify with the group key: %v", name, err)
		}
	}

	got := readGroupDB(t, md)
	if len(got) != 3 || got["tool"].Version != "1-1" || got["mine"].Version != "2-1" || got["vim"].Version != "9.0-1" {
		t.Fatalf("merged database = %+v, want tool 1-1 (hosted wins), mine, vim", got)
	}

	if got["vim"].PGPSig != "VIMSIG" {
		t.Errorf("upstream signature of vim = %q, want it kept", got["vim"].PGPSig)
	}

	if f.owner(grp.ID, "os/x86_64/"+mine.Filename) != hosted.ID {
		t.Error("tool must be owned by the hosted member")
	}

	if f.owner(grp.ID, "os/x86_64/"+got["vim"].Filename) != up.ID {
		t.Error("vim must be owned by the proxy member")
	}

	if st, err := f.pg.GetGroupMetadataState(ctx, grp.ID); err != nil || st.Stale || !slices.Contains(st.Dists, "x86_64") {
		t.Errorf("state = %+v, %v; want fresh with the architecture remembered", st, err)
	}

	// An architecture nobody has is not published.
	if err := w.Generate(ctx, grp.ID, "aarch64", nil); !errors.Is(err, group.ErrDistNotFound) {
		t.Errorf("Generate(unknown arch) = %v, want ErrDistNotFound", err)
	}

	if err := w.Generate(ctx, grp.ID, "../x", nil); !errors.Is(err, group.ErrDistNotFound) {
		t.Errorf("Generate(invalid arch) = %v, want ErrDistNotFound", err)
	}

	// The hosted worker's stale hook reaches the group worker: a change of a
	// member is what marks the group stale.
	var notified []string

	pw := NewPacmanMetadataWorker(f.pg, f.blobs, fixedResolver{}, testLogger(t)).
		WithStaleHook(func(id string) { notified = append(notified, id) })
	t.Cleanup(pw.Stop)

	pw.NotifyStale(hosted.ID)

	if !slices.Equal(notified, []string{hosted.ID}) {
		t.Errorf("stale hook calls = %v, want the hosted member once", notified)
	}

	if groups, err := f.pg.ListMetadataGroupsOfMember(ctx, hosted.ID); err != nil || !slices.Equal(groups, []string{grp.ID}) {
		t.Errorf("groups of the member = %v, %v; want the pacman group", groups, err)
	}
}

func TestGroupPacmanMergeVersionsPicksNewest(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hosted := f.repo("arch-a", "pacman", "hosted")
	up := f.repo("core", "pacman", "proxy")
	grp := f.repo("arch-group", "pacman", "group", hosted.ID, up.ID)

	f.pacmanPackage(hosted, "tool", "1-1")

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE repositories SET merge_proxy_versions = true WHERE id = $1`, grp.ID); err != nil {
		t.Fatal(err)
	}

	w := NewGroupMetadataWorker(
		f.pg,
		f.blobs,
		upstreamPacman(t, up.ID, "core", upstreamEntry("tool", "9-1", "")),
		nil,
		fixedResolver{},
		testLogger(t),
	)
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "x86_64", nil); err != nil {
		t.Fatal(err)
	}

	md := f.metadata(grp.ID)
	if got := readGroupDB(t, md); got["tool"].Version != "9-1" {
		t.Errorf("tool = %+v, want the newest (9-1) with merge_proxy_versions", got["tool"])
	}

	if _, ok := md["os/x86_64/db.tar.gz.sig"]; ok {
		t.Error("an unsigned group publishes no signature")
	}
}
