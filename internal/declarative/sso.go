// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

const ssoPath = "/api/v1/admin/sso-providers"

// ssoSlugRe mirrors the slug rule of the server.
var ssoSlugRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ssoOIDCSecret is the dotted path of the OIDC client secret.
const ssoOIDCSecret = "oidc_config.client_secret" //nolint:gosec // field path, not a credential

// ssoCreateKeys are the spec fields POST /admin/sso-providers accepts besides
// the slug; "enabled" is left out because the server forces true on create.
func ssoCreateKeys() []string {
	return []string{
		"kind", "display_name", "oidc_config", "saml_config", "claim_mapping",
		"group_mapping", "permission_mapping", "auto_provision_users", "link_by_email",
	}
}

var errSSOInvalid = errors.New("invalid SSOProvider")

// ssoKind implements the SSOProvider manifest kind.
type ssoKind struct{}

// NewSSOProviderKind returns the SSOProvider kind. Its natural key is the
// provider slug (metadata.name); the slug and the provider kind are immutable.
func NewSSOProviderKind() Kind { return ssoKind{} }

func (ssoKind) Name() string { return KindSSOProvider }

func (ssoKind) Rank() int { return 60 }

func (ssoKind) MetaKeys() []string { return nil }

func (ssoKind) Key(o *Object) string { return o.Name }

func (ssoKind) Order(*Object) int { return 0 }

func (ssoKind) Refs(*Object) []Ref { return nil }

func ssoStr() *Field { return &Field{Type: TString} }

func ssoStrSet(nullable bool) *Field {
	return &Field{Type: TSet, Nullable: nullable, Elem: &Field{Type: TString}}
}

func ssoBool() *Field { return &Field{Type: TBool} }

func ssoStringMap() *Field { return &Field{Type: TMap, Elem: &Field{Type: TString}} }

// ssoOIDCFields is the schema of every field of the server's OIDCConfig.
func ssoOIDCFields() map[string]*Field {
	return map[string]*Field{
		"type":                   {Type: TString, Enum: []string{"", "github", "oauth2"}},
		"issuer":                 ssoStr(),
		"client_id":              ssoStr(),
		"client_secret":          {Type: TString, Secret: true},
		"scopes":                 ssoStrSet(true),
		"username_claim":         ssoStr(),
		"email_claim":            ssoStr(),
		"name_claim":             ssoStr(),
		"groups_claim":           ssoStr(),
		"allowed_tenants":        ssoStrSet(false),
		"hosted_domain":          ssoStr(),
		"require_email_verified": ssoBool(),
		"graph_groups":           ssoBool(),
		"graph_endpoint":         ssoStr(),
		"authorization_url":      ssoStr(),
		"token_url":              ssoStr(),
		"userinfo_url":           ssoStr(),
		"auth_base_url":          ssoStr(),
		"api_base_url":           ssoStr(),
		"allowed_orgs":           ssoStrSet(false),
	}
}

// ssoSAMLFields is the schema of every field of the server's SAMLConfig.
func ssoSAMLFields() map[string]*Field {
	return map[string]*Field{
		"idp_metadata_url":             ssoStr(),
		"idp_metadata_xml":             ssoStr(),
		"idp_metadata_refresh_seconds": {Type: TInt, HasMin: true},
		"username_attribute":           ssoStr(),
		"email_attribute":              ssoStr(),
		"name_attribute":               ssoStr(),
		"groups_attribute":             ssoStr(),
		"allow_idp_initiated":          ssoBool(),
	}
}

// Schema describes the spec of an SSOProvider.
func (ssoKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"kind":                 {Type: TString, Required: true, Immutable: true, Enum: []string{"oidc", "saml"}},
		"display_name":         {Type: TString, Required: true},
		"enabled":              ssoBool(),
		"oidc_config":          {Type: TObject, Fields: ssoOIDCFields()},
		"saml_config":          {Type: TObject, Fields: ssoSAMLFields()},
		"claim_mapping":        {Type: TAny},
		"group_mapping":        ssoStringMap(),
		"permission_mapping":   ssoStringMap(),
		"auto_provision_users": ssoBool(),
		"link_by_email":        ssoBool(),
	}}
}

// Validate checks the rules the schema cannot express: the config block must
// match the provider kind and carry what the server requires on create.
func (ssoKind) Validate(o *Object) []error {
	var errs []error

	if !ssoSlugRe.MatchString(o.Name) {
		errs = append(errs, fmt.Errorf("%w: metadata.name %q must match ^[a-z0-9_-]{1,64}$", errSSOInvalid, o.Name))
	}

	oidc, hasOIDC := o.Spec["oidc_config"].(map[string]any)
	saml, hasSAML := o.Spec["saml_config"].(map[string]any)

	switch Str(o.Spec, "kind") {
	case "oidc":
		errs = append(errs, ssoValidateOIDC(oidc, hasOIDC)...)
		if hasSAML {
			errs = append(errs, fmt.Errorf("%w: saml_config is not allowed for kind oidc", errSSOInvalid))
		}
	case "saml":
		errs = append(errs, ssoValidateSAML(saml, hasSAML)...)
		if hasOIDC {
			errs = append(errs, fmt.Errorf("%w: oidc_config is not allowed for kind saml", errSSOInvalid))
		}
	}

	return errs
}

func ssoValidateOIDC(cfg map[string]any, present bool) []error {
	if !present {
		return []error{fmt.Errorf("%w: kind oidc requires oidc_config", errSSOInvalid)}
	}

	var errs []error

	need := func(field string) {
		if Str(cfg, field) == "" {
			errs = append(errs, fmt.Errorf("%w: oidc_config.%s is required", errSSOInvalid, field))
		}
	}

	need("client_id")

	switch Str(cfg, "type") {
	case "":
		need("issuer")
	case "github":
		if len(StrList(cfg, "allowed_orgs")) == 0 {
			errs = append(errs, fmt.Errorf("%w: oidc_config.allowed_orgs is required for type github", errSSOInvalid))
		}
	case "oauth2":
		need("authorization_url")
		need("token_url")
		need("userinfo_url")
	}

	if !ssoHasSecret(cfg["client_secret"]) {
		errs = append(errs, fmt.Errorf("%w: oidc_config.client_secret is required (use secretRef)", errSSOInvalid))
	}

	return errs
}

func ssoHasSecret(v any) bool {
	switch t := v.(type) {
	case SecretRef:
		return t != SecretRef{}
	case string:
		return t != ""
	}

	return false
}

func ssoValidateSAML(cfg map[string]any, present bool) []error {
	if !present {
		return []error{fmt.Errorf("%w: kind saml requires saml_config", errSSOInvalid)}
	}

	if Str(cfg, "idp_metadata_url") == "" && Str(cfg, "idp_metadata_xml") == "" {
		return []error{fmt.Errorf("%w: saml_config requires idp_metadata_url or idp_metadata_xml", errSSOInvalid)}
	}

	return nil
}

// ssoZero is the value a config field has on the server when it was never set.
func ssoZero(f *Field) any {
	switch f.Type {
	case TString:
		return ""
	case TBool:
		return false
	case TInt, TNumber:
		return float64(0)
	case TList, TSet:
		if f.Nullable {
			return nil
		}

		return []any{}
	case TMap, TObject:
		return map[string]any{}
	}

	return nil
}

// ssoNormalizeConfig turns a config object of the API into manifest shape:
// only schema fields, secrets dropped, fields the server omits when empty
// filled with their zero value so that explicit zero values in manifests
// compare equal.
func ssoNormalizeConfig(raw any, fields map[string]*Field) map[string]any {
	src, _ := raw.(map[string]any)
	out := make(map[string]any, len(fields))

	for name, f := range fields {
		if f.Secret {
			continue
		}

		if v, ok := src[name]; ok && (v != nil || f.Nullable) {
			out[name] = Canon(v)

			continue
		}

		out[name] = ssoZero(f)
	}

	return out
}

func ssoMapOrEmpty(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}

	return map[string]any{}
}

// ssoLive maps one API provider to its manifest-shape Live.
func ssoLive(m map[string]any) *Live {
	spec := map[string]any{
		"kind":                 Str(m, "kind"),
		"display_name":         Str(m, "display_name"),
		"enabled":              Bool(m, "enabled"),
		"auto_provision_users": Bool(m, "auto_provision_users"),
		"link_by_email":        Bool(m, "link_by_email"),
		"claim_mapping":        ssoMapOrEmpty(m["claim_mapping"]),
		"group_mapping":        ssoMapOrEmpty(m["group_mapping"]),
		"permission_mapping":   ssoMapOrEmpty(m["permission_mapping"]),
	}

	live := &Live{Kind: KindSSOProvider, Key: Str(m, "slug"), ID: Str(m, "id"), Spec: spec, Secrets: map[string]bool{}}

	if oidc, ok := m["oidc_config"].(map[string]any); ok {
		spec["oidc_config"] = ssoNormalizeConfig(oidc, ssoOIDCFields())
		live.Secrets[ssoOIDCSecret] = Bool(oidc, "has_secret")
	}

	if saml, ok := m["saml_config"].(map[string]any); ok {
		spec["saml_config"] = ssoNormalizeConfig(saml, ssoSAMLFields())
	}

	return live
}

func (ssoKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	var resp struct {
		Providers []map[string]any `json:"providers"`
	}

	if err := env.Client.Get(ctx, ssoPath, &resp); err != nil {
		return nil, fmt.Errorf("list SSO providers: %w", err)
	}

	out := make([]*Live, 0, len(resp.Providers))
	for _, p := range resp.Providers {
		m, _ := Canon(p).(map[string]any)
		out = append(out, ssoLive(m))
	}

	return out, nil
}

// Create posts the provider and, when the manifest asks for enabled: false,
// disables it afterwards because the server always creates providers enabled.
// When that follow-up fails the provider id is still returned with the error.
func (ssoKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := Pick(o.Spec, ssoCreateKeys()...)
	body["slug"] = o.Name

	var resp struct {
		ID string `json:"id"`
	}

	if err := env.Client.Do(ctx, http.MethodPost, ssoPath, body, &resp); err != nil {
		return "", fmt.Errorf("create SSO provider %q: %w", o.Name, err)
	}

	if enabled, ok := o.Spec["enabled"].(bool); ok && !enabled {
		patch := map[string]any{"enabled": false}

		if err := env.Client.Do(ctx, http.MethodPatch, ssoPath+"/"+PathEscape(o.Name), patch, nil); err != nil {
			return resp.ID, fmt.Errorf("disable new SSO provider %q: %w", o.Name, err)
		}
	}

	return resp.ID, nil
}

// Update PATCHes only the changed fields; the server merges the config blocks
// field by field and replaces the mappings whole.
func (ssoKind) Update(ctx context.Context, env *Env, live *Live, _ *Object, changes []Change) (string, error) {
	if len(changes) == 0 {
		return live.ID, nil
	}

	err := env.Client.Do(ctx, http.MethodPatch, ssoPath+"/"+PathEscape(live.Key), PatchBody(changes), nil)
	if err != nil {
		return live.ID, fmt.Errorf("update SSO provider %q: %w", live.Key, err)
	}

	return live.ID, nil
}

func (ssoKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.Do(ctx, http.MethodDelete, ssoPath+"/"+PathEscape(live.Key), nil, nil); err != nil {
		return fmt.Errorf("delete SSO provider %q: %w", live.Key, err)
	}

	return nil
}
