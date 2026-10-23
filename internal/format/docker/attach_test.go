// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

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

	if _, err := g.h.AttachReferrer(context.Background(), repo, attachReq(digestOf([]byte("nope")), "x")); !errors.Is(err, ErrSubjectNotFound) {
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
