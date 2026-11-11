// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
)

const (
	// maxUpstreamVersions caps the versions one list_held_back_versions or
	// list_proxy_versions result carries (newest first).
	maxUpstreamVersions = 200

	// sourceNotFound is the reason of a version a hosted repository does not
	// store. The availability endpoint judges holds, not existence, so this
	// source exists only here.
	sourceNotFound = "not_found"
)

var (
	errAvailabilityParams = fmt.Errorf("package and version are required (at most %d bytes each)", api.MaxAvailabilityParam)
	errPackageParam       = fmt.Errorf("package is required (at most %d bytes)", api.MaxAvailabilityParam)
	// errGroupAvailability is api.ErrGroupAvailability with where to find the members.
	errGroupAvailability = errors.New(
		"a group repository has no availability of its own: ask its members (get_repository lists them in group order)",
	)
)

func (s *Server) registerGateTools() {
	addTool(s, readTool(
		"check_availability",
		"Check package availability",
		"Explain whether one exact package version can be installed from a repository and, if not, why. Use it when an install "+
			"fails with 404 or 403 for a version that should exist, or before pinning a version. Returns available (true/false) and "+
			"reasons, each with a source: min_age (the version is younger than the repository's minimum age; until is when it "+
			"unlocks), curation (a curation rule or the allowlist hides it; rule_id names the rule, detail quotes its reason), "+
			"quarantine (a vulnerability or secret scan quarantined it), license_policy, repository_blocked, or not_found (a hosted "+
			"repository stores no such package version, judged on the names get_package lists). Also returns published_at "+
			"and min_age: off (no minimum age), checked, or unknown (the publish time is not cached yet, so the age cannot be "+
			"judged). A proxy repository is judged on its cached upstream metadata; the upstream is never asked. A group "+
			"repository has no verdict of its own: ask its members, which get_repository lists in group order with the rule "+
			"that decides between them. Requires read permission on the repository.",
	),
		true, s.checkAvailability)

	addTool(s, readTool(
		"list_held_back_versions",
		"List held-back versions",
		"List the versions of a package that a proxy repository hides from its clients, each with why (minimum age, with the "+
			"until time it unlocks, or a curation rule) and when the upstream published it. Use it to tell a version that is "+
			"held back from one that does not exist, and to learn when a recent release becomes installable. Reads the cached "+
			"upstream metadata only: supported=false means the format cannot list versions or the package metadata is not cached "+
			"yet (install or fetch the package once). Only for proxy repositories. Requires read permission on the repository.",
	),
		true, s.listHeldBackVersions)

	addTool(s, readTool(
		"list_proxy_versions",
		"List proxy versions",
		"List every version of a package that the cached upstream metadata of a proxy repository names, newest first, each "+
			"with status (available or held), the publish time, the reasons when held, and cached (whether a file of the version "+
			"is already in the proxy cache). Use it to pick a version that is installable and, when offline, one already cached. "+
			"supported=false means the format cannot list versions or the package metadata is not cached yet; cache_known=false "+
			"means cached cannot be told for this format. Only for proxy repositories. Requires read permission on the repository.",
	),
		true, s.listProxyVersions)
}

func (s *Server) checkAvailability(ctx context.Context, in availabilityIn) (availabilityResult, error) {
	if in.Package == "" || in.Version == "" || len(in.Package) > api.MaxAvailabilityParam || len(in.Version) > api.MaxAvailabilityParam {
		return availabilityResult{}, errAvailabilityParams
	}

	repo, err := s.resolveRepo(ctx, in.Repository)
	if err != nil {
		return availabilityResult{}, err
	}

	resp, err := api.ExplainAvailability(ctx, s.db, s.upstream, repo, in.Package, in.Version)
	if errors.Is(err, api.ErrGroupAvailability) {
		return availabilityResult{}, errGroupAvailability
	}

	if err != nil {
		return availabilityResult{}, s.internalError("explain availability", err, "repository", repo.Name)
	}

	if repo.RepoType == repotype.Hosted {
		if err := s.addNotFound(ctx, repo, in, &resp); err != nil {
			return availabilityResult{}, err
		}
	}

	capReasonDetails(resp.Reasons)

	return availabilityResult{
		provenance:           newProvenance(),
		Repository:           repo.Name,
		Package:              in.Package,
		Version:              in.Version,
		AvailabilityResponse: resp,
	}, nil
}

// addNotFound adds the not_found reason when the hosted repository stores no
// such package version. The lookup is the one get_package does, so both tools
// agree; when its bounded scan is cut short nothing is asserted.
func (s *Server) addNotFound(ctx context.Context, repo *models.Repository, in availabilityIn, resp *responses.AvailabilityResponse) error {
	matches, truncated, err := s.findPackageVersions(ctx, repo, in.Package)
	if err != nil {
		return s.internalError("find package versions", err, "repository", repo.Name, "package", in.Package)
	}

	for _, c := range matches {
		if c.Version == in.Version {
			return nil
		}
	}

	if truncated {
		return nil
	}

	detail := "the repository stores no version of this package"
	if len(matches) > 0 {
		detail = "the repository stores this package, but not this version"
	}

	resp.Reasons = append(resp.Reasons, responses.AvailabilityReason{Source: sourceNotFound, Detail: detail})
	resp.Available = false

	return nil
}

// capReasonDetails bounds the free text of reasons: a curation rule reason is
// written by a repository admin.
func capReasonDetails(reasons []responses.AvailabilityReason) {
	for i := range reasons {
		reasons[i].Detail = truncateText(reasons[i].Detail, maxUntrustedText)
	}
}

// proxyPackage resolves the repository and validates the package of the
// upstream-version tools.
func (s *Server) proxyPackage(ctx context.Context, in upstreamPackageIn) (*models.Repository, error) {
	if in.Package == "" || len(in.Package) > api.MaxAvailabilityParam {
		return nil, errPackageParam
	}

	return s.resolveRepo(ctx, in.Repository)
}

func (s *Server) listHeldBackVersions(ctx context.Context, in upstreamPackageIn) (heldBackResult, error) {
	repo, err := s.proxyPackage(ctx, in)
	if err != nil {
		return heldBackResult{}, err
	}

	resp, err := api.HeldBackVersions(ctx, s.upstream, repo, in.Package)
	if errors.Is(err, api.ErrNotProxy) {
		return heldBackResult{}, err //nolint:wrapcheck // fixed message meant for the model
	}

	if err != nil {
		return heldBackResult{}, s.internalError("list held-back versions", err, "repository", repo.Name, "package", in.Package)
	}

	out := heldBackResult{
		provenance: newProvenance(),
		Repository: repo.Name,
		Package:    in.Package,
		Supported:  resp.Supported,
		Versions:   resp.Versions,
	}

	if len(out.Versions) > maxUpstreamVersions {
		out.Versions, out.Truncated = out.Versions[:maxUpstreamVersions], true
	}

	for i := range out.Versions {
		capReasonDetails(out.Versions[i].Reasons)
	}

	return out, nil
}

func (s *Server) listProxyVersions(ctx context.Context, in upstreamPackageIn) (proxyVersionsResult, error) {
	repo, err := s.proxyPackage(ctx, in)
	if err != nil {
		return proxyVersionsResult{}, err
	}

	resp, err := api.ProxyVersionList(ctx, s.upstream, repo, in.Package)
	if errors.Is(err, api.ErrNotProxy) {
		return proxyVersionsResult{}, err //nolint:wrapcheck // fixed message meant for the model
	}

	if err != nil {
		return proxyVersionsResult{}, s.internalError("list proxy versions", err, "repository", repo.Name, "package", in.Package)
	}

	out := proxyVersionsResult{
		provenance:    newProvenance(),
		Repository:    repo.Name,
		Package:       in.Package,
		Supported:     resp.Supported,
		CacheKnown:    resp.CacheKnown,
		TotalVersions: len(resp.Versions),
		Versions:      resp.Versions,
	}

	if len(out.Versions) > maxUpstreamVersions {
		out.Versions, out.Truncated = out.Versions[:maxUpstreamVersions], true
	}

	for i := range out.Versions {
		capReasonDetails(out.Versions[i].Reasons)
	}

	return out, nil
}
