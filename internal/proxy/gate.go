// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
)

// The version gate.
//
// Two settings of a proxy repository keep versions of an upstream package from
// its clients:
//
//   - the minimum age (min_age_hours): a version the upstream published less
//     than that long ago is held back, because a freshly published malicious
//     release is usually found and pulled within hours or days;
//   - package curation (curation_mode and its rules, internal/curation): an
//     administrator's denylist or allowlist of packages and versions. A matching
//     allow rule also exempts a version from the minimum age, the path of an
//     urgent security fix.
//
// Both are one question to every format, asked of a versionGate built once per
// request (Service.gateFor) from the repository, its rules and the time: is
// this version of this package unavailable, and why (Hidden, checkGate). A
// format that knows the versions of a package from the metadata it renders
// applies the gate twice:
//
//   - in the metadata it renders, the versions that are unavailable are left out
//     (and pointers such as npm's dist-tags move to the newest version left),
//     so resolvers pick another release on their own;
//   - in the locate hook of a download, a file of an unavailable version answers
//     the error of checkGate (403 with the reason), so a client that kept or
//     guessed the file's URL gets no further.
//
// The minimum age needs a publish time; a version whose publish time the
// upstream does not state passes it (the gate only holds back what it can
// date). Curation needs only the package name and the version. A file already
// in the cache is served without asking again only when the gate is inactive:
// a download of a cached file still goes through the format's locate hook, which
// re-checks the gate before the cache is read.

// versionGate is the per-request gate; see curation.Gate.
type versionGate = curation.Gate

var (
	// errTooNew marks a version held back by the minimum age.
	errTooNew = errors.New("version is younger than the repository's minimum age")
	// errCurated marks a version unavailable under the repository's curation.
	errCurated = errors.New("version is not available under the repository's curation rules")
	// errNoCuration is returned when a repository has curation on but the
	// service has no rule source: the request fails instead of serving
	// everything.
	errNoCuration = errors.New("curation rules are not available")
)

// CurationSource supplies the curation rules of a repository (the database).
type CurationSource interface {
	ListCurationRules(ctx context.Context, repoID string) ([]curation.Rule, error)
}

// WithCuration sets where the curation rules of proxy repositories are read
// from. Without it a repository whose curation is on fails its requests closed.
// It must be called before the service serves requests.
func (s *Service) WithCuration(src CurationSource) *Service {
	s.curation = src
	return s
}

// gateFor builds the gate of repo at now. The rules are read only when the
// repository's curation is on; a failure to read them is an error, never an
// open gate.
func (s *Service) gateFor(ctx context.Context, repo *models.Repository, now time.Time) (versionGate, error) {
	var rules []curation.Rule

	if repo != nil && repo.CurationMode != "" && repo.CurationMode != curation.ModeOff {
		if s.curation == nil {
			return versionGate{}, errNoCuration
		}

		var err error

		rules, err = s.curation.ListCurationRules(ctx, repo.ID)
		if err != nil {
			return versionGate{}, fmt.Errorf("load curation rules: %w", err)
		}
	}

	return newGate(repo, rules, now), nil
}

// newGate is the gate of repo with the given rules (nil when its curation is off).
func newGate(repo *models.Repository, rules []curation.Rule, now time.Time) versionGate {
	if repo == nil {
		return versionGate{Now: now}
	}

	return curation.NewGate(repo.Format, repo.CurationMode, rules, repo.MinAgeHours, now)
}

// gateError is the refusal of a download by the gate.
type gateError struct {
	pkg, version string
	hold         curation.Hold
}

func (e *gateError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.pkg, e.version, e.hold.Detail)
}

// Is makes the error match the sentinel of its source.
func (e *gateError) Is(target error) bool {
	switch target {
	case errTooNew:
		return e.hold.Source == curation.SourceMinAge
	case errCurated:
		return e.hold.Source == curation.SourceCuration
	}

	return false
}

// checkGate is nil when version of pkg is available, else the *gateError that
// answers a download of it with 403. published is zero when the upstream does
// not state it.
func checkGate(g versionGate, pkg, version string, published time.Time) error {
	if hidden, hold := g.Hidden(pkg, version, published); hidden {
		return &gateError{pkg: pkg, version: version, hold: hold}
	}

	return nil
}

// writeGateError answers a gateError with 403 and the reason; false when err is
// not one. A minimum-age refusal keeps its fixed message.
func writeGateError(w http.ResponseWriter, err error) bool {
	var ge *gateError
	if !errors.As(err, &ge) {
		return false
	}

	msg := ge.hold.Detail
	if ge.hold.Source == curation.SourceMinAge {
		msg = errTooNew.Error()
	}

	http.Error(w, msg, http.StatusForbidden)

	return true
}

// PublishTime is the publish time the upstream states for version of pkg in
// proxy repository repo, read from the metadata already in the cache (it never
// fetches). ok is false when the format has no publish time, the metadata is
// not cached or it does not date the version. The REST availability
// explanation uses it to evaluate the minimum age.
func (s *Service) PublishTime(ctx context.Context, repo *models.Repository, pkg, version string) (time.Time, bool) {
	h, found := hooks.Lookup(repo.Format)
	if !found || h.Published == nil {
		return time.Time{}, false
	}

	return h.Published(s, ctx, repo, pkg, version)
}
