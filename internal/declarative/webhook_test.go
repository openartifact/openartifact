// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func webhookJSON(id, name string, extra map[string]any) map[string]any {
	w := map[string]any{
		"id": id, "name": name, "url": "https://hooks.example.com/" + name, "enabled": true,
		"events": []any{"artifact.uploaded", "repository.created"}, "content_type": "application/json",
		"payload_format": "openartifact", "has_secret": false, "ssl_verify": true, "timeout_seconds": 30, "max_retries": 3,
	}

	maps.Copy(w, extra)

	return w
}

func webhookListAnswer(req repoFakeReq) (int, any) {
	if req.Method != http.MethodGet {
		return repoNotFound(req)
	}

	switch req.Path {
	case "/api/v1/repositories":
		return http.StatusOK, map[string]any{"total": 3, "repositories": []any{
			map[string]any{"id": "r1", "name": "r1", "format": "raw", "repo_type": "hosted"},
			map[string]any{"id": "r2", "name": "r2", "format": "raw", "repo_type": "hosted"},
			map[string]any{"id": "r3", "name": "r3", "format": "raw", "repo_type": "hosted"},
		}}
	case "/api/v1/webhooks":
		return http.StatusOK, map[string]any{"webhooks": []any{
			webhookJSON("g1", "audit", map[string]any{
				"has_secret": true, "payload_format": "slack", "ssl_verify": false, "enabled": false,
				"timeout_seconds": 5, "max_retries": 0, "events": []any{"bundle.promoted"},
			}),
		}}
	case "/api/v1/repositories/r1/webhooks":
		return http.StatusOK, map[string]any{"webhooks": []any{webhookJSON("w1", "deploy", nil)}}
	case "/api/v1/repositories/r2/webhooks":
		return http.StatusOK, map[string]any{"webhooks": []any{
			webhookJSON("w2", "dup", nil), webhookJSON("w3", "dup", map[string]any{"url": "https://other.example.com"}),
		}}
	case "/api/v1/repositories/r3/webhooks":
		return http.StatusOK, map[string]any{"webhooks": []any{}}
	}

	return repoNotFound(req)
}

func TestWebhookList(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, webhookListAnswer)

	lives, err := NewWebhookKind().List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		key, id   string
		hasSecret bool
		wantSpec  map[string]any
	}{
		{
			key: "audit", id: "g1", hasSecret: true,
			wantSpec: map[string]any{
				"url": "https://hooks.example.com/audit", "events": []any{"bundle.promoted"}, "content_type": "application/json",
				"payload_format": "slack", "ssl_verify": false, "max_retries": 0.0, "timeout_seconds": 5.0, "enabled": false,
			},
		},
		{
			key: "r1/deploy", id: "w1",
			wantSpec: map[string]any{
				"url": "https://hooks.example.com/deploy", "events": []any{"artifact.uploaded", "repository.created"},
				"content_type": "application/json", "payload_format": "openartifact", "ssl_verify": true,
				"max_retries": 3.0, "timeout_seconds": 30.0, "enabled": true,
			},
		},
		{key: "r2/dup", id: "w2", wantSpec: nil},
		{key: "r2/dup", id: "w3", wantSpec: nil},
	}

	if len(lives) != len(tests) {
		t.Fatalf("got %d webhooks, want %d: %+v", len(lives), len(tests), lives)
	}

	for i, tc := range tests {
		live := lives[i]

		if live.Kind != KindWebhook || live.Key != tc.key || live.ID != tc.id || live.Seeded {
			t.Errorf("lives[%d] identity = %s %q %q seeded=%v, want %q %q", i, live.Kind, live.Key, live.ID, live.Seeded, tc.key, tc.id)
		}

		if got, ok := live.Secrets["secret"]; !ok || got != tc.hasSecret {
			t.Errorf("%s secrets = %v, want secret=%v", tc.key, live.Secrets, tc.hasSecret)
		}

		if tc.wantSpec != nil && !reflect.DeepEqual(live.Spec, tc.wantSpec) {
			t.Errorf("%s spec =\n%#v\nwant\n%#v", tc.key, live.Spec, tc.wantSpec)
		}
	}

	if lives[3].Spec["url"] != "https://other.example.com" {
		t.Errorf("duplicate-key webhooks must both be returned with their own spec, got %v", lives[3].Spec)
	}
}

// TestWebhookLiveIsValidManifest checks the export -> apply loop.
func TestWebhookLiveIsValidManifest(t *testing.T) {
	t.Parallel()

	env, _ := newRepoFake(t, webhookListAnswer)
	kind := NewWebhookKind()

	lives, err := kind.List(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}

	validator, ok := kind.(Validator)
	if !ok {
		t.Fatal("Webhook kind does not implement Validator")
	}

	identifier, ok := kind.(Identifier)
	if !ok {
		t.Fatal("Webhook kind does not implement Identifier")
	}

	schema := kind.Schema()

	for _, live := range lives {
		for k := range live.Spec {
			if _, known := schema.Fields[k]; !known {
				t.Errorf("%s: live field %q is not in the schema", live.Key, k)
			}
		}

		name, meta := identifier.Identity(live)
		o := &Object{Kind: KindWebhook, Name: name, Meta: map[string]string{}, Spec: live.Spec}

		maps.Copy(o.Meta, meta)

		if errs := validator.Validate(o); len(errs) != 0 {
			t.Errorf("%s: live webhook fails validation: %v", live.Key, errs)
		}

		if got := kind.Key(o); got != live.Key {
			t.Errorf("Key(Identity(%q)) = %q", live.Key, got)
		}

		if res := diffSpec(schema, live.Spec, live.Spec, live.Secrets, false); len(res.Changes) != 0 {
			t.Errorf("%s: live spec differs from itself: %v", live.Key, res.Changes)
		}
	}
}

func TestWebhookIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key      string
		wantName string
		wantMeta map[string]string
	}{
		{key: "audit", wantName: "audit"},
		{key: "repo-1/deploy", wantName: "deploy", wantMeta: map[string]string{"repository": "repo-1"}},
		{key: "repo_2/team/deploy", wantName: "team/deploy", wantMeta: map[string]string{"repository": "repo_2"}},
		{key: "has space/x", wantName: "has space/x"},
	}

	identifier, ok := NewWebhookKind().(Identifier)
	if !ok {
		t.Fatal("Webhook kind does not implement Identifier")
	}

	for _, tc := range tests {
		name, meta := identifier.Identity(&Live{Key: tc.key})
		if name != tc.wantName || !reflect.DeepEqual(meta, tc.wantMeta) {
			t.Errorf("Identity(%q) = %q %v, want %q %v", tc.key, name, meta, tc.wantName, tc.wantMeta)
		}
	}
}

func TestWebhookKeyOrderRefs(t *testing.T) {
	t.Parallel()

	kind := NewWebhookKind()

	if kind.Name() != KindWebhook || kind.Rank() != 70 || !reflect.DeepEqual(kind.MetaKeys(), []string{"repository"}) {
		t.Errorf("name/rank/meta = %s %d %v", kind.Name(), kind.Rank(), kind.MetaKeys())
	}

	tests := []struct {
		name     string
		obj      *Object
		wantKey  string
		wantRefs []Ref
	}{
		{name: "global", obj: &Object{Name: "audit", Meta: map[string]string{}}, wantKey: "audit"},
		{
			name: "repository scoped", obj: &Object{Name: "deploy", Meta: map[string]string{"repository": "r1"}},
			wantKey:  "r1/deploy",
			wantRefs: []Ref{{Kind: KindRepository, Key: "r1", Field: "metadata.repository"}},
		},
	}

	for _, tc := range tests {
		if got := kind.Key(tc.obj); got != tc.wantKey {
			t.Errorf("%s: Key = %q, want %q", tc.name, got, tc.wantKey)
		}

		if got := kind.Refs(tc.obj); !reflect.DeepEqual(got, tc.wantRefs) {
			t.Errorf("%s: Refs = %+v, want %+v", tc.name, got, tc.wantRefs)
		}

		if got := kind.Order(tc.obj); got != 0 {
			t.Errorf("%s: Order = %d, want 0", tc.name, got)
		}
	}
}

func TestWebhookCreate(t *testing.T) {
	t.Parallel()

	spec := func() map[string]any {
		return map[string]any{
			"url": "https://hooks.example.com/x", "secret": "s3cr3t", "events": []any{"artifact.uploaded"},
			"payload_format": "teams", "timeout_seconds": 10.0,
		}
	}

	tests := []struct {
		name     string
		obj      *Object
		wantPath string
		wantBody map[string]any
	}{
		{
			name:     "global",
			obj:      &Object{Name: "audit", Meta: map[string]string{}, Spec: spec()},
			wantPath: "/api/v1/webhooks",
			wantBody: map[string]any{
				"name": "audit", "url": "https://hooks.example.com/x", "secret": "s3cr3t", "events": []any{"artifact.uploaded"},
				"payload_format": "teams", "timeout_seconds": 10.0,
			},
		},
		{
			name:     "repository scoped",
			obj:      &Object{Name: "deploy", Meta: map[string]string{"repository": "my-repo"}, Spec: spec()},
			wantPath: "/api/v1/repositories/my-repo/webhooks",
			wantBody: map[string]any{
				"name": "deploy", "url": "https://hooks.example.com/x", "secret": "s3cr3t", "events": []any{"artifact.uploaded"},
				"payload_format": "teams", "timeout_seconds": 10.0,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newRepoFake(t, func(repoFakeReq) (int, any) {
				return http.StatusCreated, map[string]any{"id": "wh-new"}
			})

			id, err := NewWebhookKind().Create(context.Background(), env, tc.obj)
			if err != nil {
				t.Fatal(err)
			}

			if id != "wh-new" {
				t.Errorf("id = %q, want wh-new", id)
			}

			assertRepoMutations(t, fake, []repoFakeReq{{Method: http.MethodPost, Path: tc.wantPath, Body: tc.wantBody}})
		})
	}
}

func TestWebhookUpdateSendsOnlyChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changes []Change
		want    map[string]any
	}{
		{
			name:    "scalars",
			changes: []Change{{Path: "enabled", Old: true, New: false}, {Path: "max_retries", Old: 3.0, New: 5.0}},
			want:    map[string]any{"enabled": false, "max_retries": 5.0},
		},
		{
			name:    "events are replaced as a whole",
			changes: []Change{{Path: "events", Old: []any{"artifact.uploaded"}, New: []any{"artifact.deleted", "artifact.uploaded"}}},
			want:    map[string]any{"events": []any{"artifact.deleted", "artifact.uploaded"}},
		},
		{
			name:    "secret change carries the resolved value",
			changes: []Change{{Path: "secret", New: "s3cr3t", Secret: true}},
			want:    map[string]any{"secret": "s3cr3t"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, fake := newRepoFake(t, func(repoFakeReq) (int, any) { return http.StatusOK, map[string]any{} })
			live := &Live{Kind: KindWebhook, Key: "r1/deploy", ID: "wh-1", Spec: map[string]any{}}

			id, err := NewWebhookKind().Update(context.Background(), env, live, &Object{}, tc.changes)
			if err != nil {
				t.Fatal(err)
			}

			if id != "wh-1" {
				t.Errorf("id = %q, want wh-1", id)
			}

			assertRepoMutations(t, fake, []repoFakeReq{{Method: http.MethodPut, Path: "/api/v1/webhooks/wh-1", Body: tc.want}})
		})
	}
}

func TestWebhookUpdateAndDeleteKeepAPIError(t *testing.T) {
	t.Parallel()

	env, fake := newRepoFake(t, func(req repoFakeReq) (int, any) {
		if req.Method == http.MethodDelete {
			return http.StatusNotFound, map[string]any{"error": "webhook not found"}
		}

		return http.StatusBadRequest, map[string]any{"error": "invalid url"}
	})

	kind := NewWebhookKind()
	live := &Live{Key: "audit", ID: "wh-1"}

	_, err := kind.Update(context.Background(), env, live, &Object{}, []Change{{Path: "url", New: "x"}})

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("update err = %v, want *APIError 400", err)
	}

	err = kind.Delete(context.Background(), env, live)
	if !IsStatus(err, http.StatusNotFound) {
		t.Fatalf("delete err = %v, want 404", err)
	}

	reqs := fake.requests()
	if len(reqs) != 2 || reqs[1].Method != http.MethodDelete || reqs[1].Path != "/api/v1/webhooks/wh-1" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestWebhookDeleteUsesID(t *testing.T) {
	t.Parallel()

	env, fake := newRepoFake(t, func(repoFakeReq) (int, any) { return http.StatusNoContent, nil })

	if err := NewWebhookKind().Delete(context.Background(), env, &Live{Key: "r1/deploy", ID: "wh-9"}); err != nil {
		t.Fatal(err)
	}

	assertRepoMutations(t, fake, []repoFakeReq{{Method: http.MethodDelete, Path: "/api/v1/webhooks/wh-9"}})
}

func TestWebhookValidate(t *testing.T) {
	t.Parallel()

	good := func() map[string]any {
		return map[string]any{"url": "https://hooks.example.com/x", "events": []any{"artifact.uploaded"}}
	}

	withSpec := func(k string, v any) map[string]any {
		s := good()
		s[k] = v

		return s
	}

	tests := []struct {
		name    string
		obj     *Object
		wantErr string
	}{
		{name: "global", obj: &Object{Name: "audit", Meta: map[string]string{}, Spec: good()}},
		{name: "scoped with slash in name", obj: &Object{Name: "a/b", Meta: map[string]string{"repository": "r1"}, Spec: good()}},
		{name: "blank name", obj: &Object{Name: " ", Meta: map[string]string{}, Spec: good()}, wantErr: "metadata.name"},
		{name: "padded name", obj: &Object{Name: " x", Meta: map[string]string{}, Spec: good()}, wantErr: "metadata.name"},
		{
			name:    "global name that looks scoped",
			obj:     &Object{Name: "r1/deploy", Meta: map[string]string{}, Spec: good()},
			wantErr: "must not look like <repository>/<name>",
		},
		{
			name:    "bad repository",
			obj:     &Object{Name: "x", Meta: map[string]string{"repository": "a/b"}, Spec: good()},
			wantErr: "not a valid repository name",
		},
		{name: "non http url", obj: &Object{Name: "x", Meta: map[string]string{}, Spec: withSpec("url", "ftp://x.example.com")}, wantErr: "http(s)"},
		{
			name:    "no events",
			obj:     &Object{Name: "x", Meta: map[string]string{}, Spec: withSpec("events", []any{})},
			wantErr: "between 1 and",
		},
		{
			name:    "too many retries",
			obj:     &Object{Name: "x", Meta: map[string]string{}, Spec: withSpec("max_retries", 11.0)},
			wantErr: "max_retries must be between 0 and 10",
		},
		{
			name:    "timeout above the clamp",
			obj:     &Object{Name: "x", Meta: map[string]string{}, Spec: withSpec("timeout_seconds", 31.0)},
			wantErr: "timeout_seconds must be between 1 and 30",
		},
	}

	kind, ok := NewWebhookKind().(Validator)
	if !ok {
		t.Fatal("Webhook kind does not implement Validator")
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			errs := kind.Validate(tc.obj)
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}

				return
			}

			if joined := errors.Join(errs...); joined == nil || !strings.Contains(joined.Error(), tc.wantErr) {
				t.Fatalf("errors = %v, want one containing %q", joined, tc.wantErr)
			}
		})
	}
}
