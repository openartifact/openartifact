// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesbom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	subject = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	spdxDoc = `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"img","packages":[` +
		`{"name":"lodash","versionInfo":"4.17.20","licenseConcluded":"MIT","externalRefs":[` +
		`{"referenceType":"purl","referenceLocator":"pkg:npm/lodash@4.17.20"}]},` +
		`{"name":"openssl","versionInfo":"3.0.7","externalRefs":[` +
		`{"referenceType":"purl","referenceLocator":"pkg:deb/debian/openssl@3.0.7?distro=debian-12"}]}]}`

	cdxDoc = `{"bomFormat":"CycloneDX","specVersion":"1.5","components":[` +
		`{"type":"library","name":"requests","version":"2.19.0","purl":"pkg:pypi/requests@2.19.0"}]}`

	tagValue = "SPDXVersion: SPDX-2.3\nPackageName: zlib\nPackageVersion: 1.2.11\n"
)

func testLog(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return log
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func statement(predicateType, predicate string) string {
	return `{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"` + predicateType + `","predicate":` + predicate + `}`
}

func dsse(payload string) string {
	return `{"payloadType":"application/vnd.in-toto+json","payload":"` + b64(payload) + `","signatures":[]}`
}

func TestDecode(t *testing.T) {
	cosignSPDX := statement("https://spdx.dev/Document", `{"Data":`+quote(spdxDoc)+`}`)
	bundle := `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","dsseEnvelope":` + dsse(statement(
		"https://cyclonedx.org/bom/v1.5", cdxDoc)) + `}`

	tests := []struct {
		name      string
		in        string
		format    string
		predicate string
		names     []string
		wantErr   error
	}{
		{"raw SPDX", spdxDoc, FormatSPDXJSON, "", []string{"lodash", "openssl"}, nil},
		{"raw CycloneDX", cdxDoc, FormatCycloneDX, "", []string{"requests"}, nil},
		{"SPDX tag-value", tagValue, FormatSPDX, "", []string{"zlib"}, nil},
		{"DSSE of an SPDX statement", dsse(cosignSPDX), FormatSPDXJSON, "https://spdx.dev/Document", []string{"lodash", "openssl"}, nil},
		{"bare CycloneDX statement", statement("https://cyclonedx.org/bom", cdxDoc), FormatCycloneDX, "https://cyclonedx.org/bom",
			[]string{"requests"}, nil},
		{
			"sigstore bundle with a versioned predicate",
			bundle,
			FormatCycloneDX,
			"https://cyclonedx.org/bom/v1.5",
			[]string{"requests"},
			nil,
		},
		{"statement of another predicate", statement("https://slsa.dev/provenance/v1", `{"buildType":"x"}`), "", "", nil, ErrNotSBOM},
		{"garbage", "not json at all", "", "", nil, ErrNotSBOM},
		{"empty", "", "", "", nil, ErrNotSBOM},
		{"JSON that is no SBOM", `{"hello":"world"}`, "", "", nil, ErrNotSBOM},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Decode([]byte(tt.in))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			if doc.Format != tt.format || doc.PredicateType != tt.predicate {
				t.Errorf("format/predicate = %q/%q, want %q/%q", doc.Format, doc.PredicateType, tt.format, tt.predicate)
			}

			var got []string
			for _, c := range doc.Components {
				got = append(got, c.Name)
			}

			if strings.Join(got, ",") != strings.Join(tt.names, ",") {
				t.Errorf("components = %v, want %v", got, tt.names)
			}
		})
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDecodeKeepsPURLsAndLicenses(t *testing.T) {
	doc, err := Decode([]byte(spdxDoc))
	if err != nil {
		t.Fatal(err)
	}

	if got := doc.Components[0]; got.PURL != "pkg:npm/lodash@4.17.20" || len(got.Licenses) != 1 || got.Licenses[0] != "MIT" {
		t.Errorf("component = %+v", got)
	}
}

func TestDecodeRefusesTooManyComponents(t *testing.T) {
	var b strings.Builder

	b.WriteString(`{"bomFormat":"CycloneDX","specVersion":"1.5","components":[`)

	for i := range MaxComponents + 1 {
		if i > 0 {
			b.WriteByte(',')
		}

		fmt.Fprintf(&b, `{"name":"p%d","version":"1"}`, i)
	}

	b.WriteString(`]}`)

	if _, err := Decode([]byte(b.String())); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		push Push
		want string
		ok   bool
	}{
		{"cosign sbom tag", Push{Tag: "sha256-" + strings.Repeat("a", 64) + ".sbom"}, subject, true},
		{"cosign attestation tag", Push{Tag: "sha256-" + strings.Repeat("a", 64) + ".att"}, subject, true},
		{"cosign signature tag", Push{Tag: "sha256-" + strings.Repeat("a", 64) + ".sig"}, "", false},
		{"plain tag", Push{Tag: "latest"}, "", false},
		{"spdx referrer", Push{ArtifactType: "application/spdx+json", SubjectDigest: subject}, subject, true},
		{"cyclonedx referrer", Push{ArtifactType: "application/vnd.cyclonedx+json", SubjectDigest: subject}, subject, true},
		{"referrer of another type", Push{ArtifactType: "application/vnd.example", SubjectDigest: subject}, "", false},
		{"SBOM type without a subject", Push{ArtifactType: "application/spdx+json"}, "", false},
		{"bundle referrer carrying an SBOM", Push{
			ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", SubjectDigest: subject,
			Annotations: map[string]string{"dev.sigstore.bundle.predicateType": "https://spdx.dev/Document"},
		}, subject, true},
		{"bundle referrer carrying provenance", Push{
			ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", SubjectDigest: subject,
			Annotations: map[string]string{"dev.sigstore.bundle.predicateType": "https://slsa.dev/provenance/v1"},
		}, "", false},
		{"OpenArtifact's own attestation referrer", Push{
			ArtifactType: "application/spdx+json", SubjectDigest: subject,
			Annotations: map[string]string{"dev.openartifact.attestation.kind": "sbom"},
		}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := Classify(tt.push)
			if ok != tt.ok || got != tt.want {
				t.Errorf("Classify = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// fakeBlobs is an in-memory blob store keyed by the sha256 of the content.
type fakeBlobs struct {
	storage.BlobStore

	mu   sync.Mutex
	data map[string][]byte
}

func newFakeBlobs() *fakeBlobs { return &fakeBlobs{data: map[string][]byte{}} }

func (f *fakeBlobs) ExistsBySHA256(_ context.Context, sha string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	_, ok := f.data[sha]

	return sha, ok, nil
}

func (f *fakeBlobs) Put(_ context.Context, r io.Reader, meta map[string]string) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.data[meta["sha256"]] = b

	return meta["sha256"], nil
}

func (f *fakeBlobs) Get(_ context.Context, id string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.data[id]
	if !ok {
		return nil, errors.New("not found")
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeBlobs) add(content string) string {
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	f.data[sha] = []byte(content)

	return sha
}

type fakeLocker struct{}

func (fakeLocker) WithBlobLock(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// fakeReports stores reports like the database does.
type fakeReports struct{ reports []*models.SBOMReport }

func (f *fakeReports) CreateSBOMReport(_ context.Context, r *models.SBOMReport) error {
	f.reports = append(f.reports, r)
	return nil
}

func (f *fakeReports) GetSBOMReportsByArtifact(_ context.Context, repoID, artifactID string) ([]*models.SBOMReport, error) {
	var out []*models.SBOMReport

	for _, r := range f.reports {
		if r.RepositoryID == repoID && r.ArtifactID == artifactID {
			out = append(out, r)
		}
	}

	return out, nil
}

func manifestOf(mediaType string, shas ...string) []byte {
	var layers []map[string]any
	for _, s := range shas {
		layers = append(layers, map[string]any{"mediaType": mediaType, "digest": "sha256:" + s, "size": 10})
	}

	b, _ := json.Marshal(map[string]any{"schemaVersion": 2, "layers": layers})

	return b
}

func TestIngestThenRead(t *testing.T) {
	tag := "sha256-" + strings.Repeat("a", 64)

	tests := []struct {
		name      string
		tag       string
		mediaType string
		content   string
		want      []string
	}{
		{".sbom tag with an SPDX layer", tag + ".sbom", "text/spdx+json", spdxDoc, []string{"lodash", "openssl"}},
		{".sbom tag with a CycloneDX layer", tag + ".sbom", "application/vnd.cyclonedx+json", cdxDoc, []string{"requests"}},
		{".att tag with a DSSE layer", tag + ".att", "application/vnd.dsse.envelope.v1+json",
			dsse(statement("https://spdx.dev/Document", `{"Data":`+quote(spdxDoc)+`}`)), []string{"lodash", "openssl"}},
		{".att tag with a CycloneDX statement", tag + ".att", "application/vnd.dsse.envelope.v1+json",
			dsse(statement("https://cyclonedx.org/bom", cdxDoc)), []string{"requests"}},
		{".att tag carrying provenance only", tag + ".att", "application/vnd.dsse.envelope.v1+json",
			dsse(statement("https://slsa.dev/provenance/v0.2", `{}`)), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobs, reports := newFakeBlobs(), &fakeReports{}
			sha := blobs.add(tt.content)

			var scanned []string

			ing := NewIngestor(reports, fakeLocker{}, blobs, testLog(t)).
				OnIngested(func(_ context.Context, repoID, digest string) { scanned = append(scanned, repoID+"/"+digest) })

			err := ing.Handle(t.Context(), Push{RepoID: "r1", RepoName: "img", Tag: tt.tag, Manifest: manifestOf(tt.mediaType, sha)})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}

			rd := NewReader(reports, blobs)

			has, err := rd.HasSBOM(t.Context(), "r1", subject)
			if err != nil || has != (tt.want != nil) {
				t.Fatalf("HasSBOM = %v, %v; want %v", has, err, tt.want != nil)
			}

			if tt.want == nil {
				if len(scanned) != 0 {
					t.Errorf("scan started for an image without SBOM: %v", scanned)
				}

				return
			}

			comps, err := rd.Components(t.Context(), "r1", subject)
			if err != nil {
				t.Fatalf("Components: %v", err)
			}

			var got []string
			for _, c := range comps {
				got = append(got, c.Name)
			}

			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("components = %v, want %v", got, tt.want)
			}

			if len(scanned) != 1 || scanned[0] != "r1/"+subject {
				t.Errorf("onIngested = %v", scanned)
			}
		})
	}
}

func TestReaderWithoutSBOM(t *testing.T) {
	rd := NewReader(&fakeReports{}, newFakeBlobs())

	if _, err := rd.Components(t.Context(), "r1", subject); !errors.Is(err, ErrNoSBOM) {
		t.Fatalf("err = %v, want ErrNoSBOM", err)
	}
}

func TestReaderMergesReportsWithoutDuplicates(t *testing.T) {
	blobs, reports := newFakeBlobs(), &fakeReports{}
	ing := NewIngestor(reports, fakeLocker{}, blobs, testLog(t))
	tag := "sha256-" + strings.Repeat("a", 64) + ".sbom"

	for _, doc := range []string{spdxDoc, spdxDoc, cdxDoc} {
		sha := blobs.add(doc)
		if err := ing.Handle(t.Context(), Push{RepoID: "r1", Tag: tag, Manifest: manifestOf("application/json", sha)}); err != nil {
			t.Fatal(err)
		}
	}

	comps, err := NewReader(reports, blobs).Components(t.Context(), "r1", subject)
	if err != nil || len(comps) != 3 {
		t.Fatalf("components = %d, %v; want 3 distinct", len(comps), err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, s := range []string{spdxDoc, cdxDoc, tagValue, dsse(statement("https://cyclonedx.org/bom", cdxDoc)),
		`{"dsseEnvelope":{"payload":""}}`, `{"Data":"x"}`, `{"@context":"https://spdx.org/rdf/3.0.1/spdx-context.jsonld"}`, ""} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Decode(data)
		if err != nil {
			return
		}

		if doc.Format == "" || len(doc.Body) == 0 || len(doc.Components) > MaxComponents {
			t.Fatalf("inconsistent document: %+v", doc)
		}
	})
}
