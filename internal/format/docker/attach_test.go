// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
)

const testBundleType = "application/vnd.dev.sigstore.bundle.v0.3+json"

type referrerList struct {
	Manifests []struct {
		Digest       string            `json:"digest"`
		ArtifactType string            `json:"artifactType"` //nolint:tagliatelle // OCI wire format
		Annotations  map[string]string `json:"annotations"`
	} `json:"manifests"`
}

func (g *registry) referrers(name, digest string) referrerList {
	g.t.Helper()

	rec := g.do(http.MethodGet, "/v2/"+name+"/referrers/"+digest, "")
	if rec.Code != http.StatusOK {
		g.t.Fatalf("referrers = %d; body=%s", rec.Code, rec.Body.String())
	}

	var out referrerList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		g.t.Fatalf("decode referrers: %v", err)
	}

	return out
}

// pushImage pushes a one-layer image to repository reg and returns its manifest digest.
func (g *registry) pushImage(tag string) string {
	g.t.Helper()

	config, layer := []byte(`{"os":"linux"}`), []byte(strings.Repeat("layer", 20))
	g.mustPushBlob("reg", config)
	g.mustPushBlob("reg", layer)

	body := imageManifest(g.t, config, layer)
	g.mustPutManifest("reg", tag, body)

	return digestOf([]byte(body))
}

func attachReq(subject, content string) ReferrerRequest {
	return ReferrerRequest{
		SubjectDigest: subject, ArtifactType: testBundleType, Content: []byte(content),
		Annotations: map[string]string{"kind": "sbom", "predicate": "cyclonedx"},
		SlotKeys:    []string{"kind", "predicate"},
	}
}

func TestAttachReferrerListsMetersAndReplacesBySlot(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	subject := g.pushImage("1.0")
	repo := db.repos["reg"]
	before := db.usage("reg")

	first, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":1}`))
	if err != nil || !first.Created || first.Replaced != "" {
		t.Fatalf("first attach = %+v, %v; want created", first, err)
	}

	list := g.referrers("reg", subject)
	if len(list.Manifests) != 1 || list.Manifests[0].Digest != first.Digest ||
		list.Manifests[0].ArtifactType != testBundleType || list.Manifests[0].Annotations["kind"] != "sbom" {
		t.Fatalf("referrers = %+v; want the attached bundle with its annotations", list)
	}

	// Referrer manifest + layer + the empty config are metered like a client push.
	after := db.usage("reg")
	if after.artifacts != before.artifacts+1 || after.bytes <= before.bytes {
		t.Errorf("usage = %+v, was %+v; want one more manifest and its bytes", after, before)
	}

	// The registry serves the manifest and its subject link survives a read.
	if rec := g.do(http.MethodGet, "/v2/reg/manifests/"+first.Digest, ""); rec.Code != http.StatusOK {
		t.Fatalf("get referrer manifest = %d", rec.Code)
	}

	// New content for the same slot replaces the old referrer.
	next, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":2}`))
	if err != nil || !next.Created || next.Replaced != first.Digest {
		t.Fatalf("replacing attach = %+v, %v; want created, replacing %s", next, err, first.Digest)
	}

	if list = g.referrers("reg", subject); len(list.Manifests) != 1 || list.Manifests[0].Digest != next.Digest {
		t.Fatalf("referrers after replace = %+v; want only the new one", list)
	}

	if rec := g.do(http.MethodGet, "/v2/reg/manifests/"+first.Digest, ""); rec.Code != http.StatusNotFound {
		t.Errorf("replaced referrer manifest = %d; want 404", rec.Code)
	}
}

func TestAttachReferrerIsIdempotent(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	subject := g.pushImage("1.0")
	repo := db.repos["reg"]

	first, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}

	used := db.usage("reg")

	again, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":1}`))
	if err != nil || again.Created || again.Digest != first.Digest {
		t.Fatalf("second attach = %+v, %v; want the same referrer, not created", again, err)
	}

	if db.usage("reg") != used {
		t.Errorf("an idempotent attach changed the usage")
	}
}

func TestAttachReferrerKeepsOtherSlots(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	subject := g.pushImage("1.0")
	repo := db.repos["reg"]

	if _, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":1}`)); err != nil {
		t.Fatal(err)
	}

	other := attachReq(subject, `{"v":3}`)
	other.Annotations = map[string]string{"kind": "vex", "predicate": "openvex"}

	if _, err := g.h.AttachReferrer(context.Background(), repo, other); err != nil {
		t.Fatal(err)
	}

	if list := g.referrers("reg", subject); len(list.Manifests) != 2 {
		t.Fatalf("referrers = %+v; want the SBOM and the VEX slot", list)
	}
}

func TestAttachReferrerNeedsTheSubjectAndRespectsQuota(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	repo := db.repos["reg"]

	if _, err := g.h.AttachReferrer(
		context.Background(),
		repo,
		attachReq(digestOf([]byte("nope")), "x"),
	); !errors.Is(
		err,
		ErrSubjectNotFound,
	) {
		t.Fatalf("attach to a missing subject = %v; want ErrSubjectNotFound", err)
	}

	subject := g.pushImage("1.0")
	used := db.usage("reg")
	db.ledger.maxBytes = used.bytes + 10 // no room for a bundle, its config and manifest

	_, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, strings.Repeat("b", 200)))
	if !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("attach over quota = %v; want quota.ErrExceeded", err)
	}

	if db.usage("reg") != used {
		t.Errorf("a refused attach leaked usage: %+v, was %+v", db.usage("reg"), used)
	}

	if list := g.referrers("reg", subject); len(list.Manifests) != 0 {
		t.Errorf("referrers = %+v; want none", list)
	}
}

func TestFindReferrerMatchesAnnotations(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	subject := g.pushImage("1.0")
	repo := db.repos["reg"]

	res, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}

	got, err := g.h.FindReferrer(context.Background(), repo, "", subject, testBundleType, map[string]string{"kind": "sbom"})
	if err != nil || got != res.Digest {
		t.Fatalf("find = %q, %v; want %s", got, err, res.Digest)
	}

	if got, _ = g.h.FindReferrer(context.Background(), repo, "", subject, testBundleType, map[string]string{"kind": "vex"}); got != "" {
		t.Errorf("find for another kind = %q; want none", got)
	}
}

var errNestedBlobLock = errors.New("blob lock taken while another is held")

// lockGuardDB is a regFakeDB whose blob locks model the database's bounded lock
// slots: every lock holder owns one, so a lock taken while another is held is
// refused (the real pool deadlocks once concurrent holders all wait for a
// second slot). It records the lock set held while manifest rows are written.
// It is single-goroutine, like the registry fake it wraps.
type lockGuardDB struct {
	*regFakeDB

	held      []string            // lock set of the section being run
	nested    []string            // every lock attempt made while holding another
	writes    map[string][]string // "create|delete <manifest digest>" -> locks held
	reclaimed []string            // blobs SafeDeleteBlob was asked to delete
}

func newLockGuardDB(repoNames ...string) *lockGuardDB {
	return &lockGuardDB{regFakeDB: newRegFakeDB(repoNames...), writes: map[string][]string{}}
}

func (d *lockGuardDB) acquire(op string, keys []string) error {
	if len(d.held) > 0 {
		d.nested = append(d.nested, op+" while holding "+strings.Join(d.held, ","))
		return errNestedBlobLock
	}

	d.held = slices.Sorted(slices.Values(keys))

	return nil
}

func (d *lockGuardDB) WithBlobLocks(ctx context.Context, shas []string, fn func(ctx context.Context) error) error {
	if err := d.acquire("WithBlobLocks("+strings.Join(shas, ",")+")", shas); err != nil {
		return err
	}

	defer func() { d.held = nil }()

	return fn(ctx)
}

func (d *lockGuardDB) WithBlobLock(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
	return d.WithBlobLocks(ctx, []string{sha}, fn)
}

func (d *lockGuardDB) DeleteBlobIfUnreferenced(ctx context.Context, sha string) (bool, error) {
	if err := d.acquire("DeleteBlobIfUnreferenced("+sha+")", []string{sha}); err != nil {
		return false, err
	}

	defer func() { d.held = nil }()

	refs, err := d.BlobReferenceCount(ctx, sha)
	if err != nil {
		return false, err
	}

	if refs == 0 {
		d.reclaimed = append(d.reclaimed, sha)
	}

	return refs == 0, nil
}

func (d *lockGuardDB) CreateDockerManifest(ctx context.Context, m *models.DockerManifest) error {
	d.writes["create "+m.Digest] = slices.Clone(d.held)

	return d.regFakeDB.CreateDockerManifest(ctx, m)
}

func (d *lockGuardDB) DeleteDockerManifest(ctx context.Context, repoID, image, digest string) error {
	d.writes["delete "+digest] = slices.Clone(d.held)

	return d.regFakeDB.DeleteDockerManifest(ctx, repoID, image, digest)
}

// AttachReferrer must never take a blob lock while holding one: the slot lock
// and the manifest blob's lock are taken together, and the layer, the config
// and the replaced referrer's content blob are committed and reclaimed outside
// them. A nested lock holds a lock slot while waiting for another, which
// deadlocks the pool once enough attaches run at the same time.
func TestAttachReferrerNeverNestsBlobLocks(t *testing.T) {
	db := newLockGuardDB("reg")
	g := newRegistry(t, db)
	subject := g.pushImage("1.0")
	repo := db.repos["reg"]

	first, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":1}`))
	if err != nil {
		t.Fatalf("first attach: %v (nested locks: %v)", err, db.nested)
	}

	next, err := g.h.AttachReferrer(context.Background(), repo, attachReq(subject, `{"v":2}`))
	if err != nil || next.Replaced != first.Digest {
		t.Fatalf("replacing attach = %+v, %v; want it to replace %s (nested locks: %v)", next, err, first.Digest, db.nested)
	}

	if len(db.nested) != 0 {
		t.Fatalf("blob locks were nested: %v", db.nested)
	}

	// The referrer rows are written, and the replaced one removed, under the
	// manifest's own lock and ONE more lock: the slot's, the same for both.
	slotOf := func(digest string) string {
		t.Helper()

		held := db.writes["create "+digest]
		if len(held) != 2 || !slices.Contains(held, manifestBlobKey(digest)) {
			t.Fatalf("create %s ran under locks %v; want the slot lock and the manifest's", digest, held)
		}

		return slices.DeleteFunc(slices.Clone(held), func(k string) bool { return k == manifestBlobKey(digest) })[0]
	}

	slot := slotOf(first.Digest)
	if got := slotOf(next.Digest); got != slot {
		t.Errorf("the replacing attach locked slot %s; the first locked %s", got, slot)
	}

	// The replaced row goes in the replacing attach's critical section.
	want := slices.Sorted(slices.Values([]string{slot, manifestBlobKey(next.Digest)}))
	if held := db.writes["delete "+first.Digest]; !slices.Equal(held, want) {
		t.Errorf("the replaced referrer was removed under locks %v; want %v", held, want)
	}

	// Its content blob is reclaimed once the locks are released.
	if !slices.Contains(db.reclaimed, manifestBlobKey(first.Digest)) {
		t.Errorf("replaced referrer's blob was not reclaimed: %v", db.reclaimed)
	}
}
