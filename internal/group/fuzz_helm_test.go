// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	pkghelm "github.com/openartifact/openartifact/pkg/helm"
	"go.yaml.in/yaml/v3"
)

// helmFuzzListing reads the versions an index document lists under each chart
// key, straight from the YAML tree (independent of pkg/helm).
func helmFuzzListing(t *testing.T, doc []byte) map[string]map[string]bool {
	t.Helper()

	var n yaml.Node
	if err := yaml.Unmarshal(doc, &n); err != nil || n.Kind != yaml.DocumentNode || len(n.Content) != 1 {
		t.Fatalf("index document is not YAML (%v):\n%s", err, doc)
	}

	out := map[string]map[string]bool{}

	root := n.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "entries" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}

		entries := root.Content[i+1]
		for j := 0; j+1 < len(entries.Content); j += 2 {
			key := entries.Content[j].Value

			for _, e := range entries.Content[j+1].Content {
				if e.Kind != yaml.MappingNode {
					continue
				}

				for k := 0; k+1 < len(e.Content); k += 2 {
					if v := e.Content[k+1]; e.Content[k].Value == "version" && v.Kind == yaml.ScalarNode && v.Tag != "!!null" && v.Value != "" {
						if out[key] == nil {
							out[key] = map[string]bool{}
						}

						out[key][v.Value] = true

						break
					}
				}
			}
		}
	}

	return out
}

// FuzzServeHelmGroup serves the index.yaml of a helm group (a hosted member and
// a proxy member, hosted wins) from two arbitrary member documents: the proxy's
// is whatever its upstream published. Whatever the documents hold, the answer
// is a readable index that only points at the group and never lets the proxy
// list a version under a chart name the hosted member lists.
func FuzzServeHelmGroup(f *testing.F) {
	hosted := memberIndex("hosted", map[string][]string{"internal": {"1.0.0"}, "app": {"1.0.0"}})
	mirror := memberIndex("mirror", map[string][]string{"internal": {"9.9.9"}, "app": {"1.0.0", "2.0.0"}, "other": {"0.1.0"}})

	f.Add([]byte(hosted), []byte(mirror))
	f.Add([]byte(hosted), []byte("apiVersion: v1\nentries:\n  other:\n  - {name: other, version: 99.0.0, urls: ['https://elsewhere.example/other-99.0.0.tgz']}\n"))
	f.Add([]byte("apiVersion: v1\nentries:\n  a: &x []\n  b: *x\n"), []byte(mirror))
	f.Add([]byte("not an index"), []byte(mirror))
	f.Add([]byte(hosted), []byte("<html>"))
	f.Add([]byte("apiVersion: v1\nentries: {}\n"), []byte("apiVersion: v1\nentries:\n  a:\n  - {version: 1.0.0, urls: ['../x/../a-1.0.0.tgz', 'oci://r/a', '']}\n"))

	f.Fuzz(helmFuzzServe)
}

// helmFuzzCheckURLs checks that every URL of the served index is the group's
// and names a file one of the members listed.
func helmFuzzCheckURLs(t *testing.T, merged *pkghelm.Index, hostedDoc, mirrorDoc []byte) {
	t.Helper()

	const chartBase = helmBase + "/repos/all/charts/"

	listed := map[string]bool{}

	for _, doc := range [][]byte{hostedDoc, mirrorDoc} {
		ix, err := pkghelm.ParseIndex(doc)
		if err != nil {
			continue
		}

		for cv := range ix.Versions() {
			for _, raw := range cv.URLs {
				if file, ok := pkghelm.RelativeChartFile(raw); ok {
					listed[file] = true
				}
			}
		}
	}

	for cv := range merged.Versions() {
		if len(cv.URLs) == 0 {
			t.Fatalf("%s %s has no URL", cv.Name, cv.Version)
		}

		for _, u := range cv.URLs {
			tail, ok := strings.CutPrefix(u, chartBase)
			if !ok {
				t.Fatalf("%s %s URL %q does not point at the group", cv.Name, cv.Version, u)
			}

			file, err := url.PathUnescape(tail)
			if err != nil || !listed[file] || strings.Contains(file, "/") {
				t.Fatalf("%s %s URL %q names %q (%v), which no member listed", cv.Name, cv.Version, u, file, err)
			}
		}
	}
}

func helmFuzzServe(t *testing.T, hostedDoc, mirrorDoc []byte) {
	fx := newFixture(t, "helm", member("hosted", "helm", "hosted"), member("mirror", "helm", "proxy"))
	fx.h.SetBaseURL(helmBase + "/")
	fx.grp.MergeProxyVersions = false
	fx.set("hosted", "index.yaml", http.StatusOK, string(hostedDoc))
	fx.set("mirror", "index.yaml", http.StatusOK, string(mirrorDoc))

	_, hostedErr := pkghelm.ParseIndex(hostedDoc)
	_, mirrorErr := pkghelm.ParseIndex(mirrorDoc)

	w := fx.get(http.MethodGet, "index.yaml")

	// A hosted index that cannot be read must fail the request while a
	// proxy index is in play; no readable index at all fails it too.
	wantOK := hostedErr == nil

	if (wantOK && w.Code != http.StatusOK) || (!wantOK && w.Code != http.StatusBadGateway) {
		t.Fatalf("status %d (hosted err %v, mirror err %v): %s", w.Code, hostedErr, mirrorErr, w.Body.String())
	}

	if !wantOK {
		return
	}

	body := w.Body.Bytes()

	merged, err := pkghelm.ParseIndex(body)
	if err != nil {
		t.Fatalf("served index is not an index: %v\n%s", err, body)
	}

	helmFuzzCheckURLs(t, merged, hostedDoc, mirrorDoc)

	// Hosted wins: under a chart name the hosted member lists, the group
	// publishes only versions the hosted member lists.
	hostedList := helmFuzzListing(t, hostedDoc)

	for key, versions := range helmFuzzListing(t, body) {
		owned, held := hostedList[key]
		if !held {
			continue
		}

		for v := range versions {
			if !owned[v] {
				t.Fatalf("chart %q version %q comes from the proxy although the hosted member lists %q\nhosted:\n%s\nproxy:\n%s\nmerged:\n%s",
					key, v, key, hostedDoc, mirrorDoc, body)
			}
		}
	}
}
