// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const auditSentinel = "zz-sentinel"

// auditServer answers every GET with one item under every list key the CLI
// reads, carrying the sentinel as name and id. A positional that completes
// from the server therefore shows the sentinel.
func auditServer(t *testing.T) *httptest.Server {
	t.Helper()

	item := `[{"name":"` + auditSentinel + `","id":"` + auditSentinel + `","user_id":"` + auditSentinel + `"}]`
	var body strings.Builder

	body.WriteString(`{"total":1`)

	for _, k := range []string{"repositories", "data", "policies", "items", "keys", "projects", "webhooks", "backups", "tokens", "packages", "reports", "scans", "statements", "members"} {
		body.WriteString(`,"` + k + `":` + item)
	}

	body.WriteString("}")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body.String()))
	}))
	t.Cleanup(srv.Close)

	return srv
}

// noSuggestion lists the positionals that deliberately complete to nothing
// (free text the user invents, or an id the server cannot list), keyed by
// "command path#index" with a 1-based index. Every other positional must
// complete from the server, from a fixed list or from files.
//
//nolint:gochecknoglobals // immutable test table
var noSuggestion = map[string]string{
	"oa bundle add#1":     "bundle names are the user's to choose",
	"oa bundle add#2":     "bundle versions are the user's to choose",
	"oa bundle create#1":  "bundle names are the user's to choose",
	"oa bundle create#2":  "bundle versions are the user's to choose",
	"oa bundle seal#1":    "bundles have no list endpoint",
	"oa bundle seal#2":    "bundles have no list endpoint",
	"oa bundle show#1":    "bundles have no list endpoint",
	"oa bundle show#2":    "bundles have no list endpoint",
	"oa promote#1":        "bundles have no list endpoint",
	"oa promote#2":        "bundles have no list endpoint",
	"oa group create#1":   "a new name",
	"oa project create#1": "a new name",
	"oa repo create#1":    "a new name",
	"oa search#1":         "free-text query",
	"oa props delete#3":   "property keys are free text",
	"oa props search#2":   "property keys are free text",
	"oa props search#3":   "property values are free text",
	"oa props set#3":      "property keys are free text",
	"oa props set#4":      "property values are free text",
	"oa trash purge#2":    "the trash list depends on the kind and on --repo, which is not known yet",
	"oa trash restore#2":  "the trash list depends on the kind and on --repo, which is not known yet",
}

var positionalToken = regexp.MustCompile(`^[<\[(]`)

// positionalsOf is the positional placeholders of a Use line; a trailing "..."
// marks the last one as repeating.
func positionalsOf(use string) []string {
	var out []string

	for _, f := range strings.Fields(use)[1:] {
		// Flags and alternatives (<id> | --repo N --all) end the positionals.
		if f == "|" || strings.HasPrefix(f, "-") || strings.HasPrefix(f, "[-") {
			break
		}

		if positionalToken.MatchString(f) {
			out = append(out, f)
		}
	}

	return out
}

func walkRunnable(cmd *cobra.Command, visit func(*cobra.Command)) {
	for _, c := range cmd.Commands() {
		if c.Name() == "completion" || c.Name() == "help" || strings.HasPrefix(c.Name(), "__") {
			continue
		}

		if c.Runnable() {
			visit(c)
		}

		walkRunnable(c, visit)
	}
}

// Every positional of every command has a completer or is listed as taking no
// suggestion, and the answer comes from the right source.
func TestEveryPositionalHasACompleter(t *testing.T) {
	srv := auditServer(t)
	completionEnv(t, srv.URL, secretToken)

	seen := map[string]bool{}

	walkRunnable(newRootCmd(), func(c *cobra.Command) {
		pos := positionalsOf(c.Use)
		if len(pos) == 0 {
			return
		}

		if c.ValidArgsFunction == nil {
			t.Errorf("%s: takes %v but has no ValidArgsFunction (cobra would complete files)", c.CommandPath(), pos)

			return
		}

		path := strings.Fields(c.CommandPath())[1:]

		for i := range pos {
			args := append(slices.Clone(path), slices.Repeat([]string{"x"}, i)...)
			got, directive := complete(t, append(args, "")...)
			key := fmt.Sprintf("%s#%d", c.CommandPath(), i+1)
			seen[key] = true

			reason, listed := noSuggestion[key]

			switch {
			case len(got) == 0 && directive == ":0":
				// files: the shell completes paths
				if listed {
					t.Errorf("%s: listed as no-suggestion but completes files", key)
				}
			case len(got) == 0:
				if !listed {
					t.Errorf("%s (%s): completes to nothing; add a completer or list it in noSuggestion", key, pos[i])
				}
			default:
				if listed {
					t.Errorf("%s: listed as no-suggestion (%s) but completes %v", key, reason, got)
				}
			}
		}
	})

	for key := range noSuggestion {
		if !seen[key] {
			t.Errorf("noSuggestion lists %s, which is not a positional of any command", key)
		}
	}
}

// User and group ids, group members and cleanup policy ids come from the
// server; a policy id is looked up in the repository named before it.
func TestCompleteIDs(t *testing.T) {
	var paths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/cleanup-policies"):
			_, _ = w.Write([]byte(`{"policies":[{"id":"p1"},{"id":"p2"}]}`))
		case strings.HasSuffix(r.URL.Path, "/members"):
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case r.URL.Path == "/api/v1/users":
			_, _ = w.Write([]byte(`{"data":[{"id":"u1"},{"id":"u2"}]}`))
		case r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`{"data":[{"id":"g1"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	completionEnv(t, srv.URL, secretToken)

	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"user", "show", ""}, []string{"u1", "u2"}},
		{[]string{"user", "show", "u"}, []string{"u1", "u2"}},
		{[]string{"user", "update", ""}, []string{"u1", "u2"}},
		{[]string{"user", "delete", ""}, []string{"u1", "u2"}},
		{[]string{"group", "show", ""}, []string{"g1"}},
		{[]string{"group", "delete", ""}, []string{"g1"}},
		{[]string{"group", "members", ""}, []string{"g1"}},
		{[]string{"group", "add-member", "g1", ""}, []string{"u1", "u2"}},
		{[]string{"group", "rm-member", "g1", ""}, []string{"m1"}},
		{[]string{"cleanup", "show", "myrepo", ""}, []string{"p1", "p2"}},
		{[]string{"cleanup", "update", "myrepo", "p"}, []string{"p1", "p2"}},
		{[]string{"cleanup", "run", "myrepo", ""}, []string{"p1", "p2"}},
	} {
		got, _ := complete(t, tc.args...)
		if !slices.Equal(got, tc.want) {
			t.Errorf("__complete %v = %v, want %v", tc.args, got, tc.want)
		}
	}

	if !slices.Contains(paths, "/api/v1/repositories/myrepo/cleanup-policies") {
		t.Errorf("policy ids were not read from the named repository: %v", paths)
	}
}
