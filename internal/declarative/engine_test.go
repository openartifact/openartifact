// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeKind is an in-memory Kind used to exercise the engine without a server.
type fakeKind struct {
	name string
	rank int
	refs func(o *Object) []Ref

	mu      sync.Mutex
	store   map[string]*Live
	calls   *[]string
	failOn  map[string]error
	secrets map[string]string
	nextID  int
}

func newFake(name string, rank int, calls *[]string) *fakeKind {
	return &fakeKind{name: name, rank: rank, store: map[string]*Live{}, calls: calls, failOn: map[string]error{}, secrets: map[string]string{}}
}

func (f *fakeKind) Name() string         { return f.name }
func (f *fakeKind) Rank() int            { return f.rank }
func (f *fakeKind) MetaKeys() []string   { return nil }
func (f *fakeKind) Key(o *Object) string { return o.Name }
func (f *fakeKind) Order(*Object) int    { return 0 }
func (f *fakeKind) Refs(o *Object) []Ref {
	if f.refs == nil {
		return nil
	}

	return f.refs(o)
}

func (f *fakeKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"title":  {Type: TString},
		"count":  {Type: TInt, HasMin: true, Min: 0},
		"flavor": {Type: TString, Enum: []string{"a", "b"}, Immutable: true},
		"tags":   {Type: TSet, Elem: &Field{Type: TString}},
		"items": {Type: TSet, KeyBy: "id", Elem: &Field{Type: TObject, Fields: map[string]*Field{
			"id":  {Type: TString, Required: true},
			"val": {Type: TInt},
		}}},
		"nested": {Type: TObject, Fields: map[string]*Field{"x": {Type: TInt}, "y": {Type: TBool}}},
		"token":  {Type: TString, Secret: true},
		"owner":  {Type: TString, FromMeta: true},
	}}
}

func (f *fakeKind) List(context.Context, *Env) ([]*Live, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []*Live
	for _, l := range f.store {
		out = append(out, l)
	}

	return out, nil
}

func (f *fakeKind) record(op, key string) error {
	*f.calls = append(*f.calls, op+" "+f.name+"/"+key)

	return f.failOn[op+" "+key]
}

func (f *fakeKind) Create(_ context.Context, _ *Env, o *Object) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("create", o.Name); err != nil {
		return "", err
	}

	f.nextID++
	id := fmt.Sprintf("%s-%d", strings.ToLower(f.name), f.nextID)
	spec := map[string]any{}

	for k, v := range o.Spec {
		if s, ok := v.(string); ok && k == "token" {
			f.secrets[o.Name] = s
			continue
		}

		spec[k] = v
	}

	f.store[o.Name] = &Live{Kind: f.name, Key: o.Name, ID: id, Spec: spec, Secrets: map[string]bool{"token": f.secrets[o.Name] != ""}}

	return id, nil
}

func (f *fakeKind) Update(_ context.Context, _ *Env, live *Live, o *Object, changes []Change) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("update", o.Name); err != nil {
		return "", err
	}

	for _, c := range changes {
		if c.Secret {
			f.secrets[o.Name], _ = c.New.(string)
			live.Secrets["token"] = true

			continue
		}

		live.Spec[TopField(c.Path)] = o.Spec[TopField(c.Path)]
	}

	return live.ID, nil
}

func (f *fakeKind) Delete(_ context.Context, _ *Env, live *Live) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.record("delete", live.Key); err != nil {
		return err
	}

	delete(f.store, live.Key)

	return nil
}

func (f *fakeKind) seed(key string, spec map[string]any, seeded bool) *Live {
	l := &Live{Kind: f.name, Key: key, ID: "id-" + key, Spec: spec, Seeded: seeded, Secrets: map[string]bool{}}
	f.store[key] = l

	return l
}

// markerServer is a fake of the ownership-marker endpoints.
type markerServer struct {
	mu      sync.Mutex
	markers map[string]Marker
	srv     *httptest.Server
}

func newMarkerServer(t *testing.T) *markerServer {
	t.Helper()

	m := &markerServer{markers: map[string]Marker{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))

	t.Cleanup(m.srv.Close)

	return m
}

func (m *markerServer) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		items := []Marker{}
		for _, mk := range m.markers {
			items = append(items, mk)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case http.MethodPut:
		var body struct {
			Items []Marker `json:"items"`
		}

		_ = json.NewDecoder(r.Body).Decode(&body)

		for _, it := range body.Items {
			m.markers[Ident(it.Kind, it.Name)] = it
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"items": body.Items})
	case http.MethodDelete:
		id := Ident(r.URL.Query().Get("kind"), r.URL.Query().Get("name"))
		if _, ok := m.markers[id]; !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		delete(m.markers, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (m *markerServer) client() *Client { return NewClient(m.srv.URL, "tok", m.srv.Client()) }

func (m *markerServer) has(kind, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.markers[Ident(kind, name)]

	return ok
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func loadYAML(t *testing.T, reg *Registry, yaml string) ([]*Object, error) {
	t.Helper()

	p := writeFile(t, t.TempDir(), "m.yaml", yaml)

	return Load(reg, LoadOptions{Paths: []string{p}})
}

func TestLoadValidDocuments(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))

	objs, err := loadYAML(t, reg, `
apiVersion: openartifact.io/v1
kind: Alpha
metadata:
  name: one
  owner: team-a
spec:
  title: First
  count: 3
  tags: [x, y]
  items:
    - {id: a, val: 1}
  token:
    secretRef: {env: ONE_TOKEN}
---
apiVersion: openartifact.io/v1
kind: Alpha
metadata:
  name: two
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(objs) != 2 || objs[0].Name != "one" || objs[1].Name != "two" {
		t.Fatalf("objects = %+v", objs)
	}

	if got := objs[0].Spec["owner"]; got != "team-a" {
		t.Errorf("metadata.owner not mapped into spec: %v", got)
	}

	if got := objs[0].Spec["token"]; got != (SecretRef{Env: "ONE_TOKEN"}) {
		t.Errorf("token = %#v, want env secretRef", got)
	}

	if got := objs[0].Spec["count"]; got != float64(3) {
		t.Errorf("count = %#v, want float64(3)", got)
	}
}

func TestLoadReportsEveryProblemWithLine(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))

	_, err := loadYAML(t, reg, `apiVersion: openartifact.io/v1
kind: Alpha
metadata:
  name: bad
spec:
  count: -1
  flavor: zzz
  bogus: 1
  token: inline-secret
  owner: nope
  items:
    - {val: 2}
`)
	if err == nil {
		t.Fatal("expected validation errors")
	}

	msg := err.Error()

	for _, want := range []string{
		"m.yaml:6: Alpha/bad: count: must be at least 0",
		`m.yaml:7: Alpha/bad: flavor: invalid value "zzz"`,
		"m.yaml:8: Alpha/bad: bogus: unknown field",
		"m.yaml:9: Alpha/bad: token: inline secret value not allowed",
		"metadata.owner, not in spec",
		"items[0].id: required",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error output missing %q:\n%s", want, msg)
		}
	}
}

func TestLoadRejectsStructuralMistakes(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))

	cases := map[string]struct{ yaml, want string }{
		"wrong apiVersion": {"apiVersion: v2\nkind: Alpha\nmetadata: {name: a}\n", "apiVersion: unsupported"},
		"unknown kind":     {"apiVersion: openartifact.io/v1\nkind: Nope\nmetadata: {name: a}\n", `unknown kind "Nope"`},
		"missing name":     {"apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {}\n", "metadata.name: required"},
		"duplicate key":    {"apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: a}\nspec: {count: 1, count: 2}\n", "duplicate key"},
		"duplicate object": {"apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: a}\n---\napiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: a}\n", "defined twice"},
		"syntax":           {"apiVersion: [\n", "invalid YAML"},
		"secret both":      {"apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: a}\nspec:\n  token: {secretRef: {env: A, file: b}}\n", "exactly one of env or file"},
		"unknown meta":     {"apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: a, color: red}\n", "unknown metadata field"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadYAML(t, reg, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadDirectoryRecursiveAndStdin(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))
	doc := func(name string) string {
		return "apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: " + name + "}\n"
	}

	dir := t.TempDir()
	writeFile(t, dir, "a.yaml", doc("top"))
	writeFile(t, dir, "sub/b.yml", doc("nested"))
	writeFile(t, dir, "ignored.txt", "not yaml: [")

	flat, err := Load(reg, LoadOptions{Paths: []string{dir}})
	if err != nil || len(flat) != 1 || flat[0].Name != "top" {
		t.Fatalf("non-recursive = %v, %v", flat, err)
	}

	rec, err := Load(reg, LoadOptions{Paths: []string{dir}, Recursive: true})
	if err != nil || len(rec) != 2 {
		t.Fatalf("recursive = %v, %v", rec, err)
	}

	in, err := Load(reg, LoadOptions{Paths: []string{"-"}, Stdin: strings.NewReader(doc("piped"))})
	if err != nil || len(in) != 1 || in[0].Name != "piped" || in[0].Pos.File != "<stdin>" {
		t.Fatalf("stdin = %+v, %v", in, err)
	}
}

func TestLoadEmptyDirectoryHintsAtRecursive(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))
	dir := t.TempDir()
	writeFile(t, dir, "sub/b.yaml", "apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: nested}\n")

	_, err := Load(reg, LoadOptions{Paths: []string{dir}})
	if err == nil || !strings.Contains(err.Error(), "-R") {
		t.Fatalf("error = %v, want a hint to use -R", err)
	}
}

func TestSecretFileResolvedRelativeToManifest(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))
	dir := t.TempDir()
	writeFile(t, dir, "pw.txt", "s3cret\n\n")

	p := writeFile(t, dir, "m.yaml", "apiVersion: openartifact.io/v1\nkind: Alpha\nmetadata: {name: a}\nspec:\n  token: {secretRef: {file: pw.txt}}\n")

	objs, err := Load(reg, LoadOptions{Paths: []string{p}})
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := SecretResolver{}.resolveObject(objs[0])
	if err != nil {
		t.Fatal(err)
	}

	if resolved.Spec["token"] != "s3cret" {
		t.Errorf("token = %q, want trailing newlines stripped", resolved.Spec["token"])
	}

	if _, still := objs[0].Spec["token"].(SecretRef); !still {
		t.Error("resolving must not mutate the loaded object")
	}
}

func TestSecretResolverFailures(t *testing.T) {
	r := SecretResolver{
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "SET":
				return "v", true
			case "EMPTY":
				return "", true
			}

			return "", false
		},
		ReadFile: func(string) ([]byte, error) { return []byte("\n"), nil },
	}

	if v, err := r.Resolve(SecretRef{Env: "SET"}); err != nil || v != "v" {
		t.Errorf("SET = %q, %v", v, err)
	}

	for name, ref := range map[string]SecretRef{
		"unset env": {Env: "MISSING"}, "empty env": {Env: "EMPTY"}, "blank file": {File: "/x"},
	} {
		if _, err := r.Resolve(ref); !errors.Is(err, ErrSecretUnresolved) {
			t.Errorf("%s: err = %v, want ErrSecretUnresolved", name, err)
		}
	}
}

func desired(kind, name string, spec map[string]any) *Object {
	return &Object{Kind: kind, Name: name, Spec: spec, Meta: map[string]string{}}
}

func planFor(reg *Registry, in PlanInput) *Plan {
	in.Registry = reg
	if in.Live == nil {
		in.Live = map[string][]*Live{}
	}

	if in.Markers == nil {
		in.Markers = Markers{}
	}

	return BuildPlan(in)
}

func liveOf(k *fakeKind) map[string][]*Live {
	l, _ := k.List(context.Background(), nil)

	return map[string][]*Live{k.name: l}
}

func TestPlanCreateUpdateUnchanged(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.seed("same", map[string]any{"title": "t", "count": float64(1)}, false)
	a.seed("changed", map[string]any{"title": "old", "count": float64(1)}, false)
	reg := NewRegistry(a)

	plan := planFor(reg, PlanInput{
		Live: liveOf(a),
		Desired: []*Object{
			desired("Alpha", "new", map[string]any{"title": "n"}),
			desired("Alpha", "same", map[string]any{"title": "t"}),
			desired("Alpha", "changed", map[string]any{"title": "new"}),
		},
	})

	got := map[string]Action{}
	for _, it := range plan.Items {
		got[it.Key] = it.Action
	}

	want := map[string]Action{"new": ActionCreate, "same": ActionUnchanged, "changed": ActionUpdate}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: action = %s, want %s", k, got[k], w)
		}
	}

	var changed *Item

	for _, it := range plan.Items {
		if it.Key == "changed" {
			changed = it
		}
	}

	if len(changed.Changes) != 1 || changed.Changes[0].Path != "title" || changed.Changes[0].Old != "old" || changed.Changes[0].New != "new" {
		t.Errorf("changes = %+v, want only title old->new (omitted count untouched)", changed.Changes)
	}

	if !plan.HasChanges() {
		t.Error("HasChanges = false")
	}
}

func TestPlanNoChangesWhenEverythingMatches(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.seed("x", map[string]any{
		"title": "t", "tags": []any{"b", "a"},
		"nested": map[string]any{"x": float64(1), "y": true},
		"items":  []any{map[string]any{"id": "i2", "val": float64(2), "extra": "ignored"}, map[string]any{"id": "i1", "val": float64(1)}},
	}, false)

	plan := planFor(NewRegistry(a), PlanInput{
		Live: liveOf(a),
		Desired: []*Object{desired("Alpha", "x", map[string]any{
			"title": "t", "tags": []any{"a", "b"},
			"nested": map[string]any{"x": float64(1)},
			"items":  []any{map[string]any{"id": "i1", "val": float64(1)}, map[string]any{"id": "i2", "val": float64(2)}},
		})},
	})

	if plan.HasChanges() {
		t.Fatalf("expected no changes, got %+v", plan.Items[0].Changes)
	}
}

func TestPlanKeyedSetChangesAreElementLevel(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.seed("x", map[string]any{"items": []any{
		map[string]any{"id": "keep", "val": float64(1)},
		map[string]any{"id": "mod", "val": float64(1)},
		map[string]any{"id": "gone", "val": float64(9)},
	}}, false)

	plan := planFor(NewRegistry(a), PlanInput{
		Live: liveOf(a),
		Desired: []*Object{desired("Alpha", "x", map[string]any{"items": []any{
			map[string]any{"id": "keep", "val": float64(1)},
			map[string]any{"id": "mod", "val": float64(2)},
			map[string]any{"id": "add", "val": float64(3)},
		}})},
	})

	paths := map[string]Change{}
	for _, c := range plan.Items[0].Changes {
		paths[c.Path] = c
	}

	if len(paths) != 3 {
		t.Fatalf("changes = %+v", plan.Items[0].Changes)
	}

	if c := paths["items[mod].val"]; c.Old != float64(1) || c.New != float64(2) {
		t.Errorf("modify = %+v", c)
	}

	if c := paths["items[add]"]; c.New == nil || c.Old != nil {
		t.Errorf("add = %+v", c)
	}

	if c := paths["items[gone]"]; c.Old == nil || c.New != nil {
		t.Errorf("remove = %+v", c)
	}
}

func TestPlanImmutableFieldIsAnErrorWithHint(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.seed("x", map[string]any{"flavor": "a"}, false)

	plan := planFor(NewRegistry(a), PlanInput{
		Live:    liveOf(a),
		Desired: []*Object{desired("Alpha", "x", map[string]any{"flavor": "b"})},
	})

	errs := plan.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "immutable field(s) flavor") || !strings.Contains(errs[0].Error(), "recreate") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestPlanAmbiguousLiveKeyIsAnError(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	live := map[string][]*Live{"Alpha": {{Key: "dup", ID: "1"}, {Key: "dup", ID: "2"}}}

	plan := planFor(NewRegistry(a), PlanInput{Live: live, Desired: []*Object{desired("Alpha", "dup", nil)}})
	if errs := plan.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "ambiguous") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestPlanOrdersCreatesByRankAndDeletesInReverse(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	b := newFake("Beta", 20, &calls)
	reg := NewRegistry(a, b)

	b.seed("old-b", nil, false)
	a.seed("old-a", nil, false)

	markers := Markers{
		Ident("Alpha", "old-a"): {Kind: "Alpha", Name: "old-a", ObjectID: "id-old-a"},
		Ident("Beta", "old-b"):  {Kind: "Beta", Name: "old-b", ObjectID: "id-old-b"},
	}

	live := liveOf(a)
	live["Beta"] = liveOf(b)["Beta"]

	plan := planFor(reg, PlanInput{
		Live: live, Markers: markers, Prune: true,
		Desired: []*Object{desired("Beta", "b1", nil), desired("Alpha", "a1", nil)},
	})

	var order []string
	for _, it := range plan.Items {
		order = append(order, string(it.Action)+" "+it.Key)
	}

	want := "create a1,create b1,delete old-b,delete old-a"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}

func TestPlanPruneOnlyDeletesManagedObjects(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.seed("managed", nil, false)
	a.seed("manual", nil, false)
	a.seed("seeded", nil, true)
	a.seed("recreated", nil, false)

	markers := Markers{
		Ident("Alpha", "managed"):   {ObjectID: "id-managed"},
		Ident("Alpha", "seeded"):    {ObjectID: "id-seeded"},
		Ident("Alpha", "recreated"): {ObjectID: "a-different-object-id"},
	}

	del := func(prune, all bool) map[string]bool {
		plan := planFor(NewRegistry(a), PlanInput{Live: liveOf(a), Markers: markers, Prune: prune, PruneAll: all})
		out := map[string]bool{}

		for _, it := range plan.Items {
			if it.Action == ActionDelete {
				out[it.Key] = true
			}
		}

		return out
	}

	if got := del(false, false); len(got) != 0 {
		t.Errorf("no prune flag deleted %v", got)
	}

	if got := del(true, false); len(got) != 1 || !got["managed"] {
		t.Errorf("--prune deleted %v, want only managed", got)
	}

	if got := del(true, true); len(got) != 3 || !got["managed"] || !got["manual"] || !got["recreated"] || got["seeded"] {
		t.Errorf("--prune-all deleted %v, want everything but seeded", got)
	}
}

func TestPlanPruneAllSpareUsersUnlessNamed(t *testing.T) {
	var calls []string

	u := newFake(KindUser, 10, &calls)
	u.seed("alice", nil, false)

	reg := NewRegistry(u)

	in := PlanInput{Live: liveOf(u), Prune: true, PruneAll: true}
	if plan := planFor(reg, in); len(plan.Items) != 0 {
		t.Errorf("--prune-all deleted users without --kind User: %+v", plan.Items)
	}

	in.Kinds = map[string]bool{KindUser: true}
	if plan := planFor(reg, in); len(plan.Items) != 1 || plan.Items[0].Action != ActionDelete {
		t.Errorf("--prune-all --kind User should delete: %+v", plan.Items)
	}
}

func TestPlanPruneRespectsKindFilter(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	b := newFake("Beta", 20, &calls)
	a.seed("x", nil, false)
	b.seed("y", nil, false)

	live := liveOf(a)
	live["Beta"] = liveOf(b)["Beta"]

	plan := planFor(NewRegistry(a, b), PlanInput{Live: live, Prune: true, PruneAll: true, Kinds: map[string]bool{"Beta": true}})
	if len(plan.Items) != 1 || plan.Items[0].Kind != "Beta" {
		t.Fatalf("items = %+v", plan.Items)
	}
}

func TestPlanMissingReference(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	b := newFake("Beta", 20, &calls)
	b.refs = func(o *Object) []Ref { return []Ref{{Kind: "Alpha", Key: o.Name, Field: "alpha"}} }
	a.seed("exists", nil, false)

	reg := NewRegistry(a, b)
	live := liveOf(a)

	plan := planFor(reg, PlanInput{Live: live, Desired: []*Object{
		desired("Beta", "exists", nil), desired("Beta", "missing", nil), desired("Alpha", "inline", nil), desired("Beta", "inline", nil),
	}})

	errs := plan.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `Alpha "missing"`) {
		t.Fatalf("errors = %v, want exactly the missing reference", errs)
	}
}

func TestPlanSecretSemantics(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	set := a.seed("has", nil, false)
	set.Secrets = map[string]bool{"token": true}
	unset := a.seed("hasnot", nil, false)
	unset.Secrets = map[string]bool{"token": false}
	a.seed("unknown", nil, false).Secrets = nil

	ref := map[string]any{"token": SecretRef{Env: "T"}}
	objs := []*Object{desired("Alpha", "has", ref), desired("Alpha", "hasnot", ref), desired("Alpha", "unknown", ref)}

	action := func(force bool) map[string]Action {
		plan := planFor(NewRegistry(a), PlanInput{Live: liveOf(a), Desired: objs, ForceSecrets: force})
		out := map[string]Action{}

		for _, it := range plan.Items {
			out[it.Key] = it.Action
		}

		return out
	}

	got := action(false)
	if got["has"] != ActionUnchanged || got["unknown"] != ActionUnchanged || got["hasnot"] != ActionUpdate {
		t.Errorf("without force: %v", got)
	}

	for k, a := range action(true) {
		if a != ActionUpdate {
			t.Errorf("force: %s = %s, want update", k, a)
		}
	}
}

func TestPlanAdoptsUnmarkedExistingObjects(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.seed("x", map[string]any{"title": "t"}, false)

	plan := planFor(NewRegistry(a), PlanInput{
		Live: liveOf(a), Desired: []*Object{desired("Alpha", "x", map[string]any{"title": "t"})},
	})

	if plan.HasChanges() {
		t.Error("adoption alone must not count as a change")
	}

	if !plan.Items[0].Adopt {
		t.Error("existing unmarked object should be adopted")
	}
}

func prepared(t *testing.T, reg *Registry, ms *markerServer, items ...*Item) (Options, *Prepared) {
	t.Helper()

	return Options{Registry: reg, Client: ms.client()}, &Prepared{Plan: &Plan{Items: items}, Env: NewEnv(ms.client())}
}

func TestExecuteWritesMarkersAndResolvesSecrets(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	ms := newMarkerServer(t)
	reg := NewRegistry(a)

	obj := desired("Alpha", "x", map[string]any{"token": SecretRef{Env: "TOK"}})
	opts, p := prepared(t, reg, ms, &Item{Action: ActionCreate, Kind: "Alpha", Key: "x", Desired: obj})
	opts.Resolver = SecretResolver{LookupEnv: func(string) (string, bool) { return "hunter2", true }}

	out := Execute(context.Background(), opts, p)
	if len(Failed(out)) != 0 {
		t.Fatalf("failed: %v", Failed(out))
	}

	if a.secrets["x"] != "hunter2" {
		t.Errorf("server received secret %q", a.secrets["x"])
	}

	if !ms.has("Alpha", "x") {
		t.Error("ownership marker not written")
	}

	if ms.markers[Ident("Alpha", "x")].AppliedHash != SpecHash(obj) {
		t.Error("marker hash mismatch")
	}
}

func TestExecuteFailFastSkipsRest(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.failOn["create b"] = errors.New("boom")
	ms := newMarkerServer(t)

	items := []*Item{}
	for _, n := range []string{"a", "b", "c"} {
		items = append(items, &Item{Action: ActionCreate, Kind: "Alpha", Key: n, Desired: desired("Alpha", n, nil)})
	}

	opts, p := prepared(t, NewRegistry(a), ms, items...)
	out := Execute(context.Background(), opts, p)

	st := []Status{out[0].Status, out[1].Status, out[2].Status}
	if st[0] != StatusApplied || st[1] != StatusFailed || st[2] != StatusSkipped {
		t.Errorf("statuses = %v", st)
	}

	if ms.has("Alpha", "c") || !ms.has("Alpha", "a") {
		t.Error("markers must exist only for objects actually applied")
	}
}

func TestExecuteContinueOnError(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	a.failOn["create b"] = errors.New("boom")
	ms := newMarkerServer(t)

	items := []*Item{}
	for _, n := range []string{"a", "b", "c"} {
		items = append(items, &Item{Action: ActionCreate, Kind: "Alpha", Key: n, Desired: desired("Alpha", n, nil)})
	}

	opts, p := prepared(t, NewRegistry(a), ms, items...)
	opts.ContinueOnError = true

	out := Execute(context.Background(), opts, p)
	if len(Failed(out)) != 1 || out[2].Status != StatusApplied || !ms.has("Alpha", "c") {
		t.Errorf("outcomes = %+v", out)
	}
}

func TestExecuteDeleteDropsMarkerAndAdoptsUnchanged(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	ms := newMarkerServer(t)
	ms.markers[Ident("Alpha", "old")] = Marker{Kind: "Alpha", Name: "old", ObjectID: "id-old"}

	old := a.seed("old", nil, false)
	same := a.seed("same", nil, false)

	opts, p := prepared(t, NewRegistry(a), ms,
		&Item{Action: ActionUnchanged, Kind: "Alpha", Key: "same", Desired: desired("Alpha", "same", nil), Live: same, Adopt: true},
		&Item{Action: ActionDelete, Kind: "Alpha", Key: "old", Live: old, Managed: true},
	)

	if out := Execute(context.Background(), opts, p); len(Failed(out)) != 0 {
		t.Fatalf("failed: %v", Failed(out))
	}

	if ms.has("Alpha", "old") || !ms.has("Alpha", "same") {
		t.Errorf("markers = %v", ms.markers)
	}

	if _, still := a.store["old"]; still {
		t.Error("object not deleted")
	}
}

func TestPreflightMapsStatusCodesToHints(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized: "OA_TOKEN",
		http.StatusForbidden:    "administrator token",
		http.StatusNotFound:     "upgrade the server",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"x"}`, status)
		}))

		_, err := Preflight(context.Background(), NewClient(srv.URL, "t", srv.Client()))

		srv.Close()

		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: err = %v, want containing %q", status, err, want)
		}
	}
}

func TestReportsNeverContainSecretValues(t *testing.T) {
	plan := &Plan{Items: []*Item{{
		Action: ActionUpdate, Kind: "Alpha", Key: "x",
		Changes: []Change{{Path: "token", New: SecretRef{Env: "T"}, Secret: true}, {Path: "title", Old: "a", New: "b"}},
	}}}

	var diff strings.Builder

	WriteDiff(&diff, plan)

	if !strings.Contains(diff.String(), "token: (secret, will be sent)") || !strings.Contains(diff.String(), `-   title: "a"`) {
		t.Errorf("diff output:\n%s", diff.String())
	}

	raw, _ := json.Marshal(BuildJSON(plan, nil))
	if strings.Contains(string(raw), "env") || !strings.Contains(string(raw), `"secret":true`) {
		t.Errorf("json = %s", raw)
	}
}

func TestExportRoundTripsToNoChanges(t *testing.T) {
	var calls []string

	a := newFake("Alpha", 10, &calls)
	live := a.seed("x", map[string]any{
		"title": "t", "count": float64(4), "tags": []any{"a"}, "owner": "team",
		"nested": map[string]any{"x": float64(1), "y": false},
		"items":  []any{map[string]any{"id": "i", "val": float64(2)}},
	}, false)
	live.Secrets = map[string]bool{"token": true}
	a.seed("skipped", nil, true)

	reg := NewRegistry(a)

	raw, err := exportObject(a, live, false)
	if err != nil {
		t.Fatal(err)
	}

	text := string(raw)
	for _, want := range []string{"apiVersion: openartifact.io/v1", "name: x", "owner: team", "env: OA_ALPHA_X_TOKEN"} {
		if !strings.Contains(text, want) {
			t.Errorf("export missing %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "spec:\n  owner") {
		t.Errorf("metadata field leaked into spec:\n%s", text)
	}

	objs, err := loadYAML(t, reg, text)
	if err != nil {
		t.Fatalf("exported manifest does not load: %v\n%s", err, text)
	}

	plan := planFor(reg, PlanInput{Live: liveOf(a), Desired: objs})
	if plan.HasChanges() || len(plan.Errors()) != 0 {
		t.Fatalf("re-applying the export is not a no-op: %+v %v", plan.Items[0].Changes, plan.Errors())
	}
}

func TestWriteDirSanitizesNamesAndDetectsCollisions(t *testing.T) {
	dir := t.TempDir()

	paths, err := WriteDir(dir, []ExportDoc{{Kind: "Webhook", Key: "repo/hook", YAML: []byte("a: 1\n")}})
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != "repo_hook.yaml" {
		t.Fatalf("paths = %v, %v", paths, err)
	}

	_, err = WriteDir(t.TempDir(), []ExportDoc{{Kind: "Webhook", Key: "a/b"}, {Kind: "Webhook", Key: "a_b"}})
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Errorf("err = %v, want collision", err)
	}
}

func TestKindFilterRejectsUnknown(t *testing.T) {
	var calls []string

	reg := NewRegistry(newFake("Alpha", 10, &calls))

	if f, err := KindFilter(reg, []string{"alpha"}); err != nil || !f["Alpha"] {
		t.Errorf("filter = %v, %v", f, err)
	}

	if _, err := KindFilter(reg, []string{"nope"}); err == nil {
		t.Error("expected error for unknown kind")
	}
}
