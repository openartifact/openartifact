// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/config"
)

// The configuration reference is derived from three sources that the code
// already keeps: the config.Config struct tree (YAML keys, types, defaults via
// config.Defaults), the doc comments of its fields (descriptions) and the
// override calls of applyEnvOverrides (environment variables). Generation also
// proves that the sources agree: every key has an environment override and a
// description, every override feeds a key, and both example files mention
// every key.

const (
	configPage     = "config.md"
	configPkgDir   = "internal/config"
	configEnvEntry = "applyEnvOverrides"
	// configMaxCallDepth bounds the helper calls followed from the entry point.
	configMaxCallDepth = 8

	configEnvExample  = ".env.example"
	configYAMLExample = "openartifact.example.yaml"
)

var (
	errConfigScan   = errors.New("cannot analyse the environment overrides")
	errConfigSchema = errors.New("unsupported configuration struct")
	errConfigSource = errors.New("cannot read the configuration sources")
	errConfigDrift  = errors.New("configuration sources are out of sync")
	errConfigMD     = errors.New("cannot render description")
)

// configLooseEnv names the environment variables that applyEnvOverrides reads
// into a local variable instead of a config field, so no `&cfg.A.B` path ties
// them to a key. The generator fails when the code stops reading one of them
// or reads a new one that is not listed here.
//
//nolint:gochecknoglobals // fixed table
var configLooseEnv = map[string]struct {
	// key is the dotted YAML key the variable feeds; empty when it has none.
	key string
	// note describes a variable without a key in the environment-only table.
	note string
}{
	// applyTelemetryEnv percent-decodes the pairs into the headers map.
	"OTEL_EXPORTER_OTLP_HEADERS": {key: "telemetry.otlp.headers"},
	"OTEL_SDK_DISABLED":          {note: "`true` forces telemetry off, overriding `telemetry.otlp.enabled` and `OPENARTIFACT_OTLP_ENABLED`."},
}

// configReplicaIgnored are the storage.* keys that storage.replica inherits
// through the embedded StorageConfig but that a replica never reads: it is
// never presigned or uploaded to by clients (see storage.ReplicaConfig).
//
//nolint:gochecknoglobals // fixed table
var configReplicaIgnored = []string{
	"storage.replica.presignTtl",
	"storage.replica.presignUploadTtl",
	"storage.replica.presignMaxPendingUploads",
	"storage.replica.presignDownloads",
	"storage.replica.presignMinSize",
	"storage.replica.presignEndpoint",
	"storage.replica.stagingPath",
}

// configYAMLOverride gives the YAML path of Config fields tagged `yaml:"-"`
// that are decoded from the file separately (see loadReplicaFromYAML).
//
//nolint:gochecknoglobals // fixed table
var configYAMLOverride = map[string][]string{
	"StorageReplica": {"storage", "replica"},
}

// configSecretWords mark a key as secret when its last segment contains one.
// A token only counts as a suffix: maxTokens is a number, not a credential.
//
//nolint:gochecknoglobals // fixed table
var configSecretWords = []string{"password", "passphrase", "secret"}

// configSecretKeys are secrets the name heuristic does not catch: last
// segments (lower case) or dotted paths. Key file paths such as tlsKey and
// access key IDs such as gcsHmacKey are deliberately absent.
//
//nolint:gochecknoglobals // fixed table
var configSecretKeys = map[string]bool{
	"apikey":                 true,
	"auth.hmackey":           true,
	"samlspkey":              true,
	"azureaccountkey":        true,
	"telemetry.otlp.headers": true,
}

var (
	configDocRef   = regexp.MustCompile(`\bdocs/([A-Za-z0-9_./-]+\.md)(#[A-Za-z0-9_-]+)?`)
	configEnvToken = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+(?:_\*)?(?:=[A-Za-z0-9]+)?|\b[A-Z][A-Z0-9]*_\*`)
	configEnvLine  = regexp.MustCompile(`^#?\s*([A-Z][A-Z0-9_]*)=`)
	configYAMLLine = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*):(?:\s|$)`)
)

func init() {
	register(generator{
		name: "config",
		owns: func(rel string) bool { return rel == configPage },
		run:  runConfig,
	})
}

// configKey is one leaf of the configuration tree.
type configKey struct {
	// path is the YAML key path, goPath the dotted Go field path (embedded
	// structs promoted) that environment overrides are resolved against.
	path   []string
	goPath string
	// owner is the struct that declares the field, field its Go name.
	owner reflect.Type
	field string
	kind  string
	def   string
	envs  []string
	desc  string
}

func (k configKey) dotted() string { return strings.Join(k.path, ".") }

// configGroup is a struct-valued field: a YAML mapping holding further keys.
type configGroup struct {
	path  []string
	owner reflect.Type
	field string
	typ   reflect.Type
}

func (g configGroup) dotted() string { return strings.Join(g.path, ".") }

func runConfig(root string) (map[string][]byte, error) {
	keys, groups, err := configSchema(config.Defaults())
	if err != nil {
		return nil, err
	}

	files, err := configParseDir(filepath.Join(root, configPkgDir))
	if err != nil {
		return nil, err
	}

	scan, err := configScanEnv(files)
	if err != nil {
		return nil, err
	}

	problems := configBindEnv(keys, scan)

	docs, err := newConfigDocs(root)
	if err != nil {
		return nil, err
	}

	for i := range keys {
		desc, derr := docs.keyDescription(keys[i])
		if derr != nil {
			problems = append(problems, derr.Error())

			continue
		}

		keys[i].desc = desc
	}

	intros, introProblems := docs.groupIntros(groups)
	problems = append(problems, introProblems...)

	exampleProblems, err := configCheckExamples(root, keys, groups, scan)
	if err != nil {
		return nil, err
	}

	problems = append(problems, exampleProblems...)

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w:\n  - %s", errConfigDrift, strings.Join(problems, "\n  - "))
	}

	return map[string][]byte{configPage: renderConfig(keys, groups, intros, scan)}, nil
}

// ---------------------------------------------------------------------------
// Schema: the YAML key tree, by reflection over config.Config.

func configSchema(c *config.Config) ([]configKey, []configGroup, error) {
	var (
		keys   []configKey
		groups []configGroup
	)

	if err := configWalk(reflect.ValueOf(c).Elem(), nil, nil, &keys, &groups); err != nil {
		return nil, nil, err
	}

	ignored := 0

	keys = slices.DeleteFunc(keys, func(k configKey) bool {
		if slices.Contains(configReplicaIgnored, k.dotted()) {
			ignored++

			return true
		}

		return false
	})

	if ignored != len(configReplicaIgnored) {
		return nil, nil, fmt.Errorf("%w: configReplicaIgnored lists keys that no longer exist (matched %d of %d)", errConfigSchema, ignored, len(configReplicaIgnored))
	}

	return keys, groups, nil
}

func configWalk(v reflect.Value, yamlPath, goPath []string, keys *[]configKey, groups *[]configGroup) error {
	t := v.Type()

	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")

		if slices.Contains(strings.Split(opts, ","), "inline") {
			if err := configWalk(v.Field(i), yamlPath, goPath, keys, groups); err != nil {
				return err
			}

			continue
		}

		segs, err := configSegments(t, f, name)
		if err != nil {
			return err
		}

		if segs == nil {
			continue
		}

		path := append(slices.Clone(yamlPath), segs...)
		gp := append(slices.Clone(goPath), f.Name)

		if f.Type.Kind() == reflect.Struct {
			*groups = append(*groups, configGroup{path: path, owner: t, field: f.Name, typ: f.Type})

			if err := configWalk(v.Field(i), path, gp, keys, groups); err != nil {
				return err
			}

			continue
		}

		kind, err := configKind(f.Type)
		if err != nil {
			return fmt.Errorf("%s: %w", strings.Join(gp, "."), err)
		}

		*keys = append(*keys, configKey{
			path:   path,
			goPath: strings.Join(gp, "."),
			owner:  t,
			field:  f.Name,
			kind:   kind,
			def:    configDefault(v.Field(i)),
		})
	}

	return nil
}

// configSegments returns the YAML path segments of a field, nil when the field
// is not a configuration key.
func configSegments(owner reflect.Type, f reflect.StructField, name string) ([]string, error) {
	switch name {
	case "":
		return nil, fmt.Errorf("%w: %s.%s has no yaml tag", errConfigSchema, owner.Name(), f.Name)
	case "-":
		if owner == reflect.TypeFor[config.Config]() {
			return configYAMLOverride[f.Name], nil
		}

		return nil, nil
	default:
		return []string{name}, nil
	}
}

// configKind is the user-facing type name of a leaf.
func configKind(t reflect.Type) (string, error) {
	switch {
	case t == reflect.TypeFor[time.Duration]():
		return "duration", nil
	case t.Kind() == reflect.String:
		return "string", nil
	case t.Kind() == reflect.Int, t.Kind() == reflect.Int64:
		return "int", nil
	case t.Kind() == reflect.Bool:
		return "bool", nil
	case t.Kind() == reflect.Float64:
		return "float", nil
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return "list", nil
	case t.Kind() == reflect.Map && t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String:
		return "map", nil
	}

	return "", fmt.Errorf("%w: field type %s", errConfigSchema, t)
}

// configDefault renders a default value as plain text (not yet markdown).
func configDefault(v reflect.Value) string {
	switch {
	case v.Type() == reflect.TypeFor[time.Duration]():
		return configDuration(time.Duration(v.Int()))
	case v.Kind() == reflect.String:
		return v.String()
	case v.Kind() == reflect.Int, v.Kind() == reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case v.Kind() == reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case v.Kind() == reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case v.Kind() == reflect.Slice:
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = v.Index(i).String()
		}

		return strings.Join(parts, ",")
	case v.Kind() == reflect.Map:
		parts := make([]string, 0, v.Len())

		for iter := v.MapRange(); iter.Next(); {
			parts = append(parts, iter.Key().String()+"="+iter.Value().String())
		}

		slices.Sort(parts)

		return strings.Join(parts, ",")
	}

	return ""
}

// configDuration renders a duration with its largest exact unit: 5m, 24h, 720h.
func configDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d%time.Second == 0:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}

	return d.String()
}

// configIsSecret reports whether a key holds a credential.
func configIsSecret(path []string) bool {
	leaf := strings.ToLower(path[len(path)-1])

	if strings.HasSuffix(leaf, "token") {
		return true
	}

	for _, w := range configSecretWords {
		if strings.Contains(leaf, w) {
			return true
		}
	}

	return configSecretKeys[leaf] || configSecretKeys[strings.ToLower(strings.Join(path, "."))]
}

// ---------------------------------------------------------------------------
// Environment overrides, by go/ast over internal/config.

// configEnvScan is the result of interpreting applyEnvOverrides.
type configEnvScan struct {
	funcs  map[string]*ast.FuncDecl
	consts map[string]string
	// byPath maps a dotted Go field path to the variables that set it, in
	// source order.
	byPath map[string][]string
	// chained marks variables read only when an earlier one of the same
	// if/else-if chain is unset, so the first listed variable wins.
	chained map[string]bool
	// loose are variables that are read into something other than a field.
	loose []string
}

func configParseDir(dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigSource, err)
	}

	fset := token.NewFileSet()

	var files []*ast.File

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errConfigSource, err)
		}

		files = append(files, f)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no Go files in %s", errConfigSource, dir)
	}

	return files, nil
}

func configScanEnv(files []*ast.File) (*configEnvScan, error) {
	sc := &configEnvScan{
		funcs:   map[string]*ast.FuncDecl{},
		consts:  map[string]string{},
		byPath:  map[string][]string{},
		chained: map[string]bool{},
	}

	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					sc.funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				collectConfigConsts(d, sc.consts)
			}
		}
	}

	entry, ok := sc.funcs[configEnvEntry]
	if !ok || entry.Type.Params == nil || len(entry.Type.Params.List) == 0 || len(entry.Type.Params.List[0].Names) == 0 {
		return nil, fmt.Errorf("%w: %s(cfg *Config) not found in %s", errConfigScan, configEnvEntry, configPkgDir)
	}

	bind := map[string][]string{entry.Type.Params.List[0].Names[0].Name: {}}

	if err := sc.walk(entry, bind, 0); err != nil {
		return nil, err
	}

	if len(sc.byPath) == 0 {
		return nil, fmt.Errorf("%w: %s sets no config field", errConfigScan, configEnvEntry)
	}

	return sc, nil
}

// walk interprets the statements of fn in source order. bind maps the names
// that refer to (a part of) the config to their Go field path.
func (sc *configEnvScan) walk(fn *ast.FuncDecl, bind map[string][]string, depth int) error {
	if depth > configMaxCallDepth {
		return fmt.Errorf("%w: %s nests helper calls deeper than %d", errConfigScan, fn.Name.Name, configMaxCallDepth)
	}

	consts := maps.Clone(sc.consts)

	var err error

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GenDecl:
			collectConfigConsts(n, consts)
		case *ast.AssignStmt:
			bindConfigAlias(n, bind)
		case *ast.IfStmt:
			err = sc.lookupIf(n, bind, consts)
		case *ast.CallExpr:
			err = sc.call(n, bind, consts, depth)
		}

		return err == nil
	})

	return err
}

// call handles overrideX(&field, "NAME") and calls of helpers that receive the
// config (or a part of it).
func (sc *configEnvScan) call(call *ast.CallExpr, bind map[string][]string, consts map[string]string, depth int) error {
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return nil
	}

	if strings.HasPrefix(id.Name, "override") && len(call.Args) == 2 {
		name, ok := configConstString(call.Args[1], consts)
		if !ok {
			return fmt.Errorf("%w: the variable name passed to %s is not a constant string", errConfigScan, id.Name)
		}

		if path, ok := configGoPath(call.Args[0], bind); ok {
			sc.add(strings.Join(path, "."), name)
		} else if !slices.Contains(sc.loose, name) {
			sc.loose = append(sc.loose, name)
		}

		return nil
	}

	callee, ok := sc.funcs[id.Name]
	if !ok || callee.Type.Params == nil {
		return nil
	}

	calleeBind := map[string][]string{}
	i := 0

	for _, p := range callee.Type.Params.List {
		for _, name := range p.Names {
			if i < len(call.Args) {
				if path, ok := configGoPath(call.Args[i], bind); ok {
					calleeBind[name.Name] = path
				}
			}

			i++
		}
	}

	if len(calleeBind) == 0 {
		return nil
	}

	return sc.walk(callee, calleeBind, depth+1)
}

// lookupIf handles `if v, ok := os.LookupEnv("NAME"); ok { cfg.A.B = v }`: the
// variable feeds every config field assigned in the body.
func (sc *configEnvScan) lookupIf(n *ast.IfStmt, bind map[string][]string, consts map[string]string) error {
	name, ok, err := lookupEnvName(n, consts)
	if err != nil || !ok {
		return err
	}

	found := false

	ast.Inspect(n.Body, func(c ast.Node) bool {
		as, ok := c.(*ast.AssignStmt)
		if !ok || as.Tok != token.ASSIGN {
			return true
		}

		for _, lhs := range as.Lhs {
			if path, ok := configGoPath(lhs, bind); ok {
				sc.add(strings.Join(path, "."), name)

				found = true
			}
		}

		return true
	})

	if !found && !slices.Contains(sc.loose, name) {
		sc.loose = append(sc.loose, name)
	}

	if next, ok := n.Else.(*ast.IfStmt); ok {
		if alt, ok, err := lookupEnvName(next, consts); err != nil {
			return err
		} else if ok {
			sc.chained[alt] = true
		}
	}

	return nil
}

// lookupEnvName returns the variable of an `if ...:= os.LookupEnv("NAME"); ...` statement.
func lookupEnvName(n *ast.IfStmt, consts map[string]string) (string, bool, error) {
	as, ok := n.Init.(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return "", false, nil
	}

	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false, nil
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "LookupEnv" {
		return "", false, nil
	}

	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
		return "", false, nil
	}

	name, ok := configConstString(call.Args[0], consts)
	if !ok {
		return "", false, fmt.Errorf("%w: os.LookupEnv is called with a non-constant name", errConfigScan)
	}

	return name, true, nil
}

func (sc *configEnvScan) add(goPath, name string) {
	if !slices.Contains(sc.byPath[goPath], name) {
		sc.byPath[goPath] = append(sc.byPath[goPath], name)
	}
}

// bindConfigAlias records `r := &cfg.A.B` so later `r.C` resolves to A.B.C.
func bindConfigAlias(as *ast.AssignStmt, bind map[string][]string) {
	if as.Tok != token.DEFINE || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return
	}

	id, ok := as.Lhs[0].(*ast.Ident)
	if !ok {
		return
	}

	if path, ok := configGoPath(as.Rhs[0], bind); ok {
		bind[id.Name] = path
	}
}

func collectConfigConsts(d *ast.GenDecl, into map[string]string) {
	if d.Tok != token.CONST {
		return
	}

	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		for i, name := range vs.Names {
			if i >= len(vs.Values) {
				break
			}

			if s, ok := configConstString(vs.Values[i], into); ok {
				into[name.Name] = s
			}
		}
	}
}

// configConstString evaluates string literals, named constants and their
// concatenation.
func configConstString(e ast.Expr, consts map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}

		s, err := strconv.Unquote(e.Value)

		return s, err == nil
	case *ast.Ident:
		s, ok := consts[e.Name]

		return s, ok
	case *ast.ParenExpr:
		return configConstString(e.X, consts)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}

		l, lok := configConstString(e.X, consts)
		r, rok := configConstString(e.Y, consts)

		return l + r, lok && rok
	}

	return "", false
}

// configGoPath resolves `&cfg.A.B`, `cfg.A.B` or a bound alias to its Go field
// path.
func configGoPath(e ast.Expr, bind map[string][]string) ([]string, bool) {
	switch e := e.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return configGoPath(e.X, bind)
		}
	case *ast.ParenExpr:
		return configGoPath(e.X, bind)
	case *ast.Ident:
		path, ok := bind[e.Name]

		return slices.Clone(path), ok
	case *ast.SelectorExpr:
		base, ok := configGoPath(e.X, bind)
		if !ok {
			return nil, false
		}

		return append(base, e.Sel.Name), true
	}

	return nil, false
}

// configBindEnv attaches the environment variables to the keys and returns the
// inconsistencies between the two.
func configBindEnv(keys []configKey, sc *configEnvScan) []string {
	var problems []string

	known := map[string]bool{}

	for i := range keys {
		k := &keys[i]
		known[k.goPath] = true
		k.envs = slices.Clone(sc.byPath[k.goPath])

		for _, name := range slices.Sorted(maps.Keys(configLooseEnv)) {
			if a := configLooseEnv[name]; a.key == k.dotted() {
				k.envs = append(k.envs, name)
			}
		}

		if len(k.envs) == 0 {
			problems = append(problems, fmt.Sprintf("yaml key %s has no environment override (add one to %s in %s)", k.dotted(), configEnvEntry, configPkgDir))
		}

		for _, name := range sc.byPath[k.goPath][min(1, len(sc.byPath[k.goPath])):] {
			if !sc.chained[name] {
				problems = append(problems, fmt.Sprintf("yaml key %s is set by several variables (%s) that are not an if/else-if chain, so the first listed would not win", k.dotted(), strings.Join(sc.byPath[k.goPath], ", ")))

				break
			}
		}
	}

	for _, goPath := range slices.Sorted(maps.Keys(sc.byPath)) {
		if !known[goPath] {
			problems = append(problems, fmt.Sprintf("environment override %s sets config field %s, which is not a yaml key", strings.Join(sc.byPath[goPath], ", "), goPath))
		}
	}

	return append(problems, configCheckLoose(keys, sc)...)
}

// configCheckLoose verifies configLooseEnv against the scan and the schema.
func configCheckLoose(keys []configKey, sc *configEnvScan) []string {
	var problems []string

	for _, name := range sc.loose {
		if _, ok := configLooseEnv[name]; !ok {
			problems = append(problems, fmt.Sprintf("environment override %s does not set a config field; list it in configLooseEnv in cmd/docgen/config.go", name))
		}
	}

	for _, name := range slices.Sorted(maps.Keys(configLooseEnv)) {
		a := configLooseEnv[name]

		if !slices.Contains(sc.loose, name) {
			problems = append(problems, fmt.Sprintf("configLooseEnv lists %s, which %s no longer reads that way", name, configEnvEntry))
		}

		if a.key != "" && !slices.ContainsFunc(keys, func(k configKey) bool { return k.dotted() == a.key }) {
			problems = append(problems, fmt.Sprintf("configLooseEnv maps %s to unknown yaml key %s", name, a.key))
		}
	}

	return problems
}

// ---------------------------------------------------------------------------
// Descriptions, from the doc comments of the config structs.

type configFieldDoc struct{ doc, trailing string }

type configTypeDoc struct {
	doc    string
	fields map[string]configFieldDoc
}

type configDocs struct {
	root   string
	module string
	pkgs   map[string]map[string]configTypeDoc
}

func newConfigDocs(root string) (*configDocs, error) {
	pkg := reflect.TypeFor[config.Config]().PkgPath()

	module, ok := strings.CutSuffix(pkg, "/"+configPkgDir)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not at %s", errConfigSource, pkg, configPkgDir)
	}

	return &configDocs{root: root, module: module, pkgs: map[string]map[string]configTypeDoc{}}, nil
}

// typeDoc returns the documentation of a struct type, parsing its package on
// first use.
func (d *configDocs) typeDoc(t reflect.Type) (configTypeDoc, error) {
	pkg := t.PkgPath()

	types, ok := d.pkgs[pkg]
	if !ok {
		rel, inModule := strings.CutPrefix(pkg, d.module+"/")
		if !inModule {
			return configTypeDoc{}, fmt.Errorf("%w: %s lives outside the module", errConfigSource, t)
		}

		files, err := configParseDir(filepath.Join(d.root, filepath.FromSlash(rel)))
		if err != nil {
			return configTypeDoc{}, err
		}

		types = configStructDocs(files)
		d.pkgs[pkg] = types
	}

	td, ok := types[t.Name()]
	if !ok {
		return configTypeDoc{}, fmt.Errorf("%w: struct %s not found in %s", errConfigSource, t.Name(), pkg)
	}

	return td, nil
}

func configStructDocs(files []*ast.File) map[string]configTypeDoc {
	out := map[string]configTypeDoc{}

	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}

			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}

				doc := ts.Doc
				if doc == nil {
					doc = gd.Doc
				}

				td := configTypeDoc{doc: configProse(doc), fields: map[string]configFieldDoc{}}

				for _, fld := range st.Fields.List {
					for _, name := range fld.Names {
						td.fields[name.Name] = configFieldDoc{doc: configProse(fld.Doc), trailing: configProse(fld.Comment)}
					}
				}

				out[ts.Name.Name] = td
			}
		}
	}

	return out
}

// configProse flattens a comment group into one line of text.
func configProse(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}

	return strings.Join(strings.Fields(cg.Text()), " ")
}

// keyDescription is the markdown description of a key, secret flag included.
func (d *configDocs) keyDescription(k configKey) (string, error) {
	td, err := d.typeDoc(k.owner)
	if err != nil {
		return "", err
	}

	fd := td.fields[k.field]

	text := strings.TrimSpace(fd.doc + " " + fd.trailing)
	if text == "" {
		return "", fmt.Errorf("yaml key %s has no description (add a doc comment to %s.%s)", k.dotted(), k.owner.Name(), k.field)
	}

	text = configSubject(text, k.field, k.path[len(k.path)-1])

	md, err := configMarkdown(d.root, text)
	if err != nil {
		return "", fmt.Errorf("yaml key %s: %w", k.dotted(), err)
	}

	if configIsSecret(k.path) {
		md = "**Secret.** " + md
	}

	return md, nil
}

// groupIntros returns the introduction of every group, keyed by dotted path:
// the doc comment of the group's field, else of its struct type.
func (d *configDocs) groupIntros(groups []configGroup) (map[string]string, []string) {
	intros := map[string]string{}

	var problems []string

	for _, g := range groups {
		text, err := d.groupText(g)
		if err != nil {
			problems = append(problems, err.Error())

			continue
		}

		intros[g.dotted()] = text
	}

	return intros, problems
}

func (d *configDocs) groupText(g configGroup) (string, error) {
	owner, err := d.typeDoc(g.owner)
	if err != nil {
		return "", err
	}

	text, name := owner.fields[g.field].doc, g.field

	if text == "" {
		own, err := d.typeDoc(g.typ)
		if err != nil {
			return "", err
		}

		text, name = own.doc, g.typ.Name()
	}

	if text == "" {
		return "", fmt.Errorf("section %s has no description (document %s.%s or type %s)", g.dotted(), g.owner.Name(), g.field, g.typ.Name())
	}

	md, err := configMarkdown(d.root, configSubject(text, name, g.dotted()))
	if err != nil {
		return "", fmt.Errorf("section %s: %w", g.dotted(), err)
	}

	return md, nil
}

// configSubject swaps the leading Go identifier of a doc comment ("Interval
// is ...") for the YAML key in code style, which is what a reader knows.
func configSubject(text, goName, yamlName string) string {
	if rest, ok := strings.CutPrefix(text, goName+" "); ok {
		return "`" + yamlName + "` " + rest
	}

	return text
}

// configMarkdown turns prose into markdown that renders the same on GitHub and
// in VitePress: docs/*.md references become relative links, and characters
// that start HTML, Vue interpolation or table cells are escaped outside code
// spans.
func configMarkdown(root, text string) (string, error) {
	parts := strings.Split(text, "`")
	if len(parts)%2 == 0 {
		return "", fmt.Errorf("%w: unbalanced backtick in %q", errConfigMD, text)
	}

	var (
		b       strings.Builder
		linkErr error
	)

	for i, part := range parts {
		if i%2 == 1 {
			b.WriteString("`" + strings.ReplaceAll(part, "|", `\|`) + "`")

			continue
		}

		part = configDocRef.ReplaceAllStringFunc(part, func(ref string) string {
			m := configDocRef.FindStringSubmatch(ref)

			if _, err := os.Stat(filepath.Join(root, "docs", filepath.FromSlash(m[1]))); err != nil && linkErr == nil {
				linkErr = fmt.Errorf("%w: %s links to docs/%s, which does not exist", errConfigMD, text, m[1])
			}

			return "[" + m[1] + m[2] + "](../" + m[1] + m[2] + ")"
		})

		part = configEnvToken.ReplaceAllString(part, "`${0}`")
		part = strings.NewReplacer("|", `\|`, "<", `\<`, "{{", `\{\{`, "}}", `\}\}`).Replace(part)
		b.WriteString(part)
	}

	return b.String(), linkErr
}

// ---------------------------------------------------------------------------
// Example files.

// configCheckExamples fails when .env.example or openartifact.example.yaml
// misses a variable or a key.
func configCheckExamples(root string, keys []configKey, groups []configGroup, sc *configEnvScan) ([]string, error) {
	envText, err := os.ReadFile(filepath.Join(root, configEnvExample))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigSource, err)
	}

	yamlText, err := os.ReadFile(filepath.Join(root, configYAMLExample))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigSource, err)
	}

	var problems []string

	have := configEnvExampleNames(string(envText))

	for _, name := range configAllEnv(keys, sc) {
		if !have[name] {
			problems = append(problems, fmt.Sprintf("%s does not mention %s (expected a line starting with `%s=` or `# %s=`)", configEnvExample, name, name, name))
		}
	}

	segments := map[string]bool{}

	for _, k := range keys {
		for _, s := range k.path {
			segments[s] = true
		}
	}

	for _, g := range groups {
		for _, s := range g.path {
			segments[s] = true
		}
	}

	found := configYAMLExampleKeys(string(yamlText), segments)

	for _, k := range keys {
		if !found[k.dotted()] {
			problems = append(problems, fmt.Sprintf("%s does not mention %s (expected `%s:` or `# %s:` nested under %s)",
				configYAMLExample, k.dotted(), k.path[len(k.path)-1], k.path[len(k.path)-1], strings.Join(k.path[:len(k.path)-1], ".")))
		}
	}

	return problems, nil
}

// configAllEnv lists every variable of the schema, in key order, without duplicates.
func configAllEnv(keys []configKey, sc *configEnvScan) []string {
	var out []string

	for _, k := range keys {
		for _, name := range k.envs {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}

	for _, name := range sc.loose {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}

	return out
}

// configEnvExampleNames returns the variables an env file mentions at the start
// of a line, commented out or not.
func configEnvExampleNames(content string) map[string]bool {
	out := map[string]bool{}

	for line := range strings.Lines(content) {
		if m := configEnvLine.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}

	return out
}

// configYAMLExampleKeys returns the dotted paths of the keys a YAML example
// mentions, commented out or not. Nesting follows indentation; for a commented
// key the indentation is the column of the `#` plus the spaces after the first
// one, so both `  # key:` and `#  key:` land at their original column. Comment
// lines only count when their word is a segment of a real key (segments), so
// prose such as "Note: ..." cannot disturb the nesting.
func configYAMLExampleKeys(content string, segments map[string]bool) map[string]bool {
	type frame struct {
		indent int
		name   string
	}

	var stack []frame

	found := map[string]bool{}

	for line := range strings.Lines(content) {
		indent, text, commented := configYAMLLineParts(strings.TrimRight(line, "\r\n"))

		m := configYAMLLine.FindStringSubmatch(text)
		if m == nil || (commented && !segments[m[1]]) {
			continue
		}

		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}

		stack = append(stack, frame{indent: indent, name: m[1]})

		names := make([]string, len(stack))
		for i, f := range stack {
			names[i] = f.name
		}

		found[strings.Join(names, ".")] = true
	}

	return found
}

// configYAMLLineParts splits a line into its indentation, its text after the
// comment markers and whether it was commented out.
func configYAMLLineParts(line string) (int, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	lead := len(line) - len(trimmed)

	rest, ok := strings.CutPrefix(trimmed, "#")
	if !ok {
		return lead, trimmed, false
	}

	body := strings.TrimLeft(rest, " ")
	indent := lead + max(len(rest)-len(body)-1, 0)

	// A second marker (`#   # key:`) only comments out an optional key.
	if inner, ok := strings.CutPrefix(body, "#"); ok {
		body = strings.TrimLeft(inner, " ")
	}

	return indent, body, true
}

// ---------------------------------------------------------------------------
// Rendering.

func renderConfig(keys []configKey, groups []configGroup, intros map[string]string, sc *configEnvScan) []byte {
	var b bytes.Buffer

	b.Write(header("Configuration reference", "`internal/config` and the packages it embeds"))
	b.WriteString(configIntro)

	if root := configSection(keys, func(k configKey) bool { return len(k.path) == 1 }); len(root) > 0 {
		b.WriteString("## Top level\n\n")
		writeConfigTable(&b, root)
	}

	for _, g := range groups {
		if len(g.path) != 1 {
			continue
		}

		b.WriteString("## " + g.dotted() + "\n\n" + intros[g.dotted()] + "\n\n")

		if direct := configSection(keys, func(k configKey) bool { return len(k.path) == 2 && k.path[0] == g.path[0] }); len(direct) > 0 {
			writeConfigTable(&b, direct)
		}

		for _, sub := range groups {
			if len(sub.path) != 2 || sub.path[0] != g.path[0] {
				continue
			}

			rows := configSection(keys, func(k configKey) bool { return len(k.path) > 2 && k.path[0] == sub.path[0] && k.path[1] == sub.path[1] })
			if len(rows) == 0 {
				continue
			}

			b.WriteString("### `" + sub.dotted() + "`\n\n" + intros[sub.dotted()] + "\n\n")
			writeConfigTable(&b, rows)
		}
	}

	writeConfigEnvOnly(&b, sc)

	return b.Bytes()
}

// configIntro explains the page and the table columns.
const configIntro = "This page lists every configuration key with its type, built-in default and the environment variable that overrides it. " +
	"It is generated from the code (`internal/config` and the packages whose settings it embeds), so it cannot drift from what the server reads. " +
	"For the load order (defaults, then the YAML file, then the environment), validation and how to start the server with a file, see [Configuration](../getting-started/configuration.md). " +
	"Annotated copies of every key are in [`openartifact.example.yaml`](../../openartifact.example.yaml) and [`.env.example`](../../.env.example).\n\n" +
	"- **YAML key**: the dotted path in the configuration file; `server.port` is `port` under `server:`.\n" +
	"- **Env**: the environment variable that overrides the key; it takes precedence over the file. " +
	"When several variables are listed, they are aliases and the first one that is set wins.\n" +
	"- **Type**: `string`, `int`, `bool`, `float`, `duration` (Go syntax such as `5m` or `90s`), " +
	"`list` (a YAML sequence; comma-separated in the environment) or `map` (a YAML mapping; comma-separated `key=value` pairs in the environment).\n" +
	"- **Default**: the value used when neither the file nor the environment sets the key; `empty` is an empty string, list or map.\n" +
	"- Credentials are marked **Secret**: keep them in environment variables or in a file only the server can read.\n\n"

func configSection(keys []configKey, in func(configKey) bool) []configKey {
	var out []configKey

	for _, k := range keys {
		if in(k) {
			out = append(out, k)
		}
	}

	return out
}

func writeConfigTable(b *bytes.Buffer, keys []configKey) {
	b.WriteString("| YAML key | Env | Type | Default | Description |\n|---|---|---|---|---|\n")

	for _, k := range keys {
		envs := make([]string, len(k.envs))
		for i, e := range k.envs {
			envs[i] = "`" + e + "`"
		}

		def := "`empty`"
		if k.def != "" {
			def = "`" + strings.ReplaceAll(k.def, "|", `\|`) + "`"
		}

		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n", k.dotted(), strings.Join(envs, ", "), k.kind, def, k.desc)
	}

	b.WriteString("\n")
}

// writeConfigEnvOnly lists the variables the loader reads that have no key and
// points to the hand-written page for the ones read elsewhere.
func writeConfigEnvOnly(b *bytes.Buffer, sc *configEnvScan) {
	b.WriteString("## Environment-only variables\n\n")

	var names []string

	for _, name := range sc.loose {
		if configLooseEnv[name].key == "" {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	if len(names) > 0 {
		b.WriteString("These variables are read by the configuration loader but have no YAML key.\n\n| Variable | Description |\n|---|---|\n")

		for _, name := range names {
			fmt.Fprintf(b, "| `%s` | %s |\n", name, configLooseEnv[name].note)
		}

		b.WriteString("\n")
	}

	b.WriteString("Variables read outside the configuration loader, such as the secret-encryption key, the metrics scrape token and the migration switch, " +
		"are described in [Environment-only variables](../getting-started/configuration.md#environment-only-variables).\n")
}
