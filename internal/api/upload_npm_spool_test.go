// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
)

// spoolDir points the temp directory at a fresh one, so a test can see which
// spooled files a publish leaves behind.
func spoolDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	return dir
}

func spooledFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

// A body that repeats an attachment name, or an attachment's "data" key, spools
// a tarball per occurrence. Only the last is kept, so the earlier files must be
// removed when they are replaced: nothing else points at them afterwards, and a
// writer could fill the temp directory by repeating a key.
func TestParseNPMPublishBodyRemovesReplacedSpoolFiles(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("tarball-bytes"))

	for name, body := range map[string]string{
		"duplicate attachment name": `{"name":"demo","_attachments":{` +
			`"demo-1.0.0.tgz":{"content_type":"application/octet-stream","data":"` + data + `","length":13},` +
			`"demo-1.0.0.tgz":{"content_type":"application/octet-stream","data":"` + data + `","length":13}}}`,
		"duplicate data key": `{"name":"demo","_attachments":{"demo-1.0.0.tgz":{` +
			`"content_type":"application/octet-stream","data":"` + data + `","data":"` + data + `","length":13}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := spoolDir(t)

			_, cleanup, err := parseNPMPublishBody(strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}

			if got := spooledFiles(t, dir); len(got) != 1 {
				t.Errorf("spool files while the publish is handled = %v, want exactly the surviving one", got)
			}

			cleanup()

			if got := spooledFiles(t, dir); len(got) != 0 {
				t.Errorf("spool files after cleanup = %v, want none", got)
			}
		})
	}
}

// blockingBody is a request body that reports when its last byte was read (the
// JSON parser stops at the closing brace, so it may never ask for io.EOF).
type blockingBody struct {
	r    *strings.Reader
	done chan struct{}
}

func (b *blockingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil || b.r.Len() == 0 {
		select {
		case <-b.done:
		default:
			close(b.done)
		}
	}

	return n, err
}

func (*blockingBody) Close() error { return nil }

// The publish slots bound the work a publish does once its body is in; a client
// that is still sending must not hold one, or four slow clients block every
// npm publish. With every slot taken the body has to be read to the end anyway,
// and the publish waits for a slot only after that.
func TestNPMPublishReadsBodyBeforeTakingSlot(t *testing.T) {
	dir := spoolDir(t)

	h := NewNPMUploadHandler(formattest.NewLogger(t), &npmPublishDB{DB: formattest.NewDB()}, formattest.NewBlobStore(), nil)
	for range cap(h.publishSlots) {
		h.publishSlots <- struct{}{}
	}

	payload := []byte("tarball-bytes")
	body := &blockingBody{
		r:    strings.NewReader(npmBody("demo", "1.0.0", base64.StdEncoding.EncodeToString(payload), len(payload), "data-middle")),
		done: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(middleware.WithPrincipal(context.Background(), &auth.Principal{Username: "alice"}))
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/repos/myrepo/demo", body)
	rec := httptest.NewRecorder()
	router := chi.NewRouter()
	h.Register(router)

	finished := make(chan struct{})

	go func() {
		defer close(finished)

		router.ServeHTTP(rec, req)
	}()

	select {
	case <-body.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the publish did not read its body while every slot was taken: it holds a slot before reading")
	}

	// The body is spooled and the publish is waiting for a slot.
	select {
	case <-finished:
		t.Fatalf("the publish finished with every slot taken: %d %s", rec.Code, rec.Body.String())
	case <-time.After(100 * time.Millisecond):
	}

	if got := spooledFiles(t, dir); len(got) != 1 {
		t.Errorf("spool files while waiting for a slot = %v, want the one tarball", got)
	}

	cancel()
	<-finished

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a cancelled wait for a slot = %d, want 503", rec.Code)
	}

	if got := spooledFiles(t, dir); len(got) != 0 {
		t.Errorf("spool files after the publish returned = %v, want none", got)
	}
}
