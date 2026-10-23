// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import "time"

const (
	debTestFilename = "libshop6_2.4.0-1_amd64.deb"
	rpmTestFilename = "shop-2.4.0-3.el9.x86_64.rpm"
)

// seedDebPackage stores a deb package of repoID in distribution dist and
// component comp, creating the distribution, component and architecture rows
// when the repository lacks them (so it can be called repeatedly). Every
// package column holds a non-default value. It returns the package row id.
func seedDebPackage(h *harness, repoID, blobID, dist, comp, filename string) string {
	distID := h.str(`INSERT INTO debian_distributions (repository_id, name, codename, description)
		VALUES ($1, $2, 'Bookworm', 'the stable distribution')
		ON CONFLICT (repository_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id::text`, repoID, dist)
	compID := h.str(`INSERT INTO debian_components (distribution_id, name, description)
		VALUES ($1, $2, $3)
		ON CONFLICT (distribution_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id::text`,
		distID, comp, "the "+comp+" component")

	h.exec(`INSERT INTO debian_architectures (distribution_id, name) VALUES ($1, 'amd64')
		ON CONFLICT (distribution_id, name) DO NOTHING`, distID)

	return h.row("deb_packages", repoID, "blob_id", blobID, map[string]any{
		"distribution_id": distID, "component_id": compID,
		"name": "libshop6", "version": "1:2.4.0-1", "arch": "amd64",
		"summary": "a shop library", "description": "a shop library\n with a long description",
		"license": "MIT", "maintainer": "Ann <ann@example.com>", "homepage": "https://shop.example.com",
		"filename": filename, "installed_size": 4321,
		"control_fields": sqlExpr(`'{"Depends":"libc6 (>= 2.36)","Section":"libs","Priority":"optional"}'::jsonb`),
	})
}

// debFixture covers deb: the package row plus the distribution, component and
// architecture rows it lives in.
func debFixture() fixture {
	return fixture{
		format: "deb", version: "1:2.4.0-1", path: debTestFilename,
		tables: []string{"deb_packages"}, compare: []string{"deb_packages"},
		// The distribution and component rows are per repository.
		ignore: []string{"distribution_id", "component_id"},
		seed: func(h *harness, repoID, blobID string) {
			seedDebPackage(h, repoID, blobID, "bookworm", "main", debTestFilename)
		},
		verify: func(h *harness, src, dst string) {
			h.t.Helper()

			if got := h.count(`SELECT COUNT(*) FROM debian_distributions
				WHERE repository_id = $1 AND name = 'bookworm' AND codename = 'Bookworm' AND description = 'the stable distribution'`, dst); got != 1 {
				h.t.Errorf("target has %d matching distributions, want a copy of the source's", got)
			}

			if got := h.count(`SELECT COUNT(*) FROM debian_components c JOIN debian_distributions d ON d.id = c.distribution_id
				WHERE d.repository_id = $1 AND c.name = 'main' AND c.description = 'the main component'`, dst); got != 1 {
				h.t.Errorf("target has %d matching components, want a copy of the source's", got)
			}

			if got := h.count(`SELECT COUNT(*) FROM debian_architectures a JOIN debian_distributions d ON d.id = a.distribution_id
				WHERE d.repository_id = $1 AND a.name = 'amd64'`, dst); got != 1 {
				h.t.Errorf("target has %d amd64 architectures, want 1", got)
			}

			// The copied package points at the target's own distribution and component.
			if got := h.count(`SELECT COUNT(*) FROM deb_packages p
				JOIN debian_distributions d ON d.id = p.distribution_id AND d.repository_id = p.repository_id
				JOIN debian_components c ON c.id = p.component_id AND c.distribution_id = d.id
				WHERE p.repository_id = $1`, dst); got != 1 {
				h.t.Errorf("%d target packages reference the target's own distribution and component, want 1", got)
			}
		},
	}
}

// seedRPMPackage stores an rpm package of repoID with the given epoch and
// file name, and the repository's rpm_repos row (flagged fresh and holding a
// signing key, which a promotion must not carry over). Every package column
// holds a non-default value. It returns the package row id.
func seedRPMPackage(h *harness, repoID, blobID string, epoch int, filename string) string {
	h.exec(`INSERT INTO rpm_repos (repository_id, base_arch, metadata_stale, signing_key_id, signing_key_fingerprint)
		VALUES ($1, 'x86_64', false, 'SRCKEYID', 'SRCKEYFINGERPRINT')
		ON CONFLICT (repository_id, base_arch) DO NOTHING`, repoID)

	return h.row("rpm_packages", repoID, "blob_id", blobID, map[string]any{
		"name": "shop", "version": "2.4.0", "release": "3.el9", "epoch": epoch, "arch": "x86_64",
		"summary": "a shop", "description": "a shop with a long description", "license": "MIT",
		"url": "https://shop.example.com", "vendor": "Acme", "group_name": "Applications/Internet",
		"packager": "Ann <ann@example.com>", "source_rpm": "shop-2.4.0-3.el9.src.rpm", "build_host": "builder.example.com",
		"build_time": time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC), "filename": filename,
		"package_size": 5000, "installed_size": 12000, "archive_size": 4800,
		"header_start": 280, "header_end": 4096,
		"rpm_extra": sqlExpr(`'{"requires":["libc.so.6"],"provides":["shop"]}'::jsonb`),
	})
}

// rpmFixture covers rpm: the package row and the repodata flag of the target.
func rpmFixture() fixture {
	return fixture{
		format: "rpm", version: "2:2.4.0-3.el9", path: rpmTestFilename,
		tables: []string{"rpm_packages"}, compare: []string{"rpm_packages"},
		seed: func(h *harness, repoID, blobID string) {
			seedRPMPackage(h, repoID, blobID, 2, rpmTestFilename)
		},
		verify: func(h *harness, src, dst string) {
			h.t.Helper()

			if got := h.count(`SELECT COUNT(*) FROM rpm_repos WHERE repository_id = $1 AND signing_key_id = 'SRCKEYID'`, src); got != 1 {
				h.t.Fatalf("source repository has %d signing keys, the fixture should set one", got)
			}

			if got := h.count(`SELECT COUNT(*) FROM rpm_repos WHERE repository_id = $1`, dst); got != 1 {
				h.t.Errorf("target has %d rpm_repos rows, want 1", got)
			}

			if got := h.count(`SELECT COUNT(*) FROM rpm_repos
				WHERE repository_id = $1 AND metadata_stale AND signing_key_id IS NULL AND signing_key_fingerprint IS NULL`, dst); got != 1 {
				h.t.Errorf("target has %d stale rpm_repos rows without a signing key, want 1", got)
			}
		},
	}
}
