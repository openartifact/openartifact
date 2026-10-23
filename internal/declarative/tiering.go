// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

const (
	tierPath   = "/api/v1/admin/tiering/tiers"
	policyPath = "/api/v1/admin/tiering/policies"
)

var (
	errTierNoBackend    = errors.New("a storage tier needs a backend: the primary tier is created by the server and cannot be created declaratively")
	errTierPolicyFromTo = errors.New("from_tier must differ from target_tier")
)

// tierItems is the list answer of the tiering endpoints.
type tierItems struct {
	Items []map[string]any `json:"items"`
}

func tierList(ctx context.Context, env *Env, path, what string) ([]map[string]any, error) {
	var resp tierItems
	if err := env.Client.Get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}

	out := make([]map[string]any, len(resp.Items))

	for i, it := range resp.Items {
		m, _ := Canon(it).(map[string]any)
		out[i] = m
	}

	return out, nil
}

// ---- StorageTier ----

type tierKind struct{}

// NewStorageTierKind returns the StorageTier kind. The key is the tier name.
// spec.backend names a storage backend that must already exist (StorageBackend
// is not a managed kind); the server-created primary tier has a null backend,
// is never pruned and cannot be created declaratively.
func NewStorageTierKind() Kind { return tierKind{} }

func (tierKind) Name() string { return KindStorageTier }

func (tierKind) Rank() int { return 80 }

func (tierKind) MetaKeys() []string { return nil }

func (tierKind) Key(o *Object) string { return o.Name }

func (tierKind) Order(*Object) int { return 0 }

func (tierKind) Refs(*Object) []Ref { return nil }

// Schema describes the spec of a StorageTier.
func (tierKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"backend":         {Type: TString, Nullable: true, Immutable: true},
		"rank":            {Type: TInt, HasMin: true},
		"promote_on_read": {Type: TBool},
	}}
}

func tierLive(m map[string]any) *Live {
	backend := m["backend"]
	if s, ok := backend.(string); !ok || s == "" {
		backend = nil
	}

	return &Live{
		Kind: KindStorageTier,
		Key:  Str(m, "name"),
		ID:   Str(m, "id"),
		Spec: map[string]any{
			"backend":         backend,
			"rank":            m["rank"],
			"promote_on_read": Bool(m, "promote_on_read"),
		},
		Seeded: Bool(m, "primary"),
	}
}

func (tierKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	items, err := tierList(ctx, env, tierPath, "storage tiers")
	if err != nil {
		return nil, err
	}

	out := make([]*Live, len(items))
	for i, it := range items {
		out[i] = tierLive(it)
	}

	return out, nil
}

func (tierKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	if Str(o.Spec, "backend") == "" {
		return "", fmt.Errorf("create storage tier %q: %w", o.Name, errTierNoBackend)
	}

	body := Pick(o.Spec, "backend", "rank", "promote_on_read")
	body["name"] = o.Name

	var resp struct {
		ID string `json:"id"`
	}

	if err := env.Client.Do(ctx, http.MethodPost, tierPath, body, &resp); err != nil {
		return "", fmt.Errorf("create storage tier %q: %w", o.Name, err)
	}

	return resp.ID, nil
}

func (tierKind) Update(ctx context.Context, env *Env, live *Live, _ *Object, changes []Change) (string, error) {
	if len(changes) == 0 {
		return live.ID, nil
	}

	if err := env.Client.Do(ctx, http.MethodPatch, tierPath+"/"+PathEscape(live.Key), PatchBody(changes), nil); err != nil {
		return live.ID, fmt.Errorf("update storage tier %q: %w", live.Key, err)
	}

	return live.ID, nil
}

func (tierKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, tierPath+"/"+PathEscape(live.Key), nil, nil); err != nil {
		return fmt.Errorf("delete storage tier %q: %w", live.Key, err)
	}

	return nil
}

// ---- TierPolicy ----

type policyKind struct{}

// NewTierPolicyKind returns the TierPolicy kind. The key is the policy name,
// which the server enforces to be unique.
func NewTierPolicyKind() Kind { return policyKind{} }

func (policyKind) Name() string { return KindTierPolicy }

func (policyKind) Rank() int { return 90 }

func (policyKind) MetaKeys() []string { return nil }

func (policyKind) Key(o *Object) string { return o.Name }

func (policyKind) Order(*Object) int { return 0 }

// policyFields lists every spec field of a TierPolicy, in no particular order.
func policyFields() []string {
	return []string{
		"enabled", "position", "target_tier", "from_tier", "min_age_seconds", "min_idle_seconds",
		"min_size_bytes", "max_size_bytes", "repo_glob", "formats", "path_glob",
	}
}

func policyNullableInt() *Field { return &Field{Type: TInt, Nullable: true, HasMin: true} }

// Schema describes the spec of a TierPolicy.
func (policyKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"enabled":          {Type: TBool},
		"position":         {Type: TInt, HasMin: true},
		"target_tier":      {Type: TString, Required: true},
		"from_tier":        {Type: TString, Nullable: true},
		"min_age_seconds":  policyNullableInt(),
		"min_idle_seconds": policyNullableInt(),
		"min_size_bytes":   policyNullableInt(),
		"max_size_bytes":   policyNullableInt(),
		"repo_glob":        {Type: TString, Nullable: true},
		"formats":          {Type: TSet, Elem: &Field{Type: TString}},
		"path_glob":        {Type: TString, Nullable: true},
	}}
}

// Validate rejects a policy that moves blobs from a tier to itself.
func (policyKind) Validate(o *Object) []error {
	from := Str(o.Spec, "from_tier")
	if from != "" && from == Str(o.Spec, "target_tier") {
		return []error{errTierPolicyFromTo}
	}

	return nil
}

// Refs lists the tiers a policy moves blobs between.
func (policyKind) Refs(o *Object) []Ref {
	refs := []Ref{{Kind: KindStorageTier, Key: Str(o.Spec, "target_tier"), Field: "target_tier"}}

	if from := Str(o.Spec, "from_tier"); from != "" {
		refs = append(refs, Ref{Kind: KindStorageTier, Key: from, Field: "from_tier"})
	}

	return refs
}

func policyLive(m map[string]any) *Live {
	spec := make(map[string]any, len(policyFields()))

	for _, f := range policyFields() {
		spec[f] = m[f]
	}

	if s, ok := spec["from_tier"].(string); ok && s == "" {
		spec["from_tier"] = nil
	}

	if _, ok := spec["formats"].([]any); !ok {
		spec["formats"] = []any{}
	}

	spec["enabled"] = Bool(m, "enabled")

	return &Live{Kind: KindTierPolicy, Key: Str(m, "name"), ID: Str(m, "id"), Spec: spec}
}

func (policyKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	items, err := tierList(ctx, env, policyPath, "storage tier policies")
	if err != nil {
		return nil, err
	}

	out := make([]*Live, len(items))
	for i, it := range items {
		out[i] = policyLive(it)
	}

	return out, nil
}

func (policyKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := Pick(o.Spec, policyFields()...)
	body["name"] = o.Name

	var resp struct {
		ID string `json:"id"`
	}

	if err := env.Client.Do(ctx, http.MethodPost, policyPath, body, &resp); err != nil {
		return "", fmt.Errorf("create tier policy %q: %w", o.Name, err)
	}

	return resp.ID, nil
}

// Update merges the changes into the live policy and PUTs the whole input,
// because the server replaces a policy as a whole.
func (policyKind) Update(ctx context.Context, env *Env, live *Live, _ *Object, changes []Change) (string, error) {
	if len(changes) == 0 {
		return live.ID, nil
	}

	body := Pick(live.Spec, policyFields()...)
	body["name"] = live.Key

	for _, c := range changes {
		body[c.Path] = c.New
	}

	if err := env.Client.Do(ctx, http.MethodPut, policyPath+"/"+PathEscape(live.ID), body, nil); err != nil {
		return live.ID, fmt.Errorf("update tier policy %q: %w", live.Key, err)
	}

	return live.ID, nil
}

func (policyKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, policyPath+"/"+PathEscape(live.ID), nil, nil); err != nil {
		return fmt.Errorf("delete tier policy %q: %w", live.Key, err)
	}

	return nil
}
