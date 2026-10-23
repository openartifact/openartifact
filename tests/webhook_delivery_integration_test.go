// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
)

// seedWebhookFor creates a webhook (global when repoID is empty) subscribed to events.
func seedWebhookFor(t *testing.T, pg *database.PostgresDB, name, repoID, url, secretEnc string, enabled bool, events ...string) *models.Webhook {
	t.Helper()

	wh := &models.Webhook{
		RepositoryID:   repoID,
		Name:           name,
		URL:            url,
		SecretEnc:      secretEnc,
		Enabled:        enabled,
		Events:         events,
		ContentType:    "application/json",
		SSLVerify:      true,
		MaxRetries:     1,
		TimeoutSeconds: 10,
	}
	if err := pg.CreateWebhook(context.Background(), wh); err != nil {
		t.Fatalf("CreateWebhook(%s): %v", name, err)
	}

	return wh
}

// TestListWebhooksForEvent_SelectsSubscribers runs the lookup behind every
// Dispatch against real Postgres. The event name is bound to an array column
// comparison (`$2 = ANY(events)`), the one statement whose parameter typing
// the in-memory fakes can never exercise.
func TestListWebhooksForEvent_SelectsSubscribers(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	repoA, repoB := newRepo(t, pg, "repo-a", "raw"), newRepo(t, pg, "repo-b", "raw")

	const (
		uploaded = webhook.EventArtifactUploaded
		deleted  = webhook.EventArtifactDeleted
		created  = webhook.EventRepoCreated
	)

	seedWebhookFor(t, pg, "global-up-create", "", "https://example.com/g", "", true, uploaded, created)
	seedWebhookFor(t, pg, "global-deleted", "", "https://example.com/d", "", true, deleted)
	seedWebhookFor(t, pg, "global-disabled", "", "https://example.com/x", "", false, uploaded)
	seedWebhookFor(t, pg, "a-uploaded", repoA, "https://example.com/a", "", true, uploaded)
	seedWebhookFor(t, pg, "b-uploaded", repoB, "https://example.com/b", "", true, uploaded)

	for _, tc := range []struct {
		name, repoID, event string
		want                []string
	}{
		{"global and repo-scoped subscribers of the repo", repoA, uploaded, []string{"a-uploaded", "global-up-create"}},
		{"other repo's scoped webhook is not mixed in", repoB, uploaded, []string{"b-uploaded", "global-up-create"}},
		{"event without repo-scoped subscriber", repoA, created, []string{"global-up-create"}},
		{"event only one global webhook subscribes to", repoA, deleted, []string{"global-deleted"}},
		{"global event without repository", "", uploaded, []string{"global-up-create"}},
		{"event nobody subscribes to", repoA, webhook.EventRepoPurged, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pg.ListWebhooksForEvent(ctx, tc.repoID, tc.event)
			if err != nil {
				t.Fatalf("ListWebhooksForEvent(%q, %q): %v", tc.repoID, tc.event, err)
			}

			names := make([]string, 0, len(got))
			for _, wh := range got {
				names = append(names, wh.Name)
			}

			slices.Sort(names)

			if !slices.Equal(names, tc.want) {
				t.Errorf("subscribers = %v, want %v", names, tc.want)
			}
		})
	}
}

// TestWebhookPayloadFormatPersists round-trips payload_format through every
// read path against real Postgres, and checks the column's CHECK constraint.
func TestWebhookPayloadFormatPersists(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repo := newRepo(t, pg, "fmt-repo", "raw")

	cloud := seedWebhookFor(t, pg, "cloud", "", "https://example.com/c", "", true, webhook.EventArtifactUploaded)
	if cloud.PayloadFormat != "" {
		t.Fatalf("seed must start unset, got %q", cloud.PayloadFormat)
	}

	// An unset format (older backup, older caller) is stored as the default.
	got, err := pg.GetWebhook(ctx, cloud.ID)
	if err != nil || got.PayloadFormat != models.PayloadFormatOpenArtifact {
		t.Fatalf("default format = %q, err %v", got.PayloadFormat, err)
	}

	got.PayloadFormat = models.PayloadFormatCloudEvents
	if err := pg.UpdateWebhook(ctx, got); err != nil {
		t.Fatal(err)
	}

	scoped := &models.Webhook{
		RepositoryID: repo, Name: "slack", URL: "https://example.com/s", Enabled: true,
		Events: []string{webhook.EventArtifactUploaded}, ContentType: "application/json",
		PayloadFormat: models.PayloadFormatSlack, SSLVerify: true, MaxRetries: 1, TimeoutSeconds: 10,
	}
	if err := pg.CreateWebhook(ctx, scoped); err != nil {
		t.Fatal(err)
	}

	byID, _ := pg.GetWebhook(ctx, cloud.ID)
	global, _ := pg.ListWebhooks(ctx, "")
	byRepo, _ := pg.ListWebhooks(ctx, repo)
	forEvent, _ := pg.ListWebhooksForEvent(ctx, repo, webhook.EventArtifactUploaded)

	if byID.PayloadFormat != "cloudevents" || len(global) != 1 || global[0].PayloadFormat != "cloudevents" ||
		len(byRepo) != 1 || byRepo[0].PayloadFormat != "slack" || len(forEvent) != 2 {
		t.Fatalf("format not persisted: get=%q global=%v repo=%v forEvent=%d", byID.PayloadFormat, global, byRepo, len(forEvent))
	}

	for _, wh := range forEvent {
		want := map[string]string{"cloud": "cloudevents", "slack": "slack"}[wh.Name]
		if wh.PayloadFormat != want {
			t.Errorf("ListWebhooksForEvent %s format = %q, want %q", wh.Name, wh.PayloadFormat, want)
		}
	}

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE webhooks SET payload_format = 'xml' WHERE id = $1`, cloud.ID); err == nil {
		t.Fatal("the CHECK constraint accepted an unknown payload_format")
	}
}

// receivedHook is one request that reached the webhook receiver.
type receivedHook struct {
	path      string
	signature string
	body      []byte
}

func (r receivedHook) payload(t *testing.T) map[string]any {
	t.Helper()

	var p map[string]any
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatalf("webhook body is not JSON: %v: %s", err, r.body)
	}

	return p
}

// newHookReceiver starts a loopback receiver that records every delivery.
func newHookReceiver(t *testing.T) (string, <-chan receivedHook) {
	t.Helper()

	got := make(chan receivedHook, 16)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- receivedHook{path: r.URL.Path, signature: r.Header.Get("X-Hub-Signature-256"), body: body}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return srv.URL, got
}

func awaitHook(t *testing.T, got <-chan receivedHook) receivedHook {
	t.Helper()

	select {
	case r := <-got:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("no webhook delivery reached the receiver")
		return receivedHook{}
	}
}

func requireSigned(t *testing.T, r receivedHook, secret string) {
	t.Helper()

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(r.body)

	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); r.signature != want {
		t.Errorf("X-Hub-Signature-256 = %q, want %q", r.signature, want)
	}
}

// requireDeliveriesSucceed waits until wh has exactly wantRows delivery rows,
// every one a success answered with 200.
func requireDeliveriesSucceed(t *testing.T, pg *database.PostgresDB, wh *models.Webhook, wantRows int) {
	t.Helper()

	waitUntil(t, 15*time.Second, "webhook "+wh.Name+" deliveries to succeed", func() bool {
		rows, err := pg.ListWebhookDeliveries(context.Background(), wh.ID, 50)
		if err != nil {
			t.Fatalf("ListWebhookDeliveries(%s): %v", wh.Name, err)
		}

		if len(rows) != wantRows {
			return false
		}

		return !slices.ContainsFunc(rows, func(d *models.WebhookDelivery) bool {
			return d.Status != "success" || d.ResponseStatus != http.StatusOK
		})
	})
}

func requireNoDeliveries(t *testing.T, pg *database.PostgresDB, wh *models.Webhook) {
	t.Helper()

	rows, err := pg.ListWebhookDeliveries(context.Background(), wh.ID, 10)
	if err != nil {
		t.Fatalf("ListWebhookDeliveries(%s): %v", wh.Name, err)
	}

	if len(rows) != 0 {
		t.Errorf("webhook %s has %d deliveries, want none", wh.Name, len(rows))
	}
}

// webhookStack is a real Postgres + Dispatcher behind the repository and raw
// upload handlers, mirroring how cmd/server wires them.
type webhookStack struct {
	pg     *database.PostgresDB
	serve  func(method, target, body string) *httptest.ResponseRecorder
	secret string
	// secretEnc is secret as stored in webhooks.secret_enc.
	secretEnc string
}

func newWebhookStack(t *testing.T) *webhookStack {
	t.Helper()

	pg := freshPostgres(t, 10)
	ctx := context.Background()

	// Receivers listen on loopback, which outbound-URL validation refuses
	// unless explicitly allowed (read when the dispatcher builds its client).
	t.Setenv(netutil.EnvAllowPrivate, "true")

	if err := crypto.Init("webhook-delivery-test-key-0123456789"); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	const secret = "whsec-delivery-test"

	secretEnc, err := crypto.Encrypt(secret)
	if err != nil {
		t.Fatalf("crypto.Encrypt: %v", err)
	}

	log := newTestLogger(t)
	dispatcher := webhook.NewDispatcher(pg, log)
	dispatcher.Start()
	t.Cleanup(dispatcher.Stop)

	db := downloadAdminDB{pg}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	rawHandler := formatraw.NewHTTPHandler(log, db, store, "http://localhost")
	rawHandler.SetPublisher(dispatcher)

	router := chi.NewRouter()
	api.NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithPublisher(dispatcher).RegisterAuthed(router)
	router.Route("/repos/{name}", rawHandler.RegisterRepoRoutes)

	userID := "webhook-admin"

	return &webhookStack{
		pg:        pg,
		secret:    secret,
		secretEnc: secretEnc,
		serve: func(method, target, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "admin"}))

			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)

			return rw
		},
	}
}

func requireRepoCreatedPayload(t *testing.T, r receivedHook, repo string) {
	t.Helper()

	p := r.payload(t)
	if p["event"] != webhook.EventRepoCreated || p["repository"] != repo || p["format"] != "raw" || p["type"] != "hosted" {
		t.Errorf("repository.created payload = %v", p)
	}
}

func requireUploadedPayload(t *testing.T, r receivedHook, repo, path, content string) {
	t.Helper()

	sum := sha256.Sum256([]byte(content))

	p := r.payload(t)
	if p["event"] != webhook.EventArtifactUploaded || p["repository"] != repo || p["format"] != "raw" {
		t.Errorf("%s payload = %v", r.path, p)
	}

	artifact, _ := p["artifact"].(map[string]any)
	if artifact["name"] != path || artifact["path"] != path ||
		artifact["sha256"] != hex.EncodeToString(sum[:]) || artifact["size"] != float64(len(content)) {
		t.Errorf("%s artifact = %v", r.path, artifact)
	}
}

// TestWebhooksDeliveredAfterRepoCreateAndUpload is the end-to-end contract of
// the webhook feature on real Postgres: creating a repository and uploading an
// artifact each publish an event, and the subscribed webhooks (and only those)
// receive a signed POST with the documented payload.
func TestWebhooksDeliveredAfterRepoCreateAndUpload(t *testing.T) {
	stack := newWebhookStack(t)
	pg, ctx := stack.pg, context.Background()
	url, received := newHookReceiver(t)

	otherRepoID := newRepo(t, pg, "other-repo", "raw")
	global := seedWebhookFor(t, pg, "global", "", url+"/global", stack.secretEnc, true,
		webhook.EventRepoCreated, webhook.EventArtifactUploaded)
	otherScoped := seedWebhookFor(t, pg, "other-scoped", otherRepoID, url+"/other", "", true, webhook.EventArtifactUploaded)

	// Creating a repository publishes repository.created to the global webhook.
	if rw := stack.serve(http.MethodPost, "/api/v1/repositories", `{"name":"raw-main","format":"raw","repo_type":"hosted"}`); rw.Code != http.StatusCreated {
		t.Fatalf("create repository: status %d: %s", rw.Code, rw.Body.String())
	}

	created := awaitHook(t, received)
	if created.path != "/global" {
		t.Errorf("repository.created delivered to %q, want /global", created.path)
	}

	requireSigned(t, created, stack.secret)
	requireRepoCreatedPayload(t, created, "raw-main")

	// A webhook scoped to the new repository, created afterwards, only sees
	// what happens in that repository from now on.
	repo, err := pg.GetRepositoryByName(ctx, "raw-main")
	if err != nil || repo == nil {
		t.Fatalf("GetRepositoryByName(raw-main) = %v, %v", repo, err)
	}

	scoped := seedWebhookFor(t, pg, "raw-main-scoped", repo.ID, url+"/raw-main", "", true, webhook.EventArtifactUploaded)

	// Uploading publishes artifact.uploaded to the global and the repo-scoped
	// webhook, and not to the webhook scoped to another repository.
	const content = "hello webhook"

	if rw := stack.serve(http.MethodPut, "/repos/raw-main/dir/file.txt", content); rw.Code != http.StatusCreated {
		t.Fatalf("upload: status %d: %s", rw.Code, rw.Body.String())
	}

	byPath := map[string]receivedHook{}

	for range 2 {
		r := awaitHook(t, received)
		byPath[r.path] = r
	}

	for _, path := range []string{"/global", "/raw-main"} {
		r, ok := byPath[path]
		if !ok {
			t.Fatalf("artifact.uploaded never reached %s (got %d deliveries)", path, len(byPath))
		}

		requireUploadedPayload(t, r, "raw-main", "dir/file.txt", content)
	}

	requireSigned(t, byPath["/global"], stack.secret)

	if sig := byPath["/raw-main"].signature; sig != "" {
		t.Errorf("webhook without a secret was signed: %q", sig)
	}

	// The delivery log records what happened.
	requireDeliveriesSucceed(t, pg, global, 2)
	requireDeliveriesSucceed(t, pg, scoped, 1)
	requireNoDeliveries(t, pg, otherScoped)
}
