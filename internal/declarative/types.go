// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package declarative implements `oa apply`, `oa diff` and `oa export`: it
// loads Kubernetes-style YAML manifests, validates them against per-kind
// schemas, diffs them against the live server state fetched through the REST
// API and applies the difference in dependency order.
package declarative

import (
	"context"
	"fmt"
)

// APIVersion is the only manifest apiVersion.
const APIVersion = "openartifact.io/v1"

// Manifest kinds.
const (
	KindUser             = "User"
	KindProject          = "Project"
	KindGroup            = "Group"
	KindRepository       = "Repository"
	KindPermissionTarget = "PermissionTarget"
	KindSSOProvider      = "SSOProvider"
	KindWebhook          = "Webhook"
	KindStorageTier      = "StorageTier"
	KindTierPolicy       = "TierPolicy"
)

// Pos is a position in a manifest file.
type Pos struct {
	File string
	Line int
}

func (p Pos) String() string {
	if p.File == "" {
		return "<unknown>"
	}

	if p.Line <= 0 {
		return p.File
	}

	return fmt.Sprintf("%s:%d", p.File, p.Line)
}

// SecretRef points at a secret resolved on the machine running oa. Exactly one
// field is set. File is already absolute (relative paths are resolved against
// the manifest's directory when the manifest is loaded).
type SecretRef struct {
	Env  string
	File string
}

func (r SecretRef) String() string {
	if r.Env != "" {
		return "env:" + r.Env
	}

	return "file:" + r.File
}

// Object is one desired object loaded from a manifest document.
//
// Spec holds JSON-typed values (string, float64, bool, []any, map[string]any,
// nil) in the same shape and snake_case names as the REST API. Fields the
// schema marks Secret hold a SecretRef until the engine resolves it.
type Object struct {
	Kind string
	Name string
	// Meta holds the metadata keys other than name that the kind allows and
	// that are not mapped into the spec (e.g. Webhook "repository").
	Meta map[string]string
	Spec map[string]any
	Pos  Pos
	// FieldPos maps a dotted spec path to the position of its value.
	FieldPos map[string]Pos
}

// Live is one object as it exists on the server, normalized into manifest
// shape: Spec only contains fields the kind's schema declares.
type Live struct {
	Kind string
	// Key is the natural key (see Kind.Key).
	Key string
	// ID is the server-side identifier (uuid, slug or name) recorded in the
	// ownership marker.
	ID   string
	Spec map[string]any
	// Secrets reports, per write-only field path, whether a value is set on
	// the server. A path missing from the map means presence is unknown.
	Secrets map[string]bool
	// Seeded marks objects created by the server itself (admin-all,
	// anonymous-read, the system project, the bootstrap admin). They are never
	// pruned.
	Seeded bool
}

// Change is one field-level difference between the live and the desired state.
type Change struct {
	// Path is the dotted spec path, e.g. "public" or "oidc_config.issuer".
	Path string `json:"path"`
	Old  any    `json:"old,omitempty"`
	New  any    `json:"new,omitempty"`
	// Secret changes never carry values in reports; New holds the resolved
	// secret only for the kind's Update call.
	Secret bool `json:"secret,omitempty"`
}

// Ref is a reference from one object to another that must exist (in the
// manifests or on the server) for the object to be applied.
type Ref struct {
	Kind string
	Key  string
	// Field is the spec path holding the reference, for error messages.
	Field string
}

// Env is the per-run context handed to kinds.
type Env struct {
	Client *Client
	// Cache is scratch space for kinds (e.g. username to id lookups). Kinds
	// must delete entries they invalidate by creating objects.
	Cache map[string]any
}

// NewEnv returns an Env for client.
func NewEnv(client *Client) *Env {
	return &Env{Client: client, Cache: map[string]any{}}
}

// Kind implements one manifest kind against the REST API.
type Kind interface {
	// Name is the manifest kind, e.g. "Repository".
	Name() string
	// Rank orders kinds for apply (ascending) and prune (descending).
	Rank() int
	// Schema describes the manifest spec of this kind.
	Schema() *Field
	// MetaKeys lists the metadata keys besides name this kind accepts that stay
	// in Object.Meta (Webhook: "repository"). Keys mapped into the spec through
	// Field.FromMeta are not listed.
	MetaKeys() []string
	// Key returns the natural key of o: the name, or scope/name where names are
	// only unique within a scope.
	Key(o *Object) string
	// Order orders objects of this kind within an apply pass (ascending); e.g.
	// group repositories after their members.
	Order(o *Object) int
	// Refs lists the objects o depends on.
	Refs(o *Object) []Ref
	// List returns every object of this kind that exists on the server.
	List(ctx context.Context, env *Env) ([]*Live, error)
	// Create creates o. Secrets in o.Spec are already resolved to strings.
	// It returns the server-side id for the ownership marker.
	Create(ctx context.Context, env *Env, o *Object) (id string, err error)
	// Update applies changes to live. Only the changes may be sent; secret
	// changes carry their resolved value in New. It returns the id for the
	// ownership marker (normally live.ID).
	Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (id string, err error)
	// Delete removes live from the server.
	Delete(ctx context.Context, env *Env, live *Live) error
}

// Type is the type of a manifest field.
type Type int

// Field types.
const (
	// TObject is a mapping with the fixed keys in Field.Fields.
	TObject Type = iota
	TString
	TInt
	TNumber
	TBool
	// TList is an ordered list of Field.Elem.
	TList
	// TSet is an unordered list of Field.Elem (compared sorted; Elem objects
	// are ordered by Field.KeyBy).
	TSet
	// TMap is a mapping with free string keys and Field.Elem values; it is
	// replaced as a whole.
	TMap
	// TAny accepts any JSON-typed value; it is compared as a whole.
	TAny
)

// Field is a node of a kind's spec schema.
type Field struct {
	Type     Type
	Required bool
	// Nullable accepts an explicit null (the server then inherits a default).
	Nullable bool
	// Enum restricts a TString.
	Enum []string
	// Min is the lower bound of a TInt/TNumber when HasMin is set.
	Min    float64
	HasMin bool
	// Secret marks a write-only field: manifests give a {secretRef: ...}
	// instead of a value and the server never returns it.
	Secret bool
	// Immutable fields cannot change on an existing object.
	Immutable bool
	// FromMeta takes the value from metadata.<field name> instead of spec
	// (Repository project).
	FromMeta bool
	// Fields are the keys of a TObject.
	Fields map[string]*Field
	// Elem is the element type of a TList/TSet/TMap.
	Elem *Field
	// KeyBy names the identity key of TSet/TList elements that are objects.
	KeyBy string
}

// Validator is implemented by kinds with cross-field rules the schema cannot
// express (e.g. an SSO provider needs the config block matching its kind). It
// runs after schema validation; returned errors are reported at the object's
// position.
type Validator interface {
	Validate(o *Object) []error
}

// ExportOmitter is implemented by kinds whose Live.Spec carries fields that
// `oa export` leaves out by default because they are usually filled by other
// systems (Group "members", populated by SSO group sync). The fields are
// emitted with `oa export --with-members`.
type ExportOmitter interface {
	ExportOmit() []string
}

// Identifier is implemented by kinds whose natural key is not just the
// metadata name (Webhook: "<repository>/<name>"). `oa export` uses it to split
// a live object back into metadata.name and the extra metadata keys.
type Identifier interface {
	Identity(live *Live) (name string, meta map[string]string)
}

// Prunable is implemented by kinds where some live objects are not worth
// deleting: User "deletion" only disables the account, so an already disabled
// user would show up as a pending deletion forever.
type Prunable interface {
	Prunable(live *Live) bool
}
