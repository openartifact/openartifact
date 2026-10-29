// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
)

func engClock(t *testing.T, s *engSite) (int64, int64) {
	t.Helper()

	var wall, counter int64

	err := s.db.QueryRowContext(context.Background(),
		`SELECT hlc_wall, hlc_counter FROM federation_repos WHERE repo_id = $1`, s.repoID).Scan(&wall, &counter)
	if err != nil {
		t.Fatalf("%s: read the repository clock: %v", s.name, err)
	}

	return wall, counter
}

// A peer's stamp is applied to the repository clock, which never goes back:
// a far-future wall or a counter near the integer limit would stay for good.
func TestEnginePeerStampsCannotPoisonTheRepositoryClock(t *testing.T) {
	tests := []struct {
		name     string
		stamp    func(origin uuid.UUID) Stamp
		inUnit   bool // the log page is honest and the unit answer carries the stamp
		accepted bool
	}{
		{"a year ahead", func(o uuid.UUID) Stamp {
			return Stamp{Wall: time.Now().Add(365 * 24 * time.Hour).UnixMicro(), Origin: o}
		}, false, false},
		{"the largest wall", func(o uuid.UUID) Stamp { return Stamp{Wall: math.MaxInt64, Origin: o} }, false, false},
		{"counter at the int32 limit", func(o uuid.UUID) Stamp {
			return Stamp{Wall: time.Now().UnixMicro(), Counter: math.MaxInt32, Origin: o}
		}, false, false},
		{"negative counter", func(o uuid.UUID) Stamp {
			return Stamp{Wall: time.Now().UnixMicro(), Counter: -5, Origin: o}
		}, false, false},
		{"a year ahead in the unit answer only", func(o uuid.UUID) Stamp {
			return Stamp{Wall: time.Now().Add(365 * 24 * time.Hour).UnixMicro(), Origin: o}
		}, true, false},
		{"a minute ahead is within the skew", func(o uuid.UUID) Stamp {
			return Stamp{Wall: time.Now().Add(time.Minute).UnixMicro(), Counter: 4, Origin: o}
		}, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

			a.put(t, "x", "payload")

			stamp := tc.stamp(a.instance(t))

			if tc.inUnit {
				a.setIntercept(engRewrite(engUnitPath, func(_ *http.Request, doc map[string]any) {
					entry, _ := doc["entry"].(map[string]any)
					entry["stamp"] = map[string]any{"wall": stamp.Wall, "counter": stamp.Counter, "origin": stamp.Origin.String()}
				}))
			} else {
				a.setStamp(t, "x", stamp)
			}

			wallBefore, counterBefore := engClock(t, b)

			peer := b.addPeer(t, a)
			b.mustPull(t, peer) // a rejected stamp never fails the pull

			wall, counter := engClock(t, b)

			if tc.accepted {
				b.mustRead(t, "x", "payload")
				engEqual(t, "deferred units", len(b.deferred(t, peer)), 0)

				return
			}

			engEqual(t, "repository clock wall", wall, wallBefore)
			engEqual(t, "repository clock counter", counter, counterBefore)
			b.mustBeAbsent(t, "x")
			b.mustHaveNoBlob(t, "payload")

			d := b.deferred(t, peer)
			if len(d) != 1 || d[0].UnitKey != "x" || d[0].Reason != "clock_skew" || d[0].LastError != "clock_skew" {
				t.Fatalf("deferred = %+v, want x parked as clock_skew", d)
			}

			// The clock of b is still usable: a local write carries the real time.
			b.put(t, "local", "mine")

			if got := b.entry(t, "local").Stamp; got.Wall > time.Now().Add(time.Minute).UnixMicro() || got.Counter > 10 {
				t.Errorf("local stamp after the rejected one = %+v, want a current one", got)
			}
		})
	}
}

// A change set aside for its clock is applied once the stamp is no longer
// ahead (here: the skew allowed is raised, standing for time passing).
func TestEngineClockSkewDeferralIsRetriedAndApplies(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "x", "payload")
	a.setStamp(t, "x", Stamp{Wall: time.Now().Add(time.Hour).UnixMicro(), Origin: a.instance(t)})

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)
	b.mustBeAbsent(t, "x")

	b.opts.MaxClockSkew = 2 * time.Hour
	b.newEngine(t)
	b.makeDue(t, peer)
	b.mustPull(t, peer)

	b.mustRead(t, "x", "payload")
	engEqual(t, "deferred after the retry", len(b.deferred(t, peer)), 0)
}

// engLieAboutBlob rewrites the size the unit declares for its blob.
func engLieAboutBlob(unit string, size int64) engIntercept {
	return engRewrite(engUnitPath, func(r *http.Request, doc map[string]any) {
		if r.URL.Query().Get("key") != unit {
			return
		}

		content, _ := doc["content"].(map[string]any)

		blobs, _ := content["blobs"].([]any)
		for _, b := range blobs {
			b.(map[string]any)["size"] = size
		}
	})
}

func TestEngineBlobSizeDeclaredByThePeerIsEnforced(t *testing.T) {
	tests := []struct {
		name   string
		size   int64
		reason string
	}{
		{"zero for a blob that has bytes", 0, "invalid_blob"},
		{"negative", -3, "invalid_blob"},
		{"smaller than the bytes", 3, "digest_mismatch"},
		{"larger than the bytes", 100, "digest_mismatch"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

			a.put(t, "x", "payload")
			a.setIntercept(engLieAboutBlob("x", tc.size))

			peer := b.addPeer(t, a)
			b.mustPull(t, peer)

			b.mustBeAbsent(t, "x")
			b.mustHaveNoBlob(t, "payload")
			b.wantQuota(t, 0, 0)

			if n := b.scalar(t, `SELECT count(*) FROM blobs WHERE sha256 = $1`, engSHA("payload")); n != 0 {
				t.Errorf("blobs rows for the rejected blob = %d", n)
			}

			d := b.deferred(t, peer)
			if len(d) != 1 || d[0].Reason != tc.reason || d[0].LastError != "blob_rejected" {
				t.Fatalf("deferred = %+v, want x parked as %s with the blob_rejected reason", d, tc.reason)
			}
		})
	}
}

func TestEngineBlobAboveTheLimitIsNeverFetched(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	b.opts.MaxBlobBytes = 10
	b.newEngine(t)

	a.put(t, "small", "tiny")
	a.put(t, "big", "this is longer than ten bytes")

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	b.mustRead(t, "small", "tiny")
	b.mustBeAbsent(t, "big")
	engEqual(t, "blob requests for big", a.count(engBlobPath, func(q url.Values) bool { return q.Get("unit") == "big" }), 0)

	d := b.deferred(t, peer)
	if len(d) != 1 || d[0].UnitKey != "big" || d[0].Reason != "blob_too_large" || d[0].LastError != "blob_rejected" {
		t.Fatalf("deferred = %+v, want big parked as blob_too_large", d)
	}
}

func TestEngineQuotaIsCheckedBeforeTheBlobIsFetched(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	b.exec(t, `INSERT INTO storage_quotas (repository_id, max_size_bytes, max_artifact_count) VALUES ($1, 5, 0)`, b.repoID)

	a.put(t, "u1", "aaaa")
	a.put(t, "u2", "bbbbbbbb")

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	b.mustRead(t, "u1", "aaaa")
	b.mustBeAbsent(t, "u2")
	b.wantQuota(t, 4, 1)
	engEqual(t, "blob requests for u2", a.count(engBlobPath, func(q url.Values) bool { return q.Get("unit") == "u2" }), 0)

	d := b.deferred(t, peer)
	if len(d) != 1 || d[0].Reason != "quota_exceeded" || d[0].LastError != "quota_exceeded" {
		t.Fatalf("deferred = %+v, want u2 parked as quota_exceeded", d)
	}
}

// The recorded size is the number of bytes that were received and hashed.
func TestEngineRecordsTheVerifiedBlobSize(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")

	a.put(t, "x", "payload")

	b.mustPull(t, b.addPeer(t, a))

	if got := b.scalar(t, `SELECT size_bytes FROM blobs WHERE sha256 = $1`, engSHA("payload")); got != int64(len("payload")) {
		t.Errorf("blobs.size_bytes = %d, want %d", got, len("payload"))
	}

	b.wantQuota(t, int64(len("payload")), 1)
}

// engForgeBlob makes the unit of key name the blob sha (size bytes) instead of
// its own, in the blob list and in its file.
func engForgeBlob(key, sha string, size int64) engIntercept {
	return engRewrite(engUnitPath, func(r *http.Request, doc map[string]any) {
		if r.URL.Query().Get("key") != key {
			return
		}

		content, _ := doc["content"].(map[string]any)
		content["blobs"] = []any{map[string]any{"sha256": sha, "size": size}}

		data, _ := content["data"].(map[string]any)
		files, _ := data["files"].([]any)

		for _, f := range files {
			f.(map[string]any)["blob"] = sha
		}
	})
}

// A digest is not proof of ownership: a blob that another repository of the
// instance (a private one) holds is not linked to a federated repository on the
// word of a peer, it has to be downloaded and verified like any other.
func TestEngineBlobOfAnotherRepositoryIsNotLinkedWithoutDownload(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	privateID, _ := b.addRepo(t, "site-b-private")

	const secret = "not-for-the-federated-repository"

	b.putIn(t, privateID, "secret", secret)
	a.put(t, "victim", "harmless")
	a.setIntercept(engForgeBlob("victim", engSHA(secret), int64(len(secret))))

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	b.mustBeAbsent(t, "victim")

	if n := b.scalar(t, `SELECT count(*) FROM package_files WHERE repository_id = $1`, b.repoID); n != 0 {
		t.Errorf("package_files of the federated repository = %d, want 0 (the private blob was linked)", n)
	}

	// The peer had to be asked for the bytes, and could not give them.
	engEqual(t, "blob requests for victim", a.count(engBlobPath, func(q url.Values) bool { return q.Get("unit") == "victim" }), 1)

	d := b.deferred(t, peer)
	if len(d) != 1 || d[0].UnitKey != "victim" || d[0].Reason != "blob_missing" {
		t.Fatalf("deferred = %+v, want victim parked as blob_missing", d)
	}
}

// The same digest held by another repository is downloaded once and then
// shared; a blob the repository already references is not fetched again.
func TestEngineBlobIsFetchedOncePerRepositoryNotOncePerInstance(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	privateID, _ := b.addRepo(t, "site-b-private")

	const shared = "bytes both repositories hold"

	b.putIn(t, privateID, "private-copy", shared)
	a.put(t, "f1", shared)
	a.put(t, "f2", shared)

	peer := b.addPeer(t, a)
	b.mustPull(t, peer)

	b.mustRead(t, "f1", shared)
	b.mustRead(t, "f2", shared)

	// f1 proves the bytes to the peer; f2 finds them referenced by this repository.
	engEqual(t, "blob requests", a.count(engBlobPath, nil), 1)
	engEqual(t, "blobs rows", b.scalar(t, `SELECT count(*) FROM blobs WHERE sha256 = $1`, engSHA(shared)), int64(1))
}

// seedDebRepo creates a deb repository with a distribution and a component and
// one file that uses them, and returns the file's side ids.
func seedDebRepo(t *testing.T, s *engSite, name string) (string, string, string) {
	t.Helper()

	var distID, compID string

	repoID := seedRepo(t, s.db, name, "deb")

	if err := s.db.QueryRowContext(
		context.Background(),
		`INSERT INTO debian_distributions (repository_id, name) VALUES ($1, 'stable') RETURNING id::text`,
		repoID,
	).Scan(&distID); err != nil {
		t.Fatalf("seed distribution: %v", err)
	}

	if err := s.db.QueryRowContext(context.Background(),
		`INSERT INTO debian_components (distribution_id, name) VALUES ($1, 'main') RETURNING id::text`, distID).Scan(&compID); err != nil {
		t.Fatalf("seed component: %v", err)
	}

	s.exec(t, `INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'deb', 'pkg', 'pkg')`, repoID)
	s.exec(t, `INSERT INTO package_versions (package_id, repository_id, version, version_key)
	           SELECT id, repository_id, '1', '1' FROM packages WHERE repository_id = $1`, repoID)
	s.exec(t, `INSERT INTO package_files (version_id, repository_id, path, filename, unit_key)
	           SELECT id, repository_id, 'seed.deb', 'seed.deb', 'seed.deb' FROM package_versions WHERE repository_id = $1`, repoID)
	s.exec(t, `INSERT INTO deb_files (id, distribution_id, component_id)
	           SELECT id, $2::uuid, $3::uuid FROM package_files WHERE repository_id = $1`, repoID, distID, compID)

	return repoID, distID, compID
}

// The reference columns of a side-table row (deb_files.distribution_id and
// component_id) are the peer's JSON like the rest of the row, and must not
// attach the file to a distribution or component of another repository.
func TestPackageCodecSideRowCannotReferenceAnotherRepository(t *testing.T) {
	s := newEngSite(t, "site-a", "raw")

	repoID, distID, compID := seedDebRepo(t, s, "deb-target")
	_, otherDist, otherComp := seedDebRepo(t, s, "deb-other")

	codec := newPackageCodec("deb", formatstorage.Storage{
		Federation: &formatstorage.Federation{Unit: formatstorage.UnitFile},
		SideTables: []formatstorage.SideTable{{Name: "deb_files", Refs: []string{"distribution_id", "component_id"}}},
	})

	row := func(dist, comp string) map[string]json.RawMessage {
		return map[string]json.RawMessage{
			"deb_files": json.RawMessage(`{"distribution_id":` + dist + `,"component_id":` + comp + `,"control_fields":{"Package":"pkg"}}`),
		}
	}
	q := func(id string) string { return `"` + id + `"` }

	tests := []struct {
		name string
		rows map[string]json.RawMessage
		ok   bool
	}{
		{"its own distribution and component", row(q(distID), q(compID)), true},
		{"the distribution of another repository", row(q(otherDist), q(compID)), false},
		{"the component of another repository", row(q(distID), q(otherComp)), false},
		{"both of another repository", row(q(otherDist), q(otherComp)), false},
		{"an id nothing has", row(q(uuid.NewString()), q(compID)), false},
		{"not an id", row(`"x'; DROP TABLE deb_files; --"`, q(compID)), false},
		{"null", row(`null`, q(compID)), false},
		{"a number", row(`7`, q(compID)), false},
		{"missing", map[string]json.RawMessage{"deb_files": json.RawMessage(`{"control_fields":{}}`)}, false},
		{"not an object", map[string]json.RawMessage{"deb_files": json.RawMessage(`[1]`)}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fileID string

			s.exec(t, `INSERT INTO package_files (version_id, repository_id, path, filename, unit_key)
			           SELECT id, repository_id, $2, $2, $2 FROM package_versions WHERE repository_id = $1`,
				repoID, "new-"+uuid.NewString()+".deb")

			err := s.db.QueryRowContext(
				context.Background(),
				`SELECT id::text FROM package_files WHERE repository_id = $1 AND path LIKE 'new-%' ORDER BY created_at DESC, id LIMIT 1`,
				repoID,
			).Scan(&fileID)
			if err != nil {
				t.Fatalf("find file: %v", err)
			}

			tx, err := s.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}

			defer func() { _ = tx.Rollback() }()

			err = codec.writeSides(context.Background(), tx, repoID, fileID, tc.rows)
			if tc.ok {
				if err != nil {
					t.Fatalf("writeSides = %v, want accepted", err)
				}

				return
			}

			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("writeSides = %v, want ErrInvalid", err)
			}

			var n int
			if err := tx.QueryRowContext(context.Background(), `SELECT count(*) FROM deb_files WHERE id = $1::uuid`, fileID).
				Scan(&n); err != nil ||
				n != 0 {
				t.Errorf("side rows written for the refused row = %d (%v)", n, err)
			}
		})
	}
}
