// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sync/errgroup"
)

const (
	webhookMetaRepository = "repository"
	webhookPath           = "/api/v1/webhooks"
	webhookContentJSON    = "application/json"
	webhookMaxName        = 255
	webhookMaxURL         = 2048
	webhookMaxEvents      = 32
	webhookMaxRetries     = 10
	webhookMaxTimeout     = 30
	webhookFanout         = 8
)

var errWebhookInvalid = errors.New("invalid Webhook")

// webhookEvents mirrors webhook.SubscribableEvents.
func webhookEvents() []string {
	return []string{
		"artifact.uploaded", "artifact.deleted", "repository.created", "repository.deleted",
		"artifact.restored", "artifact.purged", "repository.restored", "repository.purged",
		"bundle.promoted",
	}
}

// webhookPayloadFormats mirrors webhook.PayloadFormats.
func webhookPayloadFormats() []string {
	return []string{"openartifact", "cloudevents", "slack", "teams", "discord"}
}

// webhookKind implements the Webhook manifest kind. Webhook names are not
// unique on the server, so the natural key is the name for a global webhook and
// "<repository>/<name>" for one scoped to a repository (metadata.repository).
type webhookKind struct{}

// NewWebhookKind returns the Webhook kind.
func NewWebhookKind() Kind { return webhookKind{} }

func (webhookKind) Name() string { return KindWebhook }

func (webhookKind) Rank() int { return 70 }

func (webhookKind) MetaKeys() []string { return []string{webhookMetaRepository} }

func (webhookKind) Key(o *Object) string {
	if repo := o.Meta[webhookMetaRepository]; repo != "" {
		return repo + "/" + o.Name
	}

	return o.Name
}

func (webhookKind) Order(*Object) int { return 0 }

func (webhookKind) Refs(o *Object) []Ref {
	repo := o.Meta[webhookMetaRepository]
	if repo == "" {
		return nil
	}

	return []Ref{{Kind: KindRepository, Key: repo, Field: "metadata." + webhookMetaRepository}}
}

// Identity splits a live key back into the webhook name and its repository
// scope. A key only names a repository scope when its first segment is a valid
// repository name; Validate refuses global webhook names that would be
// mistaken for one.
func (webhookKind) Identity(live *Live) (string, map[string]string) {
	repo, name, found := strings.Cut(live.Key, "/")
	if !found || !repoNameRe.MatchString(repo) {
		return live.Key, nil
	}

	return name, map[string]string{webhookMetaRepository: repo}
}

func (webhookKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"url":    {Type: TString, Required: true},
		"secret": {Type: TString, Secret: true},
		"events": {Type: TSet, Required: true, Elem: &Field{Type: TString, Enum: webhookEvents()}},
		"content_type": {
			Type: TString, Enum: []string{webhookContentJSON},
		},
		"payload_format":  {Type: TString, Enum: webhookPayloadFormats()},
		"ssl_verify":      {Type: TBool},
		"max_retries":     {Type: TInt, Min: 0, HasMin: true},
		"timeout_seconds": {Type: TInt, Min: 1, HasMin: true},
		"enabled":         {Type: TBool},
	}}
}

// Validate mirrors the server rules a schema cannot express. Values the server
// clamps (retries, timeout) are rejected instead, since a clamped value would
// differ from the manifest on every run.
func (webhookKind) Validate(o *Object) []error {
	var errs []error

	add := func(format string, args ...any) {
		errs = append(errs, invalidf(errWebhookInvalid, format, args...))
	}

	switch {
	case strings.TrimSpace(o.Name) == "" || strings.TrimSpace(o.Name) != o.Name:
		add("metadata.name must not be blank or have leading/trailing whitespace")
	case len(o.Name) > webhookMaxName:
		add("metadata.name is longer than %d characters", webhookMaxName)
	}

	repo := o.Meta[webhookMetaRepository]
	if repo != "" && !repoNameRe.MatchString(repo) {
		add("metadata.repository %q is not a valid repository name", repo)
	}

	if repo == "" {
		if prefix, _, found := strings.Cut(o.Name, "/"); found && repoNameRe.MatchString(prefix) {
			add("a global webhook name must not look like <repository>/<name>; set metadata.repository for a repository webhook")
		}
	}

	if msg := webhookCheckURL(Str(o.Spec, "url")); msg != "" {
		add("%s", msg)
	}

	if events, ok := o.Spec["events"].([]any); ok && (len(events) == 0 || len(events) > webhookMaxEvents) {
		add("events must list between 1 and %d events", webhookMaxEvents)
	}

	if v, ok := o.Spec["max_retries"].(float64); ok && v > webhookMaxRetries {
		add("max_retries must be between 0 and %d", webhookMaxRetries)
	}

	if v, ok := o.Spec["timeout_seconds"].(float64); ok && v > webhookMaxTimeout {
		add("timeout_seconds must be between 1 and %d", webhookMaxTimeout)
	}

	return errs
}

// webhookCheckURL returns the problem with raw, or "" when it is acceptable
// (or empty, which the schema handles).
func webhookCheckURL(raw string) string {
	if raw == "" {
		return ""
	}

	if len(raw) > webhookMaxURL {
		return fmt.Sprintf("url is longer than %d characters", webhookMaxURL)
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "url must be an absolute http(s) URL"
	}

	return ""
}

// webhookAPI is a webhook as the API returns it.
type webhookAPI struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Enabled        bool     `json:"enabled"`
	Events         []string `json:"events"`
	ContentType    string   `json:"content_type"`
	PayloadFormat  string   `json:"payload_format"`
	HasSecret      bool     `json:"has_secret"`
	SSLVerify      bool     `json:"ssl_verify"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxRetries     int      `json:"max_retries"`
}

// live renders the webhook in manifest shape; repo is its repository name or
// "" for a global webhook.
func (w *webhookAPI) live(repo string) *Live {
	events := make([]any, len(w.Events))
	for i, e := range w.Events {
		events[i] = e
	}

	key := w.Name
	if repo != "" {
		key = repo + "/" + w.Name
	}

	return &Live{
		Kind: KindWebhook,
		Key:  key,
		ID:   w.ID,
		Spec: map[string]any{
			"url":             w.URL,
			"events":          events,
			"content_type":    w.ContentType,
			"payload_format":  w.PayloadFormat,
			"ssl_verify":      w.SSLVerify,
			"max_retries":     float64(w.MaxRetries),
			"timeout_seconds": float64(w.TimeoutSeconds),
			"enabled":         w.Enabled,
		},
		Secrets: map[string]bool{"secret": w.HasSecret},
	}
}

func webhookFetch(ctx context.Context, c *Client, path, repo string) ([]*Live, error) {
	var resp struct {
		Webhooks []webhookAPI `json:"webhooks"`
	}

	if err := c.Get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("list webhooks (%s): %w", path, err)
	}

	out := make([]*Live, len(resp.Webhooks))
	for i := range resp.Webhooks {
		out[i] = resp.Webhooks[i].live(repo)
	}

	return out, nil
}

// List returns the global webhooks followed by those of every repository.
// Webhooks sharing a key are all returned; the engine reports the ambiguity.
func (webhookKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	repos, err := repoList(ctx, env.Client)
	if err != nil {
		return nil, err
	}

	global, err := webhookFetch(ctx, env.Client, webhookPath, "")
	if err != nil {
		return nil, err
	}

	perRepo := make([][]*Live, len(repos))

	var g errgroup.Group

	g.SetLimit(webhookFanout)

	for i := range repos {
		g.Go(func() error {
			lives, err := webhookFetch(ctx, env.Client, repoURL(repos[i].Name, "webhooks"), repos[i].Name)
			perRepo[i] = lives

			return err
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	out := global
	for _, lives := range perRepo {
		out = append(out, lives...)
	}

	return out, nil
}

func (webhookKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	path := webhookPath
	if repo := o.Meta[webhookMetaRepository]; repo != "" {
		path = repoURL(repo, "webhooks")
	}

	body := Omit(o.Spec)
	body["name"] = o.Name

	var resp struct {
		ID string `json:"id"`
	}

	if err := env.Client.Do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return "", fmt.Errorf("create webhook %q: %w", (webhookKind{}).Key(o), err)
	}

	return resp.ID, nil
}

// Update PUTs only the changed fields; the endpoint leaves omitted fields as
// they are.
func (webhookKind) Update(ctx context.Context, env *Env, live *Live, _ *Object, changes []Change) (string, error) {
	if len(changes) == 0 {
		return live.ID, nil
	}

	if err := env.Client.Do(ctx, http.MethodPut, webhookPath+"/"+PathEscape(live.ID), PatchBody(changes), nil); err != nil {
		return live.ID, fmt.Errorf("update webhook %q: %w", live.Key, err)
	}

	return live.ID, nil
}

func (webhookKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, webhookPath+"/"+PathEscape(live.ID), nil, nil); err != nil {
		return fmt.Errorf("delete webhook %q: %w", live.Key, err)
	}

	return nil
}
