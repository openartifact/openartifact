// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocimanifest"
)

// FuzzParseContentRange feeds arbitrary Content-Range header values of chunked
// blob uploads: a successful parse is a non-negative, ordered range that
// survives being rendered and parsed again, and the chunk offset check accepts
// exactly the contiguous start.
func FuzzParseContentRange(f *testing.F) {
	for _, s := range []string{
		"0-99", "100-199", " 5 - 10 ", "", "-", "0-", "-1-5", "5-3",
		"bytes 0-1/2", "0--0", "+1-+2", "9223372036854775807-9223372036854775807",
		"9223372036854775808-9223372036854775809",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, header string) {
		start, end, err := parseContentRange(header)
		if err != nil {
			return
		}

		if start < 0 || end < start {
			t.Fatalf("parseContentRange(%q) = %d, %d: not an ordered non-negative range", header, start, end)
		}

		start2, end2, err := parseContentRange(fmt.Sprintf("%d-%d", start, end))
		if err != nil || start2 != start || end2 != end {
			t.Fatalf("parseContentRange(%q) = %d, %d does not survive rendering: got %d, %d, %v", header, start, end, start2, end2, err)
		}

		if err := validateChunkOffset(start, start); err != nil {
			t.Fatalf("validateChunkOffset(%d, %d) = %v, want nil", start, start, err)
		}

		if start < math.MaxInt64 {
			if err := validateChunkOffset(start+1, start); !errors.Is(err, ErrChunkOffsetMismatch) {
				t.Fatalf("validateChunkOffset(%d, %d) = %v, want ErrChunkOffsetMismatch", start+1, start, err)
			}
		}
	})
}

// FuzzParseOCIManifestMeta feeds arbitrary manifest bodies to the Referrers
// metadata extraction: content that is not JSON yields no metadata, and the
// extracted metadata is stable when rendered and parsed again.
func FuzzParseOCIManifestMeta(f *testing.F) {
	for _, s := range []string{
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/x-sbom","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[],"subject":{"digest":"sha256:abc","size":1},"annotations":{"org.opencontainers.image.licenses":"MIT"}}`,
		`{"subject":null,"annotations":null}`,
		`{"SUBJECT":{"Digest":"sha256:abc"},"Annotations":{"a":"b"}}`,
		`{"subject":[],"annotations":{"a":1}}`,
		`{"schemaVersion":1,"fsLayers":[{"blobSum":"sha256:abc"}]}`,
		`[]`, `null`, `{`, ``,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		meta := parseOCIManifestMeta(body)

		if !json.Valid(body) && !reflect.DeepEqual(meta, ociManifestMeta{}) {
			t.Fatalf("parseOCIManifestMeta(%q) = %+v, want zero value for invalid JSON", body, meta)
		}

		rendered, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("marshal %+v: %v", meta, err)
		}

		if again := parseOCIManifestMeta(rendered); !reflect.DeepEqual(meta, again) {
			t.Fatalf("metadata of %q is not stable: %+v then %+v", body, meta, again)
		}
	})
}

// FuzzReferrerManifest renders the referrer manifest of arbitrary requests: the
// output is deterministic, and what the Referrers API reads back from it
// (subject, artifactType, annotations, referenced blobs) is what was put in.
func FuzzReferrerManifest(f *testing.F) {
	f.Add("application/vnd.cyclonedx+json", "sha256:aaaa", "application/vnd.oci.image.manifest.v1+json", "sha256:bbbb", int64(7), "k", "v", []byte("{}"))
	f.Add("", "", "", "", int64(-1), "", "", []byte(nil))
	f.Add("a/b\"c\\", "sha256:x", "text/html", "sha256:y", int64(math.MaxInt64), "a\x00b", "\xff", []byte{0xff})

	f.Fuzz(func(t *testing.T, artifactType, layerDigest, subjectType, subjectDigest string, subjectSize int64, annKey, annValue string, content []byte) {
		subject := &models.DockerManifest{MediaType: subjectType, Digest: subjectDigest, Size: subjectSize}
		req := ReferrerRequest{
			SubjectDigest: subjectDigest,
			ArtifactType:  artifactType,
			Content:       content,
			Annotations:   map[string]string{annKey: annValue},
		}

		body, err := referrerManifest(subject, layerDigest, req)
		if err != nil {
			t.Fatalf("referrerManifest: %v", err)
		}

		again, err := referrerManifest(subject, layerDigest, req)
		if err != nil || !bytes.Equal(body, again) {
			t.Fatalf("referrerManifest is not deterministic: %q vs %q (%v)", body, again, err)
		}

		// json.Marshal replaces invalid UTF-8 with U+FFFD, so only valid strings round-trip exactly.
		if !allValidUTF8(artifactType, layerDigest, subjectType, subjectDigest, annKey, annValue) {
			return
		}

		meta := parseOCIManifestMeta(body)

		if meta.Subject == nil || meta.Subject.Digest != subjectDigest {
			t.Fatalf("subject digest of %s = %+v, want %q", body, meta.Subject, subjectDigest)
		}

		if meta.ArtifactType != artifactType {
			t.Fatalf("artifactType of %s = %q, want %q", body, meta.ArtifactType, artifactType)
		}

		if meta.Annotations[annKey] != annValue || len(meta.Annotations) != 1 {
			t.Fatalf("annotations of %s = %v, want {%q: %q}", body, meta.Annotations, annKey, annValue)
		}

		if meta.Config == nil || meta.Config.MediaType != mediaTypeOCIEmpty || meta.Config.Digest != emptyConfigDigest {
			t.Fatalf("config of %s = %+v, want the empty descriptor", body, meta.Config)
		}

		refs := ocimanifest.ParseRefs(body)

		want := []string{emptyConfigDigest}
		if layerDigest != "" && layerDigest != emptyConfigDigest {
			want = append(want, layerDigest)
		}

		if !slices.Equal(refs.Blobs, want) || len(refs.Manifests) != 0 {
			t.Fatalf("refs of %s = %+v, want blobs %v and no manifests", body, refs, want)
		}
	})
}

// FuzzDockerManifestHandler feeds arbitrary bodies to the docker format handler:
// a manifest that validates parses, and a parsed manifest is a JSON object.
func FuzzDockerManifestHandler(f *testing.F) {
	for _, s := range []string{
		`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`,
		`{"schemaVersion":2}`, `{"mediaType":"x"}`, `{}`, `[]`, `null`, `"x"`, `{`, ``,
	} {
		f.Add([]byte(s))
	}

	h := NewDockerHandler()

	f.Fuzz(func(t *testing.T, body []byte) {
		ctx := context.Background()

		parsed, parseErr := h.Parse(ctx, bytes.NewReader(body))
		validateErr := h.Validate(ctx, bytes.NewReader(body))

		if validateErr == nil && parseErr != nil {
			t.Fatalf("Validate accepted %q but Parse failed: %v", body, parseErr)
		}

		if validateErr == nil {
			_, hasMediaType := parsed["mediaType"]
			_, hasSchema := parsed["schemaVersion"]

			if !hasMediaType && !hasSchema {
				t.Fatalf("Validate accepted %q without mediaType or schemaVersion", body)
			}
		}
	})
}

// fuzzCatalog serves the names "img00000", "img00001", ... in bytewise order,
// the contract of database.DockerCatalog.
type fuzzCatalog struct {
	names []string
	repos map[string]*models.Repository
}

func (c *fuzzCatalog) ListDockerCatalog(_ context.Context, after string, limit int) ([]*models.DockerCatalogEntry, error) {
	i := sort.Search(len(c.names), func(i int) bool { return c.names[i] > after })

	var out []*models.DockerCatalogEntry

	for ; i < len(c.names) && len(out) < limit; i++ {
		repo := c.repos[c.names[i]]
		out = append(out, &models.DockerCatalogEntry{Name: c.names[i], Repository: repo, Sources: []*models.Repository{repo}})
	}

	return out, nil
}

// FuzzCollectCatalog pages through a catalog of up to a few thousand names with
// arbitrary readability and page size: the pages, followed through their
// resume names, list every readable name after the start exactly once, in
// order, however many are unreadable.
func FuzzCollectCatalog(f *testing.F) {
	f.Add(uint16(10), []byte{0xff}, "", uint8(3))
	f.Add(uint16(1200), []byte{0x01, 0x00, 0x00}, "img00010", uint8(7))
	f.Add(uint16(600), []byte{0x00}, "", uint8(1))
	f.Add(uint16(0), []byte(nil), "zzz", uint8(0))

	f.Fuzz(func(t *testing.T, count uint16, mask []byte, last string, size uint8) {
		cat := &fuzzCatalog{repos: map[string]*models.Repository{}}
		readable := map[string]bool{}

		var want []string

		for i := range int(count) % 2500 {
			name := fmt.Sprintf("img%05d", i)
			cat.names = append(cat.names, name)
			cat.repos[name] = &models.Repository{ID: name}
			readable[name] = len(mask) > 0 && mask[i%len(mask)]&(1<<(i%8)) != 0

			if readable[name] && name > last {
				want = append(want, name)
			}
		}

		canRead := func(repo *models.Repository, _ string) (bool, error) { return readable[repo.ID], nil }
		n := int(size)%50 + 1

		var got []string

		for pages := 0; ; pages++ {
			if pages > len(cat.names)+1 {
				t.Fatalf("paging did not terminate after %d pages", pages)
			}

			names, more, err := collectCatalog(context.Background(), cat, canRead, last, n)
			if err != nil {
				t.Fatalf("collectCatalog: %v", err)
			}

			if len(names) > n {
				t.Fatalf("page of %d names exceeds n=%d", len(names), n)
			}

			got = append(got, names...)

			if !more {
				break
			}

			if len(names) != n {
				t.Fatalf("a page with more behind it holds %d names, want n=%d", len(names), n)
			}

			last = names[len(names)-1]
		}

		if !slices.Equal(got, want) {
			t.Fatalf("paged catalog = %v, want %v", got, want)
		}
	})
}

func allValidUTF8(list ...string) bool {
	for _, s := range list {
		if !utf8.ValidString(s) {
			return false
		}
	}

	return true
}
