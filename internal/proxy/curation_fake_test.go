// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"sync"

	"github.com/openartifact/openartifact/internal/curation"
)

// fakeRuleSource is an in-memory CurationSource whose rules a test may change
// between requests.
type fakeRuleSource struct {
	mu    sync.Mutex
	rules []curation.Rule
}

func (s *fakeRuleSource) ListCurationRules(context.Context, string) ([]curation.Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]curation.Rule(nil), s.rules...), nil
}

func (s *fakeRuleSource) set(rules ...curation.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rules = rules
}

// useCuration turns the fixture's curation on in mode with rules and returns the
// source, to change the rules later.
func (f *fixture) useCuration(mode string, rules ...curation.Rule) *fakeRuleSource {
	f.t.Helper()

	src := &fakeRuleSource{rules: rules}
	f.svc.WithCuration(src)
	f.repo.CurationMode = mode

	return src
}

func blockRule(pkg, version string) curation.Rule {
	return curation.Rule{Action: curation.ActionBlock, Package: pkg, Version: version}
}

func allowRule(pkg, version string) curation.Rule {
	return curation.Rule{Action: curation.ActionAllow, Package: pkg, Version: version}
}
