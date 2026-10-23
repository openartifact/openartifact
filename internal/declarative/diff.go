// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// diffResult is the outcome of comparing one desired spec with a live spec.
type diffResult struct {
	Changes []Change
	// Immutable lists the paths of changed fields the schema marks immutable.
	Immutable []string
}

// diffSpec compares the keys present in want against have.
//
// Fields absent from want are never touched. Secret fields are write-only: a
// secret differs when force is set or when the server says no value is set;
// when the server gives no indication it is assumed unchanged.
func diffSpec(schema *Field, want, have map[string]any, secrets map[string]bool, force bool) diffResult {
	var res diffResult

	diffObject("", schema, want, have, secrets, force, &res)

	return res
}

func diffObject(prefix string, f *Field, want, have map[string]any, secrets map[string]bool, force bool, res *diffResult) {
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		sub := f.Fields[k]
		if sub == nil {
			continue
		}

		path := joinPath(prefix, k)
		w := want[k]
		h, present := have[k]

		switch {
		case sub.Secret:
			diffSecret(path, w, secrets, force, res)
		case sub.Type == TSet && sub.Elem != nil && sub.Elem.Type == TObject && sub.KeyBy != "":
			diffKeyedSet(path, sub, w, h, secrets, force, res)
		case sub.Type == TObject:
			wm, _ := w.(map[string]any)
			hm, _ := h.(map[string]any)

			if w == nil || wm == nil {
				if !equalValue(sub, w, h, present) {
					res.add(path, sub, h, w)
				}

				continue
			}

			diffObject(path, sub, wm, hm, secrets, force, res)
		default:
			if !equalValue(sub, w, h, present) {
				res.add(path, sub, h, w)
			}
		}
	}
}

// diffKeyedSet emits element-level changes for a set of objects identified by
// f.KeyBy: path[key] for an added or removed element, path[key].field for a
// modified one. A null desired value is left to equalValue's caller semantics
// and treated as an empty set.
func diffKeyedSet(path string, f *Field, want, have any, secrets map[string]bool, force bool, res *diffResult) {
	wl, _ := want.([]any)
	hl, _ := have.([]any)

	wantBy := indexElems(f, wl)
	haveBy := indexElems(f, hl)

	for _, key := range sortedKeys(wantBy) {
		elemPath := fmt.Sprintf("%s[%s]", path, key)

		h, ok := haveBy[key]
		if !ok {
			res.Changes = append(res.Changes, Change{Path: elemPath, New: wantBy[key]})
			continue
		}

		diffObject(elemPath, f.Elem, wantBy[key], h, secrets, force, res)
	}

	for _, key := range sortedKeys(haveBy) {
		if _, ok := wantBy[key]; !ok {
			res.Changes = append(res.Changes, Change{Path: fmt.Sprintf("%s[%s]", path, key), Old: haveBy[key]})
		}
	}
}

func indexElems(f *Field, l []any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(l))

	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out[elemIdentity(f, m)] = m
		}
	}

	return out
}

func diffSecret(path string, want any, secrets map[string]bool, force bool, res *diffResult) {
	set, known := secrets[path]

	if force || (known && !set) {
		res.Changes = append(res.Changes, Change{Path: path, New: want, Secret: true})
	}
}

func (r *diffResult) add(path string, f *Field, old, nw any) {
	r.Changes = append(r.Changes, Change{Path: path, Old: old, New: nw})

	if f.Immutable {
		r.Immutable = append(r.Immutable, path)
	}
}

// equalValue reports whether the desired value is satisfied by the live one.
// Desired objects only have to be a subset of the live ones (omitted keys
// inside list elements are left alone); sets ignore order; empty and absent
// lists and maps are equal.
func equalValue(f *Field, want, have any, present bool) bool {
	if want == nil {
		return have == nil || !present
	}

	switch f.Type {
	case TObject:
		wm, wok := want.(map[string]any)
		hm, hok := have.(map[string]any)

		if !wok || !hok {
			return false
		}

		return subsetObject(f, wm, hm)
	case TList:
		return equalList(f, want, have, false)
	case TSet:
		return equalList(f, want, have, true)
	case TMap, TAny:
		return equalLoose(want, have)
	}

	return reflect.DeepEqual(want, have)
}

func subsetObject(f *Field, want, have map[string]any) bool {
	for k, w := range want {
		sub := f.Fields[k]
		h, present := have[k]

		if sub == nil {
			if !equalLoose(w, h) {
				return false
			}

			continue
		}

		if sub.Secret {
			continue
		}

		if !equalValue(sub, w, h, present) {
			return false
		}
	}

	return true
}

func equalList(f *Field, want, have any, unordered bool) bool {
	wl, _ := want.([]any)
	hl, _ := have.([]any)

	if len(wl) != len(hl) {
		return false
	}

	if len(wl) == 0 {
		return true
	}

	if unordered {
		wl = sortElems(f, wl)
		hl = sortElems(f, hl)
	}

	for i := range wl {
		if !equalValue(f.Elem, wl[i], hl[i], true) {
			return false
		}
	}

	return true
}

// sortElems returns a sorted copy of a set (by key field when the elements are
// objects, by their printed form otherwise).
func sortElems(f *Field, l []any) []any {
	out := append([]any(nil), l...)

	sort.SliceStable(out, func(i, j int) bool {
		return elemIdentity(f, out[i]) < elemIdentity(f, out[j])
	})

	return out
}

// equalLoose compares whole values, treating nil and empty containers alike.
func equalLoose(want, have any) bool {
	if isEmpty(want) && isEmpty(have) {
		return true
	}

	return reflect.DeepEqual(want, have)
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}

	return false
}

// FormatValue renders a value for reports: secrets as "(secret)", whole
// numbers without a decimal point, strings quoted, containers as compact JSON.
func FormatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case SecretRef:
		return "(secret " + t.String() + ")"
	case string:
		return fmt.Sprintf("%q", t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}

		return fmt.Sprintf("%g", t)
	case bool:
		return strconv.FormatBool(t)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = FormatValue(e)
		}

		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := sortedKeys(t)
		parts := make([]string, len(keys))

		for i, k := range keys {
			parts[i] = k + ": " + FormatValue(t[k])
		}

		return "{" + strings.Join(parts, ", ") + "}"
	}

	return fmt.Sprint(v)
}
