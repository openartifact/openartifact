// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const testPartSize = storage.MinPartSize

// multipartStore adds an in-memory multipart staging area to stagingBlobStore:
// parts are kept per upload and CompleteMultipart assembles them like S3 does,
// after checking the planned part sizes.
type multipartStore struct {
	*stagingBlobStore

	parts     map[string]map[int][]byte
	initiated []string
	aborted   []string
}

func (m *multipartStore) MultipartSupported() bool { return true }

func (m *multipartStore) InitiateMultipart(_ context.Context, id string, _, _ int64) (string, error) {
	m.initiated = append(m.initiated, id)
	m.parts[id] = map[int][]byte{}

	return "handle-" + id, nil
}

func (m *multipartStore) PresignStagingPart(_ context.Context, id, handle string, number int, length int64, ttl time.Duration) (storage.PresignedPut, error) {
	if handle != "handle-"+id {
		return storage.PresignedPut{}, fmt.Errorf("wrong handle %q", handle)
	}

	return storage.PresignedPut{
		URL:     fmt.Sprintf("https://s3.example/artifacts/staging/%s?partNumber=%d&size=%d&X-Amz-Signature=sig", id, number, length),
		Headers: map[string]string{},
		Expires: time.Now().Add(ttl),
	}, nil
}

func (m *multipartStore) CompleteMultipart(_ context.Context, id, _ string, size, partSize int64) error {
	if _, done := m.staged[id]; done {
		return nil
	}

	var assembled []byte

	for n := 1; n <= storage.PartCount(size, partSize); n++ {
		part, ok := m.parts[id][n]
		if !ok || int64(len(part)) != storage.PartLength(size, partSize, n) {
			return fmt.Errorf("%w: part %d", storage.ErrMultipartIncomplete, n)
		}

		assembled = append(assembled, part...)
	}

	m.staged[id] = assembled

	return nil
}

func (m *multipartStore) DeleteStaging(ctx context.Context, id string) error {
	if _, ok := m.parts[id]; ok {
		m.aborted = append(m.aborted, id)
		delete(m.parts, id)
	}

	return m.stagingBlobStore.DeleteStaging(ctx, id)
}

type multipartFixture struct {
	*presignUploadFixture

	mp *multipartStore
}

func newMultipartFixture(t *testing.T) *multipartFixture {
	t.Helper()

	f := newPresignUploadFixture(t, "raw", "hosted")

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	mp := &multipartStore{stagingBlobStore: f.store, parts: map[string]map[int][]byte{}}

	f.handler = NewPresignedUploadHandler(log, f.db, mp, auth.NewEvaluator(f.db, log, false), 30*time.Minute, 20, nil)
	f.router = chi.NewRouter()
	f.handler.Register(f.router)

	return &multipartFixture{presignUploadFixture: f, mp: mp}
}

func (f *multipartFixture) startMultipart(t *testing.T, content []byte, partSize int64) CreatePresignedUploadResponse {
	t.Helper()

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{
		Path: "releases/big.bin", Size: int64(len(content)), SHA256: digestOf(content), Multipart: true, PartSize: partSize,
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", rw.Code, rw.Body.String())
	}

	var resp CreatePresignedUploadResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	return resp
}

// upload plays the client: it requests the URLs of the parts (a part number is
// a 1-based index into the split content) and "PUTs" each part.
func (f *multipartFixture) upload(t *testing.T, id string, content []byte, partSize int64, numbers ...int) {
	t.Helper()

	rw := f.call(t, false, partsPath(id), PresignedPartsRequest{PartNumbers: numbers})
	if rw.Code != http.StatusOK {
		t.Fatalf("parts status = %d, body %s", rw.Code, rw.Body.String())
	}

	var resp PresignedPartsResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode parts response: %v", err)
	}

	for _, p := range resp.Parts {
		off := int64(p.PartNumber-1) * partSize
		if got := min(partSize, int64(len(content))-off); p.Size != got {
			t.Fatalf("part %d size = %d, want %d", p.PartNumber, p.Size, got)
		}

		f.mp.parts[id][p.PartNumber] = bytes.Clone(content[off : off+p.Size])
	}
}

func partsPath(id string) string { return "/api/v1/repositories/files/uploads/" + id + "/parts" }
func abortPath(id string) string { return "/api/v1/repositories/files/uploads/" + id + "/abort" }

func twoPartContent() []byte {
	return bytes.Repeat([]byte("multipart-"), int(testPartSize)*2/10+37)
}

func TestPresignedMultipart_CreateReturnsPlan(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()

	resp := f.startMultipart(t, content, testPartSize)

	if !resp.Multipart || resp.PartSize != testPartSize || resp.PartCount != 3 || resp.UploadURL != "" || resp.Method != "" {
		t.Errorf("unexpected response %+v", resp)
	}

	row := f.db.uploads[resp.ID]
	if row == nil || row.PartSize != testPartSize || row.MultipartID != "handle-"+resp.ID || row.SizeBytes != int64(len(content)) {
		t.Fatalf("recorded upload = %+v", row)
	}

	if len(f.mp.initiated) != 1 || f.mp.initiated[0] != resp.ID {
		t.Errorf("initiated = %v, want [%s]", f.mp.initiated, resp.ID)
	}
}

func TestPresignedMultipart_DefaultPartSizeKeepsPartCountBounded(t *testing.T) {
	f := newMultipartFixture(t)

	for _, size := range []int64{6 << 30, 3 << 40} {
		rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{
			Path: "big.bin", Size: size, SHA256: digestOf([]byte("x")), Multipart: true,
		})
		if rw.Code != http.StatusCreated {
			t.Fatalf("size %d: status = %d, body %s", size, rw.Code, rw.Body.String())
		}

		var resp CreatePresignedUploadResponse
		if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}

		if resp.PartSize < storage.DefaultPartSize || resp.PartCount < 1 || resp.PartCount > storage.MaxParts {
			t.Errorf("size %d: part size %d, count %d", size, resp.PartSize, resp.PartCount)
		}
	}
}

func TestPresignedMultipart_CreateValidation(t *testing.T) {
	good := digestOf([]byte("x"))

	tests := []struct {
		name string
		body CreatePresignedUploadRequest
		want int
		code string
	}{
		{"above single limit without multipart", CreatePresignedUploadRequest{Path: "a", Size: 6 << 30, SHA256: good}, http.StatusBadRequest, "invalid_size"},
		{"part size without multipart", CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: good, PartSize: 10 << 20}, http.StatusBadRequest, "invalid_part_size"},
		{"empty file", CreatePresignedUploadRequest{Path: "a", Size: 0, SHA256: good, Multipart: true}, http.StatusBadRequest, "invalid_size"},
		{"part size too small", CreatePresignedUploadRequest{Path: "a", Size: 10, SHA256: good, Multipart: true, PartSize: 1 << 20}, http.StatusBadRequest, "invalid_part_size"},
		{"part size too large", CreatePresignedUploadRequest{Path: "a", Size: 10, SHA256: good, Multipart: true, PartSize: 5 << 30}, http.StatusBadRequest, "invalid_part_size"},
		{"too many parts", CreatePresignedUploadRequest{Path: "a", Size: 1 << 40, SHA256: good, Multipart: true, PartSize: 64 << 20}, http.StatusBadRequest, "invalid_part_size"},
		{"above the object limit", CreatePresignedUploadRequest{Path: "a", Size: 6 << 40, SHA256: good, Multipart: true}, http.StatusBadRequest, "invalid_size"},
		{"small file may use multipart", CreatePresignedUploadRequest{Path: "a", Size: 10, SHA256: good, Multipart: true}, http.StatusCreated, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMultipartFixture(t)
			rw := f.call(t, false, "/api/v1/repositories/files/uploads", tc.body)

			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rw.Code, tc.want, rw.Body.String())
			}

			if tc.code != "" && !strings.Contains(rw.Body.String(), tc.code) {
				t.Errorf("body %s lacks code %s", rw.Body.String(), tc.code)
			}

			if tc.want != http.StatusCreated && len(f.db.uploads) != 0 {
				t.Errorf("a refused create left %d upload rows", len(f.db.uploads))
			}
		})
	}
}

// A backend that cannot stage parts answers 501 and records nothing, while
// single uploads on it keep working.
func TestPresignedMultipart_UnsupportedBackend(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{
		Path: "a", Size: 6 << 30, SHA256: digestOf([]byte("x")), Multipart: true,
	})
	if rw.Code != http.StatusNotImplemented || len(f.db.uploads) != 0 {
		t.Fatalf("status = %d, rows %d; body %s", rw.Code, len(f.db.uploads), rw.Body.String())
	}

	if single := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: digestOf([]byte("x"))}); single.Code != http.StatusCreated {
		t.Errorf("single upload status = %d", single.Code)
	}
}

func TestPresignedMultipart_PartsValidationAndAccess(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()
	resp := f.startMultipart(t, content, testPartSize)

	many := make([]int, maxPartsPerRequest+1)
	for i := range many {
		many[i] = i + 1
	}

	for name, nums := range map[string][]int{
		"empty": nil, "zero": {0}, "past the end": {4}, "negative": {-1}, "duplicate": {1, 1}, "too many": many,
	} {
		if rw := f.call(t, false, partsPath(resp.ID), PresignedPartsRequest{PartNumbers: nums}); rw.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rw.Code)
		}
	}

	t.Run("another user", func(t *testing.T) {
		rw := f.callAs(t, userPrincipal("other", "other-1"), partsPath(resp.ID), PresignedPartsRequest{PartNumbers: []int{1}})
		if rw.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rw.Code)
		}
	})

	t.Run("unknown upload", func(t *testing.T) {
		if rw := f.call(t, false, partsPath("00000000-0000-0000-0000-000000000000"), PresignedPartsRequest{PartNumbers: []int{1}}); rw.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rw.Code)
		}
	})

	t.Run("not a multipart upload", func(t *testing.T) {
		single := f.start(t, []byte("tiny"))
		if rw := f.call(t, false, partsPath(single.ID), PresignedPartsRequest{PartNumbers: []int{1}}); rw.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rw.Code)
		}
	})

	t.Run("expired", func(t *testing.T) {
		f.db.uploads[resp.ID].ExpiresAt = time.Now().Add(-time.Minute)

		if rw := f.call(t, false, partsPath(resp.ID), PresignedPartsRequest{PartNumbers: []int{1}}); rw.Code != http.StatusGone {
			t.Errorf("status = %d, want 410", rw.Code)
		}
	})
}

func TestPresignedMultipart_PartsSignExactSizesAndExtendTheUpload(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()
	resp := f.startMultipart(t, content, testPartSize)

	row := f.db.uploads[resp.ID]
	row.ExpiresAt = time.Now().Add(time.Minute)

	rw := f.call(t, false, partsPath(resp.ID), PresignedPartsRequest{PartNumbers: []int{3, 1}})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}

	if rw.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rw.Header().Get("Cache-Control"))
	}

	var parts PresignedPartsResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &parts); err != nil {
		t.Fatal(err)
	}

	last := int64(len(content)) - 2*testPartSize
	want := []struct {
		number int
		size   int64
	}{{3, last}, {1, testPartSize}}

	if len(parts.Parts) != len(want) {
		t.Fatalf("got %d parts, want %d", len(parts.Parts), len(want))
	}

	for i, w := range want {
		p := parts.Parts[i]
		if p.PartNumber != w.number || p.Size != w.size || p.Method != http.MethodPut || !strings.Contains(p.UploadURL, fmt.Sprintf("partNumber=%d&size=%d", w.number, w.size)) {
			t.Errorf("part %d = %+v, want number %d size %d", i, p, w.number, w.size)
		}
	}

	if earliest := time.Now().Add(30*time.Minute + 50*time.Minute); row.ExpiresAt.Before(earliest) {
		t.Errorf("ExpiresAt %v was not extended", row.ExpiresAt)
	}
}

func TestPresignedMultipart_CompleteAssemblesVerifiesAndRecords(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()
	resp := f.startMultipart(t, content, testPartSize)

	f.upload(t, resp.ID, content, testPartSize, 1, 2, 3)

	rw := f.call(t, false, completePath(resp.ID), nil)
	if rw.Code != http.StatusCreated {
		t.Fatalf("complete status = %d, body %s", rw.Code, rw.Body.String())
	}

	art := f.db.artifacts["releases/big.bin"]
	if art == nil || art.SHA256 != digestOf(content) || art.SizeBytes != int64(len(content)) {
		t.Fatalf("recorded artifact = %+v", art)
	}

	if !bytes.Equal(f.store.copied[digestOf(content)], content) {
		t.Error("the promoted bytes differ from the assembled upload")
	}

	// A retried complete returns the first result.
	if again := f.call(t, false, completePath(resp.ID), nil); again.Code != http.StatusOK {
		t.Errorf("retried complete status = %d, want 200", again.Code)
	}
}

func TestPresignedMultipart_IncompleteUploadStaysOpen(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()
	resp := f.startMultipart(t, content, testPartSize)

	f.upload(t, resp.ID, content, testPartSize, 1, 3)

	rw := f.call(t, false, completePath(resp.ID), nil)
	if rw.Code != http.StatusConflict || !strings.Contains(rw.Body.String(), "upload_incomplete") {
		t.Fatalf("status = %d, body %s; want 409 upload_incomplete", rw.Code, rw.Body.String())
	}

	row := f.db.uploads[resp.ID]
	if row == nil || row.CompletingSince != nil {
		t.Fatalf("an incomplete upload must stay retryable, row = %+v", row)
	}

	f.upload(t, resp.ID, content, testPartSize, 2)

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
		t.Fatalf("complete after the missing part: status = %d, body %s", rw.Code, rw.Body.String())
	}
}

func TestPresignedMultipart_WrongContentIsRejected(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()
	resp := f.startMultipart(t, content, testPartSize)

	tampered := bytes.Clone(content)
	tampered[10] ^= 0xff

	f.upload(t, resp.ID, tampered, testPartSize, 1, 2, 3)

	rw := f.call(t, false, completePath(resp.ID), nil)
	if rw.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", rw.Code, rw.Body.String())
	}

	if len(f.db.artifacts) != 0 || len(f.db.uploads) != 0 || len(f.mp.staged) != 0 {
		t.Errorf("a rejected upload left artifacts=%d rows=%d staged=%d", len(f.db.artifacts), len(f.db.uploads), len(f.mp.staged))
	}
}

func TestPresignedMultipart_Abort(t *testing.T) {
	f := newMultipartFixture(t)
	content := twoPartContent()

	t.Run("creator discards parts and row", func(t *testing.T) {
		resp := f.startMultipart(t, content, testPartSize)
		f.upload(t, resp.ID, content, testPartSize, 1)

		if rw := f.call(t, false, abortPath(resp.ID), nil); rw.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
		}

		if f.db.uploads[resp.ID] != nil || len(f.mp.parts[resp.ID]) != 0 || len(f.mp.aborted) != 1 {
			t.Errorf("row %v, parts %d, aborted %v", f.db.uploads[resp.ID], len(f.mp.parts[resp.ID]), f.mp.aborted)
		}
	})

	t.Run("another user cannot", func(t *testing.T) {
		resp := f.startMultipart(t, content, testPartSize)

		if rw := f.callAs(t, userPrincipal("other", "other-1"), abortPath(resp.ID), nil); rw.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rw.Code)
		}

		if f.db.uploads[resp.ID] == nil {
			t.Error("a refused abort removed the upload")
		}
	})

	t.Run("a completing upload is busy", func(t *testing.T) {
		resp := f.startMultipart(t, content, testPartSize)
		now := time.Now()
		f.db.uploads[resp.ID].CompletingSince = &now

		if rw := f.call(t, false, abortPath(resp.ID), nil); rw.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rw.Code)
		}
	})

	t.Run("a completed upload cannot be aborted", func(t *testing.T) {
		resp := f.startMultipart(t, content, testPartSize)
		f.upload(t, resp.ID, content, testPartSize, 1, 2, 3)

		if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
			t.Fatalf("complete status = %d", rw.Code)
		}

		if rw := f.call(t, false, abortPath(resp.ID), nil); rw.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rw.Code)
		}
	})
}

func TestCompleteTimeout(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want time.Duration
	}{
		{"small", 1 << 20, presignedCompleteTimeout},
		{"5 GiB", 5 << 30, presignedCompleteTimeout + time.Duration(5<<30/presignedCompleteScan)*time.Second},
		{"huge is capped", 4 << 40, presignedCompleteMaxTimeout},
	}

	for _, tc := range tests {
		if got := completeTimeout(tc.size); got != tc.want {
			t.Errorf("%s: completeTimeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func (d *presignUploadDB) SetPresignedUploadMultipartID(_ context.Context, id, handle string) error {
	if u := d.uploads[id]; u != nil {
		u.MultipartID = handle
	}

	return nil
}

func (d *presignUploadDB) ExtendPresignedUploadExpiry(_ context.Context, id string, expiresAt time.Time) error {
	if u := d.uploads[id]; u != nil && expiresAt.After(u.ExpiresAt) {
		u.ExpiresAt = expiresAt
	}

	return nil
}
