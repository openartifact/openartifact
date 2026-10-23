// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// docLoader validates one YAML document into an Object, collecting every
// problem with its position.
type docLoader struct {
	reg     *Registry
	file    string
	baseDir string
	errs    []error

	obj *Object
}

func (d *docLoader) pos(n *yaml.Node) Pos { return Pos{File: d.file, Line: n.Line} }

func (d *docLoader) errorf(n *yaml.Node, path, format string, args ...any) {
	ident := ""
	if d.obj != nil && d.obj.Kind != "" {
		ident = Ident(d.obj.Kind, d.obj.Name)
	}

	d.errs = append(d.errs, &ValidationError{Pos: d.pos(n), Ident: ident, Path: path, Msg: fmt.Sprintf(format, args...)})
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}

	return n
}

// mapping returns the key/value node pairs of a mapping node, reporting
// duplicate and non-string keys.
func (d *docLoader) mapping(n *yaml.Node, path string) (map[string]*yaml.Node, map[string]*yaml.Node, []string, bool) {
	n = deref(n)
	if n.Kind != yaml.MappingNode {
		d.errorf(n, path, "must be a mapping")
		return nil, nil, nil, false
	}

	vals := make(map[string]*yaml.Node, len(n.Content)/2)
	keys := make(map[string]*yaml.Node, len(n.Content)/2)
	order := make([]string, 0, len(n.Content)/2)
	ok := true

	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := deref(n.Content[i]), n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			d.errorf(k, path, "mapping keys must be strings")

			ok = false

			continue
		}

		if _, dup := vals[k.Value]; dup {
			d.errorf(k, joinPath(path, k.Value), "duplicate key")

			ok = false

			continue
		}

		vals[k.Value] = v
		keys[k.Value] = k

		order = append(order, k.Value)
	}

	return vals, keys, order, ok
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}

	return prefix + "." + key
}

// load validates the document root.
func (d *docLoader) load(root *yaml.Node) *Object {
	d.obj = &Object{Pos: d.pos(root), FieldPos: map[string]Pos{}, Meta: map[string]string{}}

	vals, keys, _, ok := d.mapping(root, "")
	if !ok {
		return nil
	}

	for k, kn := range keys {
		switch k {
		case "apiVersion", "kind", "metadata", "spec":
		default:
			d.errorf(kn, k, "unknown top-level field (allowed: apiVersion, kind, metadata, spec)")
		}
	}

	d.checkAPIVersion(root, vals)

	kind := d.kind(root, vals)
	if kind == nil {
		return d.obj
	}

	d.obj.Kind = kind.Name()
	d.metadata(root, vals, kind)

	spec := d.spec(root, vals, kind)
	d.obj.Spec = spec

	d.fromMeta(kind)

	if len(d.errs) == 0 {
		if v, isV := kind.(Validator); isV {
			for _, err := range v.Validate(d.obj) {
				d.errs = append(d.errs, &ValidationError{
					Pos: d.obj.Pos, Ident: Ident(d.obj.Kind, d.obj.Name), Msg: err.Error(),
				})
			}
		}
	}

	return d.obj
}

func (d *docLoader) checkAPIVersion(root *yaml.Node, vals map[string]*yaml.Node) {
	n, ok := vals["apiVersion"]
	if !ok {
		d.errorf(root, "apiVersion", "required; use %q", APIVersion)
		return
	}

	if v := deref(n); v.Kind != yaml.ScalarNode || v.Value != APIVersion {
		d.errorf(n, "apiVersion", "unsupported value; use %q", APIVersion)
	}
}

func (d *docLoader) kind(root *yaml.Node, vals map[string]*yaml.Node) Kind {
	n, ok := vals["kind"]
	if !ok {
		d.errorf(root, "kind", "required (one of %s)", strings.Join(d.reg.Names(), ", "))
		return nil
	}

	v := deref(n)
	if v.Kind != yaml.ScalarNode {
		d.errorf(n, "kind", "must be a string")
		return nil
	}

	k, found := d.reg.Get(v.Value)
	if !found {
		d.errorf(n, "kind", "unknown kind %q (one of %s)", v.Value, strings.Join(d.reg.Names(), ", "))
		return nil
	}

	return k
}

func (d *docLoader) metadata(root *yaml.Node, vals map[string]*yaml.Node, kind Kind) {
	n, ok := vals["metadata"]
	if !ok {
		d.errorf(root, "metadata", "required (metadata.name)")
		return
	}

	mv, mk, _, ok := d.mapping(n, "metadata")
	if !ok {
		return
	}

	allowed := map[string]bool{"name": true}
	schema := kind.Schema()

	for name, f := range schema.Fields {
		if f.FromMeta {
			allowed[name] = true
		}
	}

	for _, name := range kind.MetaKeys() {
		allowed[name] = true
	}

	for k, kn := range mk {
		if !allowed[k] {
			d.errorf(kn, "metadata."+k, "unknown metadata field for kind %s (allowed: %s)", kind.Name(), strings.Join(sortedKeys(allowed), ", "))
		}
	}

	for k, vn := range mv {
		if !allowed[k] {
			continue
		}

		s := deref(vn)
		if s.Kind != yaml.ScalarNode || s.Tag != "!!str" || s.Value == "" {
			d.errorf(vn, "metadata."+k, "must be a non-empty string")
			continue
		}

		if k == "name" {
			d.obj.Name = s.Value
		} else {
			d.obj.Meta[k] = s.Value
		}

		d.obj.FieldPos["metadata."+k] = d.pos(vn)
	}

	if _, has := mv["name"]; !has {
		d.errorf(n, "metadata.name", "required")
	}
}

func (d *docLoader) spec(root *yaml.Node, vals map[string]*yaml.Node, kind Kind) map[string]any {
	n, ok := vals["spec"]
	if !ok || isNull(deref(n)) {
		// An empty spec is valid for kinds with no required fields.
		n = &yaml.Node{Kind: yaml.MappingNode, Line: root.Line}
	}

	schema := kind.Schema()
	spec, _ := d.object(n, schema, "", true).(map[string]any)

	if spec == nil {
		spec = map[string]any{}
	}

	return spec
}

// fromMeta moves metadata values of FromMeta fields into the spec.
func (d *docLoader) fromMeta(kind Kind) {
	for name, f := range kind.Schema().Fields {
		if !f.FromMeta {
			continue
		}

		if v, ok := d.obj.Meta[name]; ok {
			d.obj.Spec[name] = v

			delete(d.obj.Meta, name)
		}
	}
}

// value validates a node against a field and returns its canonical value.
func (d *docLoader) value(n *yaml.Node, f *Field, path string) any {
	n = deref(n)
	d.obj.FieldPos[path] = d.pos(n)

	if isNull(n) {
		if f.Nullable {
			return nil
		}

		d.errorf(n, path, "must not be null")

		return nil
	}

	if f.Secret {
		return d.secret(n, path)
	}

	switch f.Type {
	case TObject:
		return d.object(n, f, path, false)
	case TString:
		return d.str(n, f, path)
	case TInt, TNumber:
		return d.number(n, f, path)
	case TBool:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			d.errorf(n, path, "must be a boolean (true or false)")
			return nil
		}

		return n.Value == "true"
	case TList, TSet:
		return d.list(n, f, path)
	case TMap:
		return d.stringMap(n, f, path)
	case TAny:
		var v any
		if err := n.Decode(&v); err != nil {
			d.errorf(n, path, "invalid value: %v", err)
			return nil
		}

		return Canon(normalizeYAML(v))
	}

	d.errorf(n, path, "unsupported schema type")

	return nil
}

// normalizeYAML converts map[any]any (non-string keys) into map[string]any.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeYAML(e)
		}

		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = normalizeYAML(e)
		}

		return out
	case []any:
		for i, e := range t {
			t[i] = normalizeYAML(e)
		}

		return t
	}

	return v
}

func (d *docLoader) str(n *yaml.Node, f *Field, path string) any {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		d.errorf(n, path, "must be a string")
		return nil
	}

	if len(f.Enum) > 0 && !slices.Contains(f.Enum, n.Value) {
		d.errorf(n, path, "invalid value %q (one of %s)", n.Value, enumList(f.Enum))
		return nil
	}

	return n.Value
}

func enumList(enum []string) string {
	q := make([]string, len(enum))
	for i, e := range enum {
		q[i] = fmt.Sprintf("%q", e)
	}

	return strings.Join(q, ", ")
}

func (d *docLoader) number(n *yaml.Node, f *Field, path string) any {
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && (f.Type != TNumber || n.Tag != "!!float")) {
		if f.Type == TInt {
			d.errorf(n, path, "must be an integer")
		} else {
			d.errorf(n, path, "must be a number")
		}

		return nil
	}

	var num float64
	if err := n.Decode(&num); err != nil {
		d.errorf(n, path, "must be a number")
		return nil
	}

	if f.HasMin && num < f.Min {
		d.errorf(n, path, "must be at least %v", f.Min)
		return nil
	}

	return num
}

// object validates a mapping against the fixed keys of f. root marks the spec
// root, where FromMeta fields are forbidden.
func (d *docLoader) object(n *yaml.Node, f *Field, path string, root bool) any {
	vals, keys, order, ok := d.mapping(n, path)
	if !ok {
		return nil
	}

	out := make(map[string]any, len(vals))

	for _, k := range order {
		sub, known := f.Fields[k]
		kp := joinPath(path, k)

		switch {
		case !known:
			d.errorf(keys[k], kp, "unknown field (allowed: %s)", strings.Join(allowedFields(f, root), ", "))
		case root && sub.FromMeta:
			d.errorf(keys[k], kp, "set this in metadata.%s, not in spec", k)
		default:
			out[k] = d.value(vals[k], sub, kp)
		}
	}

	for _, name := range sortedKeys(f.Fields) {
		sub := f.Fields[name]
		if !sub.Required || sub.FromMeta {
			continue
		}

		if _, has := vals[name]; !has {
			d.errorf(n, joinPath(path, name), "required")
		}
	}

	return out
}

func allowedFields(f *Field, root bool) []string {
	var names []string

	for name, sub := range f.Fields {
		if root && sub.FromMeta {
			continue
		}

		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func (d *docLoader) list(n *yaml.Node, f *Field, path string) any {
	if n.Kind != yaml.SequenceNode {
		d.errorf(n, path, "must be a list")
		return nil
	}

	out := make([]any, 0, len(n.Content))
	seen := map[string]bool{}

	for i, e := range n.Content {
		ep := fmt.Sprintf("%s[%d]", path, i)
		v := d.value(e, f.Elem, ep)
		out = append(out, v)

		if f.Type != TSet {
			continue
		}

		id := elemIdentity(f, v)
		if seen[id] {
			d.errorf(e, ep, "duplicate entry %s", id)
		}

		seen[id] = true
	}

	return out
}

// elemIdentity is the dedupe/sort identity of a set element.
func elemIdentity(f *Field, v any) string {
	if m, ok := v.(map[string]any); ok && f.KeyBy != "" {
		return fmt.Sprint(m[f.KeyBy])
	}

	return fmt.Sprint(v)
}

func (d *docLoader) stringMap(n *yaml.Node, f *Field, path string) any {
	vals, _, order, ok := d.mapping(n, path)
	if !ok {
		return nil
	}

	out := make(map[string]any, len(vals))

	for _, k := range order {
		out[k] = d.value(vals[k], f.Elem, joinPath(path, k))
	}

	return out
}

// secret parses {secretRef: {env: NAME}} or {secretRef: {file: PATH}}.
func (d *docLoader) secret(n *yaml.Node, path string) any {
	const hint = "secrets are never inline: use {secretRef: {env: NAME}} or {secretRef: {file: PATH}}"

	if n.Kind != yaml.MappingNode {
		d.errorf(n, path, "inline secret value not allowed; %s", hint)
		return nil
	}

	vals, _, _, ok := d.mapping(n, path)
	if !ok {
		return nil
	}

	refNode, has := vals["secretRef"]
	if !has || len(vals) != 1 {
		d.errorf(n, path, "must contain only secretRef; %s", hint)
		return nil
	}

	rv, rk, _, ok := d.mapping(refNode, path+".secretRef")
	if !ok {
		return nil
	}

	for k, kn := range rk {
		if k != "env" && k != "file" {
			d.errorf(kn, path+".secretRef."+k, "unknown field (allowed: env, file)")
			ok = false
		}
	}

	if !ok {
		return nil
	}

	if len(rv) != 1 {
		d.errorf(refNode, path+".secretRef", "set exactly one of env or file")
		return nil
	}

	for k, vn := range rv {
		s := deref(vn)
		if s.Kind != yaml.ScalarNode || s.Tag != "!!str" || s.Value == "" {
			d.errorf(vn, path+".secretRef."+k, "must be a non-empty string")
			return nil
		}

		if k == "env" {
			return SecretRef{Env: s.Value}
		}

		p := s.Value
		if !filepath.IsAbs(p) {
			p = filepath.Join(d.baseDir, p)
		}

		return SecretRef{File: p}
	}

	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}
