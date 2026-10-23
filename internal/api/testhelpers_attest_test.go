// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func helperMsg(msg []any) string {
	if len(msg) == 0 {
		return ""
	}
	if f, ok := msg[0].(string); ok {
		return ": " + fmt.Sprintf(f, msg[1:]...)
	}
	return ": " + fmt.Sprint(msg...)
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Chan, reflect.String, reflect.Array:
		return rv.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil() || isEmptyValue(rv.Elem().Interface())
	}
	return rv.IsZero()
}

func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

func containsValue(container, elem any) bool {
	if s, ok := container.(string); ok {
		e, _ := elem.(string)
		return strings.Contains(s, e)
	}
	rv := reflect.ValueOf(container)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if reflect.DeepEqual(rv.Index(i).Interface(), elem) {
				return true
			}
		}
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if reflect.DeepEqual(k.Interface(), elem) {
				return true
			}
		}
	}
	return false
}

func jsonEqual(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func expectEqual(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("not equal: want %#v, got %#v%s", want, got, helperMsg(msg))
	}
}

func mustEqual(t *testing.T, want, got any, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not equal: want %#v, got %#v%s", want, got, helperMsg(msg))
	}
}

func mustNoError(t *testing.T, err error, msg ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v%s", err, helperMsg(msg))
	}
}

func expectContains(t *testing.T, container, elem any, msg ...any) {
	t.Helper()
	if !containsValue(container, elem) {
		t.Errorf("%#v does not contain %#v%s", container, elem, helperMsg(msg))
	}
}

func expectNotContains(t *testing.T, container, elem any, msg ...any) {
	t.Helper()
	if containsValue(container, elem) {
		t.Errorf("%#v should not contain %#v%s", container, elem, helperMsg(msg))
	}
}

func expectTrue(t *testing.T, v bool, msg ...any) {
	t.Helper()
	if !v {
		t.Errorf("expected true%s", helperMsg(msg))
	}
}

func expectFalse(t *testing.T, v bool, msg ...any) {
	t.Helper()
	if v {
		t.Errorf("expected false%s", helperMsg(msg))
	}
}

func expectJSONEq(t *testing.T, want, got string, msg ...any) {
	t.Helper()
	if !jsonEqual(want, got) {
		t.Errorf("JSON not equal: want %s, got %s%s", want, got, helperMsg(msg))
	}
}

func expectNil(t *testing.T, v any, msg ...any) {
	t.Helper()
	if !isNilValue(v) {
		t.Errorf("expected nil, got %#v%s", v, helperMsg(msg))
	}
}

func mustNotNil(t *testing.T, v any, msg ...any) {
	t.Helper()
	if isNilValue(v) {
		t.Fatalf("expected non-nil%s", helperMsg(msg))
	}
}

func expectEmpty(t *testing.T, v any, msg ...any) {
	t.Helper()
	if !isEmptyValue(v) {
		t.Errorf("expected empty, got %#v%s", v, helperMsg(msg))
	}
}

func expectNotEmpty(t *testing.T, v any, msg ...any) {
	t.Helper()
	if isEmptyValue(v) {
		t.Errorf("expected non-empty%s", helperMsg(msg))
	}
}

func mustNotEmpty(t *testing.T, v any, msg ...any) {
	t.Helper()
	if isEmptyValue(v) {
		t.Fatalf("expected non-empty%s", helperMsg(msg))
	}
}

func expectZero(t *testing.T, v any, msg ...any) {
	t.Helper()
	if v != nil && !reflect.ValueOf(v).IsZero() {
		t.Errorf("expected zero, got %#v%s", v, helperMsg(msg))
	}
}

func mustLen(t *testing.T, v any, n int, msg ...any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if rv.Len() != n {
		t.Fatalf("expected len %d, got %d%s", n, rv.Len(), helperMsg(msg))
	}
}
