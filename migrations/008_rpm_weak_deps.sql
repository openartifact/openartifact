-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Weak dependencies (recommends/suggests/supplements/enhances) are persisted in
-- rpm_packages.rpm_extra (JSON) with a "weak_deps": true marker. Rows written
-- before this migration lack the marker; the RPM metadata worker re-reads their
-- headers from the stored blobs on the next regeneration and rewrites rpm_extra.
-- Flag every RPM repository stale so that regeneration happens at startup.
UPDATE rpm_repos SET metadata_stale = true;
