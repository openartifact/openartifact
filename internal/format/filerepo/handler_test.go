// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package filerepo_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/format/filerepo"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type fileRow struct {
	ID, SHA256, BlobKey string
	Size                int64
	Path                string
	CreatedAt           time.Time
}

// store is an in-memory table shared by the spec closures.
type store struct {
	rows map[string]*fileRow
	seq  int
}

func newSpec(format string, kind models.TrashKind, groupKey string, s *store) filerepo.Spec[fileRow] {
	key := func(c base.ArtifactPath) string {
		return strings.Join([]string{c.Group, c.Module, c.Version, c.Filename}, "|")
	}

	return filerepo.Spec[fileRow]{
		Format:    format,
		TrashKind: kind,
		GroupKey:  groupKey,
		Get: func(_ context.Context, _ database.Database, _ string, c base.ArtifactPath) (*fileRow, error) {
			if row := s.rows[key(c)]; row != nil {
				cp := *row

				return &cp, nil
			}

			return nil, nil // not found is (nil, nil) in the Database contract
		},
		New: func(_ string, c base.ArtifactPath, size int64, sha256Hex string) *fileRow {
			return &fileRow{Path: key(c), Size: size, SHA256: sha256Hex}
		},
		Create: func(_ context.Context, _ database.Database, a *fileRow) error {
			if old := s.rows[a.Path]; old != nil {
				a.ID = old.ID
			} else {
				s.seq++
				a.ID = "r-" + strconv.Itoa(s.seq)
			}

			s.rows[a.Path] = a

			return nil
		},
		Delete: func(_ context.Context, _ database.Database, id string) error {
			for k, row := range s.rows {
				if row.ID == id {
					delete(s.rows, k)
				}
			}

			return nil
		},
		View: func(a *fileRow) filerepo.Row {
			return filerepo.Row{ID: a.ID, SHA256: a.SHA256, BlobKey: a.BlobKey, SizeBytes: a.Size}
		},
		Stamp: func(a *fileRow, blobKey string, createdAt time.Time) {
			a.BlobKey, a.CreatedAt = blobKey, createdAt
		},
	}
}

// quotaDB accepts every quota reservation.
type quotaDB struct{ *formattest.DB }

func (quotaDB) ReserveStorageUsage(context.Context, string, int64, int) (bool, error) {
	return true, nil
}

func (quotaDB) DecrementStorageUsage(context.Context, string, int64, int) error { return nil }

// metaStore records the metadata each blob was stored with.
type metaStore struct {
	*formattest.BlobStore

	meta map[string]string
}

func (m *metaStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	m.meta = metadata

	return m.BlobStore.Put(ctx, data, metadata)
}

const filePath = "org.example/mod/1.0/mod-1.0.jar"

// rig is one handler over an in-memory table.
type rig struct {
	t   *testing.T
	h   *filerepo.Handler[fileRow]
	tbl *store
	bs  *metaStore
	pub *formattest.Publisher
}

func newRig(t *testing.T, spec func(*store) filerepo.Spec[fileRow]) *rig {
	t.Helper()

	r := &rig{t: t, tbl: &store{rows: map[string]*fileRow{}}, bs: &metaStore{BlobStore: formattest.NewBlobStore()}, pub: &formattest.Publisher{}}
	r.h = filerepo.New(spec(r.tbl), formattest.NewLogger(t), quotaDB{formattest.NewDB()}, r.bs, "http://localhost")
	r.h.SetPublisher(r.pub)

	return r
}

func (r *rig) do(method, target, body string) *httptest.ResponseRecorder {
	r.t.Helper()

	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}

	return formattest.Do(r.h.RegisterRepoRoutes, formattest.Request(method, "/repos/myrepo/"+target, rd))
}

func (r *rig) expect(want int, method, target, body string) {
	r.t.Helper()

	if got := r.do(method, target, body).Code; got != want {
		r.t.Fatalf("%s %s = %d, want %d", method, target, got, want)
	}
}

func TestFormatSpecificsFlowThroughTheSharedHandler(t *testing.T) {
	tests := []struct {
		format   string
		kind     models.TrashKind
		groupKey string
	}{
		{"ivy", models.TrashKindIvy, "org"},
		{"sbt", models.TrashKindSBT, "group"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			r := newRig(t, func(s *store) filerepo.Spec[fileRow] { return newSpec(tt.format, tt.kind, tt.groupKey, s) })

			r.expect(http.StatusNotFound, http.MethodGet, filePath, "")
			r.expect(http.StatusCreated, http.MethodPut, filePath, "data")

			if got := r.bs.meta[tt.groupKey]; got != "org.example" {
				t.Errorf("blob metadata[%q] = %q, want org.example (metadata %v)", tt.groupKey, got, r.bs.meta)
			}

			r.expect(http.StatusOK, http.MethodHead, filePath, "")

			if rec := r.do(http.MethodGet, filePath, ""); rec.Code != http.StatusOK || rec.Body.String() != "data" {
				t.Errorf("GET = %d %q, want 200 data", rec.Code, rec.Body.String())
			}

			r.expect(http.StatusCreated, http.MethodPut, filePath, "data")  // same bytes: no-op
			r.expect(http.StatusCreated, http.MethodPut, filePath, "newer") // different bytes replace

			if len(r.tbl.rows) != 1 {
				t.Fatalf("rows after republish = %d, want 1", len(r.tbl.rows))
			}

			r.expect(http.StatusNoContent, http.MethodDelete, filePath, "")

			if len(r.tbl.rows) != 0 {
				t.Errorf("rows after delete = %d, want 0", len(r.tbl.rows))
			}

			types := []string{}
			for _, ev := range r.pub.Events() {
				types = append(types, ev.Type)
			}

			want := []string{webhook.EventArtifactUploaded, webhook.EventArtifactUploaded, webhook.EventArtifactDeleted}
			if !slices.Equal(types, want) {
				t.Errorf("event types = %v, want %v", types, want)
			}
		})
	}
}

func TestInvalidPathsAreRejected(t *testing.T) {
	r := newRig(t, func(s *store) filerepo.Spec[fileRow] { return newSpec("ivy", models.TrashKindIvy, "org", s) })

	r.expect(http.StatusBadRequest, http.MethodPut, "too/short", "x")
	r.expect(http.StatusNotFound, http.MethodGet, "too/short", "")
	r.expect(http.StatusNotFound, http.MethodDelete, "too/short", "")
}
