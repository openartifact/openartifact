// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrNotIndex is returned for a document that is not a Helm repository index.
var ErrNotIndex = errors.New("not a helm repository index")

// Index is a parsed index.yaml of a classic Helm chart repository. It keeps
// the YAML tree, so everything but the fields a caller rewrites is reproduced
// as the upstream published it (annotations, dependencies, icons, ...).
type Index struct {
	root *yaml.Node
	// entries is the "entries" mapping (chart name -> sequence of versions);
	// nil when the index lists no charts.
	entries *yaml.Node
}

// ChartVersion is the part of one index entry that proxying and merging need.
type ChartVersion struct {
	// Name is the key of the entry's list under "entries": the name helm
	// resolves, whatever the entry's own "name" field says. Merging and the
	// hosted-wins rule go by it, so an upstream cannot list a version of a
	// chart it does not own under another name.
	Name    string
	Version string
	// Digest is the entry's "digest" field verbatim (SHA-256 hex for charts
	// built by helm package), "" when absent.
	Digest string
	// URLs are the entry's "urls" as published: absolute, or relative to the
	// repository URL.
	URLs []string
}

// ParseIndex parses doc. The document must be a mapping with an apiVersion and
// an entries mapping (or none). YAML anchors and aliases are refused: helm's
// own index generator never writes them, and aliased entries could be made to
// expand into quadratic work.
func ParseIndex(doc []byte) (*Index, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(doc, &n); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotIndex, err)
	}

	if n.Kind != yaml.DocumentNode || len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return nil, ErrNotIndex
	}

	root := n.Content[0]

	if hasAlias(root) {
		return nil, fmt.Errorf("%w: yaml aliases are not supported", ErrNotIndex)
	}

	if v := mapValue(root, "apiVersion"); v == nil || v.Kind != yaml.ScalarNode || v.Value == "" {
		return nil, fmt.Errorf("%w: apiVersion missing", ErrNotIndex)
	}

	writable(root)

	ix := &Index{root: root}

	if e := mapValue(root, "entries"); e != nil && !isNull(e) {
		if e.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%w: entries is not a mapping", ErrNotIndex)
		}

		ix.entries = e

		for i := 1; i < len(e.Content); i += 2 {
			if s := e.Content[i]; !isNull(s) && s.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%w: entries are not lists", ErrNotIndex)
			}
		}
	}

	return ix, nil
}

// hasAlias reports whether the tree contains an alias node.
func hasAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}

	return slices.ContainsFunc(n.Content, hasAlias)
}

// writable rewrites the tree into a form the yaml encoder writes back as the
// document it was read from. It drops every comment: the encoder cannot place
// one attached to a key that is a flow collection ("[]: #c" comes out as "[]
// #c" with the ": null" on the next line, which is not YAML). And it writes
// every empty null out as "null": the encoder renders an empty plain scalar
// inside a flow collection as a quoted empty string, so "entries: {app: }"
// would marshal with a string where the null was.
func writable(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""

	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" && n.Value == "" {
		n.Value = "null"
	}

	for _, c := range n.Content {
		writable(c)
	}
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// mapValue returns the value stored under key in mapping m, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}

	return nil
}

func scalarOf(m *yaml.Node, key string) string {
	if v := mapValue(m, key); v != nil && v.Kind == yaml.ScalarNode && !isNull(v) {
		return v.Value
	}

	return ""
}

// chartVersion reads one entry node; ok is false when it is not a mapping.
func chartVersion(listName string, e *yaml.Node) (ChartVersion, bool) {
	if e.Kind != yaml.MappingNode {
		return ChartVersion{}, false
	}

	cv := ChartVersion{
		Name:    listName,
		Version: scalarOf(e, "version"),
		Digest:  scalarOf(e, "digest"),
	}

	if u := mapValue(e, "urls"); u != nil && u.Kind == yaml.SequenceNode {
		for _, c := range u.Content {
			if c.Kind == yaml.ScalarNode && c.Value != "" {
				cv.URLs = append(cv.URLs, c.Value)
			}
		}
	}

	return cv, true
}

// Versions yields every chart version in document order.
func (ix *Index) Versions() iter.Seq[ChartVersion] {
	return func(yield func(ChartVersion) bool) {
		if ix.entries == nil {
			return
		}

		for i := 0; i+1 < len(ix.entries.Content); i += 2 {
			for _, e := range ix.entries.Content[i+1].Content {
				cv, ok := chartVersion(ix.entries.Content[i].Value, e)
				if ok && !yield(cv) {
					return
				}
			}
		}
	}
}

// RewriteURLs replaces the urls of every chart version with what fn returns
// for it. A version for which fn returns no URL cannot be served and is
// removed, as is a chart left without versions.
func (ix *Index) RewriteURLs(fn func(ChartVersion) []string) {
	if ix.entries == nil {
		return
	}

	names := ix.entries.Content
	kept := make([]*yaml.Node, 0, len(names))

	for i := 0; i+1 < len(names); i += 2 {
		list := names[i+1]
		versions := list.Content[:0:0]

		for _, e := range list.Content {
			cv, ok := chartVersion(names[i].Value, e)
			if !ok {
				continue
			}

			urls := fn(cv)
			if len(urls) == 0 {
				continue
			}

			setURLs(e, urls)

			versions = append(versions, e)
		}

		if len(versions) == 0 {
			continue
		}

		list.Content = versions
		kept = append(kept, names[i], list)
	}

	ix.entries.Content = kept
}

// setURLs stores urls as the "urls" sequence of entry e.
func setURLs(e *yaml.Node, urls []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

	for _, u := range urls {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: u})
	}

	for i := 0; i+1 < len(e.Content); i += 2 {
		if e.Content[i].Value == "urls" {
			e.Content[i+1] = seq
			return
		}
	}

	e.Content = append(e.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "urls"}, seq)
}

// MergeIndexes returns the union of the indexes' charts: a chart version is
// listed once, from the first index that has it (name and version decide;
// entries without a version are skipped, as helm does). The result carries
// apiVersion v1 and no generation timestamp, so equal inputs give equal bytes.
// The entries are shared with the inputs, which must not be modified later.
func MergeIndexes(indexes []*Index) *Index {
	entries := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	lists := make(map[string]*yaml.Node)
	seen := make(map[string]struct{})

	for _, ix := range indexes {
		if ix.entries == nil {
			continue
		}

		for i := 0; i+1 < len(ix.entries.Content); i += 2 {
			name := ix.entries.Content[i].Value

			for _, e := range ix.entries.Content[i+1].Content {
				cv, ok := chartVersion(name, e)
				if !ok || cv.Version == "" {
					continue
				}

				id := name + "\x00" + cv.Version
				if _, dup := seen[id]; dup {
					continue
				}

				seen[id] = struct{}{}

				list, ok := lists[name]
				if !ok {
					list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
					lists[name] = list

					entries.Content = append(entries.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, list)
				}

				list.Content = append(list.Content, e)
			}
		}
	}

	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "apiVersion"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "v1"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "entries"},
		entries,
	}}

	return &Index{root: root, entries: entries}
}

// Marshal renders the index as YAML.
func (ix *Index) Marshal() ([]byte, error) {
	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(ix.root); err != nil {
		return nil, fmt.Errorf("marshal index: %w", err)
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshal index: %w", err)
	}

	return buf.Bytes(), nil
}

// ChartFile returns the file name a chart URL is served under (as
// charts/<name> of a proxy or group repository): the last segment of its path.
// Only http and https URLs can be served; others (oci://) report false.
func ChartFile(u *url.URL) (string, bool) {
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}

	return fileOfPath(u.Path)
}

// fileOfPath returns the last path segment when it is a usable file name.
func fileOfPath(p string) (string, bool) {
	file := path.Base(p)
	if strings.HasSuffix(p, "/") || file == "." || file == "/" || file == ".." || strings.ContainsAny(file, "\\/") {
		return "", false
	}

	for _, r := range file {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}

	return file, true
}

// RelativeChartFile is ChartFile for a URL that may be relative (a member
// index's, already pointing at one of this server's repositories or at a
// path below it).
func RelativeChartFile(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}

	if u.Scheme == "" {
		return fileOfPath(u.Path)
	}

	return ChartFile(u)
}
