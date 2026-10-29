// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/registry"
)

// Hooks is everything the group handler decides per format. A format's value
// (ansible.go's AnsibleHooks, npm.go's NPMHooks, ...) is registered once with
// Register; a format without a registered value, or with a nil field, gets the
// default documented on the field, which is what the formats that never needed
// the hook always had.
//
// Intentionally without a value: docker (its requests go through ServeOCI, which
// has its own hosted-wins reference) and raw (merges nothing, no versions to
// hide). Both are plain first-hit groups. ivy and sbt only differ in the
// hosted-wins reference.
type Hooks struct {
	// ServeMerged answers the requests the group of this format merges over its
	// readable members, or answers itself; false means the request is a plain
	// first-hit one. Nil is a plain first-hit group.
	ServeMerged func(
		h *Handler,
		w http.ResponseWriter,
		r *http.Request,
		grp *models.Repository,
		relPath string,
		readable []*models.Repository,
		be Backend,
	) bool

	// PackageRef extracts the package a request names for the hosted-wins rule
	// (dependency confusion, see hostedwins.go); ok is false for requests that
	// name none (indexes, config documents). Nil is a format with no versions to
	// hide, or whose generated indexes are resolved member by member: no
	// hosted-wins.
	PackageRef func(relPath string) (ref PkgRef, ok bool)

	// AuthPath is the path the caller is authorized against for member m: the
	// one a direct request to m would be authorized with, so a path-scoped
	// grant on the member means the same thing inside and outside the group.
	// Nil is relPath unchanged.
	AuthPath func(m *models.Repository, relPath string) string
}

var hooks registry.Registry[Hooks] //nolint:gochecknoglobals // the format hook registry, filled at startup by Register

// Register installs the group hooks of format. It panics when format is empty
// or already registered.
func Register(format string, h Hooks) { hooks.Register(format, h) }

// Lookup returns the hooks registered for format; the zero Hooks (a plain
// first-hit group) when there are none.
func Lookup(format string) (Hooks, bool) { return hooks.Lookup(format) }

// authPath is the path the caller is authorized against for member m.
func (hk Hooks) authPath(m *models.Repository, relPath string) string {
	if hk.AuthPath == nil {
		return relPath
	}

	return hk.AuthPath(m, relPath)
}

// packageRef is the package relPath names, with the member path a file request
// is made with filled in.
func (hk Hooks) packageRef(relPath string) (PkgRef, bool) {
	if hk.PackageRef == nil {
		return PkgRef{}, false
	}

	ref, ok := hk.PackageRef(relPath)
	if !ok {
		return PkgRef{}, false
	}

	if ref.pathFor == nil {
		ref.pathFor = func(*models.Repository) string { return relPath }
	}

	return ref, true
}

// serveMerged runs the format's merging handler; false is a first-hit request.
func (hk Hooks) serveMerged(
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	readable []*models.Repository,
	be Backend,
) bool {
	if hk.ServeMerged == nil {
		return false
	}

	return hk.ServeMerged(h, w, r, grp, relPath, readable, be)
}
