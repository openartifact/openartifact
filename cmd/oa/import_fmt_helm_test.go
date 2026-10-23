// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// helmChartTgz builds a chart archive with the layout `helm package` writes:
// <name>/Chart.yaml plus a template.
func helmChartTgz(t *testing.T, name, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, f := range [][2]string{
		{name + "/Chart.yaml", "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"},
		{name + "/templates/cm.yaml", "kind: ConfigMap\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1]))}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// helmFakeIdentity reads name and version out of a chart's Chart.yaml the way
// the server does: the upload names nothing, the archive decides.
func helmFakeIdentity(data []byte) (string, string, bool) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", "", false
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if err != nil {
			return "", "", false
		}

		if strings.Count(h.Name, "/") != 1 || path.Base(h.Name) != "Chart.yaml" {
			continue
		}

		raw, _ := io.ReadAll(tr)

		var name, version string

		for line := range strings.SplitSeq(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "name: "); ok {
				name = strings.TrimSpace(v)
			}

			if v, ok := strings.CutPrefix(line, "version: "); ok {
				version = strings.TrimSpace(v)
			}
		}

		return name, version, name != "" && version != ""
	}
}

func init() { //nolint:gochecknoinits // the fake destination's route registry is filled by each format's tests
	fakeOARoutes["helm"] = helmFakeRoutes
}

// helmFakeRoutes implements the hosted Helm endpoints the importer uses:
// POST /charts with the raw archive (201 {id,name,version}, 409 on a stored
// name and version) and DELETE /charts/<name>-<version>.tgz.
func helmFakeRoutes(f *fakeOA, mux *http.ServeMux) {
	mux.HandleFunc("POST /charts", func(w http.ResponseWriter, r *http.Request) {
		f.writes++

		repo := r.Header.Get("X-Fake-Repo")

		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read chart", http.StatusBadRequest)
			return
		}

		name, version, ok := helmFakeIdentity(data)
		if !ok {
			http.Error(w, "invalid chart", http.StatusBadRequest)
			return
		}

		for _, it := range f.repos[repo].items {
			if it.name == name && it.version == version {
				http.Error(w, "chart version already exists", http.StatusConflict)
				return
			}
		}

		it := f.store(repo, name+"-"+version+".tgz", data, r.Header.Get(headerImportCreatedAt))
		it.name, it.version = name, version
		// A server that stores something else than it was sent lists another digest.
		it.sha = f.reported(it)

		f.reply(w, http.StatusCreated, map[string]string{"id": it.id, "name": name, "version": version})
	})

	mux.HandleFunc("DELETE /charts/{filename}", func(w http.ResponseWriter, r *http.Request) {
		f.writes++
		f.deletes++

		repo := f.repos[r.Header.Get("X-Fake-Repo")]
		for i, it := range repo.items {
			if it.path == r.PathValue("filename") {
				repo.items = append(repo.items[:i], repo.items[i+1:]...)
				w.WriteHeader(http.StatusOK)

				return
			}
		}

		http.NotFound(w, r)
	})
}

func helmNexusFiles(t *testing.T) []fakeFile {
	t.Helper()

	return []fakeFile{
		{path: "index.yaml", data: []byte("apiVersion: v1\n"), created: nexusStamp},
		{path: "web-1.0.0.tgz", data: helmChartTgz(t, "web", "1.0.0"), created: nexusStamp, contentType: "application/x-gzip"},
		{path: "api-server-2.1.0.tgz", data: helmChartTgz(t, "api-server", "2.1.0"), created: nexusStamp, contentType: "application/x-gzip"},
		{path: "web-1.0.0.tgz.prov", data: []byte("-----BEGIN PGP SIGNED MESSAGE-----"), created: nexusStamp},
	}
}

func helmNexus(t *testing.T, files []fakeFile) (*httptest.Server, importConfig) {
	t.Helper()

	nexus := newFakeNexus(t, fakeSourceRepo{name: "charts", format: "helm", kind: "hosted", files: files})

	return nexus, parseCfg(t, "nexus", "--source-url", nexus.URL, "--repo", "charts:helm")
}

func helmOA(t *testing.T) *fakeOA {
	t.Helper()

	return newFakeOA(t, map[string]*fakeRepo{"helm": {format: "helm", repoType: "hosted"}})
}

func TestImportHelmFromNexus(t *testing.T) {
	files := helmNexusFiles(t)
	nexus, cfg := helmNexus(t, files)
	oa := helmOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 2 || rep.Ignored != 2 {
		t.Fatalf("report = %+v (index.yaml and .prov must be ignored)", rep)
	}

	for _, want := range []struct{ file, name, version string }{
		{"web-1.0.0.tgz", "web", "1.0.0"},
		{"api-server-2.1.0.tgz", "api-server", "2.1.0"},
	} {
		it := oa.find("helm", want.file)
		if it == nil || it.name != want.name || it.version != want.version {
			t.Fatalf("chart %s = %+v", want.file, it)
		}

		if it.created != nexusWant {
			t.Fatalf("%s created-at header = %q, want %q", want.file, it.created, nexusWant)
		}
	}

	if got, want := oa.find("helm", "web-1.0.0.tgz").sha, sum256(files[1].data); got != want {
		t.Fatalf("stored sha %s, want %s", got, want)
	}

	if len(oa.repos["helm"].items) != 2 {
		t.Fatalf("destination holds %d charts, want 2", len(oa.repos["helm"].items))
	}
}

func TestImportHelmFromArtifactoryCopiesProperties(t *testing.T) {
	chart := helmChartTgz(t, "web", "1.0.0")
	arti := newFakeArtifactory(t, fakeSourceRepo{name: "helm-local", format: "helm", kind: "local", files: []fakeFile{
		{path: "web-1.0.0.tgz", data: chart, created: artiStamp, props: map[string][]string{
			"build.name": {"web-ci"}, "team": {"secret-team"},
		}},
		{path: "index.yaml", data: []byte("apiVersion: v1\n"), created: artiStamp},
		{path: "web-1.0.0.tgz.prov", data: []byte("sig"), created: artiStamp},
	}})
	oa := helmOA(t)

	cfg := parseCfg(t, "artifactory", "--source-url", arti.URL, "--source-user", "svc", "--repo", "helm-local:helm")
	cfg.SourceToken = "secret"

	rep := runImportTest(t, cfg, arti, oa)

	if rep.failed() || rep.Uploaded != 1 || rep.Ignored != 2 {
		t.Fatalf("report = %+v", rep)
	}

	it := oa.find("helm", "web-1.0.0.tgz")
	if it == nil || it.created != artiWant || it.sha != sum256(chart) {
		t.Fatalf("item = %+v", it)
	}

	if len(it.props) != 1 || it.props["build.name"] != "web-ci" {
		t.Fatalf("props = %v, want only build.name", it.props)
	}
}

func TestImportHelmNamesTheChartByItsChartYAML(t *testing.T) {
	// The source file name says nothing reliable; Chart.yaml decides.
	chart := helmChartTgz(t, "web", "1.0.0")
	nexus, cfg := helmNexus(t, []fakeFile{{path: "downloads/renamed.tgz", data: chart, created: nexusStamp}})
	oa := helmOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	it := oa.find("helm", "web-1.0.0.tgz")
	if rep.failed() || it == nil || it.name != "web" || it.sha != sum256(chart) {
		t.Fatalf("report = %+v item = %+v", rep, it)
	}
}

func TestImportHelmChecksumMismatchRemovesTheCopy(t *testing.T) {
	files := []fakeFile{
		{path: "ok-1.0.0.tgz", data: helmChartTgz(t, "ok", "1.0.0"), created: nexusStamp},
		{path: "bad-1.0.0.tgz", data: helmChartTgz(t, "bad", "1.0.0"), created: nexusStamp, sha256: sum256([]byte("original"))},
	}
	nexus, cfg := helmNexus(t, files)
	oa := helmOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Uploaded != 1 || rep.Failed != 1 {
		t.Fatalf("uploaded=%d failed=%d: %+v", rep.Uploaded, rep.Failed, rep.Failures)
	}

	if f := rep.Failures[0]; !strings.Contains(f.Error, "checksum mismatch") || !strings.Contains(f.Error, "bad copy removed") {
		t.Fatalf("failure = %+v", f)
	}

	if oa.find("helm", "bad-1.0.0.tgz") != nil || oa.deletes != 1 || oa.find("helm", "ok-1.0.0.tgz") == nil {
		t.Fatalf("bad copy not removed alone (deletes=%d, items=%d)", oa.deletes, len(oa.repos["helm"].items))
	}
}

func TestImportHelmServerDigestMismatchRemovesTheCopy(t *testing.T) {
	nexus, cfg := helmNexus(t, []fakeFile{{path: "web-1.0.0.tgz", data: helmChartTgz(t, "web", "1.0.0"), created: nexusStamp}})
	oa := helmOA(t)
	oa.lieSHA = strings.Repeat("0", 64)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
		t.Fatalf("report = %+v", rep)
	}

	if len(oa.repos["helm"].items) != 0 || oa.deletes != 1 {
		t.Fatalf("corrupt copy left behind (items=%d deletes=%d)", len(oa.repos["helm"].items), oa.deletes)
	}
}

func TestImportHelmIsIdempotent(t *testing.T) {
	nexus, cfg := helmNexus(t, helmNexusFiles(t))
	oa := helmOA(t)

	runImportTest(t, cfg, nexus, oa)

	writes := oa.writes
	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 2 || oa.writes != writes {
		t.Fatalf("second run uploaded=%d present=%d writes+=%d: %+v", rep.Uploaded, rep.Present, oa.writes-writes, rep.Failures)
	}
}

// runImportWithoutSHA256 runs the importer against a source that lists no
// SHA-256 (nor SHA-1), like Nexus does for NuGet assets.
func runImportWithoutSHA256(t *testing.T, cfg importConfig, src *httptest.Server, oa *fakeOA) *importReport {
	t.Helper()

	imp, err := newImporter(cfg, noSHA256Source{newImportSource(cfg, src.Client())}, &importTarget{c: oa.client()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	defer imp.state.close()

	return imp.run(context.Background())
}

func TestImportHelmIsIdempotentWithoutSourceSHA256(t *testing.T) {
	nexus, cfg := helmNexus(t, helmNexusFiles(t))
	oa := helmOA(t)

	if rep := runImportWithoutSHA256(t, cfg, nexus, oa); rep.failed() || rep.Uploaded != 2 {
		t.Fatalf("first run: %+v", rep)
	}

	writes := oa.writes
	rep := runImportWithoutSHA256(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 0 || rep.Present != 2 || oa.writes != writes {
		t.Fatalf("second run uploaded=%d present=%d writes+=%d: %+v", rep.Uploaded, rep.Present, oa.writes-writes, rep.Failures)
	}
}

func TestImportHelmResumesFromStateFile(t *testing.T) {
	nexus, cfg := helmNexus(t, helmNexusFiles(t))
	cfg.StateFile = filepath.Join(t.TempDir(), "state.jsonl")
	oa := helmOA(t)

	if rep := runImportTest(t, cfg, nexus, oa); rep.Uploaded != 2 {
		t.Fatalf("first run: %+v", rep)
	}

	// Wipe the destination: only the state file knows the charts are done.
	oa.repos["helm"].items = nil
	writes := oa.writes

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Resumed != 2 || rep.Uploaded != 0 || oa.writes != writes {
		t.Fatalf("resumed=%d uploaded=%d writes+=%d, want resume only", rep.Resumed, rep.Uploaded, oa.writes-writes)
	}
}

func TestImportHelmDryRunWritesNothing(t *testing.T) {
	files := helmNexusFiles(t)
	nexus, cfg := helmNexus(t, files)
	cfg.DryRun = true
	oa := helmOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.failed() || rep.Uploaded != 2 || !rep.DryRun || oa.writes != 0 || len(oa.repos["helm"].items) != 0 {
		t.Fatalf("report = %+v writes=%d", rep, oa.writes)
	}

	if want := int64(len(files[1].data) + len(files[2].data)); rep.Bytes != want {
		t.Fatalf("planned bytes = %d, want %d", rep.Bytes, want)
	}
}

func TestImportHelmReportsConflictAndKeepsTheStoredChart(t *testing.T) {
	nexus, cfg := helmNexus(t, []fakeFile{{path: "web-1.0.0.tgz", data: helmChartTgz(t, "web", "1.0.0"), created: nexusStamp}})
	oa := helmOA(t)

	stored := oa.store("helm", "web-1.0.0.tgz", []byte("an earlier, different web 1.0.0"), "")
	stored.name, stored.version = "web", "1.0.0"
	want := stored.sha

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "already exists with different content") {
		t.Fatalf("report = %+v", rep)
	}

	if oa.deletes != 0 || len(oa.repos["helm"].items) != 1 || oa.repos["helm"].items[0].sha != want {
		t.Fatalf("existing chart touched (deletes=%d)", oa.deletes)
	}
}

func TestImportHelmRejectsAnArchiveThatIsNotAChart(t *testing.T) {
	nexus, cfg := helmNexus(t, []fakeFile{{path: "broken-1.0.0.tgz", data: []byte("not a gzip stream"), created: nexusStamp}})
	oa := helmOA(t)

	rep := runImportTest(t, cfg, nexus, oa)

	if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "invalid chart") || len(oa.repos["helm"].items) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImportableHelm(t *testing.T) {
	for p, want := range map[string]bool{
		"web-1.0.0.tgz":               true,
		"oa-smoke-chart-0.3.0.tgz":    true, // as listed by a real Nexus 3.76
		"charts/web-1.0.0.tgz":        true,
		"index.yaml":                  false,
		"web-1.0.0.tgz.prov":          false,
		"web-1.0.0.tgz.sha256":        false,
		".helm/index/web-1.0.0.tgz":   false,
		"nested/dir/api-server-2.tgz": true,
	} {
		if got := importable(formatHelm, p); got != want {
			t.Errorf("importable(helm, %q) = %v, want %v", p, got, want)
		}
	}
}
