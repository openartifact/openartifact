// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

const ociConfigType = "application/vnd.oci.image.config.v1+json"

// recordHooks registers a hook on g that records the events it sees.
func recordHooks(g *registry) func() []ManifestEvent {
	var (
		mu     sync.Mutex
		events []ManifestEvent
	)

	g.h.AddManifestPutHook(func(_ context.Context, ev ManifestEvent) {
		mu.Lock()
		defer mu.Unlock()

		events = append(events, ev)
	})

	return func() []ManifestEvent {
		g.h.hooks.wg.Wait()
		mu.Lock()
		defer mu.Unlock()

		return append([]ManifestEvent(nil), events...)
	}
}

func TestManifestPutHookSeesTheCommittedPush(t *testing.T) {
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)
	events := recordHooks(g)

	cfg := []byte(`{"config":{"Labels":{"quay.expires-after":"3h"}}}`)
	if rec := g.pushBlob("reg/team/app", cfg); rec.Code != http.StatusCreated {
		t.Fatalf("push config = %d: %s", rec.Code, rec.Body.String())
	}

	subject := digestOf([]byte("subject"))
	body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","artifactType":"application/vnd.example.sig",` +
		`"config":{"mediaType":"` + ociConfigType + `","digest":"` + digestOf(cfg) + `","size":55},` +
		`"subject":{"mediaType":"` + ociManifestType + `","digest":"` + subject + `","size":7},` +
		`"annotations":{"k":"v"}}`

	if rec := g.putManifest("reg/team/app", "1", body); rec.Code != http.StatusCreated {
		t.Fatalf("push by tag = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := g.putManifest("reg/team/app", digestOf([]byte(body)), body); rec.Code != http.StatusCreated {
		t.Fatalf("push by digest = %d: %s", rec.Code, rec.Body.String())
	}

	got := events()
	if len(got) != 2 {
		t.Fatalf("hook calls = %d, want 2", len(got))
	}

	for _, tt := range []struct {
		ev      ManifestEvent
		wantTag string
	}{{got[0], "1"}, {got[1], ""}} {
		ev := tt.ev
		if ev.RepoID != "id-reg" || ev.RepoName != "reg" || ev.Image != "team/app" || ev.Tag != tt.wantTag {
			t.Errorf("event identity = %+v, want repo reg, image team/app, tag %q", ev, tt.wantTag)
		}

		if ev.Digest != digestOf([]byte(body)) || ev.MediaType != ociManifestType || string(ev.Manifest) != body {
			t.Errorf("event manifest = %q %q, want the pushed body and digest", ev.Digest, ev.MediaType)
		}

		if ev.ArtifactType != "application/vnd.example.sig" || ev.SubjectDigest != subject || ev.Annotations["k"] != "v" {
			t.Errorf("event metadata = %+v", ev)
		}

		if labels := ev.ConfigLabels(context.Background()); labels["quay.expires-after"] != "3h" {
			t.Errorf("ConfigLabels = %v, want the config's labels", labels)
		}
	}
}

func TestManifestPutHookNeverFailsOrBlocksThePush(t *testing.T) {
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	release := make(chan struct{})
	started := make(chan struct{})

	g.h.AddManifestPutHook(func(context.Context, ManifestEvent) {
		close(started)
		<-release // a slow hook must not hold the response back

		panic("hook failure")
	})

	after := recordHooks(g)

	if rec := g.putManifest("reg/app", "1", plainManifest); rec.Code != http.StatusCreated {
		t.Fatalf("push = %d, want 201 while the hook is still running: %s", rec.Code, rec.Body.String())
	}

	<-started
	close(release)

	if got := after(); len(got) != 1 {
		t.Fatalf("hooks after the panicking one ran %d times, want 1", len(got))
	}
}

func TestManifestPutWithoutHooksNeedsNoRunner(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))

	if rec := g.putManifest("reg/app", "1", plainManifest); rec.Code != http.StatusCreated {
		t.Fatalf("push = %d: %s", rec.Code, rec.Body.String())
	}

	g.h.hooks.wg.Wait()
}

func TestPutManifestRecordsQuayExpiry(t *testing.T) {
	cfgWith := func(label string) []byte {
		return []byte(`{"config":{"Labels":{"quay.expires-after":"` + label + `"}}}`)
	}

	tests := []struct {
		name       string
		honour     bool
		annotation string
		label      string
		want       time.Duration // 0: no deadline
	}{
		{name: "annotation", honour: true, annotation: "1d", want: 24 * time.Hour},
		{name: "label", honour: true, label: "2h", want: 2 * time.Hour},
		{name: "earlier of annotation and label", honour: true, annotation: "1w", label: "90m", want: 90 * time.Minute},
		{name: "unparsable value never expires", honour: true, annotation: "soon", label: "0d"},
		{name: "no declaration", honour: true},
		{name: "repository that does not honour it", honour: false, annotation: "1h", label: "1h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newRegFakeDB("reg")
			db.repos["reg"].HonourQuayExpiry = tt.honour
			g := newRegistry(t, db)

			body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"`

			if tt.label != "" {
				cfg := cfgWith(tt.label)
				if rec := g.pushBlob("reg/app", cfg); rec.Code != http.StatusCreated {
					t.Fatalf("push config = %d: %s", rec.Code, rec.Body.String())
				}

				body += `,"config":{"mediaType":"` + ociConfigType + `","digest":"` + digestOf(cfg) + `","size":50}`
			}

			if tt.annotation != "" {
				body += `,"annotations":{"quay.expires-after":"` + tt.annotation + `"}`
			}

			body += "}"

			before := time.Now()

			if rec := g.putManifest("reg/app", "1", body); rec.Code != http.StatusCreated {
				t.Fatalf("push = %d: %s", rec.Code, rec.Body.String())
			}

			after := time.Now()

			m := db.manifests[scoped("id-reg", scoped("app", digestOf([]byte(body))))]
			if m == nil {
				t.Fatal("manifest not stored")
			}

			if tt.want == 0 {
				if m.ExpiresAt != nil {
					t.Fatalf("ExpiresAt = %v, want none", m.ExpiresAt)
				}

				return
			}

			if m.ExpiresAt == nil || m.ExpiresAt.Before(before.Add(tt.want)) || m.ExpiresAt.After(after.Add(tt.want)) {
				t.Fatalf("ExpiresAt = %v, want push time + %s", m.ExpiresAt, tt.want)
			}
		})
	}
}

func TestReferrersArtifactTypeFilter(t *testing.T) {
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	baseDigest := g.putManifest("reg/app", "1", plainManifest).Header().Get("Docker-Content-Digest")
	if baseDigest == "" {
		t.Fatal("base push failed")
	}

	for _, artifactType := range []string{"application/vnd.example.sbom", "application/vnd.example.sig"} {
		body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","artifactType":"` + artifactType +
			`","subject":{"mediaType":"` + ociManifestType + `","digest":"` + baseDigest + `","size":1}}`
		if rec := g.putManifest("reg/app", digestOf([]byte(body)), body); rec.Code != http.StatusCreated {
			t.Fatalf("push referrer = %d: %s", rec.Code, rec.Body.String())
		}
	}

	tests := []struct {
		name        string
		query       string
		wantTypes   []string
		wantApplied bool
	}{
		{name: "no filter lists everything", query: "", wantTypes: []string{"application/vnd.example.sbom", "application/vnd.example.sig"}},
		{
			name: "filter keeps one type", query: "?artifactType=application/vnd.example.sig",
			wantTypes: []string{"application/vnd.example.sig"}, wantApplied: true,
		},
		{name: "filter without a match is an empty list", query: "?artifactType=application/none", wantApplied: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := g.do(http.MethodGet, "/v2/reg/app/referrers/"+baseDigest+tt.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}

			if got := rec.Header().Get("Oci-Filters-Applied") == "artifactType"; got != tt.wantApplied {
				t.Errorf("OCI-Filters-Applied set = %v, want %v", got, tt.wantApplied)
			}

			var idx struct {
				Manifests []struct {
					ArtifactType string `json:"artifactType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
				} `json:"manifests"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &idx); err != nil {
				t.Fatalf("decode: %v", err)
			}

			var got []string
			for _, m := range idx.Manifests {
				got = append(got, m.ArtifactType)
			}

			if len(got) != len(tt.wantTypes) {
				t.Fatalf("artifact types = %v, want %v", got, tt.wantTypes)
			}

			seen := map[string]bool{}
			for _, ty := range got {
				seen[ty] = true
			}

			for _, w := range tt.wantTypes {
				if !seen[w] {
					t.Errorf("artifact types = %v, missing %q", got, w)
				}
			}
		})
	}
}
