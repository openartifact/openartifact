// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"context"
	"fmt"
	"strings"

	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// MaxParentDepth bounds how many <parent> links are followed. Real projects
// nest a handful of levels; a longer chain is treated as unresolved.
const MaxParentDepth = 10

// POMLookup returns the pom of a Maven coordinate, or (nil, nil) when it is not
// available (not stored, unreadable). It is called at most MaxParentDepth times
// per resolution and never for a coordinate already visited. A non-nil error
// aborts the resolution (cancelled context, storage failure).
type POMLookup func(ctx context.Context, groupID, artifactID, version string) (*pkgmaven.POM, error)

// POMParent reports the parent a pom inherits its licenses from: nil when the
// pom declares licenses itself or has no parent.
func POMParent(pom *pkgmaven.POM) *pkgmaven.Parent {
	if pom == nil || len(ParsePOM(pom)) > 0 {
		return nil
	}

	return pom.Parent
}

// InheritPOMLicenses resolves the licenses a pom inherits from its parent chain
// the way Maven does: a pom that declares licenses keeps them; one that
// declares none takes its parent's. The walk starts at parent and is bounded by
// MaxParentDepth and cycle-safe.
//
// resolved is false when the chain could not be followed to its end: a parent is
// not available through lookup, its version is a property placeholder, the chain
// is cyclic or deeper than MaxParentDepth. The licenses found before that point
// (none, since a pom is only followed while it declares none) are then empty and
// the caller keeps the pom "unresolved" to re-evaluate it later. A chain that
// ends at a root pom declaring no license is resolved with no licenses.
func InheritPOMLicenses(ctx context.Context, parent *pkgmaven.Parent, lookup POMLookup) ([]string, bool, error) {
	visited := map[string]struct{}{}

	for depth := 0; parent != nil; depth++ {
		g, a, v := strings.TrimSpace(parent.GroupID), strings.TrimSpace(parent.ArtifactID), strings.TrimSpace(parent.Version)
		key := g + ":" + a + ":" + v

		if _, seen := visited[key]; seen || depth >= MaxParentDepth || g == "" || a == "" || v == "" || strings.Contains(key, "${") {
			return nil, false, nil
		}

		visited[key] = struct{}{}

		pom, err := lookup(ctx, g, a, v)
		if err != nil {
			return nil, false, fmt.Errorf("look up parent pom %s: %w", key, err)
		}

		if pom == nil {
			return nil, false, nil
		}

		if found := ParsePOM(pom); len(found) > 0 {
			return found, true, nil
		}

		parent = pom.Parent
	}

	return nil, true, nil
}
