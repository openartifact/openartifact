// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/artifactpath"
	"github.com/openartifact/openartifact/internal/database/models"
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
// read exactly as the hosted handlers read it when storing (artifactpath.Split,
// both its current and its legacy reading); a maven-metadata document, which
// the hosted handlers store like any file but which names the module one or two
// directories up, is read both ways. ok is false for paths that name no module.
func fileRepoRef(relPath string) (PkgRef, bool) {
	rel := strings.Trim(relPath, "/")

	var mods []fileModule

	add := func(group, module string) {
		m := fileModule{group: group, module: module}
		if group != "" && module != "" && !slices.Contains(mods, m) {
			mods = append(mods, m)
		}
	}

	if cur, legacy, ok := artifactpath.Split(rel); ok {
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
		return PkgRef{}, false
	}

	return PkgRef{file: true, modules: mods}, true
}

// ivyRef and sbtRef are fileRepoRef with the table the format's hosted members
// keep their files in asked.
func ivyRef(relPath string) (PkgRef, bool) {
	ref, ok := fileRepoRef(relPath)
	ref.owner = func(h *Handler, mr *http.Request, m *models.Repository, ref PkgRef, _ Backend) ownership {
		return h.ownsFileModule(mr.Context(), m, ref.modules, h.db.IvyModuleExists)
	}

	return ref, ok
}

func sbtRef(relPath string) (PkgRef, bool) {
	ref, ok := fileRepoRef(relPath)
	ref.owner = func(h *Handler, mr *http.Request, m *models.Repository, ref PkgRef, _ Backend) ownership {
		return h.ownsFileModule(mr.Context(), m, ref.modules, h.db.SBTModuleExists)
	}

	return ref, ok
}

// IvyHooks is the ivy group: first hit, with module ownership for hosted wins.
var IvyHooks = Hooks{PackageRef: ivyRef} //nolint:gochecknoglobals // immutable format hook value

// SBTHooks is the sbt group: first hit, with module ownership for hosted wins.
var SBTHooks = Hooks{PackageRef: sbtRef} //nolint:gochecknoglobals // immutable format hook value

// ownsFileModule reports whether hosted member m holds a file of any of mods,
// by asking exists (the store's per-format module check).
func (h *Handler) ownsFileModule(
	ctx context.Context,
	m *models.Repository,
	mods []fileModule,
	exists func(ctx context.Context, repoID, group, module string) (bool, error),
) ownership {
	for _, fm := range mods {
		held, err := exists(ctx, m.ID, fm.group, fm.module)
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
