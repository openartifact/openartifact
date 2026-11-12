// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
)

// gateFixtures is what the availability, license, SBOM and activity tools read
// besides the repositories and components of fixtureDB.
type gateFixtures struct {
	rules     []curation.Rule
	holds     map[string]models.VersionHolds // by "package@version"
	license   *models.LicenseScanResult
	sboms     []*models.SBOMReport
	audit     *models.AuditPage
	lastAudit models.AuditListParams
	blobs     fakeBlobs
	upstream  fakeUpstream
}

func (m *mockDB) ListCurationRulesUncached(context.Context, string) ([]curation.Rule, error) {
	return m.gate.rules, m.err
}

func (m *mockDB) PackageVersionHolds(_ context.Context, _, pkg, version string) (models.VersionHolds, error) {
	return m.gate.holds[pkg+"@"+version], m.err
}

func (m *mockDB) GetLicenseScanResult(context.Context, string, string) (*models.LicenseScanResult, error) {
	return m.gate.license, m.err
}

func (m *mockDB) ListSBOMReports(context.Context, string) ([]*models.SBOMReport, error) {
	return m.gate.sboms, m.err
}

func (m *mockDB) ListAuditLogs(_ context.Context, p models.AuditListParams) (*models.AuditPage, error) {
	m.gate.lastAudit = p

	if m.err != nil {
		return nil, m.err
	}

	return m.gate.audit, nil
}

func (m *mockDB) ResolveAuditNames(context.Context, models.AuditNameRefs) (*models.AuditNames, error) {
	return &models.AuditNames{}, m.err
}

// fakeBlobs is a blob store by sha256.
type fakeBlobs map[string]string

var errBlobMissing = errors.New("blob missing: /var/lib/openartifact/blobs/ab/cd")

func (b *fakeBlobs) Get(_ context.Context, id string) (io.ReadCloser, error) {
	doc, ok := (*b)[id]
	if !ok {
		return nil, errBlobMissing
	}

	return io.NopCloser(strings.NewReader(doc)), nil
}

// fakeUpstream is the proxy service: publish times and cached version lists.
type fakeUpstream struct {
	published map[string]time.Time // by "package@version"
	held      []proxy.HeldBack
	versions  proxy.ProxyVersions
}

func (u *fakeUpstream) PublishTime(_ context.Context, _ *models.Repository, pkg, version string) (time.Time, bool) {
	t, ok := u.published[pkg+"@"+version]

	return t, ok
}

func (u *fakeUpstream) HeldBackVersions(context.Context, *models.Repository, string) ([]proxy.HeldBack, bool, error) {
	return u.held, true, nil
}

func (u *fakeUpstream) ListProxyVersions(context.Context, *models.Repository, string) (proxy.ProxyVersions, error) {
	return u.versions, nil
}

// CachedVersions lists the versions of pkg the published map dates; a package it
// names no version of is not cached.
func (u *fakeUpstream) CachedVersions(_ context.Context, _ *models.Repository, pkg string) ([]proxy.DatedVersion, bool) {
	var out []proxy.DatedVersion

	for key, t := range u.published {
		if name, version, ok := strings.Cut(key, "@"); ok && name == pkg {
			out = append(out, proxy.DatedVersion{Version: version, Published: t})
		}
	}

	return out, len(out) > 0
}
