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
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/metrics"
)

const (
	metricsPage   = "metrics.md"
	metricsPrefix = "openartifact_"
)

// metricsSourceDirs are scanned (recursively, tests excluded) for collector
// definitions. The scan classifies every collector and proves that the set of
// collectors docgen registers is the complete set the code defines.
//
//nolint:gochecknoglobals // immutable list of directories
var metricsSourceDirs = []string{"internal", "cmd"}

// metricGroupTitles are display names for the metric name prefixes (the token
// after openartifact_). A prefix without an entry is shown capitalised, so new
// subsystems need no entry here.
//
//nolint:gochecknoglobals // immutable display names
var metricGroupTitles = map[string]string{
	"http":        "HTTP",
	"db":          "Database connection pools",
	"federation":  "Federation",
	"recovery":    "Disaster-recovery journal",
	"replication": "Blob replication",
	"storage":     "Storage tiering",
	"blob":        "Blob store",
	"scanner":     "Vulnerability scanners",
	"uploads":     "Uploads",
	"metadata":    "Metadata regeneration",
	"trash":       "Trash",
	"audit":       "Audit log",
}

func init() {
	register(generator{
		name: "metrics",
		owns: func(rel string) bool { return rel == metricsPage },
		run:  runMetrics,
	})
}

// metricDef is one exposed metric family.
type metricDef struct {
	Name   string
	Help   string
	Type   string
	Labels []string
	// Buckets are the histogram upper bounds; nil when unknown or not a histogram.
	Buckets []float64
}

// sourceMetric is a collector definition found in the Go sources.
type sourceMetric struct {
	Name    string
	Type    string
	Buckets []float64
	Where   string // file:line, relative to the root, for error messages
}

func runMetrics(root string) (map[string][]byte, error) {
	described, err := describeRegistered()
	if err != nil {
		return nil, err
	}

	found, err := scanMetricSources(root, metricsSourceDirs)
	if err != nil {
		return nil, err
	}

	defs, err := mergeMetrics(described, found)
	if err != nil {
		return nil, err
	}

	page, err := renderMetrics(defs)
	if err != nil {
		return nil, err
	}

	return map[string][]byte{metricsPage: page}, nil
}

// describedMetric is what a collector's Desc says: name, help and label names.
type describedMetric struct {
	Name, Help string
	Labels     []string
}

// descRecorder is a prometheus.Registerer that keeps the Descs of the
// collectors instead of registering them, so collectors that expose nothing
// before their first observation (vectors) are still seen.
type descRecorder struct {
	descs []*prometheus.Desc
}

func (r *descRecorder) Register(c prometheus.Collector) error {
	ch := make(chan *prometheus.Desc)

	go func() {
		c.Describe(ch)
		close(ch)
	}()

	for d := range ch {
		r.descs = append(r.descs, d)
	}

	return nil
}

func (r *descRecorder) MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		_ = r.Register(c)
	}
}

func (r *descRecorder) Unregister(prometheus.Collector) bool { return false }

// describeRegistered builds every collector the server registers on its
// registry and returns their descriptions.
func describeRegistered() ([]describedMetric, error) {
	rec := &descRecorder{}

	metrics.NewRegistryOn(rec)
	rec.MustRegister(metrics.NewDBPoolCollector())

	if _, err := federation.NewMetrics(rec, nil); err != nil {
		return nil, fmt.Errorf("federation metrics: %w", err)
	}

	seen := map[string]bool{}
	out := make([]describedMetric, 0, len(rec.descs))

	for _, d := range rec.descs {
		m, err := parseDesc(d.String())
		if err != nil {
			return nil, err
		}

		if seen[m.Name] {
			return nil, fmt.Errorf("metric %s is described twice", m.Name)
		}

		seen[m.Name] = true

		out = append(out, m)
	}

	if len(out) == 0 {
		return nil, errors.New("no collectors were described")
	}

	return out, nil
}

// descPattern matches prometheus.Desc.String. Desc keeps its fields private and
// has no accessor, so the string form is the only way to read the help text and
// the label names; the unit test pins the format against client_golang.
var descPattern = regexp.MustCompile(
	`^Desc\{fqName: ("(?:[^"\\]|\\.)*"), help: ("(?:[^"\\]|\\.)*"), (?:unit: "(?:[^"\\]|\\.)*", )?constLabels: \{(.*)\}, variableLabels: \{(.*)\}\}$`)

// parseDesc extracts name, help and variable label names from Desc.String.
func parseDesc(s string) (describedMetric, error) {
	m := descPattern.FindStringSubmatch(s)
	if m == nil {
		return describedMetric{}, fmt.Errorf("unrecognised Desc format %q (client_golang changed Desc.String?)", s)
	}

	name, err := strconv.Unquote(m[1])
	if err != nil {
		return describedMetric{}, fmt.Errorf("desc name %s: %w", m[1], err)
	}

	help, err := strconv.Unquote(m[2])
	if err != nil {
		return describedMetric{}, fmt.Errorf("desc help of %s: %w", name, err)
	}

	if m[3] != "" {
		return describedMetric{}, fmt.Errorf("metric %s has constant labels (%s): not supported by the generator", name, m[3])
	}

	var labels []string

	if m[4] != "" {
		for l := range strings.SplitSeq(m[4], ",") {
			l = strings.TrimSuffix(strings.TrimPrefix(l, "c("), ")") // label with a value constraint

			labels = append(labels, l)
		}
	}

	return describedMetric{Name: name, Help: help, Labels: labels}, nil
}

// mergeMetrics joins the registered descriptions with the source scan and
// verifies both directions.
func mergeMetrics(described []describedMetric, found map[string]sourceMetric) ([]metricDef, error) {
	defs := make([]metricDef, 0, len(described))
	have := map[string]bool{}

	for _, d := range described {
		have[d.Name] = true

		src, ok := found[d.Name]
		if !ok {
			return nil, fmt.Errorf("cannot classify %s: no constructor with this literal name found under %s", d.Name, strings.Join(metricsSourceDirs, ", "))
		}

		defs = append(defs, metricDef{Name: d.Name, Help: d.Help, Labels: d.Labels, Type: src.Type, Buckets: src.Buckets})
	}

	var missing []string

	for name, src := range found {
		if !have[name] {
			missing = append(missing, name+" ("+src.Where+")")
		}
	}

	if len(missing) > 0 {
		slices.Sort(missing)

		return nil, fmt.Errorf("collectors defined in the sources but not built by the generator (add them to describeRegistered in cmd/docgen/metrics.go): %s", strings.Join(missing, ", "))
	}

	slices.SortFunc(defs, func(a, b metricDef) int { return strings.Compare(a.Name, b.Name) })

	return defs, nil
}

// Collector constructors by the metric type they create.
//
//nolint:gochecknoglobals // immutable lookup tables
var (
	optsConstructors = map[string]string{
		"NewCounter": "counter", "NewCounterVec": "counter", "NewCounterFunc": "counter",
		"NewGauge": "gauge", "NewGaugeVec": "gauge", "NewGaugeFunc": "gauge",
		"NewHistogram": "histogram", "NewHistogramVec": "histogram",
		"NewSummary": "summary", "NewSummaryVec": "summary",
		"NewUntypedFunc": "untyped",
	}
	valueTypes = map[string]string{
		"CounterValue": "counter", "GaugeValue": "gauge", "UntypedValue": "untyped",
	}
)

// scanMetricSources parses the non-test Go files below dirs and returns every
// collector with a literal name, keyed by name.
func scanMetricSources(root string, dirs []string) (map[string]sourceMetric, error) {
	byDir := map[string][]string{}

	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if e.IsDir() {
				if p == filepath.Join(root, "cmd", "docgen") || e.Name() == "testdata" {
					return filepath.SkipDir
				}

				return nil
			}

			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], p)
			}

			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", d, err)
		}
	}

	out := map[string]sourceMetric{}

	for dir, paths := range byDir {
		slices.Sort(paths)

		pkg, err := scanPackage(root, paths)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.ToSlash(mustRel(root, dir)), err)
		}

		for _, m := range pkg {
			if prev, dup := out[m.Name]; dup {
				if prev.Type != m.Type {
					return nil, fmt.Errorf("metric %s defined with two types (%s at %s, %s at %s)", m.Name, prev.Type, prev.Where, m.Type, m.Where)
				}

				continue
			}

			out[m.Name] = m
		}
	}

	return out, nil
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}

	return r
}

// pkgScan holds the state of scanning one package.
type pkgScan struct {
	fset  *token.FileSet
	root  string
	files []*ast.File
	// vars are package-level variable initialisers (bucket lists).
	vars map[string]ast.Expr
	// descs are NewDesc definitions awaiting their value type, by the field or
	// variable name they are assigned to.
	descs map[string][]*sourceMetric
	// descCalls are the NewDesc calls whose destination was recognised.
	descCalls map[*ast.CallExpr]bool
	// wrappers are helpers that forward a *Desc parameter to MustNewConstMetric:
	// helper name to (parameter index, value type).
	wrappers map[string]wrapper
	// wrapped are the MustNewConstMetric calls inside wrappers.
	wrapped map[*ast.CallExpr]bool
	// usage records the value types each desc key is emitted with.
	usage map[string]map[string]bool
	out   []sourceMetric
}

type wrapper struct {
	param int
	typ   string
}

func scanPackage(root string, paths []string) ([]sourceMetric, error) {
	s := &pkgScan{
		fset: token.NewFileSet(), root: root,
		vars:      map[string]ast.Expr{},
		descs:     map[string][]*sourceMetric{},
		descCalls: map[*ast.CallExpr]bool{},
		wrappers:  map[string]wrapper{},
		wrapped:   map[*ast.CallExpr]bool{},
		usage:     map[string]map[string]bool{},
	}

	for _, p := range paths {
		f, err := parser.ParseFile(s.fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}

		s.files = append(s.files, f)
	}

	s.collectVars()
	s.collectWrappers()

	for _, f := range s.files {
		if err := s.walk(f); err != nil {
			return nil, err
		}
	}

	return s.finish()
}

func (s *pkgScan) where(n ast.Node) string {
	p := s.fset.Position(n.Pos())

	return filepath.ToSlash(mustRel(s.root, p.Filename)) + ":" + strconv.Itoa(p.Line)
}

func (s *pkgScan) collectVars() {
	for _, f := range s.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}

			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}

				for i, n := range vs.Names {
					s.vars[n.Name] = vs.Values[i]
				}
			}
		}
	}
}

// collectWrappers finds functions that pass one of their parameters as the Desc
// of MustNewConstMetric/NewConstMetric, so calls to them count as emitting that
// Desc.
func (s *pkgScan) collectWrappers() {
	for _, f := range s.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Body != nil {
					s.wrapperOf(fn.Name.Name, fn.Type, fn.Body)
				}
			case *ast.AssignStmt:
				if len(fn.Lhs) == 1 && len(fn.Rhs) == 1 {
					lit, isLit := fn.Rhs[0].(*ast.FuncLit)
					id, isID := fn.Lhs[0].(*ast.Ident)

					if isLit && isID {
						s.wrapperOf(id.Name, lit.Type, lit.Body)
					}
				}
			}

			return true
		})
	}
}

func (s *pkgScan) wrapperOf(name string, ft *ast.FuncType, body *ast.BlockStmt) {
	var params []string

	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			params = append(params, "_")
		}

		for _, n := range field.Names {
			params = append(params, n.Name)
		}
	}

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isConstMetricCall(call) {
			return true
		}

		id, ok := call.Args[0].(*ast.Ident)
		if !ok {
			return true
		}

		if i := slices.Index(params, id.Name); i >= 0 {
			s.wrappers[name] = wrapper{param: i, typ: valueTypeOf(call.Args[1])}
			s.wrapped[call] = true
		}

		return true
	})
}

func isConstMetricCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) < 2 {
		return false
	}

	return isPromPkg(sel.X) && (sel.Sel.Name == "MustNewConstMetric" || sel.Sel.Name == "NewConstMetric")
}

func isPromPkg(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)

	return ok && (id.Name == "prometheus" || id.Name == "promauto")
}

// valueTypeOf maps a prometheus.XxxValue expression to a metric type, "?" when
// it is something else.
func valueTypeOf(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok && isPromPkg(sel.X) {
		if t, ok := valueTypes[sel.Sel.Name]; ok {
			return t
		}
	}

	return "?"
}

// lastName is the identifier a Desc is referred to by: x for x, a.b.x for x.
func lastName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.StarExpr:
		return lastName(v.X)
	case *ast.ParenExpr:
		return lastName(v.X)
	}

	return ""
}

func (s *pkgScan) walk(f *ast.File) error {
	var err error

	ast.Inspect(f, func(n ast.Node) bool {
		if err != nil {
			return false
		}

		switch v := n.(type) {
		case *ast.KeyValueExpr:
			s.noteDesc(lastName(v.Key), v.Value)
		case *ast.AssignStmt:
			if len(v.Lhs) == len(v.Rhs) {
				for i := range v.Lhs {
					s.noteDesc(lastName(v.Lhs[i]), v.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			if len(v.Names) == len(v.Values) {
				for i := range v.Names {
					s.noteDesc(v.Names[i].Name, v.Values[i])
				}
			}
		case *ast.CallExpr:
			err = s.visitCall(v)
		}

		return err == nil
	})

	return err
}

// noteDesc records a NewDesc assigned to key. It runs before the walk reaches
// the call itself, so visitCall can tell assigned Descs from stray ones.
func (s *pkgScan) noteDesc(key string, value ast.Expr) {
	call, ok := value.(*ast.CallExpr)
	if !ok || key == "" || !isNewDesc(call) {
		return
	}

	name, ok := stringLit(call.Args[0])
	if !ok {
		return // reported by visitCall
	}

	s.descCalls[call] = true
	s.descs[key] = append(s.descs[key], &sourceMetric{Name: name, Where: s.where(call)})
}

func isNewDesc(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)

	return ok && isPromPkg(sel.X) && sel.Sel.Name == "NewDesc" && len(call.Args) > 0
}

func (s *pkgScan) visitCall(call *ast.CallExpr) error {
	switch {
	case isNewDesc(call):
		if _, ok := stringLit(call.Args[0]); !ok {
			return fmt.Errorf("%s: NewDesc name is not a string literal", s.where(call))
		}

		if !s.descCalls[call] {
			return fmt.Errorf("%s: NewDesc is not assigned to a field or variable, so its type cannot be derived", s.where(call))
		}
	case isConstMetricCall(call):
		if !s.wrapped[call] {
			s.use(lastName(call.Args[0]), valueTypeOf(call.Args[1]))
		}
	default:
		if w, ok := s.wrappers[lastName(call.Fun)]; ok && w.param < len(call.Args) {
			s.use(lastName(call.Args[w.param]), w.typ)
		}

		return s.visitConstructor(call)
	}

	return nil
}

func (s *pkgScan) use(key, typ string) {
	if key == "" {
		return
	}

	if s.usage[key] == nil {
		s.usage[key] = map[string]bool{}
	}

	s.usage[key][typ] = true
}

// visitConstructor records prometheus.NewCounterVec(Opts{...}) style calls.
func (s *pkgScan) visitConstructor(call *ast.CallExpr) error {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	typ, ok := optsConstructors[sel.Sel.Name]
	if !ok || !isPromCtorReceiver(sel.X) || len(call.Args) == 0 {
		return nil
	}

	lit, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return fmt.Errorf("%s: %s options are not a literal", s.where(call), sel.Sel.Name)
	}

	fields := map[string]ast.Expr{}

	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			fields[lastName(kv.Key)] = kv.Value
		}
	}

	name, err := s.fqName(call, fields)
	if err != nil {
		return err
	}

	m := sourceMetric{Name: name, Type: typ, Where: s.where(call)}
	if typ == "histogram" {
		m.Buckets = s.resolveBuckets(fields["Buckets"], 0)
	}

	s.out = append(s.out, m)

	return nil
}

// isPromCtorReceiver accepts prometheus.NewX and promauto.With(reg).NewX.
func isPromCtorReceiver(x ast.Expr) bool {
	if isPromPkg(x) {
		return true
	}

	call, ok := x.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)

	return ok && isPromPkg(sel.X) && sel.Sel.Name == "With"
}

// fqName builds the metric name like prometheus.BuildFQName, from literals only.
func (s *pkgScan) fqName(call *ast.CallExpr, fields map[string]ast.Expr) (string, error) {
	var parts []string

	for _, k := range []string{"Namespace", "Subsystem", "Name"} {
		e, ok := fields[k]
		if !ok {
			continue
		}

		v, ok := stringLit(e)
		if !ok {
			return "", fmt.Errorf("%s: %s is not a string literal", s.where(call), k)
		}

		if v != "" {
			parts = append(parts, v)
		}
	}

	if _, ok := fields["Name"]; !ok {
		return "", fmt.Errorf("%s: collector without Name", s.where(call))
	}

	return strings.Join(parts, "_"), nil
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}

	v, err := strconv.Unquote(lit.Value)

	return v, err == nil
}

// finish attaches value types to the NewDesc definitions and returns all
// collectors of the package.
func (s *pkgScan) finish() ([]sourceMetric, error) {
	for key, defs := range s.descs {
		types := s.usage[key]

		for _, d := range defs {
			switch {
			case len(types) == 0:
				return nil, fmt.Errorf("%s: %s: no MustNewConstMetric emits the Desc %q, cannot derive its type", d.Where, d.Name, key)
			case len(types) > 1 || types["?"]:
				return nil, fmt.Errorf("%s: %s: Desc %q is emitted with several or non-constant value types", d.Where, d.Name, key)
			}

			for t := range types {
				d.Type = t
			}

			s.out = append(s.out, *d)
		}
	}

	return s.out, nil
}

// resolveBuckets evaluates a Buckets expression. It returns nil when the value
// is not statically known; a missing Buckets field means DefBuckets.
func (s *pkgScan) resolveBuckets(e ast.Expr, depth int) []float64 {
	if e == nil {
		return prometheus.DefBuckets
	}

	if depth > 4 {
		return nil
	}

	switch v := e.(type) {
	case *ast.CompositeLit:
		return floatList(v.Elts)
	case *ast.Ident:
		if def, ok := s.vars[v.Name]; ok {
			return s.resolveBuckets(def, depth+1)
		}
	case *ast.SelectorExpr:
		if isPromPkg(v.X) && v.Sel.Name == "DefBuckets" {
			return prometheus.DefBuckets
		}
	case *ast.CallExpr:
		return generatedBuckets(v)
	}

	return nil
}

func floatList(elts []ast.Expr) []float64 {
	out := make([]float64, 0, len(elts))

	for _, e := range elts {
		f, ok := floatLit(e)
		if !ok {
			return nil
		}

		out = append(out, f)
	}

	return out
}

func floatLit(e ast.Expr) (float64, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || (lit.Kind != token.FLOAT && lit.Kind != token.INT) {
		return 0, false
	}

	f, err := strconv.ParseFloat(lit.Value, 64)

	return f, err == nil
}

// generatedBuckets evaluates prometheus.ExponentialBuckets and LinearBuckets
// calls with literal arguments.
func generatedBuckets(call *ast.CallExpr) []float64 {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !isPromPkg(sel.X) || len(call.Args) != 3 {
		return nil
	}

	a, okA := floatLit(call.Args[0])
	b, okB := floatLit(call.Args[1])
	n, okN := floatLit(call.Args[2])

	if !okA || !okB || !okN || n < 1 {
		return nil
	}

	switch sel.Sel.Name {
	case "ExponentialBuckets":
		if a <= 0 || b <= 1 {
			return nil
		}

		return prometheus.ExponentialBuckets(a, b, int(n))
	case "LinearBuckets":
		return prometheus.LinearBuckets(a, b, int(n))
	}

	return nil
}

// renderMetrics writes the page.
func renderMetrics(defs []metricDef) ([]byte, error) {
	if len(defs) == 0 {
		return nil, errors.New("no metrics to document")
	}

	groups := map[string][]metricDef{}

	for _, d := range defs {
		g := metricGroup(d.Name)
		groups[g] = append(groups[g], d)
	}

	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}

	slices.Sort(names)

	var b bytes.Buffer

	b.Write(header("Metrics reference", "the Prometheus collectors in the server code"))
	b.WriteString("The server exposes Prometheus metrics at `GET /metrics`. Access fails closed: the endpoint answers `401` unless `METRICS_AUTH_TOKEN` is set (scrapers then send it as a bearer token) or `METRICS_ALLOW_ANONYMOUS=true`. See [Monitoring](../operations/monitoring.md#metrics) for the scrape configuration and how to read the families.\n\n")
	b.WriteString("Labelled metrics only appear after their first observation. Gauges of the database pools and of federation peers are read at scrape time.\n\n")
	b.WriteString("The Go runtime (`go_*`) and process (`process_*`) metrics of the standard client library are exposed as well and not listed here.\n\n")

	for _, g := range names {
		if err := renderMetricGroup(&b, g, groups[g]); err != nil {
			return nil, err
		}
	}

	return b.Bytes(), nil
}

func renderMetricGroup(b *bytes.Buffer, group string, defs []metricDef) error {
	b.WriteString("## " + metricGroupTitle(group) + "\n\n")
	b.WriteString("| Metric | Type | Labels | Help |\n|---|---|---|---|\n")

	for _, d := range defs {
		help, err := cellText(d.Name, d.Help)
		if err != nil {
			return err
		}

		labels := "none"
		if len(d.Labels) > 0 {
			labels = "`" + strings.Join(d.Labels, "`, `") + "`"
		}

		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", d.Name, d.Type, labels, help)
	}

	b.WriteString("\n")

	for _, d := range defs {
		if d.Type == "histogram" && len(d.Buckets) > 0 {
			strs := make([]string, len(d.Buckets))
			for i, v := range d.Buckets {
				strs[i] = strconv.FormatFloat(v, 'f', -1, 64)
			}

			fmt.Fprintf(b, "Buckets of `%s`: %s.\n\n", d.Name, strings.Join(strs, ", "))
		}
	}

	return nil
}

// cellText makes help text safe for a markdown table cell that VitePress also
// compiles: no pipes, no tags, no template delimiters.
func cellText(name, help string) (string, error) {
	if strings.Contains(help, "{{") || strings.Contains(help, "}}") {
		return "", fmt.Errorf("help of %s contains template delimiters", name)
	}

	help = strings.NewReplacer("|", `\|`, "<", "&lt;", ">", "&gt;", "\n", " ").Replace(strings.TrimSpace(help))

	return help, nil
}

// metricGroup is the first name token after the openartifact_ prefix.
func metricGroup(name string) string {
	rest := strings.TrimPrefix(name, metricsPrefix)
	if i := strings.IndexByte(rest, '_'); i > 0 {
		return rest[:i]
	}

	return rest
}

func metricGroupTitle(group string) string {
	if t, ok := metricGroupTitles[group]; ok {
		return t
	}

	return strings.ToUpper(group[:1]) + group[1:]
}
