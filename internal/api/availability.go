// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
)

// Sources of an availability reason besides the gate's own (curation.SourceMinAge,
// curation.SourceCuration).
const (
	sourceQuarantine        = "quarantine"
	sourceLicensePolicy     = "license_policy"
	sourceRepositoryBlocked = "repository_blocked"
)

// Values of AvailabilityResponse.MinAge.
const (
	minAgeOff     = "off"
	minAgeChecked = "checked"
	minAgeUnknown = "unknown"
)

// MaxAvailabilityParam bounds the package and version of an availability query.
const MaxAvailabilityParam = 1024

var (
	// ErrGroupAvailability: a group repository has no verdict of its own, it
	// serves what its members serve.
	ErrGroupAvailability = errors.New("a group repository has no availability of its own: ask one of its members")
	// ErrNotProxy: only a proxy repository has an upstream whose versions can
	// be listed.
	ErrNotProxy = errors.New("only a proxy repository has upstream versions")
)

// AvailabilityStore is the part of the database ExplainAvailability needs.
type AvailabilityStore interface {
	ListCurationRulesUncached(ctx context.Context, repoID string) ([]curation.Rule, error)
	PackageVersionHolds(ctx context.Context, repoID, pkg, version string) (models.VersionHolds, error)
}

// ExplainAvailability says whether one package version can be fetched from repo
// and, when it cannot, why: the same gate the proxy applies to its clients,
// evaluated for the query. It is the answer of
// GET /api/v1/repositories/{name}/availability and of the MCP check_availability
// tool; the caller has already checked read access and the query bounds.
//
// A proxy repository answers with its blocked flag, its curation rules and its
// minimum age (the publish time comes from the metadata already cached, never
// from the upstream; MinAge is "unknown" when the repository has a minimum age
// but the version cannot be dated). A hosted repository answers with its
// blocked flag and the quarantine and license-policy holds of the stored
// version. A group is ErrGroupAvailability. published may be nil: the minimum
// age is then reported as unknown.
func ExplainAvailability(
	ctx context.Context, db AvailabilityStore, published PublishTimeSource, repo *models.Repository, pkg, version string,
) (responses.AvailabilityResponse, error) {
	if repo.RepoType == repotype.Group {
		return responses.AvailabilityResponse{}, ErrGroupAvailability
	}

	resp := responses.AvailabilityResponse{Reasons: []responses.AvailabilityReason{}, MinAge: minAgeOff}

	if repo.Blocked {
		resp.Reasons = append(resp.Reasons, responses.AvailabilityReason{
			Source: sourceRepositoryBlocked, Detail: "the repository is blocked",
		})
	}

	var err error

	if repo.RepoType == repotype.Proxy {
		err = explainProxy(ctx, db, published, repo, pkg, version, &resp)
	} else {
		err = explainHosted(ctx, db, repo, pkg, version, &resp)
	}

	if err != nil {
		return responses.AvailabilityResponse{}, err
	}

	resp.Available = len(resp.Reasons) == 0

	return resp, nil
}

// HeldBackVersions lists the versions of a proxy package the repository's gate
// (minimum age and curation rules) keeps from its clients. The versions come
// from the upstream metadata already cached (the upstream is never asked):
// Supported is false when the format cannot list versions or nothing is cached
// yet. Anything but a proxy repository is ErrNotProxy.
func HeldBackVersions(
	ctx context.Context, published PublishTimeSource, repo *models.Repository, pkg string,
) (responses.HeldBackResponse, error) {
	if repo.RepoType != repotype.Proxy || published == nil {
		return responses.HeldBackResponse{}, ErrNotProxy
	}

	held, supported, err := published.HeldBackVersions(ctx, repo, pkg)
	if err != nil {
		return responses.HeldBackResponse{}, fmt.Errorf("list held-back versions: %w", err)
	}

	resp := responses.HeldBackResponse{Supported: supported, Versions: make([]responses.HeldBackVersion, 0, len(held))}

	for _, v := range held {
		item := responses.HeldBackVersion{Version: v.Version, Reasons: []responses.AvailabilityReason{holdReason(v.Hold)}}
		if !v.Published.IsZero() {
			item.PublishedAt = v.Published.UTC().Format(time.RFC3339)
		}

		resp.Versions = append(resp.Versions, item)
	}

	return resp, nil
}

// ProxyVersionList lists every version of a proxy package the cached upstream
// metadata names, newest first, each available or held by the gate, with its
// publish time and whether a file of it is already in the proxy cache. Like
// HeldBackVersions it reads the cache only: Supported is false when the format
// cannot list versions or nothing is cached yet, CacheKnown is false when the
// format's cache keys name no version. Anything but a proxy repository is
// ErrNotProxy.
func ProxyVersionList(
	ctx context.Context, published PublishTimeSource, repo *models.Repository, pkg string,
) (responses.ProxyVersionsResponse, error) {
	if repo.RepoType != repotype.Proxy || published == nil {
		return responses.ProxyVersionsResponse{}, ErrNotProxy
	}

	list, err := published.ListProxyVersions(ctx, repo, pkg)
	if err != nil {
		return responses.ProxyVersionsResponse{}, fmt.Errorf("list proxy versions: %w", err)
	}

	resp := responses.ProxyVersionsResponse{
		Supported:  list.Supported,
		CacheKnown: list.CacheKnown,
		Versions:   make([]responses.ProxyVersion, 0, len(list.Versions)),
	}

	for _, v := range list.Versions {
		item := responses.ProxyVersion{Version: v.Version, Status: "available", Cached: v.Cached, Reasons: []responses.AvailabilityReason{}}
		if v.Held {
			item.Status = "held"
			item.Reasons = []responses.AvailabilityReason{holdReason(v.Hold)}
		}

		if !v.Published.IsZero() {
			item.PublishedAt = v.Published.UTC().Format(time.RFC3339)
		}

		resp.Versions = append(resp.Versions, item)
	}

	return resp, nil
}

// holdReason is a gate hold as the API states it.
func holdReason(hold curation.Hold) responses.AvailabilityReason {
	reason := responses.AvailabilityReason{Source: hold.Source, Detail: hold.Detail}
	if !hold.Until.IsZero() {
		reason.Until = hold.Until.UTC().Format(time.RFC3339)
	}

	if hold.Rule != nil {
		reason.RuleID = hold.Rule.ID
	}

	return reason
}

// explainProxy adds what the gate of a proxy repository says about the version.
func explainProxy(
	ctx context.Context, db AvailabilityStore, published PublishTimeSource, repo *models.Repository,
	pkg, version string, resp *responses.AvailabilityResponse,
) error {
	var rules []curation.Rule

	if repo.CurationMode != "" && repo.CurationMode != curation.ModeOff {
		var err error

		if rules, err = db.ListCurationRulesUncached(ctx, repo.ID); err != nil {
			return fmt.Errorf("list curation rules: %w", err)
		}
	}

	gate := curation.NewGate(repo.Format, repo.CurationMode, rules, repo.MinAgeHours, time.Now())

	var publishedAt time.Time

	if published != nil {
		if t, ok := published.PublishTime(ctx, repo, pkg, version); ok {
			publishedAt = t
			resp.PublishedAt = t.UTC().Format(time.RFC3339)
		}
	}

	switch {
	case !gate.AgeActive():
		resp.MinAge = minAgeOff
	case publishedAt.IsZero():
		resp.MinAge = minAgeUnknown
	default:
		resp.MinAge = minAgeChecked
	}

	if hidden, hold := gate.Hidden(pkg, version, publishedAt); hidden {
		resp.Reasons = append(resp.Reasons, holdReason(hold))
	}

	return nil
}

// explainHosted adds the quarantine and license-policy holds of the stored
// version of a hosted repository.
func explainHosted(
	ctx context.Context, db AvailabilityStore, repo *models.Repository, pkg, version string, resp *responses.AvailabilityResponse,
) error {
	holds, err := db.PackageVersionHolds(ctx, repo.ID, pkg, version)
	if err != nil {
		return fmt.Errorf("package version holds: %w", err)
	}

	resp.Reasons = append(resp.Reasons, holdsReasons(holds)...)

	return nil
}
