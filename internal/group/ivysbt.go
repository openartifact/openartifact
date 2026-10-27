// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
)

// Ivy and sbt groups merge nothing: a file is served by the first member in
// order that has it. Their hosted members are plain file stores, so hosted wins
// (hostedwins.go) decides ownership of a module from the members' tables
// rather than from a probe document: a hosted member that holds any file of
// the module hides the proxies' files of it.

// fileModule is one reading of a request path as a (group, module) pair, the
// coordinates a hosted ivy or sbt file is stored under.
type fileModule struct{ group, module string }

// fileRepoRef names the module an ivy or sbt request is about. A file path is
// read exactly as the hosted handlers read it when storing (base.SplitArtifactPath,
// both its current and its legacy reading); a maven-metadata document, which
// the hosted handlers store like any file but which names the module one or two
// directories up, is read both ways. ok is false for paths that name no module.
func fileRepoRef(relPath string) (pkgRef, bool) {
	rel := strings.Trim(relPath, "/")

	var mods []fileModule

	add := func(group, module string) {
		m := fileModule{group: group, module: module}
		if group != "" && module != "" && !slices.Contains(mods, m) {
			mods = append(mods, m)
		}
	}

	if cur, legacy, ok := base.SplitArtifactPath(rel); ok {
		add(cur.Group, cur.Module)
		add(legacy.Group, legacy.Module)
	}

	segs := strings.Split(rel, "/")
	if last := len(segs) - 1; last > 0 && strings.HasPrefix(segs[last], "maven-metadata") {
		dir := segs[:last]
		// <group path>/<module>/maven-metadata.xml, and the version-level
		// <group path>/<module>/<version>/maven-metadata.xml of a snapshot.
		for up := 1; up <= 2; up++ {
			if n := len(dir) - up; n >= 1 {
				add(strings.Join(dir[:n], "."), dir[n])
			}
		}
	}

	if len(mods) == 0 {
		return pkgRef{}, false
	}

	return pkgRef{file: true, modules: mods}, true
}

// ownsFileModule reports whether hosted member m holds a file of any of mods.
func (h *Handler) ownsFileModule(ctx context.Context, m *models.Repository, mods []fileModule) ownership {
	for _, fm := range mods {
		var (
			held bool
			err  error
		)

		if m.Format == "sbt" {
			held, err = h.db.SBTModuleExists(ctx, m.ID, fm.group, fm.module)
		} else {
			held, err = h.db.IvyModuleExists(ctx, m.ID, fm.group, fm.module)
		}

		if err != nil {
			h.log.Errorw("failed to check member module", "member", m.Name, "error", err)
			return ownsUnknown
		}

		if held {
			return ownsYes
		}
	}

	return ownsNot
}
