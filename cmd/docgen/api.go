// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// apiSpecFile is the embedded OpenAPI document, relative to the repository root.
	apiSpecFile = "internal/api/openapi.json"
	// apiPrefix is the directory under docs/reference owned by this generator.
	apiPrefix = "api/"
	// apiRefPrefix starts every supported $ref.
	apiRefPrefix = "#/components/schemas/"
	// apiSchemasPage is the page every schema is rendered on.
	apiSchemasPage = "schemas.md"
	// apiOtherTag collects the operations that carry no tag.
	apiOtherTag = "Other"
	// apiMaxDepth bounds how deep inline objects are flattened into field tables.
	apiMaxDepth = 4
)

// errAPISpec marks every problem found in the OpenAPI document.
var errAPISpec = errors.New("invalid OpenAPI spec")

var (
	apiCodeSpan   = regexp.MustCompile("`[^`\n]*`")
	apiRiskySpan  = regexp.MustCompile(`\{\{.*?\}\}|<[^<>\s][^<>]*>`)
	apiLeadMarker = regexp.MustCompile(`^([#>+*|-]|\d+[.)])`)
	apiSchemaName = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

func init() {
	register(generator{
		name: "api",
		owns: func(rel string) bool { return strings.HasPrefix(rel, apiPrefix) },
		run:  genAPI,
	})
}

// apiMethods is the order operations of one path are rendered in.
func apiMethods() []string {
	return []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}
}

type apiSpec struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"info"`
	Servers []struct {
		URL         string `json:"url"`
		Description string `json:"description"`
	} `json:"servers"`
	Security   []map[string][]string                 `json:"security"`
	Tags       []apiTag                              `json:"tags"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		SecuritySchemes map[string]apiScheme  `json:"securitySchemes"` //nolint:tagliatelle // OpenAPI field name
		Schemas         map[string]*apiSchema `json:"schemas"`
	} `json:"components"`
}

type apiTag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type apiScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat"` //nolint:tagliatelle // OpenAPI field name
	In           string `json:"in"`
	Name         string `json:"name"`
	Description  string `json:"description"`
}

type apiSchema struct {
	Ref                  string                `json:"$ref"`
	Type                 string                `json:"type"`
	Format               string                `json:"format"`
	Description          string                `json:"description"`
	Nullable             bool                  `json:"nullable"`
	Enum                 []json.RawMessage     `json:"enum"`
	Default              json.RawMessage       `json:"default"`
	Minimum              *float64              `json:"minimum"`
	Maximum              *float64              `json:"maximum"`
	MinLength            *int                  `json:"minLength"` //nolint:tagliatelle // OpenAPI field name
	MaxLength            *int                  `json:"maxLength"` //nolint:tagliatelle // OpenAPI field name
	Pattern              string                `json:"pattern"`
	Properties           map[string]*apiSchema `json:"properties"`
	Required             []string              `json:"required"`
	Items                *apiSchema            `json:"items"`
	AllOf                []*apiSchema          `json:"allOf"`                //nolint:tagliatelle // OpenAPI field name
	AdditionalProperties apiAdditional         `json:"additionalProperties"` //nolint:tagliatelle // OpenAPI field name
}

// apiAdditional is additionalProperties: a boolean or a schema.
type apiAdditional struct {
	schema *apiSchema
}

func (a *apiAdditional) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	a.schema = &apiSchema{}

	return json.Unmarshal(trimmed, a.schema)
}

type apiParameter struct {
	Ref         string     `json:"$ref"`
	Name        string     `json:"name"`
	In          string     `json:"in"`
	Description string     `json:"description"`
	Required    bool       `json:"required"`
	Schema      *apiSchema `json:"schema"`
}

type apiMedia struct {
	Schema *apiSchema `json:"schema"`
}

type apiBody struct {
	Ref         string              `json:"$ref"`
	Description string              `json:"description"`
	Required    bool                `json:"required"`
	Content     map[string]apiMedia `json:"content"`
}

type apiResponse struct {
	Ref         string              `json:"$ref"`
	Description string              `json:"description"`
	Content     map[string]apiMedia `json:"content"`
	Headers     map[string]struct {
		Description string `json:"description"`
	} `json:"headers"`
}

type apiOperation struct {
	Summary     string                 `json:"summary"`
	Description string                 `json:"description"`
	Tags        []string               `json:"tags"`
	Deprecated  bool                   `json:"deprecated"`
	Parameters  []apiParameter         `json:"parameters"`
	RequestBody *apiBody               `json:"requestBody"` //nolint:tagliatelle // OpenAPI field name
	Responses   map[string]apiResponse `json:"responses"`
	// Security is nil when the operation inherits the global requirement and
	// non-nil (possibly empty) when it overrides it.
	Security *[]map[string][]string `json:"security"`
}

// apiOp is one operation with its place in the spec.
type apiOp struct {
	apiOperation

	method string
	path   string
}

type apiDoc struct {
	spec *apiSpec
	ops  []apiOp
}

// apiTagPage groups the operations rendered on one page.
type apiTagPage struct {
	name string
	desc string
	slug string
	ops  []*apiOp
}

func genAPI(root string) (map[string][]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(apiSpecFile)))
	if err != nil {
		return nil, fmt.Errorf("read OpenAPI spec: %w", err)
	}

	doc, err := apiParse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", apiSpecFile, err)
	}

	return doc.render()
}

// apiParse decodes the spec and rejects anything the renderer cannot present
// faithfully: unresolved references, operations without responses, schema
// names whose anchors would not be stable.
func apiParse(data []byte) (*apiDoc, error) {
	var spec apiSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		return nil, fmt.Errorf("%w: unsupported openapi version %q", errAPISpec, spec.OpenAPI)
	}

	d := &apiDoc{spec: &spec}

	if err := d.validateSchemaNames(); err != nil {
		return nil, err
	}

	if err := d.collect(); err != nil {
		return nil, err
	}

	if len(d.ops) == 0 {
		return nil, fmt.Errorf("%w: the spec has no operations", errAPISpec)
	}

	if err := d.checkRefs(); err != nil {
		return nil, err
	}

	return d, nil
}

func apiSortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// validateSchemaNames makes sure the anchor of every schema heading is the
// lowercased name on both GitHub and VitePress and that no two collide.
func (d *apiDoc) validateSchemaNames() error {
	seen := map[string]string{"schemas": "the page heading"}

	for _, name := range apiSortedKeys(d.spec.Components.Schemas) {
		if !apiSchemaName.MatchString(name) {
			return fmt.Errorf("%w: schema name %q: use letters and digits only so the heading anchor is the same on GitHub and VitePress", errAPISpec, name)
		}

		slug := strings.ToLower(name)
		if prev, ok := seen[slug]; ok {
			return fmt.Errorf("%w: schema %q has the same anchor #%s as %s", errAPISpec, name, slug, prev)
		}

		seen[slug] = "schema " + name
	}

	return nil
}

// collect flattens paths into operations sorted by path, then method order.
func (d *apiDoc) collect() error {
	for _, path := range apiSortedKeys(d.spec.Paths) {
		if strings.ContainsAny(path, "<>|`") || strings.Contains(path, "{{") {
			return fmt.Errorf("%w: path %q contains characters that break markdown headings", errAPISpec, path)
		}

		item := d.spec.Paths[path]

		var common []apiParameter

		if raw, ok := item["parameters"]; ok {
			if err := json.Unmarshal(raw, &common); err != nil {
				return fmt.Errorf("%s: parameters: %w", path, err)
			}
		}

		for _, method := range apiMethods() {
			raw, ok := item[method]
			if !ok {
				continue
			}

			var op apiOperation
			if err := json.Unmarshal(raw, &op); err != nil {
				return fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}

			if len(op.Responses) == 0 {
				return fmt.Errorf("%w: %s %s: operation has no responses", errAPISpec, strings.ToUpper(method), path)
			}

			op.Parameters = apiMergeParams(common, op.Parameters)
			d.ops = append(d.ops, apiOp{apiOperation: op, method: method, path: path})
		}
	}

	return nil
}

// apiMergeParams returns path-level parameters not overridden by the
// operation, followed by the operation's own.
func apiMergeParams(common, own []apiParameter) []apiParameter {
	var out []apiParameter

	for _, c := range common {
		if !slices.ContainsFunc(own, func(p apiParameter) bool { return p.Name == c.Name && p.In == c.In }) {
			out = append(out, c)
		}
	}

	return append(out, own...)
}

func apiRefName(ref string) string { return strings.TrimPrefix(ref, apiRefPrefix) }

// checkRefs fails on any $ref that does not point at a component schema, and
// on parameters, bodies or responses given by reference (not used by the spec,
// and not rendered).
func (d *apiDoc) checkRefs() error {
	for _, name := range apiSortedKeys(d.spec.Components.Schemas) {
		if err := d.checkSchema("schema "+name, d.spec.Components.Schemas[name]); err != nil {
			return err
		}
	}

	for i := range d.ops {
		if err := d.checkOp(&d.ops[i]); err != nil {
			return fmt.Errorf("%s %s: %w", strings.ToUpper(d.ops[i].method), d.ops[i].path, err)
		}
	}

	return nil
}

func (d *apiDoc) checkOp(op *apiOp) error {
	for _, p := range op.Parameters {
		if p.Ref != "" {
			return fmt.Errorf("%w: parameter $ref %q is not supported", errAPISpec, p.Ref)
		}

		if err := d.checkSchema("parameter "+p.Name, p.Schema); err != nil {
			return err
		}
	}

	if b := op.RequestBody; b != nil {
		if b.Ref != "" {
			return fmt.Errorf("%w: requestBody $ref %q is not supported", errAPISpec, b.Ref)
		}

		if err := d.checkContent("request body", b.Content); err != nil {
			return err
		}
	}

	for code, resp := range op.Responses {
		if resp.Ref != "" {
			return fmt.Errorf("%w: response %s $ref %q is not supported", errAPISpec, code, resp.Ref)
		}

		if err := d.checkContent("response "+code, resp.Content); err != nil {
			return err
		}
	}

	return nil
}

func (d *apiDoc) checkContent(where string, content map[string]apiMedia) error {
	for _, ct := range apiSortedKeys(content) {
		if err := d.checkSchema(where, content[ct].Schema); err != nil {
			return err
		}
	}

	return nil
}

func (d *apiDoc) checkSchema(where string, s *apiSchema) error {
	if s == nil {
		return nil
	}

	if s.Ref != "" {
		if !strings.HasPrefix(s.Ref, apiRefPrefix) || d.spec.Components.Schemas[apiRefName(s.Ref)] == nil {
			return fmt.Errorf("%w: %s: unresolved $ref %q", errAPISpec, where, s.Ref)
		}
	}

	children := []*apiSchema{s.Items, s.AdditionalProperties.schema}
	children = append(children, s.AllOf...)

	for _, name := range apiSortedKeys(s.Properties) {
		children = append(children, s.Properties[name])
	}

	for _, c := range children {
		if err := d.checkSchema(where, c); err != nil {
			return err
		}
	}

	return nil
}

// apiSlug turns a tag name into a page file name: lowercase, runs of
// non-alphanumerics become one dash.
func apiSlug(s string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}

			dash = false

			b.WriteRune(r)

			continue
		}

		dash = true
	}

	return b.String()
}

// tagPages groups operations by tag: declared tags first, in declaration
// order, then the rest alphabetically. Untagged operations go to "Other".
func (d *apiDoc) tagPages() ([]apiTagPage, error) {
	byName := map[string]*apiTagPage{}

	for i := range d.ops {
		names := d.ops[i].Tags
		if len(names) == 0 {
			names = []string{apiOtherTag}
		}

		for _, n := range names {
			p := byName[n]
			if p == nil {
				p = &apiTagPage{name: n}
				byName[n] = p
			}

			p.ops = append(p.ops, &d.ops[i])
		}
	}

	var pages []apiTagPage

	for _, t := range d.spec.Tags {
		if p := byName[t.Name]; p != nil && p.desc == "" {
			p.desc = t.Description

			pages = append(pages, *p)

			delete(byName, t.Name)
		}
	}

	rest := apiSortedKeys(byName)
	slices.SortStableFunc(rest, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })

	for _, n := range rest {
		pages = append(pages, *byName[n])
	}

	return pages, apiAssignSlugs(pages)
}

func apiAssignSlugs(pages []apiTagPage) error {
	used := map[string]string{"readme": "the index page", "schemas": "the schemas page"}

	for i := range pages {
		slug := apiSlug(pages[i].name)
		if slug == "" {
			return fmt.Errorf("%w: tag %q has no letters or digits to build a page name from", errAPISpec, pages[i].name)
		}

		if prev, ok := used[slug]; ok {
			return fmt.Errorf("%w: tag %q maps to page %s.md, already used by %s", errAPISpec, pages[i].name, slug, prev)
		}

		used[slug] = "tag " + pages[i].name
		pages[i].slug = slug
	}

	return nil
}

func (d *apiDoc) render() (map[string][]byte, error) {
	pages, err := d.tagPages()
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}
	r := &apiRenderer{doc: d, base: apiSchemasPage}

	for i := range pages {
		out[apiPrefix+pages[i].slug+".md"] = r.tagPage(&pages[i])
	}

	out[apiPrefix+"README.md"] = r.indexPage(pages)
	out[apiPrefix+apiSchemasPage] = (&apiRenderer{doc: d}).schemasPage()

	return out, nil
}

// apiRenderer renders markdown; base is the page schema links point to, empty
// when rendering the schemas page itself.
type apiRenderer struct {
	doc  *apiDoc
	base string
}

func (r *apiRenderer) schemas() map[string]*apiSchema { return r.doc.spec.Components.Schemas }

// apiPlain makes free text safe for markdown, GitHub and VitePress: text that
// looks like an HTML tag or a Vue interpolation is put in a code span.
func apiPlain(s string) string {
	var b strings.Builder

	last := 0

	for _, loc := range apiRiskySpan.FindAllStringIndex(s, -1) {
		b.WriteString(apiEscapeGap(s[last:loc[0]]))
		b.WriteString("`" + s[loc[0]:loc[1]] + "`")

		last = loc[1]
	}

	b.WriteString(apiEscapeGap(s[last:]))

	return b.String()
}

func apiEscapeGap(s string) string {
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "<", "`<`")

	return strings.ReplaceAll(s, "{{", "`{{`")
}

// apiText flattens whitespace and escapes everything outside code spans.
func apiText(s string) string {
	s = strings.Join(strings.Fields(s), " ")

	var b strings.Builder

	last := 0

	for _, loc := range apiCodeSpan.FindAllStringIndex(s, -1) {
		b.WriteString(apiPlain(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])

		last = loc[1]
	}

	b.WriteString(apiPlain(s[last:]))

	out := b.String()
	if m := apiLeadMarker.FindString(out); m != "" {
		if strings.HasSuffix(m, ".") || strings.HasSuffix(m, ")") {
			out = m[:len(m)-1] + `\` + out[len(m)-1:]
		} else {
			out = `\` + out
		}
	}

	return out
}

func apiCell(cells []string) string {
	esc := make([]string, len(cells))
	for i, c := range cells {
		if c == "" {
			c = "-"
		}

		esc[i] = strings.ReplaceAll(c, "|", `\|`)
	}

	return "| " + strings.Join(esc, " | ") + " |\n"
}

func apiTable(b *strings.Builder, head []string, rows [][]string) {
	b.WriteString(apiCell(head))
	b.WriteString("|" + strings.Repeat("---|", len(head)) + "\n")

	for _, row := range rows {
		b.WriteString(apiCell(row))
	}

	b.WriteString("\n")
}

func apiYesNo(v bool) string {
	if v {
		return "yes"
	}

	return "no"
}

func apiCode(s string) string { return "`" + strings.ReplaceAll(s, "`", "'") + "`" }

// apiValue formats a JSON scalar from enum or default.
func apiValue(raw json.RawMessage) string {
	var s string
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte{'"'}) && json.Unmarshal(raw, &s) == nil {
		return apiCode(s)
	}

	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return apiCode(string(raw))
	}

	return apiCode(buf.String())
}

func apiNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// apiNotes lists constraints of a schema as short sentences.
func apiNotes(s *apiSchema) []string {
	if s == nil || s.Ref != "" {
		return nil
	}

	var notes []string

	if s.Nullable {
		notes = append(notes, "Nullable.")
	}

	if len(s.Enum) > 0 {
		vals := make([]string, len(s.Enum))
		for i, e := range s.Enum {
			vals[i] = apiValue(e)
		}

		notes = append(notes, "One of: "+strings.Join(vals, ", ")+".")
	}

	if len(s.Default) > 0 {
		notes = append(notes, "Default: "+apiValue(s.Default)+".")
	}

	notes = append(notes, apiRangeNotes(s)...)

	if s.Type == "array" {
		for _, n := range apiNotes(s.Items) {
			notes = append(notes, "Items: "+strings.ToLower(n[:1])+n[1:])
		}
	}

	return notes
}

func apiRangeNotes(s *apiSchema) []string {
	var notes []string

	if s.Minimum != nil {
		notes = append(notes, "Minimum: "+apiNum(*s.Minimum)+".")
	}

	if s.Maximum != nil {
		notes = append(notes, "Maximum: "+apiNum(*s.Maximum)+".")
	}

	if s.MinLength != nil {
		notes = append(notes, "Minimum length: "+strconv.Itoa(*s.MinLength)+".")
	}

	if s.MaxLength != nil {
		notes = append(notes, "Maximum length: "+strconv.Itoa(*s.MaxLength)+".")
	}

	if s.Pattern != "" {
		notes = append(notes, "Pattern: "+apiCode(s.Pattern)+".")
	}

	return notes
}

// apiDescribe joins a description with the constraint notes of the schema.
func apiDescribe(desc string, s *apiSchema) string {
	text := apiText(desc)

	notes := apiNotes(s)
	if len(notes) == 0 {
		return text
	}

	if text != "" && !strings.ContainsAny(text[len(text)-1:], ".!?:") {
		text += "."
	}

	return strings.TrimSpace(text + " " + strings.Join(notes, " "))
}

func (r *apiRenderer) link(name string) string {
	return "[" + name + "](" + r.base + "#" + strings.ToLower(name) + ")"
}

// typeOf renders the type of a schema; named schemas become links.
func (r *apiRenderer) typeOf(s *apiSchema) string {
	switch {
	case s == nil:
		return "any"
	case s.Ref != "":
		return r.link(apiRefName(s.Ref))
	case len(s.AllOf) > 0:
		parts := make([]string, len(s.AllOf))
		for i, p := range s.AllOf {
			parts[i] = r.typeOf(p)
		}

		return strings.Join(parts, " + ")
	case s.Type == "array":
		if s.Items == nil {
			return "array"
		}

		return "array of " + r.typeOf(s.Items)
	case s.Type == "object" || (s.Type == "" && len(s.Properties) > 0):
		if ap := s.AdditionalProperties.schema; ap != nil && len(s.Properties) == 0 {
			return "map of " + r.typeOf(ap)
		}

		return "object"
	case s.Type == "":
		return "any"
	case s.Format != "":
		return s.Type + " (" + s.Format + ")"
	default:
		return s.Type
	}
}

// apiField is one row of a field table.
type apiField struct {
	name     string
	typ      string
	required bool
	desc     string
}

// merge collects the properties of a schema, following allOf and references
// (which only occur inside allOf here). seen guards against reference cycles.
func (r *apiRenderer) merge(s *apiSchema, seen map[string]bool, props map[string]*apiSchema, req map[string]bool) {
	if s == nil {
		return
	}

	if s.Ref != "" {
		name := apiRefName(s.Ref)
		if seen[name] {
			return
		}

		seen[name] = true
		defer delete(seen, name)

		s = r.schemas()[name]
	}

	for _, part := range s.AllOf {
		r.merge(part, seen, props, req)
	}

	maps.Copy(props, s.Properties)

	for _, n := range s.Required {
		req[n] = true
	}
}

// apiExpandable returns the inline object to expand below a property, and the
// separator that continues its field names.
func apiExpandable(p *apiSchema) (*apiSchema, string) {
	if p == nil || p.Ref != "" || len(p.AllOf) > 0 {
		return nil, ""
	}

	switch {
	case len(p.Properties) > 0:
		return p, "."
	case p.Type == "array" && p.Items != nil && p.Items.Ref == "" && len(p.Items.AllOf) == 0 && len(p.Items.Properties) > 0:
		return p.Items, "[]."
	case p.AdditionalProperties.schema != nil && len(p.AdditionalProperties.schema.Properties) > 0 && p.AdditionalProperties.schema.Ref == "":
		return p.AdditionalProperties.schema, ".*."
	}

	return nil, ""
}

// fields flattens an object schema into rows; inline nested objects are
// expanded with dotted names down to apiMaxDepth.
func (r *apiRenderer) fields(s *apiSchema, prefix string, depth int, seen map[string]bool) []apiField {
	props := map[string]*apiSchema{}
	req := map[string]bool{}

	r.merge(s, seen, props, req)

	var rows []apiField

	for _, name := range apiSortedKeys(props) {
		p := props[name]
		rows = append(rows, apiField{
			name:     prefix + name,
			typ:      r.typeOf(p),
			required: req[name],
			desc:     apiDescribe(p.Description, p),
		})

		if child, sep := apiExpandable(p); child != nil && depth < apiMaxDepth {
			rows = append(rows, r.fields(child, prefix+name+sep, depth+1, seen)...)
		}
	}

	return rows
}

func (r *apiRenderer) writeFields(b *strings.Builder, rows []apiField) {
	table := make([][]string, len(rows))
	for i, f := range rows {
		table[i] = []string{apiCode(f.name), f.typ, apiYesNo(f.required), f.desc}
	}

	apiTable(b, []string{"Field", "Type", "Required", "Description"}, table)
}

// inlineFields returns the field rows of an inline body schema; schemas given
// by reference are documented on the schemas page and yield nothing.
func (r *apiRenderer) inlineFields(s *apiSchema) []apiField {
	if s == nil || s.Ref != "" {
		return nil
	}

	if s.Type == "array" {
		if s.Items == nil || s.Items.Ref != "" {
			return nil
		}

		return r.fields(s.Items, "[].", 1, map[string]bool{})
	}

	return r.fields(s, "", 0, map[string]bool{})
}

func (r *apiRenderer) tagPage(p *apiTagPage) []byte {
	var b strings.Builder

	b.Write(header(apiText(p.name)+" API", apiSpecFile))

	if p.desc != "" {
		b.WriteString(apiText(p.desc) + "\n\n")
	}

	noun := "endpoints"
	if len(p.ops) == 1 {
		noun = "endpoint"
	}

	fmt.Fprintf(&b, "%d %s. Request and response schemas are described on the [schemas page](%s); the other tags are listed in the [API index](README.md).\n\n", len(p.ops), noun, apiSchemasPage)
	b.WriteString("## Endpoints\n\n")

	for _, op := range p.ops {
		r.operation(&b, op)
	}

	return []byte(b.String())
}

func (r *apiRenderer) operation(b *strings.Builder, op *apiOp) {
	// Code span: a heading ending in {id} would be read as an attribute list by VitePress.
	fmt.Fprintf(b, "### `%s %s`\n\n", strings.ToUpper(op.method), op.path)

	if s := apiText(op.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}

	if d := apiText(op.Description); d != "" && d != apiText(op.Summary) {
		b.WriteString(d + "\n\n")
	}

	if op.Deprecated {
		b.WriteString("**Deprecated.**\n\n")
	}

	if line := apiSecurityLine(op.Security); line != "" {
		b.WriteString(line + "\n\n")
	}

	r.parameters(b, op.Parameters)
	r.requestBody(b, op.RequestBody)
	r.responses(b, op.Responses)
}

// apiSecurityLine describes an explicit security override of an operation.
func apiSecurityLine(sec *[]map[string][]string) string {
	if sec == nil {
		return ""
	}

	if len(*sec) == 0 {
		return "**Authentication:** none (public endpoint)."
	}

	return "**Security:** " + apiAlternatives(*sec) + "."
}

// apiAlternatives renders security requirements: any one of them suffices,
// the schemes inside one must all be satisfied.
func apiAlternatives(sec []map[string][]string) string {
	alts := make([]string, len(sec))

	for i, req := range sec {
		names := make([]string, 0, len(req))

		for _, n := range apiSortedKeys(req) {
			item := apiCode(n)
			if len(req[n]) > 0 {
				scopes := make([]string, len(req[n]))
				for j, sc := range req[n] {
					scopes[j] = apiCode(sc)
				}

				item += " (scopes " + strings.Join(scopes, ", ") + ")"
			}

			names = append(names, item)
		}

		alts[i] = strings.Join(names, " and ")
	}

	return strings.Join(alts, " or ")
}

func (r *apiRenderer) parameters(b *strings.Builder, params []apiParameter) {
	if len(params) == 0 {
		return
	}

	rows := make([][]string, len(params))

	for i, p := range params {
		desc := p.Description
		if desc == "" && p.Schema != nil {
			desc = p.Schema.Description
		}

		rows[i] = []string{
			apiCode(p.Name),
			p.In,
			r.typeOf(p.Schema),
			apiYesNo(p.Required || p.In == "path"),
			apiDescribe(desc, p.Schema),
		}
	}

	b.WriteString("**Parameters**\n\n")
	apiTable(b, []string{"Name", "In", "Type", "Required", "Description"}, rows)
}

func (r *apiRenderer) requestBody(b *strings.Builder, body *apiBody) {
	if body == nil {
		return
	}

	b.WriteString("**Request body**")

	if body.Required {
		b.WriteString(" (required)")
	}

	if d := apiText(body.Description); d != "" {
		b.WriteString(": " + d)
	}

	b.WriteString("\n\n")

	if len(body.Content) == 0 {
		b.WriteString("No content type is declared.\n\n")

		return
	}

	for _, ct := range apiSortedKeys(body.Content) {
		s := body.Content[ct].Schema
		b.WriteString(apiCode(ct) + ": " + r.typeOf(s) + "\n\n")

		if rows := r.inlineFields(s); len(rows) > 0 {
			r.writeFields(b, rows)
		}
	}
}

// apiStatusLess orders numeric status codes first, then the rest (default).
func apiStatusLess(a, b string) bool {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)

	switch {
	case errA == nil && errB == nil:
		return na < nb
	case errA == nil:
		return true
	case errB == nil:
		return false
	default:
		return a < b
	}
}

func (r *apiRenderer) responseBody(content map[string]apiMedia) string {
	if len(content) == 0 {
		return "-"
	}

	parts := make([]string, 0, len(content))

	for _, ct := range apiSortedKeys(content) {
		if s := content[ct].Schema; s != nil {
			parts = append(parts, apiCode(ct)+": "+r.typeOf(s))
		} else {
			parts = append(parts, apiCode(ct))
		}
	}

	return strings.Join(parts, "; ")
}

func apiResponseDesc(resp apiResponse) string {
	desc := apiText(resp.Description)

	if len(resp.Headers) > 0 {
		names := make([]string, 0, len(resp.Headers))
		for _, n := range apiSortedKeys(resp.Headers) {
			names = append(names, apiCode(n))
		}

		if desc != "" {
			desc += " "
		}

		desc += "Headers: " + strings.Join(names, ", ") + "."
	}

	if desc == "" {
		return "-"
	}

	return desc
}

func (r *apiRenderer) responses(b *strings.Builder, resps map[string]apiResponse) {
	codes := slices.SortedFunc(maps.Keys(resps), func(a, c string) int {
		switch {
		case apiStatusLess(a, c):
			return -1
		case apiStatusLess(c, a):
			return 1
		}

		return 0
	})

	rows := make([][]string, len(codes))
	for i, code := range codes {
		rows[i] = []string{apiCode(code), apiResponseDesc(resps[code]), r.responseBody(resps[code].Content)}
	}

	b.WriteString("**Responses**\n\n")
	apiTable(b, []string{"Status", "Description", "Body"}, rows)

	for _, code := range codes {
		content := resps[code].Content

		for _, ct := range apiSortedKeys(content) {
			rows := r.inlineFields(content[ct].Schema)
			if len(rows) == 0 {
				continue
			}

			b.WriteString("Fields of the " + apiCode(code) + " response (" + apiCode(ct) + "):\n\n")
			r.writeFields(b, rows)
		}
	}
}

func (r *apiRenderer) schemasPage() []byte {
	var b strings.Builder

	b.Write(header("API schemas", apiSpecFile))
	b.WriteString("Named request and response objects of the [REST API](README.md), sorted by name. Field names of nested inline objects are dotted; `[]` marks array elements and `*` map values.\n\n")
	b.WriteString("## Schemas\n\n")

	names := apiSortedKeys(r.schemas())
	if len(names) == 0 {
		b.WriteString("The API defines no named schemas.\n")

		return []byte(b.String())
	}

	for _, name := range names {
		r.schema(&b, name, r.schemas()[name])
	}

	return []byte(b.String())
}

func (r *apiRenderer) schema(b *strings.Builder, name string, s *apiSchema) {
	b.WriteString("### " + name + "\n\n")

	if d := apiText(s.Description); d != "" {
		b.WriteString(d + "\n\n")
	}

	if len(s.AllOf) > 0 {
		var base []string

		for _, p := range s.AllOf {
			if p.Ref != "" {
				base = append(base, r.typeOf(p))
			}
		}

		if len(base) > 0 {
			b.WriteString("Includes all fields of " + strings.Join(base, ", ") + ".\n\n")
		}
	}

	if rows := r.fields(s, "", 0, map[string]bool{name: true}); len(rows) > 0 {
		r.writeFields(b, rows)

		return
	}

	b.WriteString("Type: " + r.typeOf(s))

	if notes := apiNotes(s); len(notes) > 0 {
		b.WriteString(". " + strings.Join(notes, " "))
	} else {
		b.WriteString(".")
	}

	b.WriteString("\n\n")
}

func (r *apiRenderer) indexPage(pages []apiTagPage) []byte {
	var b strings.Builder

	b.Write(header("REST API reference", apiSpecFile))

	if t := apiText(r.doc.spec.Info.Description); t != "" {
		b.WriteString(t + "\n\n")
	}

	fmt.Fprintf(&b, "This reference is generated from the embedded OpenAPI document `%s` and lists %d endpoints in %d tags. Authentication, token kinds, CSRF handling and worked examples are in the [REST API guide](../../integrations/api.md).\n\n", apiSpecFile, len(r.doc.ops), len(pages))

	r.writeSecurity(&b)
	r.writeServers(&b)

	b.WriteString("## Endpoints by tag\n\n")

	rows := make([][]string, len(pages))
	for i, p := range pages {
		rows[i] = []string{"[" + apiText(p.name) + "](" + p.slug + ".md)", strconv.Itoa(len(p.ops)), apiText(p.desc)}
	}

	apiTable(&b, []string{"Tag", "Endpoints", "Description"}, rows)

	b.WriteString("## Schemas\n\n")
	fmt.Fprintf(&b, "All %d named request and response objects are on the [schemas page](%s).\n", len(r.schemas()), apiSchemasPage)

	return []byte(b.String())
}

func (r *apiRenderer) writeSecurity(b *strings.Builder) {
	spec := r.doc.spec
	if len(spec.Components.SecuritySchemes) == 0 && len(spec.Security) == 0 {
		return
	}

	b.WriteString("## Security\n\n")

	if len(spec.Security) > 0 {
		b.WriteString("Unless an endpoint says otherwise, a request must satisfy one of: " + apiAlternatives(spec.Security) + ".\n\n")
	}

	if len(spec.Components.SecuritySchemes) == 0 {
		return
	}

	rows := make([][]string, 0, len(spec.Components.SecuritySchemes))

	for _, name := range apiSortedKeys(spec.Components.SecuritySchemes) {
		sc := spec.Components.SecuritySchemes[name]
		rows = append(rows, []string{apiCode(name), sc.Type, apiSchemeDetail(sc), apiText(sc.Description)})
	}

	apiTable(b, []string{"Scheme", "Type", "Details", "Description"}, rows)
}

func apiSchemeDetail(sc apiScheme) string {
	var parts []string

	if sc.Scheme != "" {
		parts = append(parts, "scheme "+apiCode(sc.Scheme))
	}

	if sc.BearerFormat != "" {
		parts = append(parts, "format "+apiCode(sc.BearerFormat))
	}

	if sc.In != "" {
		parts = append(parts, apiCode(sc.Name)+" in "+sc.In)
	}

	if len(parts) == 0 {
		return "-"
	}

	return strings.Join(parts, ", ")
}

func (r *apiRenderer) writeServers(b *strings.Builder) {
	if len(r.doc.spec.Servers) == 0 {
		return
	}

	b.WriteString("## Servers\n\n")

	for _, s := range r.doc.spec.Servers {
		b.WriteString("- " + apiCode(s.URL))

		if d := apiText(s.Description); d != "" {
			b.WriteString(": " + d)
		}

		b.WriteString("\n")
	}

	b.WriteString("\n")
}
