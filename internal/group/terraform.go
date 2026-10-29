// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/openartifact/openartifact/internal/database/models"
)

// maxTerraformSegmentLen bounds one path segment of a registry request a group
// looks at; the longest legitimate one is a version (100 characters).
const maxTerraformSegmentLen = 100

// terraformKind is what a request of a terraform group asks for.
type terraformKind int

const (
	// terraformOther is a request a group does not generate or merge.
	terraformOther terraformKind = iota
	// terraformModuleVersions is {name}/{system}/versions.
	terraformModuleVersions
	// terraformProviderVersions is {type}/versions.
	terraformProviderVersions
	// terraformModuleLatest is {name}/{system}: the details of the latest version.
	terraformModuleLatest
	// terraformModuleLatestDownload is {name}/{system}/download.
	terraformModuleLatestDownload
	// terraformModuleVersion is {name}/{system}/{version}: the details of one version.
	terraformModuleVersion
	// terraformModuleDownload is {name}/{system}/{version}/download.
	terraformModuleDownload
	// terraformProviderDownload is {type}/{version}/download/{os}/{arch}.
	terraformProviderDownload
)

// terraformPath is a parsed registry request. name is the module name or the
// provider type.
type terraformPath struct {
	kind                  terraformKind
	name, system, version string
}

// terraformSegment reports whether s can be a coordinate of a registry
// request: the byte set of names, versions and platforms.
func terraformSegment(s string) bool {
	if s == "" || len(s) > maxTerraformSegmentLen {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.' || c == '+' {
			continue
		}

		return false
	}

	return s != "." && !strings.Contains(s, "..")
}

// parseTerraformPath classifies relPath, a path below /repos/{group}/ of the
// registry protocols. Anything that does not have the shape of one of the
// routes (or has a segment no name or version can have) is terraformOther.
func parseTerraformPath(relPath string) terraformPath {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	if len(segs) > 5 || slices.ContainsFunc(segs, func(s string) bool { return !terraformSegment(s) }) {
		return terraformPath{}
	}

	switch len(segs) {
	case 2:
		if segs[1] == "versions" {
			return terraformPath{kind: terraformProviderVersions, name: segs[0]}
		}

		return terraformPath{kind: terraformModuleLatest, name: segs[0], system: segs[1]}
	case 3:
		switch segs[2] {
		case "versions":
			return terraformPath{kind: terraformModuleVersions, name: segs[0], system: segs[1]}
		case "download":
			return terraformPath{kind: terraformModuleLatestDownload, name: segs[0], system: segs[1]}
		}

		return terraformPath{kind: terraformModuleVersion, name: segs[0], system: segs[1], version: segs[2]}
	case 4:
		if segs[3] == "download" {
			return terraformPath{kind: terraformModuleDownload, name: segs[0], system: segs[1], version: segs[2]}
		}
	case 5:
		if segs[2] == "download" {
			return terraformPath{kind: terraformProviderDownload, name: segs[0], version: segs[1]}
		}
	}

	return terraformPath{}
}

// terraformRef is the package a terraform request names, for hosted wins: a
// module is held by a hosted member iff its version list answers 200, a
// provider type likewise. The details of a version are package metadata (they
// are rewritten for the group below), downloads are files.
func terraformRef(relPath string) (pkgRef, bool) {
	p := parseTerraformPath(relPath)

	switch p.kind {
	case terraformModuleVersions, terraformModuleLatest, terraformModuleLatestDownload, terraformModuleVersion:
		return pkgRef{probe: p.name + "/" + p.system + "/versions", accept: "application/json"}, true
	case terraformModuleDownload:
		return pkgRef{probe: p.name + "/" + p.system + "/versions", accept: "application/json", file: true}, true
	case terraformProviderVersions:
		return pkgRef{probe: p.name + "/versions", accept: "application/json"}, true
	case terraformProviderDownload:
		return pkgRef{probe: p.name + "/versions", accept: "application/json", file: true}, true
	case terraformOther:
	}

	return pkgRef{}, false
}

// serveTerraform answers the requests of a terraform group that merge over its
// members and reports whether it handled the request; module and provider
// downloads fall through to the generic first-hit resolution.
//
// The version lists of a module or provider are the union of the members'
// lists, lowest precedence first, a version several members hold described as
// the first of them describes it (a provider version lists the platforms of
// every member that has it). Hosted wins: unless the group merges proxy
// versions, a module or provider type a readable hosted member holds is
// served by the hosted members only (see applyHostedWins).
//
// The downloads come from the first member that has the version (the platform,
// for a provider) and are served as that member serves them: the URLs of a
// download call are tickets that name the file and the member it belongs to
// and are redeemed without credentials on the server's own download route, so
// they work for a client that only knows the group. A provider is therefore
// signed by the key of the member that holds it, which the client reads from
// the same document.
func (h *Handler) serveTerraform(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p := parseTerraformPath(relPath)

	switch p.kind {
	case terraformModuleVersions:
		h.serveTerraformModuleVersions(w, r, grp, p, members, be)
	case terraformProviderVersions:
		h.serveTerraformProviderVersions(w, r, grp, relPath, members, be)
	case terraformModuleLatest, terraformModuleLatestDownload:
		h.serveTerraformLatest(w, r, grp, p, members, be)
	case terraformModuleVersion:
		h.serveTerraformModuleDetails(w, r, grp, relPath, members, be)
	case terraformModuleDownload, terraformProviderDownload, terraformOther:
		return false
	}

	return true
}

// moduleVersionsDoc is the module version list: one module with its versions,
// each kept as the member wrote it.
type moduleVersionsDoc struct {
	Modules []struct {
		Versions []json.RawMessage `json:"versions"`
	} `json:"modules"`
}

// moduleVersion is one entry of a module version list.
type moduleVersion struct {
	version string
	raw     json.RawMessage
}

var errNoTerraformVersion = errors.New("version entry without a version")

// parseModuleVersions reads the version entries of a module version list.
func parseModuleVersions(doc []byte) ([]moduleVersion, error) {
	var d moduleVersionsDoc
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	var out []moduleVersion

	for _, m := range d.Modules {
		for _, raw := range m.Versions {
			var e struct {
				Version string `json:"version"`
			}

			if err := json.Unmarshal(raw, &e); err != nil {
				return nil, err //nolint:wrapcheck // logged by the caller
			}

			if e.Version == "" {
				return nil, errNoTerraformVersion
			}

			out = append(out, moduleVersion{version: e.Version, raw: raw})
		}
	}

	return out, nil
}

// compareTerraformVersions orders versions by semantic-version precedence;
// anything that is not one sorts below the versions that are, and by text among
// themselves.
func compareTerraformVersions(a, b string) int {
	if c := semver.Compare("v"+a, "v"+b); c != 0 {
		return c
	}

	return strings.Compare(a, b)
}

// latestTerraformVersion is the version "latest" means: the highest stable one,
// or the highest of all when every version is a prerelease. "" for none.
func latestTerraformVersion(versions []string) string {
	best, bestStable := "", ""

	for _, v := range versions {
		if best == "" || compareTerraformVersions(v, best) > 0 {
			best = v
		}

		if semver.Prerelease("v"+v) == "" && (bestStable == "" || compareTerraformVersions(v, bestStable) > 0) {
			bestStable = v
		}
	}

	if bestStable != "" {
		return bestStable
	}

	return best
}

// mergeModuleVersions merges the members' version entries (in member order) by
// version, the earliest member's entry winning, lowest precedence first.
func mergeModuleVersions(lists ...[]moduleVersion) []moduleVersion {
	var out []moduleVersion

	seen := make(map[string]struct{})

	for _, l := range lists {
		for _, e := range l {
			if _, dup := seen[e.version]; dup {
				continue
			}

			seen[e.version] = struct{}{}

			out = append(out, e)
		}
	}

	slices.SortStableFunc(out, func(a, b moduleVersion) int { return compareTerraformVersions(a.version, b.version) })

	return out
}

// renderModuleVersions is the version list document of module source.
func renderModuleVersions(source string, versions []moduleVersion) ([]byte, error) {
	raws := make([]json.RawMessage, 0, len(versions))
	for _, v := range versions {
		raws = append(raws, v.raw)
	}

	type entry struct {
		Source   string            `json:"source"`
		Versions []json.RawMessage `json:"versions"`
	}

	body, err := json.Marshal(map[string][]entry{"modules": {{Source: source, Versions: raws}}})
	if err != nil {
		return nil, fmt.Errorf("render module versions: %w", err)
	}

	return append(body, '\n'), nil
}

// providerPlatform is one platform of a provider version.
type providerPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// providerVersion is one version of a provider version list.
type providerVersion struct {
	Version   string             `json:"version"`
	Protocols []string           `json:"protocols"`
	Platforms []providerPlatform `json:"platforms"`
}

// parseProviderVersions reads the versions of a provider version list.
func parseProviderVersions(doc []byte) ([]providerVersion, error) {
	var d struct {
		Versions []providerVersion `json:"versions"`
	}

	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	for _, v := range d.Versions {
		if v.Version == "" {
			return nil, errNoTerraformVersion
		}
	}

	return d.Versions, nil
}

// mergeProviderVersions merges the members' versions (in member order) by
// version, lowest precedence first. A version several members hold lists the
// union of their platforms and protocols, in the order first seen: the download
// of a platform is resolved from the first member that has it.
func mergeProviderVersions(lists ...[]providerVersion) []providerVersion {
	var out []providerVersion

	index := make(map[string]int)

	for _, l := range lists {
		for _, v := range l {
			i, seen := index[v.Version]
			if !seen {
				i = len(out)
				index[v.Version] = i
				out = append(out, providerVersion{Version: v.Version, Protocols: []string{}, Platforms: []providerPlatform{}})
			}

			for _, proto := range v.Protocols {
				if !slices.Contains(out[i].Protocols, proto) {
					out[i].Protocols = append(out[i].Protocols, proto)
				}
			}

			for _, pl := range v.Platforms {
				if !slices.Contains(out[i].Platforms, pl) {
					out[i].Platforms = append(out[i].Platforms, pl)
				}
			}
		}
	}

	slices.SortStableFunc(out, func(a, b providerVersion) int { return compareTerraformVersions(a.Version, b.Version) })

	return out
}

// renderProviderVersions is the version list document of a provider.
func renderProviderVersions(versions []providerVersion) ([]byte, error) {
	body, err := json.Marshal(struct {
		Versions []providerVersion `json:"versions"`
		Warnings []string          `json:"warnings"`
	}{Versions: versions})
	if err != nil {
		return nil, fmt.Errorf("render provider versions: %w", err)
	}

	return append(body, '\n'), nil
}

// rewriteModuleDetails makes the description of a module version name the
// group as its namespace: the id is "<namespace>/<name>/<system>/<version>".
// Documents of another shape pass through as they are.
func rewriteModuleDetails(doc []byte, group string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	var err error

	if _, ok := fields["namespace"]; ok {
		if fields["namespace"], err = json.Marshal(group); err != nil {
			return nil, fmt.Errorf("render namespace: %w", err)
		}
	}

	var id string
	if raw, ok := fields["id"]; ok && json.Unmarshal(raw, &id) == nil {
		if _, rest, cut := strings.Cut(id, "/"); cut {
			if fields["id"], err = json.Marshal(group + "/" + rest); err != nil {
				return nil, fmt.Errorf("render id: %w", err)
			}
		}
	}

	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	return append(body, '\n'), nil
}

// mergeModuleVersionDocs merges the module version lists of the members'
// answers; a document that cannot be read counts as a failed member. nil when
// no document was usable.
func (h *Handler) mergeModuleVersionDocs(grp *models.Repository, c *collection) []moduleVersion {
	lists := make([][]moduleVersion, 0, len(c.docs))

	for _, d := range c.docs {
		l, err := parseModuleVersions(d.body)
		if err != nil {
			h.log.Warnw("member terraform module versions unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
			c.fail(d.member)

			continue
		}

		lists = append(lists, l)
	}

	if len(lists) == 0 {
		return nil
	}

	return mergeModuleVersions(lists...)
}

// moduleVersionsAsk asks the members for the version list of module p.
func moduleVersionsAsk(p terraformPath) memberAsk {
	return jsonAsk(p.name+"/"+p.system+"/versions", "", 0)
}

func (h *Handler) serveTerraformModuleVersions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p terraformPath,
	members []*models.Repository,
	be Backend,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         moduleVersionsAsk(p),
		what:        "terraform module versions",
		maxAge:      mergedDocMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			merged := h.mergeModuleVersionDocs(grp, c)
			if len(merged) == 0 {
				return nil, errNoDocument
			}

			return renderModuleVersions(grp.Name+"/"+p.name+"/"+p.system, merged)
		},
	})
}

func (h *Handler) serveTerraformProviderVersions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         jsonAsk(relPath, "", 0),
		what:        "terraform provider versions",
		maxAge:      mergedDocMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			lists := make([][]providerVersion, 0, len(c.docs))

			for _, d := range c.docs {
				l, err := parseProviderVersions(d.body)
				if err != nil {
					h.log.Warnw("member terraform provider versions unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
					c.fail(d.member)

					continue
				}

				lists = append(lists, l)
			}

			merged := mergeProviderVersions(lists...)
			if len(merged) == 0 {
				return nil, errNoDocument
			}

			return renderProviderVersions(merged)
		},
	})
}

// serveTerraformModuleDetails answers the description of one module version
// from the first member that has it.
func (h *Handler) serveTerraformModuleDetails(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         jsonAsk(relPath, "", 1),
		what:        "terraform module details",
		maxAge:      mergedDocMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			return rewriteModuleDetails(c.docs[0].body, grp.Name) //nolint:wrapcheck // logged by serveMergedDoc
		},
	})
}

// serveTerraformLatest answers {name}/{system} (the details of the latest
// version) and {name}/{system}/download (a redirect to its download call). The
// latest version is the one of the merged version list, not any member's own.
func (h *Handler) serveTerraformLatest(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p terraformPath,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, moduleVersionsAsk(p), be)
	merged := h.mergeModuleVersionDocs(grp, &c)

	sess.release()

	versions := make([]string, 0, len(merged))
	for _, v := range merged {
		versions = append(versions, v.version)
	}

	latest := latestTerraformVersion(versions)
	if latest == "" {
		writeNoDocument(w, r, c.partial())
		return
	}

	if p.kind == terraformModuleLatestDownload {
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("Location", h.terraformRepoPath(grp.Name)+"/"+p.name+"/"+p.system+"/"+latest+"/download")
		w.WriteHeader(http.StatusFound)

		return
	}

	h.serveTerraformModuleDetails(w, r, grp, p.name+"/"+p.system+"/"+latest, members, be)
}

// terraformRepoPath is the path a terraform repository is served under, below
// the path of the server's base URL.
func (h *Handler) terraformRepoPath(repoName string) string {
	prefix := ""
	if u, err := url.Parse(h.baseURL); err == nil {
		prefix = strings.TrimSuffix(u.EscapedPath(), "/")
	}

	return prefix + "/repos/" + url.PathEscape(repoName)
}
