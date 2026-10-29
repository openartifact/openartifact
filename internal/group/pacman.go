// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/pkg/pacman"
)

// Cache lifetimes of a pacman group's files, as the hosted handler: databases
// are revalidated within a minute, package files never change under their name.
const pacmanMetadataMaxAge = 60

// PacmanHooks is the pacman group: merged databases per architecture, files
// (and their signatures) from the member that owns the package.
var PacmanHooks = pacmanFormat.hooks() //nolint:gochecknoglobals // immutable format hook value

//nolint:gochecknoglobals // immutable format hook value
var pacmanFormat = debRPMFormat{classify: classifyPacman, files: groupFiles{sigOfPackage: true}}

// classifyPacman classifies a request path of a pacman group, which has the
// layout of a hosted repository: os/ARCH/FILE below the group, plus the public
// key. The databases (and their signatures) are generated metadata of the
// architecture; package files and their signatures are served from the member
// that owns the package.
func classifyPacman(relPath string) debRPMPath {
	rest, ok := strings.CutPrefix(relPath, "os/")
	if !ok {
		return debRPMPath{}
	}

	arch, file, ok := strings.Cut(rest, "/")
	if !ok || !pacman.ValidArch(arch) {
		return debRPMPath{}
	}

	kind, doc := pacman.ClassifyFile(file)

	switch kind {
	case pacman.FilePackage, pacman.FilePackageSig:
		return debRPMPath{class: classFile}
	case pacman.FileDatabase:
		return debRPMPath{
			class: classMetadata, dist: arch, anchor: true, lastModified: true,
			contentType: "application/gzip", maxAge: pacmanMetadataMaxAge,
			// Stored under the architecture's document name, whatever the client called it.
			storedAs: pacmanDoc(arch, doc),
		}
	case pacman.FileDatabaseSig:
		return debRPMPath{
			class: classMetadata, dist: arch, anchor: true, lastModified: true,
			contentType: "application/pgp-signature", maxAge: pacmanMetadataMaxAge,
			storedAs: pacmanDoc(arch, doc) + ".sig",
		}
	case pacman.FileInvalid:
	}

	return debRPMPath{}
}

// pacmanDoc is the path the generated document doc of an architecture is stored
// under (internal/worker's pacmanArchPath).
func pacmanDoc(arch, doc string) string { return "os/" + arch + "/" + doc }

// notModifiedSince sets Last-Modified to modified and answers 304 when the
// client's If-Modified-Since is not older (HTTP dates have one-second
// resolution), which is how pacman asks whether a database it holds changed. It
// reports whether the request is answered.
func notModifiedSince(w http.ResponseWriter, r *http.Request, modified time.Time) bool {
	modified = modified.UTC().Truncate(time.Second)
	w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))

	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	if err != nil || modified.After(since) {
		return false
	}

	w.WriteHeader(http.StatusNotModified)

	return true
}
