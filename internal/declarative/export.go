// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ExportOptions select what `oa export` emits.
type ExportOptions struct {
	Registry *Registry
	Client   *Client
	// Kinds restricts the export; empty means every kind except User. Naming
	// User explicitly includes the human users.
	Kinds []string
	// WithMembers includes fields kinds omit by default (Group members).
	WithMembers bool
}

// ExportDoc is one exported manifest document.
type ExportDoc struct {
	Kind string
	Key  string
	YAML []byte
}

// Export reads the live state and renders it as manifests. Seeded objects are
// skipped; write-only secrets that are set become secretRef placeholders.
func Export(ctx context.Context, opts ExportOptions) ([]ExportDoc, error) {
	filter, err := KindFilter(opts.Registry, opts.Kinds)
	if err != nil {
		return nil, err
	}

	env := NewEnv(opts.Client)

	var docs []ExportDoc

	for _, k := range opts.Registry.Ordered() {
		if filter == nil && k.Name() == KindUser {
			continue
		}

		if filter != nil && !filter[k.Name()] {
			continue
		}

		list, lerr := k.List(ctx, env)
		if lerr != nil {
			return nil, fmt.Errorf("read %s state from the server: %w", k.Name(), lerr)
		}

		sort.SliceStable(list, func(i, j int) bool { return list[i].Key < list[j].Key })

		for _, l := range list {
			if l.Seeded {
				continue
			}

			doc, derr := exportObject(k, l, opts.WithMembers)
			if derr != nil {
				return nil, derr
			}

			docs = append(docs, ExportDoc{Kind: k.Name(), Key: l.Key, YAML: doc})
		}
	}

	return docs, nil
}

func exportObject(k Kind, l *Live, withMembers bool) ([]byte, error) {
	name, meta := l.Key, map[string]string{}
	if idr, ok := k.(Identifier); ok {
		name, meta = idr.Identity(l)
	}

	spec := Canon(l.Spec)
	specMap, _ := spec.(map[string]any)

	if specMap == nil {
		specMap = map[string]any{}
	}

	// FromMeta fields live in metadata, not in the spec.
	for fname, f := range k.Schema().Fields {
		if !f.FromMeta {
			continue
		}

		if v, ok := specMap[fname].(string); ok && v != "" {
			meta[fname] = v
		}

		delete(specMap, fname)
	}

	if om, ok := k.(ExportOmitter); ok && !withMembers {
		for _, f := range om.ExportOmit() {
			delete(specMap, f)
		}
	}

	dropNil(specMap)
	placeholders(k.Name(), name, l.Secrets, specMap)

	return renderDoc(k.Name(), name, meta, specMap)
}

func dropNil(m map[string]any) {
	for k, v := range m {
		if v == nil {
			delete(m, k)
			continue
		}

		if sub, ok := v.(map[string]any); ok {
			dropNil(sub)
		}
	}
}

var nonEnvRe = regexp.MustCompile(`[^A-Z0-9]+`)

// placeholders writes secretRef placeholders for every secret the server says
// is set.
func placeholders(kind, name string, secrets map[string]bool, spec map[string]any) {
	for path, set := range secrets {
		if !set {
			continue
		}

		env := strings.Trim(nonEnvRe.ReplaceAllString(strings.ToUpper("OA_"+kind+"_"+name+"_"+path), "_"), "_")
		setPath(spec, path, SecretRef{Env: env})
	}
}

func setPath(m map[string]any, path string, v any) {
	head, rest, nested := strings.Cut(path, ".")
	if !nested {
		m[head] = v
		return
	}

	sub, _ := m[head].(map[string]any)
	if sub == nil {
		sub = map[string]any{}
		m[head] = sub
	}

	setPath(sub, rest, v)
}

func renderDoc(kind, name string, meta map[string]string, spec map[string]any) ([]byte, error) {
	metaNode := &yaml.Node{Kind: yaml.MappingNode}
	addPair(metaNode, "name", name)

	for _, k := range sortedKeys(meta) {
		addPair(metaNode, k, meta[k])
	}

	root := &yaml.Node{Kind: yaml.MappingNode}
	addPair(root, "apiVersion", APIVersion)
	addPair(root, "kind", kind)
	root.Content = append(root.Content, scalar("metadata"), metaNode)

	if len(spec) > 0 {
		specNode := &yaml.Node{}
		if err := specNode.Encode(yamlValue(spec)); err != nil {
			return nil, fmt.Errorf("render %s: %w", Ident(kind, name), err)
		}

		root.Content = append(root.Content, scalar("spec"), specNode)
	}

	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("render %s: %w", Ident(kind, name), err)
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render %s: %w", Ident(kind, name), err)
	}

	return buf.Bytes(), nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func addPair(m *yaml.Node, k, v string) {
	m.Content = append(m.Content, scalar(k), scalar(v))
}

// yamlValue converts SecretRefs into their manifest form and whole floats into
// integers, so the output re-parses as the schema expects.
func yamlValue(v any) any {
	switch t := v.(type) {
	case SecretRef:
		if t.Env != "" {
			return map[string]any{"secretRef": map[string]any{"env": t.Env}}
		}

		return map[string]any{"secretRef": map[string]any{"file": t.File}}
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}

		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = yamlValue(e)
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = yamlValue(e)
		}

		return out
	}

	return v
}

// WriteStream writes the documents as one multi-document YAML stream.
func WriteStream(w io.Writer, docs []ExportDoc) error {
	for i, d := range docs {
		if i > 0 {
			if _, err := io.WriteString(w, "---\n"); err != nil {
				return fmt.Errorf("write export: %w", err)
			}
		}

		if _, err := w.Write(d.YAML); err != nil {
			return fmt.Errorf("write export: %w", err)
		}
	}

	return nil
}

var unsafeFileRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// WriteDir writes one file per object at <dir>/<kind>/<key>.yaml and returns
// the paths written.
func WriteDir(dir string, docs []ExportDoc) ([]string, error) {
	var paths []string

	seen := map[string]string{}

	for _, d := range docs {
		rel := filepath.Join(strings.ToLower(d.Kind), unsafeFileRe.ReplaceAllString(d.Key, "_")+".yaml")
		if prev, dup := seen[rel]; dup {
			return paths, fmt.Errorf("export file name collision: %s and %s both map to %s", prev, Ident(d.Kind, d.Key), rel) //nolint:err113 // user-facing message
		}

		seen[rel] = Ident(d.Kind, d.Key)
		path := filepath.Join(dir, rel)

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return paths, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}

		if err := os.WriteFile(path, d.YAML, 0o600); err != nil {
			return paths, fmt.Errorf("write %s: %w", path, err)
		}

		paths = append(paths, path)
	}

	return paths, nil
}
