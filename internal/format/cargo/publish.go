// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"golang.org/x/mod/semver"
)

const (
	// maxCrateNameLen is the length cap crates.io applies to a crate name.
	maxCrateNameLen = 64
	// maxCrateVersionLen is the width of the cargo_crates.version column.
	maxCrateVersionLen = 100
)

var (
	// crateNameRE is the crate-name grammar cargo enforces: it starts with a
	// letter (or '_') and holds ASCII letters, digits, '-' and '_'.
	crateNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

	// crateVersionRE is the full MAJOR.MINOR.PATCH grammar of Semantic
	// Versioning 2.0.0 (https://semver.org), which every crate version obeys.
	crateVersionRE = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
		`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)
)

func validCrateName(name string) bool {
	return len(name) <= maxCrateNameLen && crateNameRE.MatchString(name)
}

func validCrateVersion(version string) bool {
	return len(version) <= maxCrateVersionLen && crateVersionRE.MatchString(version)
}

// versionIdentity drops SemVer build metadata: two versions that differ only in
// it are the same version, and a registry must not hold both.
func versionIdentity(version string) string {
	core, _, _ := strings.Cut(version, "+")
	return core
}

// writePublishError answers a publish with the JSON error body cargo parses
// and prints (the crates.io web API shape).
func writePublishError(w http.ResponseWriter, status int, detail string) {
	body, err := json.Marshal(map[string]any{"errors": []map[string]string{{"detail": detail}}})
	if err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_, _ = w.Write(body)
}

// readDeclared reads exactly n bytes from r. The buffer grows as data arrives:
// n is a length the client declared, and a client declaring a large one it
// never sends must not make the server allocate it up front.
func readDeclared(r io.Reader, n uint32) ([]byte, error) {
	var buf bytes.Buffer

	if _, err := io.CopyN(&buf, r, int64(n)); err != nil {
		return nil, fmt.Errorf("read %d declared bytes: %w", n, err)
	}

	return buf.Bytes(), nil
}

// stringList returns the string elements of a JSON array.
func stringList(v any) []string {
	items, _ := v.([]any)

	var out []string

	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}

	return out
}

// publishConflict explains why version of crateName cannot be published into
// the repository, or returns "" when it can. Crate names are case-insensitive
// (cargo lowercases them in index paths), so a name that differs from an
// existing crate only in case is refused.
func (h *HTTPHandler) publishConflict(ctx context.Context, repoID, crateName, version string) (string, error) {
	existing, err := h.DB.ListCargoCrateVersions(ctx, repoID, crateName)
	if err != nil {
		return "", fmt.Errorf("list crate versions: %w", err)
	}

	for _, e := range existing {
		if e.Name != crateName {
			return fmt.Sprintf("crate name `%s` is too similar to the existing crate `%s`: names are case-insensitive", crateName, e.Name), nil
		}

		if versionIdentity(e.Version) == versionIdentity(version) {
			return fmt.Sprintf("crate version `%s@%s` is already uploaded", crateName, version), nil
		}
	}

	return "", nil
}

// maxCrateVersion is the version a search shows for a crate: the highest stable
// release that is not yanked, else the highest non-yanked version, else (every
// version yanked) the highest one.
func maxCrateVersion(c *models.CargoCrateSummary) string {
	if best := highestVersion(c.LiveVersions, true); best != "" {
		return best
	}

	if best := highestVersion(c.LiveVersions, false); best != "" {
		return best
	}

	if best := highestVersion(c.Versions, false); best != "" {
		return best
	}

	// Nothing parses as SemVer (rows stored before publish validated it): the
	// newest upload is the best available answer.
	if n := len(c.Versions); n > 0 {
		return c.Versions[n-1]
	}

	return ""
}

// highestVersion returns the highest of versions by SemVer precedence,
// optionally skipping pre-releases, or "" when none qualifies.
func highestVersion(versions []string, stableOnly bool) string {
	best := ""

	for _, v := range versions {
		sv := "v" + v
		if !semver.IsValid(sv) || (stableOnly && semver.Prerelease(sv) != "") {
			continue
		}

		if best == "" || semver.Compare(sv, "v"+best) > 0 {
			best = v
		}
	}

	return best
}

// readDeclaredSpool is readDeclared for the crate file: exactly n bytes, spooled
// (memory when small, a temp file otherwise) rather than held on the heap. A
// body that ends before n bytes is io.ErrUnexpectedEOF.
func readDeclaredSpool(r io.Reader, n uint32) (*base.Spool, error) {
	s, err := base.SpoolLimited(io.LimitReader(r, int64(n)), int64(n))
	if err != nil {
		return nil, fmt.Errorf("read %d declared bytes: %w", n, err)
	}

	if s.Size() != int64(n) {
		got := s.Size()
		s.Close()

		return nil, fmt.Errorf("read %d declared bytes, got %d: %w", n, got, io.ErrUnexpectedEOF)
	}

	return s, nil
}
