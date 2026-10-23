// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var errNoManifests = errors.New("no manifest files")

// maxManifestBytes bounds one manifest file.
const maxManifestBytes = 8 << 20

// LoadOptions selects the manifest files to load.
type LoadOptions struct {
	// Paths are files or directories; "-" reads Stdin.
	Paths []string
	// Recursive descends into subdirectories of directory paths.
	Recursive bool
	// Stdin is read for the path "-".
	Stdin io.Reader
}

// ValidationError is one problem in a manifest, positioned at file:line.
type ValidationError struct {
	Pos Pos
	// Ident is "Kind/name" of the object, empty for file-level problems.
	Ident string
	// Path is the dotted field path, empty for the object itself.
	Path string
	Msg  string
}

func (e *ValidationError) Error() string {
	var b strings.Builder

	b.WriteString(e.Pos.String())
	b.WriteString(": ")

	if e.Ident != "" {
		b.WriteString(e.Ident)
		b.WriteString(": ")
	}

	if e.Path != "" {
		b.WriteString(e.Path)
		b.WriteString(": ")
	}

	b.WriteString(e.Msg)

	return b.String()
}

// ValidationErrors is the aggregate returned by Load.
type ValidationErrors []error

func (v ValidationErrors) Error() string {
	lines := make([]string, len(v))
	for i, e := range v {
		lines[i] = e.Error()
	}

	return strings.Join(lines, "\n")
}

// Unwrap lets errors.Is/As see the individual errors.
func (v ValidationErrors) Unwrap() []error { return v }

// Load reads, parses and validates every manifest under opts.Paths. All
// problems are collected: the returned error is a ValidationErrors. Objects are
// returned in file order.
func Load(reg *Registry, opts LoadOptions) ([]*Object, error) {
	files, err := expandPaths(opts)
	if err != nil {
		return nil, err
	}

	var (
		objs []*Object
		errs ValidationErrors
	)

	for _, f := range files {
		data, rerr := readManifest(f, opts.Stdin)
		if rerr != nil {
			errs = append(errs, &ValidationError{Pos: Pos{File: f.display}, Msg: rerr.Error()})
			continue
		}

		o, e := parseDocuments(reg, f, data)
		objs = append(objs, o...)
		errs = append(errs, e...)
	}

	errs = append(errs, checkDuplicates(reg, objs)...)

	if len(errs) > 0 {
		return objs, errs
	}

	if len(objs) == 0 {
		return nil, errors.New("no manifest documents found in the given paths") //nolint:err113 // user-facing message
	}

	return objs, nil
}

type manifestFile struct {
	// display is the path used in messages; path is what is opened ("" for stdin).
	display string
	path    string
	stdin   bool
}

func expandPaths(opts LoadOptions) ([]manifestFile, error) {
	var files []manifestFile

	for _, p := range opts.Paths {
		if p == "-" {
			files = append(files, manifestFile{display: "<stdin>", stdin: true})
			continue
		}

		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("manifest path %s: %w", p, err)
		}

		if !st.IsDir() {
			files = append(files, manifestFile{display: p, path: p})
			continue
		}

		dirFiles, err := walkDir(p, opts.Recursive)
		if err != nil {
			return nil, err
		}

		files = append(files, dirFiles...)
	}

	if len(files) == 0 && len(opts.Paths) > 0 {
		return nil, fmt.Errorf("%w in %s (only *.yaml and *.yml are read; add -R to descend into subdirectories)", errNoManifests, strings.Join(opts.Paths, ", "))
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w given (use -f FILE|DIR|-)", errNoManifests)
	}

	return files, nil
}

func isManifestName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))

	return ext == ".yaml" || ext == ".yml"
}

func walkDir(dir string, recursive bool) ([]manifestFile, error) {
	var files []manifestFile

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if path != dir && !recursive {
				return filepath.SkipDir
			}

			return nil
		}

		if isManifestName(d.Name()) {
			files = append(files, manifestFile{display: path, path: path})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read manifest directory %s: %w", dir, err)
	}

	// WalkDir visits in lexical order, which keeps runs deterministic.
	return files, nil
}

func readManifest(f manifestFile, stdin io.Reader) ([]byte, error) {
	var r io.Reader

	if f.stdin {
		if stdin == nil {
			return nil, errors.New("stdin is not available") //nolint:err113 // user-facing message
		}

		r = stdin
	} else {
		fh, err := os.Open(f.path)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}

		defer func() { _ = fh.Close() }()

		r = fh
	}

	data, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("manifest larger than %d bytes", maxManifestBytes) //nolint:err113 // user-facing message
	}

	return data, nil
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// parseDocuments decodes every YAML document of one file.
func parseDocuments(reg *Registry, f manifestFile, data []byte) ([]*Object, []error) {
	var (
		objs []*Object
		errs []error
	)

	dec := yaml.NewDecoder(bytes.NewReader(data))
	baseDir := "."

	if f.path != "" {
		baseDir = filepath.Dir(f.path)
	}

	for {
		var doc yaml.Node

		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			errs = append(errs, yamlSyntaxError(f.display, err))
			// A syntax error leaves the decoder unusable for this file.
			break
		}

		if doc.Kind == 0 || len(doc.Content) == 0 || isNull(doc.Content[0]) {
			continue
		}

		d := &docLoader{reg: reg, file: f.display, baseDir: baseDir}
		if obj := d.load(doc.Content[0]); obj != nil && len(d.errs) == 0 {
			objs = append(objs, obj)
		}

		errs = append(errs, d.errs...)
	}

	return objs, errs
}

func yamlSyntaxError(file string, err error) error {
	pos := Pos{File: file}

	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		pos.Line, _ = strconv.Atoi(m[1])
	}

	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n  ")

	return &ValidationError{Pos: pos, Msg: "invalid YAML: " + msg}
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// checkDuplicates reports two documents that describe the same object.
func checkDuplicates(reg *Registry, objs []*Object) []error {
	var errs []error

	seen := map[string]*Object{}

	for _, o := range objs {
		k, ok := reg.Get(o.Kind)
		if !ok {
			continue
		}

		id := Ident(o.Kind, k.Key(o))
		if prev, dup := seen[id]; dup {
			errs = append(errs, &ValidationError{
				Pos: o.Pos, Ident: id,
				Msg: fmt.Sprintf("defined twice (first at %s)", prev.Pos),
			})

			continue
		}

		seen[id] = o
	}

	return errs
}

// SortObjects orders objects for apply: kind rank, kind order, key.
func SortObjects(reg *Registry, objs []*Object) {
	sort.SliceStable(objs, func(i, j int) bool {
		ki, _ := reg.Get(objs[i].Kind)
		kj, _ := reg.Get(objs[j].Kind)

		if ki.Rank() != kj.Rank() {
			return ki.Rank() < kj.Rank()
		}

		if oi, oj := ki.Order(objs[i]), kj.Order(objs[j]); oi != oj {
			return oi < oj
		}

		return ki.Key(objs[i]) < kj.Key(objs[j])
	})
}
